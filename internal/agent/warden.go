package agent

// warden.go — "pi" / "warden" agent mode.
//
// Runs two sequential agent loops with minimal, phase-appropriate tool sets:
//
//  Loop 1 — Implement:
//    search_code, file tools, LSP tools, run_command, review_code
//    (no push_branch / create_pull_request)
//    Terminates when review_code returns APPROVE or max iterations reached.
//
//  Loop 2 — Publish (only if Loop 1 produced APPROVE):
//    push_branch, create_pull_request
//    Max 5 iterations — focused task, should complete in 1-2 turns.
//
// Keeping publish tools out of the implement loop means the model never
// attempts to push or open a PR before the code has been reviewed and approved.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"

	gh "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/mcp"
)

// publishToolNames are withheld from the implement loop and reserved for the
// publish loop. Keeping this as an explicit set makes it easy to audit.
var publishToolNames = map[string]bool{
	"push_branch":         true,
	"create_pull_request": true,
}

// maxReviewRounds is the maximum number of times the agent may call review_code
// before the implement loop is told to stop and request human review.
// This prevents endless ping-pong when reviewer and coder keep disagreeing.
const maxReviewRounds = 10

// reviewCapTool wraps the review_code MCP tool and enforces maxReviewRounds.
// After the cap is reached every subsequent call returns a synthetic verdict
// instructing the agent to stop rather than continuing to cycle.
type reviewCapTool struct {
	inner mcp.Tool
	mu    sync.Mutex
	calls int
}

func (r *reviewCapTool) Name() string                     { return r.inner.Name() }
func (r *reviewCapTool) Description() string              { return r.inner.Description() }
func (r *reviewCapTool) ParametersSchema() map[string]any { return r.inner.ParametersSchema() }

func (r *reviewCapTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()

	if n > maxReviewRounds {
		return map[string]any{
			"verdict": "HUMAN_REVIEW_REQUIRED",
			"message": fmt.Sprintf(
				"Maximum automated review rounds (%d) reached without APPROVE. "+
					"Stop implementation and leave a comment asking for human review. "+
					"Do not call review_code again.",
				maxReviewRounds,
			),
		}, nil
	}
	return r.inner.Execute(ctx, args)
}

// runWardenAgent runs the two-phase warden loop: implement then publish.
func (o *Orchestrator) runWardenAgent(ctx context.Context, session *Session, branch string) {
	defer o.cleanupNativeSession(ctx, session)

	agentLLM, err := o.resolveAgentLLM(ctx)
	if err != nil {
		o.failSession(ctx, session, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(ctx, o.config.Timeout)
	defer cancel()

	ws, err := o.prepareAgentWorkspace(ctx, session)
	if err != nil {
		o.failSession(ctx, session, err.Error())
		return
	}
	defer ws.logFile.Close()
	if ws.traceFile != nil {
		defer ws.traceFile.Close()
	}
	defer o.persistLogs(ws, session.ID)
	defer o.mcpServer.UnregisterWorkspace(session.ID)

	// Persist the session as "running" now that the workspace and branch are ready.
	o.persistSessionRunning(ctx, session, branch)

	// ── GitHub Check Run ──────────────────────────────────────────────────────
	// Create a Check Run on the base branch so progress appears in GitHub UI.
	// Falls back gracefully when the GitHub client is unavailable or the base
	// SHA cannot be fetched (e.g., network failure, insufficient permissions).
	baseSHA := o.getBaseSHA(ctx, session.Issue.RepoOwner, session.Issue.RepoName)
	checkRunID := o.createCheckRun(ctx, session.Issue, baseSHA,
		fmt.Sprintf("Session `%s` started.", session.ID))
	// Complete the Check Run when runWardenAgent returns, whatever the outcome.
	defer o.deferCheckRunCompletion(ctx, session, checkRunID)

	// ── Progress tracker ─────────────────────────────────────────────────────
	// Intercepts every tool call to write real-time log lines.
	// When a Check Run is available, progress updates edit its summary.
	// Otherwise a single rolling GitHub issue comment is created and edited.
	var (
		progressCreate func(context.Context, string) int64
		progressUpdate func(context.Context, int64, string)
	)
	if checkRunID != 0 {
		// Use the Check Run for in-progress updates — avoids issue comment noise.
		progressCreate = func(tctx context.Context, body string) int64 {
			o.updateCheckRun(tctx, session.Issue, checkRunID, body)
			return checkRunID // re-use the check run ID as the "comment" ID
		}
		progressUpdate = func(tctx context.Context, _ int64, body string) {
			o.updateCheckRun(tctx, session.Issue, checkRunID, body)
		}
	} else {
		progressCreate = func(tctx context.Context, body string) int64 {
			return o.createIssueComment(tctx, session.Issue, body)
		}
		progressUpdate = func(tctx context.Context, id int64, body string) {
			o.updateIssueComment(tctx, session.Issue, id, body)
		}
	}
	tracker := newProgressTracker(session, ws.logFile, "RAG-only", progressCreate, progressUpdate)
	tracker.start(ctx)
	defer tracker.stop()

	o.logger.Info("🛠️  IMPLEMENTATION: Starting warden agent (phased)",
		"session_id", session.ID,
		"working_dir", ws.dir,
		"timeout", o.config.Timeout,
		"model", agentLLM,
	)

	// ── Planning phase ───────────────────────────────────────────────────────
	tracker.setPhase("planning")
	plan := o.buildPlan(ctx, agentLLM, session, ws, tracker)
	o.logger.Info("warden: planning complete, starting implement loop", "session_id", session.ID)

	// ── Loop 1: Implement ────────────────────────────────────────────────────
	tracker.setPhase("implementing")
	implIter, implObs, verdict, ok := o.runImplementPhase(ctx, session, agentLLM, ws, branch, plan, tracker)
	if !ok {
		return
	}

	// ── Loop 2: Publish ──────────────────────────────────────────────────────
	tracker.setPhase("publishing")
	o.runPublishPhase(ctx, session, agentLLM, ws, branch, verdict, implIter, implObs, tracker)
}

// runImplementPhase builds and runs the implement loop. Returns the iteration
// count, observer (for token accumulation), final verdict, and whether to continue
// to the publish phase.
// Returns (0, nil, "", false) and calls failSession on any error.
func (o *Orchestrator) runImplementPhase(
	ctx context.Context,
	session *Session,
	agentLLM llms.Model,
	ws *agentWorkspace,
	branch, plan string,
	tracker *progressTracker,
) (iterations int, obs *loopObserver, verdict string, ok bool) {
	implObs := newLoopObserver(o.logger, session.ID, "implement")
	implLoop, err := o.buildImplementLoop(agentLLM, session, ws, plan, tracker, implObs)
	if err != nil {
		o.failSession(ctx, session, fmt.Sprintf("build implement loop: %v", err))
		return 0, nil, "", false
	}

	implTask := goframeagent.Task{
		ID:          session.ID + "-impl",
		Description: fmt.Sprintf("Implement GitHub issue #%d: %s", session.Issue.Number, session.Issue.Title),
		Context:     o.buildNativeTaskContext(session.Issue, branch),
		Priority:    5,
	}

	o.logger.Info("warden: starting implement loop", "session_id", session.ID)
	implResult, implErr := implLoop.Run(ctx, implTask, nil)
	if implErr != nil && !errors.Is(implErr, goframeagent.ErrMaxIterations) {
		o.logger.Error("warden: implement loop failed",
			"session_id", session.ID, "iterations", implResult.Iterations, "error", implErr)
		o.failSession(ctx, session, fmt.Sprintf("implement loop: %v", implErr))
		return 0, nil, "", false
	}
	if implErr != nil {
		o.logger.Warn("warden: implement loop hit max iterations, yielding draft PR",
			"session_id", session.ID, "iterations", implResult.Iterations)
	}

	v, _, _ := o.mcpServer.GetReviewBySession(session.ID)
	if v != "APPROVE" {
		o.logger.Warn("warden: implement loop ended without APPROVE, yielding draft PR",
			"session_id", session.ID,
			"verdict", v,
			"iterations", implResult.Iterations,
		)
		o.yieldDraftPR(ctx, session, ws, branch, implResult.Iterations,
			implResult.Tokens.Input, implResult.Tokens.Output,
			modifiedFiles(implResult.ToolCalls))
		return 0, nil, "", false
	}

	return implResult.Iterations, implObs, v, true
}

// runPublishPhase builds and runs the publish loop, assembles the final result,
// and posts the completion comment. Extracted from runWardenAgent to keep that
// function within the linter's statement-count limit.
func (o *Orchestrator) runPublishPhase(
	ctx context.Context,
	session *Session,
	agentLLM llms.Model,
	ws *agentWorkspace,
	branch, verdict string,
	implIterations int,
	implObs *loopObserver,
	tracker *progressTracker,
) {
	pubObs := newLoopObserver(o.logger, session.ID, "publish")
	pubLoop, err := o.buildPublishLoop(agentLLM, session, ws, branch, tracker, pubObs)
	if err != nil {
		o.failSession(ctx, session, fmt.Sprintf("build publish loop: %v", err))
		return
	}

	pubTask := goframeagent.Task{
		ID:          session.ID + "-pub",
		Description: fmt.Sprintf("Push branch and open PR for issue #%d", session.Issue.Number),
		Context:     fmt.Sprintf("Branch: %s\nAll changes have been reviewed and approved. Push and open a draft PR.", branch),
		Priority:    5,
	}

	o.logger.Info("warden: starting publish loop", "session_id", session.ID)
	pubResult, pubErr := pubLoop.Run(ctx, pubTask, nil)

	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()

	if pubErr != nil {
		o.logger.Error("warden: publish loop failed",
			"session_id", session.ID, "iterations", pubResult.Iterations, "error", pubErr)
		o.failSession(ctx, session, fmt.Sprintf("publish loop: %v", pubErr))
		return
	}

	// Accumulate token counts across both phases from LoopResult.Tokens.
	var totalIn, totalOut float64
	if implObs != nil {
		totalIn += implObs.totalIn
		totalOut += implObs.totalOut
	}
	totalIn += pubObs.totalIn
	totalOut += pubObs.totalOut

	result := &Result{
		Branch:       branch,
		Verdict:      verdict,
		Iterations:   implIterations + pubResult.Iterations,
		TokensInput:  int64(totalIn),
		TokensOutput: int64(totalOut),
	}
	if prInfo := extractPRInfo(pubResult.Response); prInfo != nil {
		result.PRNumber = prInfo.PRNumber
		result.PRURL = prInfo.PRURL
	}
	if files := o.mcpServer.GetReviewFilesBySession(session.ID); files != nil {
		result.FilesChanged = files
	}

	session.SetResult(result)
	session.SetStatus(StatusCompleted)
	o.postSessionCompleted(ctx, session, result)

	o.logger.Info("warden: completed",
		"session_id", session.ID,
		"verdict", result.Verdict,
		"total_iterations", result.Iterations,
		"impl_iterations", implIterations,
		"pub_iterations", pubResult.Iterations,
		"pr_url", result.PRURL,
	)
}

// buildImplementLoop builds the agent loop for the implement phase.
// All MCP tools EXCEPT push_branch and create_pull_request are included,
// plus file tools and LSP tools. plan is the output of the planning phase
// and is embedded in the system prompt to give the model a head start.
func (o *Orchestrator) buildImplementLoop(agentLLM llms.Model, session *Session, ws *agentWorkspace, plan string, tracker *progressTracker, obs *loopObserver) (*goframeagent.AgentLoop, error) {
	registry := goframeagent.NewRegistry()
	allowedTools := make(map[string]bool)

	// MCP tools — exclude publish tools; wrap review_code with the cap.
	for _, t := range o.mcpServer.Tools() {
		if publishToolNames[t.Name()] {
			continue // reserved for publish loop
		}
		tool := mcp.Tool(t) //nolint:unconvert // mcp.Tool is an interface; explicit for clarity
		if t.Name() == "review_code" {
			tool = &reviewCapTool{inner: t}
		}
		registerTool(registry, allowedTools, tool, ws, session.ID, tracker, o.logger)
	}

	// File tools (no LSP — agent uses run_command for compile checks).
	for _, t := range fileTools() {
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})
	maxIter := max(o.config.MaxIterations*15, 50)

	loopLogger := o.logger.With("session_id", session.ID, "phase", "implement")
	return goframeagent.NewAgentLoop(agentLLM, registry,
		goframeagent.WithLoopSystemPrompt(o.buildImplementSystemPrompt(session.Issue, ws.dir, false, plan, ws.projectContext)),
		goframeagent.WithLoopMaxIterations(maxIter),
		goframeagent.WithLoopGovernance(governance),
		goframeagent.WithLoopCompactionHook(o.buildCompactionHook(session, ws.traceFile, agentLLM)),
		goframeagent.WithLoopLogger(loopLogger),
		goframeagent.WithLoopObserver(obs),
	)
}

// buildPublishLoop builds the agent loop for the publish phase.
// Only push_branch and create_pull_request are available.
func (o *Orchestrator) buildPublishLoop(agentLLM llms.Model, session *Session, ws *agentWorkspace, branch string, tracker *progressTracker, obs *loopObserver) (*goframeagent.AgentLoop, error) {
	registry := goframeagent.NewRegistry()
	allowedTools := make(map[string]bool)

	for _, t := range o.mcpServer.Tools() {
		if !publishToolNames[t.Name()] {
			continue // implement-phase tools are not needed here
		}
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})

	loopLogger := o.logger.With("session_id", session.ID, "phase", "publish")
	return goframeagent.NewAgentLoop(agentLLM, registry,
		goframeagent.WithLoopSystemPrompt(o.buildPublishSystemPrompt(session.Issue, branch)),
		goframeagent.WithLoopMaxIterations(8), // push + PR creation
		goframeagent.WithLoopGovernance(governance),
		goframeagent.WithLoopLogger(loopLogger),
		goframeagent.WithLoopObserver(obs),
	)
}

// buildImplementSystemPrompt returns the system prompt for the implement loop.
// Publish tools are intentionally omitted — the model has no reason to
// attempt pushing before the review is complete.
func (o *Orchestrator) buildImplementSystemPrompt(issue Issue, workspaceDir string, _ bool, plan string, projectContext string) string {
	base := fmt.Sprintf(`You are an expert software engineer implementing GitHub issue #%d.

## Task
Title: %s
Description:
%s

## Workspace
Working directory: %s

## Available Tools

**Code exploration** (repository-indexed, read-only):
- search_code(query) — semantic search over the codebase
- get_symbol(name) — find a symbol definition
- get_structure() — project structure overview
- get_arch_context(dir) — architecture summary for a directory
- find_usages(symbol), get_callers(fn), get_callees(fn)

**File operations** (workspace-scoped, paths relative to working directory):
- read_file(path, offset?, limit?) — read a file, optionally paginated
- write_file(path, content) — create or overwrite a file
- edit_file(path, old_string, new_string) — replace an exact string in a file
- list_dir(path?) — list directory contents

**Verification**:
- run_command(command) — run whitelisted commands: "make lint", "make test"
- review_code — request an automated code review of your changes

## Workflow
1. **Explore** — use search_code / get_symbol / list_dir / read_file to understand the code.
2. **Implement** — use write_file / edit_file. Prefer edit_file for targeted changes.
3. **Verify** — run_command("make lint"), then run_command("make test"). Fix failures.
4. **Review** — call review_code. If REQUEST_CHANGES, fix and re-verify. Repeat until APPROVE.

## Rules
- Paths are relative to the working directory.
- Always run lint and tests before calling review_code.
- Your work here is done when review_code returns APPROVE. Do not attempt to push or open a PR.
- Keep changes minimal and focused on the issue.

%s`,
		issue.Number, issue.Title, truncateString(issue.Body, 2000), workspaceDir, plan)

	if projectContext != "" {
		base += "\n\n## Project Conventions\n\n" + projectContext
	}

	return base
}

// buildPublishSystemPrompt returns the system prompt for the publish loop.
func (o *Orchestrator) buildPublishSystemPrompt(issue Issue, branch string) string {
	return fmt.Sprintf(`The implementation for GitHub issue #%d ("%s") has been reviewed and approved.

Your task is to publish the changes:
1. Call push_branch to push branch "%s" to the remote.
2. Call create_pull_request to open a draft pull request referencing issue #%d.

Available tools: push_branch, create_pull_request.

Do not make any code changes. Do not review. Just push and open the PR.`,
		issue.Number, issue.Title, branch, issue.Number)
}

// compactionThreshold is the fraction of input tokens (relative to a conservative
// 128K ceiling) at which we compact the conversation history. GLM-5.1 and
// MiniMax M2.7 both support 198K+ context, but we compact well before the limit
// to leave room for tool outputs in the current iteration.
const compactionThreshold = 0.70

// compactionContextCeiling is the conservative token ceiling used to compute
// the 70% threshold. Models support more, but we stay well within headroom.
const compactionContextCeiling = 128_000

// compactionSummaryMarker prefixes compaction summary messages so we can detect
// them on subsequent compactions and use the iterative update prompt.
const compactionSummaryMarker = "## Context Summary (earlier conversation compacted)\n\n"

// compactionExplorationTools is the set of tool names whose outputs are stripped
// before the LLM summarisation step. These are read-only discovery calls —
// large, noisy, and safe to discard because the agent can re-run them if needed.
var compactionExplorationTools = map[string]bool{
	"read_file":        true,
	"list_dir":         true,
	"search_code":      true,
	"get_symbol":       true,
	"get_structure":    true,
	"get_arch_context": true,
	"find_usages":      true,
	"get_callers":      true,
	"get_callees":      true,
}

// compactionUpdatePrompt is used when a previous compaction summary already exists.
// It takes the old summary + recent messages and produces an updated summary.
const compactionUpdatePrompt = `You are updating a coding agent's context summary.

An earlier summary of the conversation exists below. New messages have been
added since that summary. Produce an UPDATED summary (max 400 words) that
merges the old summary with the new information.

Preserve from the old summary:
- Which files were read and what was found
- Which files were edited and what changes were made
- Results of lint/test runs (pass/fail, key errors)
- Review verdicts (APPROVE/REQUEST_CHANGES)
- Outstanding issues or next steps

Update with new information:
- Any new files read or edited since the summary
- New test/lint results
- New review verdicts
- Current progress on the task

Do not include any preamble. Output only the updated summary text.

--- OLD SUMMARY ---
%s

--- NEW MESSAGES ---
%s`

// compactionFreshPrompt is used when there is no previous summary.
const compactionFreshPrompt = `You are summarizing a coding agent's conversation history to save context space.

Below is the conversation so far (excluding the system prompt). Produce a concise
summary (max 400 words) that preserves:
- Which files were read and what was found
- Which files were edited and what changes were made
- Results of lint / test runs (pass/fail, key errors)
- Any review_code verdicts received
- Outstanding issues or next steps the agent was working on

Do not include any preamble. Output only the summary text.

--- CONVERSATION ---
%s`

// compactionMaxOutputLen is the maximum length of any single tool result or
// assistant message that gets included in the compaction transcript. Anything
// beyond this is truncated with a marker. This mirrors Pi's approach of
// truncating tool results to ~2000 chars during summarization to prevent
// large search_code or read_file outputs from overflowing the summary context.
const compactionMaxOutputLen = 2000

// extractPreviousSummary scans messages for an existing compaction summary and
// returns it along with only the messages that came after it. If no previous
// summary exists, it returns all messages after the system prompt.
func extractPreviousSummary(msgs []schema.MessageContent) (previousSummary string, newMsgs []schema.MessageContent) {
	for _, m := range msgs[1:] {
		for _, part := range m.Parts {
			if p, ok := part.(schema.TextContent); ok {
				if strings.HasPrefix(p.Text, compactionSummaryMarker) && string(m.Role) == "human" {
					previousSummary = strings.TrimPrefix(p.Text, compactionSummaryMarker)
					newMsgs = nil
					continue
				}
			}
		}
		newMsgs = append(newMsgs, m)
	}
	return previousSummary, newMsgs
}

// compactionFilterText replaces the output of exploration tool calls with a
// short placeholder and truncates large tool outputs to fit within the
// compaction context budget. Write-side and verification tool outputs are
// truncated rather than omitted entirely.
func compactionFilterText(text string) string {
	for name := range compactionExplorationTools {
		prefix := name + ":"
		if strings.HasPrefix(text, prefix) {
			return prefix + " [output omitted during compaction]"
		}
	}
	if len(text) > compactionMaxOutputLen {
		return text[:compactionMaxOutputLen] + "\n... [truncated during compaction]"
	}
	return text
}

// buildCompactionHook returns a goframe WithLoopCompactionHook callback that
// summarizes the conversation history when token usage exceeds 70% of the
// conservative context ceiling. The system prompt (first message) and the most
// recent user/tool messages are preserved verbatim; everything in between is
// replaced with a one-paragraph summary produced by the same LLM.
//
// If the LLM call for summarization fails the hook returns nil, leaving the
// history unchanged and letting the loop continue naturally.
func (o *Orchestrator) buildCompactionHook(session *Session, traceFile *os.File, llm llms.Model) func(ctx context.Context, msgs []schema.MessageContent, tokens goframeagent.TokenUsage) []schema.MessageContent {
	return func(ctx context.Context, msgs []schema.MessageContent, tokens goframeagent.TokenUsage) []schema.MessageContent {
		if traceFile != nil {
			writeTrace(traceFile, msgs, tokens)
		}

		used := tokens.Input + tokens.Output
		threshold := float64(compactionContextCeiling) * compactionThreshold
		if used < threshold {
			return nil
		}

		o.logger.Info("warden: context approaching limit, compacting",
			"session_id", session.ID,
			"tokens_used", used,
			"threshold", threshold,
			"messages", len(msgs),
		)

		summary, err := o.compactMessages(ctx, llm, msgs)
		if err != nil {
			o.logger.Warn("warden: compaction failed, skipping", "error", err)
			return nil
		}

		o.logger.Info("warden: context compacted",
			"session_id", session.ID,
			"summary_len", len(summary),
			"original_messages", len(msgs),
		)

		tail := msgs
		if len(msgs) > 9 {
			tail = msgs[len(msgs)-8:]
		}

		compacted := make([]schema.MessageContent, 0, 2+len(tail))
		compacted = append(compacted, msgs[0])
		compacted = append(compacted,
			schema.NewHumanMessage(compactionSummaryMarker+summary),
		)
		compacted = append(compacted, tail...)
		return compacted
	}
}

// compactMessages produces a conversation summary using iterative compaction.
// If a previous summary exists in the messages, it updates it rather than
// summarizing from scratch — more efficient and preserves continuity.
func (o *Orchestrator) compactMessages(ctx context.Context, llm llms.Model, msgs []schema.MessageContent) (string, error) {
	previousSummary, newMsgs := extractPreviousSummary(msgs)

	var transcript strings.Builder
	for _, m := range newMsgs {
		role := string(m.Role)
		for _, part := range m.Parts {
			if p, ok := part.(schema.TextContent); ok {
				text := compactionFilterText(p.Text)
				fmt.Fprintf(&transcript, "[%s] %s\n\n", role, text)
			}
		}
	}

	var summaryPrompt string
	if previousSummary != "" {
		summaryPrompt = fmt.Sprintf(compactionUpdatePrompt, previousSummary, transcript.String())
	} else {
		summaryPrompt = fmt.Sprintf(compactionFreshPrompt, transcript.String())
	}

	resp, err := llm.GenerateContent(ctx,
		[]schema.MessageContent{schema.NewHumanMessage(summaryPrompt)},
	)
	if err != nil || len(resp.Choices) == 0 {
		return "", fmt.Errorf("LLM summarization failed: %w", err)
	}

	return resp.Choices[0].Content, nil
}

// errNoChanges is returned by yieldCommitAndPush when the workspace has no
// uncommitted changes to push. The caller should skip PR creation and post
// a "no changes were made" comment instead.
var errNoChanges = fmt.Errorf("no changes to commit")

// yieldDraftPR is called when the implement loop ends without an APPROVE verdict.
// Instead of silently failing, it commits any pending changes, pushes the branch,
// and opens a draft PR so a human can pick up where the agent left off.
// When there are no changes to push, it posts an explanatory comment and marks
// the session failed rather than creating an empty draft PR.
// Errors are logged but never returned — the session status is set regardless.
// modifiedFiles extracts the unique set of file paths written or edited
// during the implement loop from the recorded tool calls.
func modifiedFiles(calls []goframeagent.ToolCallRecord) []string {
	seen := make(map[string]struct{})
	var files []string
	for _, c := range calls {
		if c.Name != "write_file" && c.Name != "edit_file" {
			continue
		}
		path, _ := c.Params["path"].(string)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; !ok {
			seen[path] = struct{}{}
			files = append(files, path)
		}
	}
	return files
}

func (o *Orchestrator) yieldDraftPR(
	ctx context.Context,
	session *Session,
	ws *agentWorkspace,
	branch string,
	iterations int,
	tokensIn, tokensOut float64,
	editedFiles []string,
) {
	o.logger.Info("warden: yielding draft PR", "session_id", session.ID, "branch", branch)

	baseBranch := o.config.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	// ── Commit + push ───────────────────────────────────────────────────────
	// The workspace remote already has the GitHub token embedded in its URL
	// (set by prepareAgentWorkspace), so no token injection is needed here.
	pushErr := yieldCommitAndPush(ctx, ws.dir, branch, editedFiles, o.logger)
	if pushErr != nil && !strings.Contains(pushErr.Error(), errNoChanges.Error()) {
		o.logger.Warn("warden: yieldDraftPR: push failed, session will still be marked draft",
			"session_id", session.ID, "error", pushErr)
	}

	// When the agent made no file changes at all, a draft PR would be empty and
	// confusing. Skip PR creation and fall through to a descriptive failure comment.
	if pushErr != nil && strings.Contains(pushErr.Error(), errNoChanges.Error()) {
		o.logger.Info("warden: no changes to push, marking session failed instead of creating empty draft",
			"session_id", session.ID)
		o.failSession(ctx, session, fmt.Sprintf(
			"implement loop ended without APPROVE and without any code changes after %d iterations", iterations))
		return
	}

	// ── Create draft PR ─────────────────────────────────────────────────────
	prURL := ""
	if o.ghClient != nil {
		pr, err := o.ghClient.CreatePullRequest(ctx, session.Issue.RepoOwner, session.Issue.RepoName, gh.PullRequestOptions{
			Title: fmt.Sprintf("WIP: %s (draft — needs human review)", session.Issue.Title),
			Body: fmt.Sprintf(
				"## Draft — automatic yield\n\n"+
					"The implementation agent could not achieve an APPROVE verdict after %d iterations.\n"+
					"Partial work has been pushed to branch `%s` for human review.\n\n"+
					"Closes #%d",
				iterations, branch, session.Issue.Number,
			),
			Head:  branch,
			Base:  baseBranch,
			Draft: true,
		})
		if err != nil {
			o.logger.Warn("warden: yieldDraftPR: failed to create draft PR",
				"session_id", session.ID, "error", err)
		} else {
			prURL = pr.GetHTMLURL()
			o.logger.Info("warden: draft PR created", "session_id", session.ID, "url", prURL)
		}
	}

	// ── Persist + comment ────────────────────────────────────────────────────
	result := &Result{
		Branch:       branch,
		Verdict:      "HUMAN_REVIEW_REQUIRED",
		Iterations:   iterations,
		PRURL:        prURL,
		TokensInput:  int64(tokensIn),
		TokensOutput: int64(tokensOut),
	}
	if files := o.mcpServer.GetReviewFilesBySession(session.ID); files != nil {
		result.FilesChanged = files
	}

	session.SetResult(result)
	session.SetStatus(StatusDraft)
	o.persistSessionCompleted(ctx, session, result)

	body := fmt.Sprintf(
		"⚠️ **Draft PR created** — session `%s`\n\n"+
			"The agent made partial progress but could not reach an APPROVE verdict after %d iterations.\n",
		session.ID, iterations,
	)
	if prURL != "" {
		body += fmt.Sprintf("\n**Draft PR:** %s\n\nA human can review the partial work, continue on branch `%s`, or close the draft.", prURL, branch)
	} else {
		body += fmt.Sprintf("\nBranch `%s` was pushed with partial changes. Open a draft PR manually to continue.", branch)
	}
	o.postIssueComment(ctx, session.Issue, body)
}

// yieldCommitAndPush stages all pending changes, commits, and pushes the branch.
// Returns errNoChanges when the workspace is clean (nothing to commit or push).
// The workspace remote URL already contains authentication (set up by
// prepareAgentWorkspace), so no explicit token injection is needed.
func yieldCommitAndPush(ctx context.Context, workspaceDir, branch string, editedFiles []string, logger *slog.Logger) error {
	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = workspaceDir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// Ensure we are on the right branch.
	logger.Info("yieldCommitAndPush: checking out branch", "branch", branch)
	if _, err := run("checkout", branch); err != nil {
		if _, err2 := run("checkout", "-b", branch); err2 != nil {
			logger.Error("yieldCommitAndPush: git checkout -b failed", "branch", branch, "error", err2)
			return fmt.Errorf("git checkout -b %s: %w", branch, err2)
		}
	}

	// Stage only files the agent explicitly wrote or edited.
	// Fall back to `git add .` if no file list is available (shouldn't happen).
	logger.Info("yieldCommitAndPush: staging changes", "files", editedFiles)
	if len(editedFiles) > 0 {
		args := append([]string{"add", "--"}, editedFiles...)
		_, _ = run(args...)
	} else {
		_, _ = run("add", ".")
	}

	// Commit — detect "nothing to commit" and surface it as errNoChanges so the
	// caller can skip PR creation rather than pushing an empty branch.
	logger.Info("yieldCommitAndPush: committing")
	out, err := run("commit", "-m", "WIP: automated partial implementation")
	if err != nil {
		if strings.Contains(out, "nothing to commit") {
			logger.Info("yieldCommitAndPush: nothing to commit, skipping push")
			return errNoChanges
		}
		logger.Error("yieldCommitAndPush: git commit failed", "error", err, "output", out)
		return fmt.Errorf("git commit: %w (output: %s)", err, out)
	}

	// Push — the remote URL already has the token embedded.
	logger.Info("yieldCommitAndPush: pushing branch", "branch", branch)
	if out, err := run("push", "-u", "origin", branch); err != nil {
		logger.Error("yieldCommitAndPush: git push failed", "branch", branch, "error", err, "output", out)
		return fmt.Errorf("git push: %w (output: %s)", err, out)
	}
	logger.Info("yieldCommitAndPush: pushed successfully", "branch", branch)
	return nil
}

// writeTrace appends a JSONL snapshot of the current conversation to the trace
// file. Each line is a JSON object: {tokens, message_count, messages}.
// The file survives session completion and can be used for post-mortem debugging
// or replaying the session from a specific iteration.
// Errors are silently dropped — tracing is best-effort.
func writeTrace(f *os.File, msgs []schema.MessageContent, tokens goframeagent.TokenUsage) {
	type traceEntry struct {
		TokensIn  float64                 `json:"tokens_in"`
		TokensOut float64                 `json:"tokens_out"`
		MsgCount  int                     `json:"msg_count"`
		Messages  []schema.MessageContent `json:"messages"`
	}
	entry := traceEntry{
		TokensIn:  tokens.Input,
		TokensOut: tokens.Output,
		MsgCount:  len(msgs),
		Messages:  msgs,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	line = append(line, '\n')
	_, _ = f.Write(line)
}
