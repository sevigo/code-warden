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
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"

	"github.com/sevigo/code-warden/internal/agent/lsp"
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
const maxReviewRounds = 5

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
	if ws.lsp != nil {
		defer ws.lsp.Stop()
	}
	defer o.mcpServer.UnregisterWorkspace(session.ID)

	// Persist the session as "running" now that the workspace and branch are ready.
	o.persistSessionRunning(ctx, session, branch)

	// ── LSP mode label ────────────────────────────────────────────────────────
	lspMode := "degraded (RAG-only)"
	if ws.lsp != nil && ws.lsp.Available() {
		lspMode = "precise (LSP)"
	}

	// ── Progress tracker ─────────────────────────────────────────────────────
	// Intercepts every tool call to write real-time log lines and EDIT a single
	// GitHub status comment every 30 s rather than posting new ones each time.
	tracker := newProgressTracker(
		session,
		ws.logFile,
		lspMode,
		func(tctx context.Context, body string) int64 {
			return o.createIssueComment(tctx, session.Issue, body)
		},
		func(tctx context.Context, id int64, body string) {
			o.updateIssueComment(tctx, session.Issue, id, body)
		},
	)
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
	implIter, implTokIn, implTokOut, verdict, ok := o.runImplementPhase(ctx, session, agentLLM, ws, branch, plan, tracker)
	if !ok {
		return
	}

	// ── Loop 2: Publish ──────────────────────────────────────────────────────
	tracker.setPhase("publishing")
	o.runPublishPhase(ctx, session, agentLLM, ws, branch, verdict, implIter, implTokIn, implTokOut, tracker)
}

// runImplementPhase builds and runs the implement loop. Returns the iteration
// count, token usage, final verdict, and whether to continue to the publish phase.
// Returns (0, 0, 0, "", false) and calls failSession on any error.
func (o *Orchestrator) runImplementPhase(
	ctx context.Context,
	session *Session,
	agentLLM llms.Model,
	ws *agentWorkspace,
	branch, plan string,
	tracker *progressTracker,
) (iterations int, tokensIn, tokensOut float64, verdict string, ok bool) {
	implLoop, err := o.buildImplementLoop(agentLLM, session, ws, plan, tracker)
	if err != nil {
		o.failSession(ctx, session, fmt.Sprintf("build implement loop: %v", err))
		return 0, 0, 0, "", false
	}

	implTask := goframeagent.Task{
		ID:          session.ID + "-impl",
		Description: fmt.Sprintf("Implement GitHub issue #%d: %s", session.Issue.Number, session.Issue.Title),
		Context:     o.buildNativeTaskContext(session.Issue, branch),
		Priority:    5,
	}

	o.logger.Info("warden: starting implement loop", "session_id", session.ID)
	implResult, implErr := implLoop.Run(ctx, implTask, nil)
	if implErr != nil {
		o.logger.Error("warden: implement loop failed",
			"session_id", session.ID, "iterations", implResult.Iterations, "error", implErr)
		o.failSession(ctx, session, fmt.Sprintf("implement loop: %v", implErr))
		return 0, 0, 0, "", false
	}

	o.logger.Info("warden: implement loop done",
		"session_id", session.ID,
		"iterations", implResult.Iterations,
		"tokens_in", implResult.Tokens.Input,
		"tokens_out", implResult.Tokens.Output,
	)

	v, _, _ := o.mcpServer.GetReviewBySession(session.ID)
	if v != "APPROVE" {
		msg := fmt.Sprintf(
			"implement loop completed without APPROVE verdict (got %q) after %d iterations",
			v, implResult.Iterations,
		)
		o.logger.Warn("warden: "+msg, "session_id", session.ID)
		o.failSession(ctx, session, msg)
		return 0, 0, 0, "", false
	}

	return implResult.Iterations, implResult.Tokens.Input, implResult.Tokens.Output, v, true
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
	implTokensIn, implTokensOut float64,
	tracker *progressTracker,
) {
	pubLoop, err := o.buildPublishLoop(agentLLM, session, ws, branch, tracker)
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

	result := &Result{
		Branch:       branch,
		Verdict:      verdict,
		Iterations:   implIterations + pubResult.Iterations,
		TokensInput:  int64(implTokensIn + pubResult.Tokens.Input),
		TokensOutput: int64(implTokensOut + pubResult.Tokens.Output),
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
func (o *Orchestrator) buildImplementLoop(agentLLM llms.Model, session *Session, ws *agentWorkspace, plan string, tracker *progressTracker) (*goframeagent.AgentLoop, error) {
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

	// File tools (with LSP diagnostic hook).
	for _, t := range fileTools(ws.lsp) {
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	// LSP tools — only if a language server is running.
	for _, t := range lsp.Tools(ws.lsp) {
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}
	if ws.lsp != nil && ws.lsp.Available() {
		o.logger.Info("buildImplementLoop: LSP tools registered", "session_id", session.ID)
	}

	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})
	maxIter := max(o.config.MaxIterations*10, 30)

	return goframeagent.NewAgentLoop(agentLLM, registry,
		goframeagent.WithLoopSystemPrompt(o.buildImplementSystemPrompt(session.Issue, ws.dir, ws.lsp != nil && ws.lsp.Available(), plan)),
		goframeagent.WithLoopMaxIterations(maxIter),
		goframeagent.WithLoopGovernance(governance),
		goframeagent.WithLoopCompactionHook(o.buildCompactionHook(session, ws.traceFile)),
	)
}

// buildPublishLoop builds the agent loop for the publish phase.
// Only push_branch and create_pull_request are available.
func (o *Orchestrator) buildPublishLoop(agentLLM llms.Model, session *Session, ws *agentWorkspace, branch string, tracker *progressTracker) (*goframeagent.AgentLoop, error) {
	registry := goframeagent.NewRegistry()
	allowedTools := make(map[string]bool)

	for _, t := range o.mcpServer.Tools() {
		if !publishToolNames[t.Name()] {
			continue // implement-phase tools are not needed here
		}
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})

	return goframeagent.NewAgentLoop(agentLLM, registry,
		goframeagent.WithLoopSystemPrompt(o.buildPublishSystemPrompt(session.Issue, branch)),
		goframeagent.WithLoopMaxIterations(5), // push + PR creation should need at most 2-3 turns
		goframeagent.WithLoopGovernance(governance),
	)
}

// buildImplementSystemPrompt returns the system prompt for the implement loop.
// Publish tools are intentionally omitted — the model has no reason to
// attempt pushing before the review is complete.
func (o *Orchestrator) buildImplementSystemPrompt(issue Issue, workspaceDir string, lspAvailable bool, plan string) string {
	lspSection := ""
	if lspAvailable {
		lspSection = `
**LSP** (live compiler feedback):
- lsp_diagnostics(path) — get compiler errors/warnings for a file
- lsp_definition(path, line, column) — jump to definition of a symbol
- lsp_references(path, line, column) — find all usages of a symbol
- lsp_hover(path, line, column) — get type info and docs for a symbol
Note: write_file and edit_file automatically return diagnostics — check them.`
	}

	return fmt.Sprintf(`You are an expert software engineer implementing GitHub issue #%d.

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
%s
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
		issue.Number, issue.Title, truncateString(issue.Body, 2000), workspaceDir, lspSection, plan)
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
	"lsp_definition":   true,
	"lsp_references":   true,
	"lsp_hover":        true,
}

// compactionFilterText replaces the output of exploration tool calls with a
// short placeholder. Write-side and verification tool outputs are passed through
// unchanged. The heuristic is simple: if the text starts with a known tool
// prefix (as JSON-serialised by goframe's tool-result formatting), strip it.
func compactionFilterText(text string) string {
	for name := range compactionExplorationTools {
		// goframe formats tool results as: `tool_name: <JSON output>`
		prefix := name + ":"
		if strings.HasPrefix(text, prefix) {
			return prefix + " [output omitted during compaction]"
		}
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
func (o *Orchestrator) buildCompactionHook(session *Session, traceFile *os.File) func(ctx context.Context, msgs []schema.MessageContent, tokens goframeagent.TokenUsage) []schema.MessageContent {
	return func(ctx context.Context, msgs []schema.MessageContent, tokens goframeagent.TokenUsage) []schema.MessageContent {
		// Always write a trace snapshot for post-mortem debugging, regardless
		// of whether compaction fires. This gives a per-iteration conversation
		// dump that survives session completion.
		if traceFile != nil {
			writeTrace(traceFile, msgs, tokens)
		}

		used := tokens.Input + tokens.Output
		threshold := float64(compactionContextCeiling) * compactionThreshold
		if used < threshold {
			return nil // still plenty of room
		}

		o.logger.Info("warden: context approaching limit, compacting",
			"session_id", session.ID,
			"tokens_used", used,
			"threshold", threshold,
			"messages", len(msgs),
		)

		// Build a pre-filtered transcript for the summarization LLM.
		// Exploration tool outputs (read_file, search_code, list_dir, etc.) are
		// replaced with a one-line placeholder — they are pure discovery noise that
		// the LLM would otherwise spend tokens summarizing inaccurately.
		// Write-side results (write_file, edit_file, review_code, run_command) are
		// preserved verbatim so the summary reliably reflects every code change.
		var transcript strings.Builder
		for _, m := range msgs[1:] {
			role := string(m.Role)
			for _, part := range m.Parts {
				if p, ok := part.(schema.TextContent); ok {
					text := compactionFilterText(p.Text)
					fmt.Fprintf(&transcript, "[%s] %s\n\n", role, text)
				}
			}
		}

		summaryPrompt := fmt.Sprintf(`You are summarizing a coding agent's conversation history to save context space.

Below is the conversation so far (excluding the system prompt). Produce a concise
summary (max 400 words) that preserves:
- Which files were read and what was found
- Which files were edited and what changes were made
- Results of lint / test runs (pass/fail, key errors)
- Any review_code verdicts received
- Outstanding issues or next steps the agent was working on

Do not include any preamble. Output only the summary text.

--- CONVERSATION ---
%s`, transcript.String())

		agentLLM, err := o.resolveAgentLLM(ctx)
		if err != nil {
			o.logger.Warn("warden: compaction: failed to resolve LLM, skipping", "error", err)
			return nil
		}

		resp, err := agentLLM.GenerateContent(ctx,
			[]schema.MessageContent{schema.NewHumanMessage(summaryPrompt)},
		)
		if err != nil || len(resp.Choices) == 0 {
			o.logger.Warn("warden: compaction: LLM summarization failed, skipping", "error", err)
			return nil
		}

		summary := resp.Choices[0].Content
		o.logger.Info("warden: context compacted",
			"session_id", session.ID,
			"summary_len", len(summary),
			"original_messages", len(msgs),
		)

		// Rebuild: system prompt + compacted summary + last 8 messages (tool results
		// from the current iteration so the model has immediate context).
		tail := msgs
		if len(msgs) > 9 {
			tail = msgs[len(msgs)-8:]
		}

		compacted := make([]schema.MessageContent, 0, 2+len(tail))
		compacted = append(compacted, msgs[0]) // system prompt
		compacted = append(compacted,
			schema.NewHumanMessage("## Context Summary (earlier conversation compacted)\n\n"+summary),
		)
		compacted = append(compacted, tail...)
		return compacted
	}
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
