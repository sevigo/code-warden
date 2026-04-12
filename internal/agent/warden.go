package agent

// warden.go — "warden" agent mode: plan → edit → review → publish.
//
//  Loop 1 — Plan (read-only, max 8 iterations):
//    Explores the codebase and produces a structured implementation plan.
//
//  Loop 2 — Edit (all tools except review_code and publish tools):
//    Implements changes, verifies with make build/lint/test.
//
//  Review state machine (orchestrator-driven, not agent-driven):
//    The Go orchestrator runs the proven RAG code review directly (same pipeline
//    as the /review command), then spawns a restricted "fix loop" if changes are
//    requested. This guarantees the review always runs and solves the diff-acquisition
//    problem (the LLM had no way to produce the diff required by review_code).
//
//  Loop 3 — Publish (only if review returns APPROVE):
//    push_branch + create_pull_request

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"

	"github.com/sevigo/code-warden/internal/core"
	gh "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/mcp/tools"
	ragreview "github.com/sevigo/code-warden/internal/rag/review"
	reviewpkg "github.com/sevigo/code-warden/internal/review"
)

// publishToolNames are withheld from the implement loop and reserved for the
// publish loop. Keeping this as an explicit set makes it easy to audit.
var publishToolNames = map[string]bool{
	"push_branch":         true,
	"create_pull_request": true,
}

// maxReviewRounds is the maximum number of orchestrator-driven review+fix
// cycles before we give up and yield a draft PR for human review.
const maxReviewRounds = 10

// fixIterationsPerRound is the LLM iteration budget for each fix loop.
// Each review round that returns REQUEST_CHANGES spawns a fresh fix loop
// with this many iterations to address the reported issues.
const fixIterationsPerRound = 8

// editFileName and writeFileName are the canonical tool names used in
// compaction helpers. Defined as constants to satisfy goconst and to make
// any future renames a single-point change.
const (
	editFileName  = "edit_file"
	writeFileName = "write_file"
)

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

	o.logger.Info("🛠️  IMPLEMENTATION: Starting warden agent (phased: edit → review → publish)",
		"session_id", session.ID,
		"working_dir", ws.dir,
		"timeout", o.config.Timeout,
		"model", agentLLM,
	)

	// ── Planning phase ───────────────────────────────────────────────────────
	tracker.setPhase("planning")
	plan := o.buildPlan(ctx, agentLLM, session, ws, tracker)
	o.logger.Info("warden: planning complete, starting edit loop", "session_id", session.ID)

	// ── Loop 1: Edit + Review state machine ──────────────────────────────────
	// runImplementPhase runs the edit loop then the orchestrator-driven review.
	implIter, implObs, verdict, ok := o.runImplementPhase(ctx, session, agentLLM, ws, branch, plan, tracker)
	if !ok {
		return
	}

	// ── Loop 2: Publish ──────────────────────────────────────────────────────
	tracker.setPhase("publishing")
	o.runPublishPhase(ctx, session, agentLLM, ws, branch, verdict, implIter, implObs, tracker)
}

// runImplementPhase runs the edit loop then the orchestrator-driven review
// state machine. The edit loop gets the full iteration budget; the review phase
// is handled by Go (not the LLM), so no iterations need to be reserved for it.
// Returns (total iterations, observer, verdict, ok). Calls failSession on error.
func (o *Orchestrator) runImplementPhase(
	ctx context.Context,
	session *Session,
	agentLLM llms.Model,
	ws *agentWorkspace,
	branch, plan string,
	tracker *progressTracker,
) (iterations int, obs *loopObserver, verdict string, ok bool) {
	editBudget := max(o.config.MaxIterations, 50)

	editObs := newLoopObserver(o.logger, session.ID, "edit")
	editLoop, err := o.buildEditLoop(agentLLM, session, ws, plan, tracker, editObs, editBudget)
	if err != nil {
		o.failSession(ctx, session, fmt.Sprintf("build edit loop: %v", err))
		return 0, nil, "", false
	}

	editTask := goframeagent.Task{
		ID:          session.ID + "-impl",
		Description: fmt.Sprintf("Implement GitHub issue #%d: %s", session.Issue.Number, session.Issue.Title),
		Context:     o.buildNativeTaskContext(session.Issue, branch),
		Priority:    5,
	}

	tracker.setPhase("editing")
	o.logger.Info("warden: starting edit loop", "session_id", session.ID, "budget", editBudget)
	editResult, editErr := editLoop.Run(ctx, editTask, nil)
	editIters := 0
	if editResult != nil {
		editIters = editResult.Iterations
	}

	if editErr != nil && !errors.Is(editErr, goframeagent.ErrMaxIterations) {
		o.logger.Error("warden: edit loop failed",
			"session_id", session.ID, "iterations", editIters, "error", editErr)
		o.failSession(ctx, session, fmt.Sprintf("edit loop: %v", editErr))
		return 0, nil, "", false
	}
	if errors.Is(editErr, goframeagent.ErrMaxIterations) {
		o.logger.Warn("warden: edit loop hit max iterations, transitioning to review",
			"session_id", session.ID, "iterations", editIters)
	}

	changedFiles := []string{}
	if editResult != nil {
		changedFiles = modifiedFiles(editResult.ToolCalls)
	}

	// ── Batch format ──────────────────────────────────────────────────────────
	// Run the project's format_command once before review (e.g. "npm run format").
	// This is configured in .code-warden.yml and is separate from per-write
	// Go formatting (which runs inside write_file/edit_file via the Formatter).
	formatNote := o.formatProject(ctx, ws)

	// ── Review+fix loop ─────────────────────────────────────────────────────
	tracker.setPhase("reviewing")
	return o.runReviewPhase(ctx, session, agentLLM, ws, branch, tracker, editIters, editObs, changedFiles, formatNote)
}

// runReviewPhase runs the orchestrator-driven review+fix state machine.
//
// Unlike the previous LLM-driven approach (where the agent was expected to call
// review_code and provide a git diff it had no way to obtain), the orchestrator
// now runs the proven RAG code review directly — the same pipeline used by the
// /review PR command. The LLM is only involved in targeted fix loops when the
// reviewer requests changes.
//
// Flow per round:
//  1. Go runs "git diff HEAD" to get all workspace changes.
//  2. Go calls review.Executor.Execute → verdict + suggestions.
//  3. Verdict recorded for PR enforcement (create_pull_request checks this).
//  4. APPROVE → return immediately.
//  5. REQUEST_CHANGES → spawn a restricted "fix loop" whose only job is to
//     fix the specific issues listed in the review findings. No codebase
//     exploration — the fix loop gets the review text as its task context.
func (o *Orchestrator) runReviewPhase(
	ctx context.Context,
	session *Session,
	agentLLM llms.Model,
	ws *agentWorkspace,
	branch string,
	tracker *progressTracker,
	editIters int,
	editObs *loopObserver,
	changedFiles []string,
	formatNote string,
) (iterations int, obs *loopObserver, verdict string, ok bool) {
	o.logger.Info("warden: starting orchestrator-driven review phase",
		"session_id", session.ID,
		"changed_files", len(changedFiles),
		"max_rounds", maxReviewRounds,
	)

	executor := reviewpkg.NewExecutor(o.ragService, reviewpkg.Config{
		ComparisonModels: nil, // single-model only for agent speed
		ReviewsDir:       o.config.ReviewsDir,
		Logger:           o.logger,
	})

	totalFixIters := 0
	lastVerdict := ""
	// Use a session-scoped context so review tracking is scoped to this session.
	sessionCtx := tools.WithSessionID(ctx, session.ID)

	for round := 1; round <= maxReviewRounds; round++ {
		tracker.setPhase(fmt.Sprintf("reviewing (round %d/%d)", round, maxReviewRounds))

		// Step 1: get the diff of all workspace changes since the last commit.
		// The edit loop edits files directly without committing, so git diff HEAD
		// captures all changes made since the workspace was cloned.
		diff := o.getWorkspaceDiff(ctx, ws.dir)
		if diff == "" {
			o.logger.Warn("warden: no diff detected in workspace, skipping review",
				"session_id", session.ID, "round", round)
			lastVerdict = core.VerdictApprove
			break
		}

		// Step 2: run the proven RAG-based code review.
		o.logger.Info("warden: running code review",
			"session_id", session.ID, "round", round, "diff_bytes", len(diff))
		parsedFiles := ragreview.ParseDiff(diff)
		event := &core.GitHubEvent{
			PRTitle:      fmt.Sprintf("Implement #%d: %s", session.Issue.Number, session.Issue.Title),
			PRBody:       session.Issue.Body,
			RepoFullName: session.Issue.RepoOwner + "/" + session.Issue.RepoName,
			HeadSHA:      "agent-workspace",
		}
		result, err := executor.Execute(ctx, reviewpkg.Params{
			RepoConfig:   o.repoConfig,
			Repo:         o.repo,
			Event:        event,
			Diff:         diff,
			ChangedFiles: parsedFiles,
		})
		if err != nil {
			o.logger.Error("warden: code review failed",
				"session_id", session.ID, "round", round, "error", err)
			o.failSession(ctx, session, fmt.Sprintf("code review round %d: %v", round, err))
			return 0, nil, "", false
		}

		lastVerdict = result.Review.Verdict
		o.logger.Info("warden: review complete",
			"session_id", session.ID, "round", round,
			"verdict", lastVerdict, "confidence", result.Review.Confidence,
		)

		// Step 3: record the review result so create_pull_request can enforce it.
		o.mcpServer.RecordReviewBySession(sessionCtx, result.Review.Verdict, result.DiffHash)
		fileNames := make([]string, len(parsedFiles))
		for i, f := range parsedFiles {
			fileNames[i] = f.Filename
		}
		o.mcpServer.RecordReviewFiles(sessionCtx, fileNames)

		if lastVerdict == core.VerdictApprove {
			break
		}

		// Step 4: reviewer requested changes — run a focused fix loop.
		addedIters, addedFiles, fixErr := o.runFixRound(ctx, agentLLM, session, ws, tracker, editObs, round, result.Review, formatNote)
		if fixErr != nil {
			o.failSession(ctx, session, fixErr.Error())
			return 0, nil, "", false
		}
		totalFixIters += addedIters
		changedFiles = mergeFileLists(changedFiles, addedFiles)
	}

	totalIters := editIters + totalFixIters

	if lastVerdict != core.VerdictApprove {
		o.logger.Warn("warden: exhausted review rounds without APPROVE, yielding draft PR",
			"session_id", session.ID,
			"verdict", lastVerdict,
			"edit_iterations", editIters,
			"fix_iterations", totalFixIters,
		)
		o.yieldDraftPR(ctx, session, ws, branch, totalIters, editObs.totalIn, editObs.totalOut, changedFiles)
		return 0, nil, "", false
	}

	return totalIters, editObs, lastVerdict, true
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

// buildEditLoop builds the agent loop for the editing phase. It has all tools
// EXCEPT review_code and publish tools. The edit phase focuses on understanding
// the codebase, making changes, and running verification — but not review.
// review_code is withheld so the agent doesn't waste review rounds before the
// code is ready.
func (o *Orchestrator) buildEditLoop(agentLLM llms.Model, session *Session, ws *agentWorkspace, plan string, tracker *progressTracker, obs *loopObserver, maxIter int) (*goframeagent.AgentLoop, error) {
	registry := goframeagent.NewRegistry()
	allowedTools := make(map[string]bool)

	// MCP tools — exclude publish tools AND review_code (reserved for review loop).
	for _, t := range o.mcpServer.Tools() {
		if publishToolNames[t.Name()] || t.Name() == "review_code" {
			continue
		}
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	// File tools — auto-format Go files after write/edit (unless disabled by repo config).
	for _, t := range fileTools(newFormatterFromConfig(o.logger, o.repoConfig)) {
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	// Search tools (grep + find) — read-only, no workspace modifications.
	for _, t := range searchTools() {
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})

	loopLogger := o.logger.With("session_id", session.ID, "phase", "edit")
	return goframeagent.NewAgentLoop(agentLLM, registry,
		goframeagent.WithLoopSystemPrompt(o.buildEditSystemPrompt(session.Issue, ws.dir, plan, ws.projectContext)),
		goframeagent.WithLoopMaxIterations(maxIter),
		goframeagent.WithLoopGovernance(governance),
		goframeagent.WithLoopCompactionHook(o.buildCompactionHook(session, ws.traceFile, agentLLM)),
		goframeagent.WithLoopLogger(loopLogger),
		goframeagent.WithLoopObserver(obs),
	)
}

// buildFixLoop builds the restricted agent loop for fixing review findings.
// This loop is spawned per review round when the reviewer returns REQUEST_CHANGES.
// Tool set is intentionally minimal: no MCP exploration tools (no search_code,
// get_symbol, etc.) — the fix context already contains the specific issues to fix.
// Only run_command, file tools, and grep/find are available.
func (o *Orchestrator) buildFixLoop(agentLLM llms.Model, session *Session, ws *agentWorkspace, tracker *progressTracker, obs *loopObserver) (*goframeagent.AgentLoop, error) {
	registry := goframeagent.NewRegistry()
	allowedTools := make(map[string]bool)

	// From MCP: only run_command (for make build/lint/test). No review_code,
	// no exploration tools (search_code, get_symbol, get_structure, etc.).
	for _, t := range o.mcpServer.Tools() {
		if t.Name() == "run_command" {
			registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
			break
		}
	}

	// File tools — read/write/edit/list_dir with auto-formatting.
	for _, t := range fileTools(newFormatterFromConfig(o.logger, o.repoConfig)) {
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	// Search tools (grep + find) — useful for locating the specific line to fix.
	for _, t := range searchTools() {
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})
	loopLogger := o.logger.With("session_id", session.ID, "phase", "fix")
	return goframeagent.NewAgentLoop(agentLLM, registry,
		goframeagent.WithLoopSystemPrompt(o.buildFixSystemPrompt(ws.dir, ws.projectContext)),
		goframeagent.WithLoopMaxIterations(fixIterationsPerRound),
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

// buildEditSystemPrompt returns the system prompt for the edit loop.
// review_code and publish tools are intentionally omitted — the agent
// explores, implements, and verifies in this phase. Review happens in a
// separate loop so it is always reached regardless of edit budget.
func (o *Orchestrator) buildEditSystemPrompt(issue Issue, workspaceDir string, plan string, projectContext string) string {
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
- grep(pattern, path?, glob?, ignore_case?) — search file contents by regex/literal; prefer over search_code when you know the exact string
- find(pattern, path?) — find files by glob pattern (e.g. *.go, **/*_test.go)

**File operations** (workspace-scoped, paths relative to working directory):
- read_file(path, offset?, limit?) — read a file, optionally paginated; when truncated, use the hint offset to continue
- write_file(path, content) — create or overwrite a file
- edit_file(path, old_string, new_string) — replace an exact string; or use edits:[{old_string, new_string}, ...] for multiple atomic replacements
- list_dir(path?) — list directory contents

**Verification**:
- run_command(command) — run whitelisted commands: "make build", "make lint", "make test"

## Workflow
1. **Explore** — use grep / search_code / get_symbol / read_file to understand the code. Prefer grep for exact pattern search, search_code for semantic discovery.
2. **Implement** — use write_file / edit_file. Prefer edit_file for targeted changes.
3. **Verify** — run_command("make build"), then run_command("make lint"), then run_command("make test"). Fix failures.

## Rules
- Paths are relative to the working directory.
- Files are auto-formatted on write (goimports or gofmt for .go files). Other languages use the project's format_command before review.
- Always run lint and tests after making changes.
- Do NOT call review_code — it is not available in this phase. Review will happen automatically in the next phase.
- Do not attempt to push or open a PR.
- Keep changes minimal and focused on the issue.

%s`,
		issue.Number, issue.Title, truncateString(issue.Body, 2000), workspaceDir, plan)

	if projectContext != "" {
		base += "\n\n## Project Conventions\n\n" + projectContext
	}

	return base
}

// runFixRound spawns a focused fix loop for a single review round.
// It returns the number of LLM iterations consumed, any new modified files,
// and a non-nil error only for fatal failures (not ErrMaxIterations).
func (o *Orchestrator) runFixRound(
	ctx context.Context,
	agentLLM llms.Model,
	session *Session,
	ws *agentWorkspace,
	tracker *progressTracker,
	editObs *loopObserver,
	round int,
	review *core.StructuredReview,
	formatNote string,
) (iterations int, changedFiles []string, err error) {
	o.logger.Info("warden: reviewer requested changes, starting fix loop",
		"session_id", session.ID, "round", round,
		"suggestions", len(review.Suggestions),
	)
	fixObs := newLoopObserver(o.logger, session.ID, fmt.Sprintf("fix-%d", round))
	fixLoop, buildErr := o.buildFixLoop(agentLLM, session, ws, tracker, fixObs)
	if buildErr != nil {
		return 0, nil, fmt.Errorf("build fix loop round %d: %w", round, buildErr)
	}

	fixTask := goframeagent.Task{
		ID:          fmt.Sprintf("%s-fix-%d", session.ID, round),
		Description: fmt.Sprintf("Fix code review findings (round %d) for issue #%d", round, session.Issue.Number),
		Context:     o.buildFixTaskContext(round, review, formatNote),
		Priority:    5,
	}

	fixResult, fixErr := fixLoop.Run(ctx, fixTask, nil)
	if fixResult != nil {
		iterations = fixResult.Iterations
		editObs.totalIn += fixObs.totalIn
		editObs.totalOut += fixObs.totalOut
		changedFiles = modifiedFiles(fixResult.ToolCalls)
	}
	if fixErr != nil && !errors.Is(fixErr, goframeagent.ErrMaxIterations) {
		return iterations, changedFiles, fmt.Errorf("fix loop round %d: %w", round, fixErr)
	}
	return iterations, changedFiles, nil
}

// getWorkspaceDiff returns the full git diff of all changes made since the
// workspace was cloned (git diff HEAD). Returns empty string on error or when
// there are no changes.
func (o *Orchestrator) getWorkspaceDiff(ctx context.Context, workspaceDir string) string {
	cmd := exec.CommandContext(ctx, "git", "diff", "HEAD")
	cmd.Dir = workspaceDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		o.logger.Warn("warden: git diff HEAD failed",
			"workspace", workspaceDir, "error", err, "output", string(out))
		return ""
	}
	return string(out)
}

// buildFixSystemPrompt returns the system prompt for a focused fix loop.
// The fixer is given the specific review findings and must fix them — it must
// not re-explore the codebase. Tools are intentionally restricted: only
// run_command (make build/lint/test), file read/write/edit, and grep/find.
func (o *Orchestrator) buildFixSystemPrompt(workspaceDir string, projectContext string) string {
	base := fmt.Sprintf(`You are a code fixer. A code review has just been run and returned REQUEST_CHANGES.
Your task is to fix the specific issues listed in the task context — nothing more.

## Workspace
Working directory: %s

## Available Tools

**File operations** (workspace-scoped, paths relative to working directory):
- read_file(path, offset?, limit?) — read a file, optionally paginated
- write_file(path, content) — create or overwrite a file
- edit_file(path, old_string, new_string) — replace an exact string; or use edits:[{old_string, new_string}, ...] for multiple atomic replacements
- list_dir(path?) — list directory contents

**Search** (for locating the exact line to fix):
- grep(pattern, path?, glob?, ignore_case?) — search file contents by regex
- find(pattern, path?) — find files by glob pattern

**Verification**:
- run_command(command) — run whitelisted commands: "make build", "make lint", "make test"

## Workflow
1. Read ONLY the specific files mentioned in the review findings.
2. Apply the minimal fix for each reported issue.
3. Run make build, make lint, make test. Fix any failures.
4. Stop. Do not explore beyond the reported issues.

## Rules
- Do not explore the codebase beyond what is needed to fix the reported issues.
- Do not call review_code — the orchestrator will run the next review round.
- Do not push or open a PR.
- Keep changes minimal and focused on the reported issues.`, workspaceDir)

	if projectContext != "" {
		base += "\n\n## Project Conventions\n\n" + projectContext
	}
	return base
}

// buildFixTaskContext formats the review findings as task context for the fix loop.
// It lists each suggestion so the fixer knows exactly what to fix.
func (o *Orchestrator) buildFixTaskContext(round int, review *core.StructuredReview, formatNote string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Code review round %d returned REQUEST_CHANGES.\n\n", round)
	if review != nil && review.Summary != "" {
		fmt.Fprintf(&b, "## Review Summary\n%s\n\n", review.Summary)
	}
	if review != nil && len(review.Suggestions) > 0 {
		b.WriteString("## Issues to Fix\n")
		for i, s := range review.Suggestions {
			loc := s.FilePath
			if s.LineNumber > 0 {
				loc = fmt.Sprintf("%s:%d", s.FilePath, s.LineNumber)
			}
			fmt.Fprintf(&b, "%d. [%s] %s — %s\n", i+1, s.Severity, loc, s.Comment)
			if s.CodeSuggestion != "" {
				fmt.Fprintf(&b, "   Suggested fix: %s\n", s.CodeSuggestion)
			}
		}
		b.WriteByte('\n')
	}
	b.WriteString("Fix the issues above, verify with make build/lint/test, then stop.")
	if formatNote != "" {
		b.WriteString("\n\n" + formatNote)
	}
	return b.String()
}

// buildPublishSystemPrompt returns the system prompt for the publish loop.
func (o *Orchestrator) buildPublishSystemPrompt(issue Issue, branch string) string {
	return fmt.Sprintf(`The implementation for GitHub issue #%d ("%s") has been reviewed and approved.

Your task is to publish the changes:
1. Call push_branch to push branch "%s" to the remote.
2. Call create_pull_request to open a draft pull request referencing issue #%d.

When calling push_branch, provide a descriptive commit_message that references the
issue number and title, for example: "Implement #%d: <short summary>". Do NOT use
generic messages like "Automated commit".

Available tools: push_branch, create_pull_request.

Do not make any code changes. Do not review. Just push and open the PR.`,
		issue.Number, issue.Title, branch, issue.Number, issue.Number)
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
	"grep":             true,
	"find":             true,
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

// findTailStart returns the index of the first message to include in the
// preserved tail after compaction. It ensures the tail starts at a
// ChatMessageTypeHuman message so a "tool"-role result is never orphaned from
// the AI turn that requested it.
//
// It walks backwards from the ideal cut point (len(msgs)-minTail) until it
// lands on a human message. The minimum index is 1 (msgs[0] is always the
// system prompt and is never included in the tail — it is prepended separately
// by the caller).
func findTailStart(msgs []schema.MessageContent, minTail int) int {
	n := len(msgs)
	if n <= minTail+1 {
		return 1 // not enough to compact — keep everything after system prompt
	}
	start := n - minTail
	for start > 1 && msgs[start].Role != schema.ChatMessageTypeHuman {
		start--
	}
	return start
}

// goframeToolResultPrefix is the prefix goframe prepends to every tool result
// message: "Tool '<name>' returned: <json>". extractPathFromToolResult depends
// on this format; if goframe changes it, file tracking will silently return
// empty paths (safe degradation, not a crash).
const goframeToolResultPrefix = "returned: "

// extractFileOpsFromMsgs parses ToolResultContent parts in the message history
// and returns two sorted, deduplicated file lists:
//   - readFiles: files touched by read_file but not subsequently modified
//   - modifiedFiles: files written or edited (write_file / edit_file)
//
// Tool results are formatted by goframe as
//
//	"Tool '<name>' returned: <json>"
//
// so we strip the prefix and unmarshal the JSON to read the "path" field.
// read_file results also include "path" (added in this PR).
func extractFileOpsFromMsgs(msgs []schema.MessageContent) (readFiles, modifiedFiles []string) {
	readSet, modSet := collectFileOpSets(msgs)
	return fileOpSetsToLists(readSet, modSet)
}

// collectFileOpSets walks the message history and returns two sets of file paths.
func collectFileOpSets(msgs []schema.MessageContent) (readSet, modSet map[string]struct{}) {
	readSet = make(map[string]struct{})
	modSet = make(map[string]struct{})
	for _, m := range msgs {
		if m.Role != schema.ChatMessageTypeTool {
			continue
		}
		for _, part := range m.Parts {
			trc, ok := part.(schema.ToolResultContent)
			if !ok {
				continue
			}
			recordFileOp(trc, readSet, modSet)
		}
	}
	return readSet, modSet
}

// recordFileOp adds a path to the appropriate set based on the tool name.
func recordFileOp(trc schema.ToolResultContent, readSet, modSet map[string]struct{}) {
	switch trc.ToolName {
	case "read_file":
		if p := extractPathFromToolResult(trc.Content); p != "" {
			readSet[p] = struct{}{}
		}
	case writeFileName, editFileName:
		if p := extractPathFromToolResult(trc.Content); p != "" {
			modSet[p] = struct{}{}
		}
	}
}

// fileOpSetsToLists converts raw read/mod sets into sorted, deduplicated lists
// where readFiles contains only paths not also in modSet.
func fileOpSetsToLists(readSet, modSet map[string]struct{}) (readFiles, modifiedFiles []string) {
	for p := range readSet {
		if _, modified := modSet[p]; !modified {
			readFiles = append(readFiles, p)
		}
	}
	for p := range modSet {
		modifiedFiles = append(modifiedFiles, p)
	}
	sort.Strings(readFiles)
	sort.Strings(modifiedFiles)
	return readFiles, modifiedFiles
}

// extractPathFromToolResult extracts the "path" field from a goframe tool
// result string of the form "Tool '<name>' returned: <json>".
func extractPathFromToolResult(content string) string {
	_, jsonPart, found := strings.Cut(content, goframeToolResultPrefix)
	if !found {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonPart)), &m); err != nil {
		return ""
	}
	path, _ := m["path"].(string)
	return path
}

// parseFileTagsFromSummary extracts the <read-files> and <modified-files> XML
// blocks that were appended by a prior compaction, so they can be merged with
// the file operations discovered in the new messages.
func parseFileTagsFromSummary(summary string) (readFiles, modifiedFiles []string) {
	readFiles = parseXMLBlock(summary, "read-files")
	modifiedFiles = parseXMLBlock(summary, "modified-files")
	return readFiles, modifiedFiles
}

// parseXMLBlock extracts newline-separated file paths from a block of the form
//
//	<tag>
//	path1
//	path2
//	</tag>
func parseXMLBlock(s, tag string) []string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	start := strings.Index(s, open)
	if start == -1 {
		return nil
	}
	start += len(open)
	end := strings.Index(s[start:], closeTag)
	if end == -1 {
		return nil
	}
	var paths []string
	for line := range strings.SplitSeq(strings.TrimSpace(s[start:start+end]), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// formatFileOps formats read and modified file lists as XML tags suitable for
// appending to a compaction summary. Returns an empty string when both lists
// are empty.
func formatFileOps(readFiles, modifiedFiles []string) string {
	if len(readFiles) == 0 && len(modifiedFiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n")
	if len(readFiles) > 0 {
		b.WriteString("<read-files>\n")
		for _, f := range readFiles {
			b.WriteString(f)
			b.WriteByte('\n')
		}
		b.WriteString("</read-files>\n")
	}
	if len(modifiedFiles) > 0 {
		if len(readFiles) > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("<modified-files>\n")
		for _, f := range modifiedFiles {
			b.WriteString(f)
			b.WriteByte('\n')
		}
		b.WriteString("</modified-files>")
	}
	return b.String()
}

// mergeFileLists merges two slices, returning a sorted, deduplicated result.
func mergeFileLists(a, b []string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, s := range a {
		seen[s] = struct{}{}
	}
	for _, s := range b {
		seen[s] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
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

		tailStart := findTailStart(msgs, 8)
		tail := msgs[tailStart:]

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
//
// File operation tracking: file paths touched by read_file / write_file /
// edit_file are extracted from the message history and appended to the summary
// as <read-files> / <modified-files> XML blocks, mirroring Pi's compaction
// details. On re-compaction the prior blocks are merged with new operations so
// the cumulative file footprint is never lost.
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

	summary := resp.Choices[0].Content

	// Merge file operations: previous summary tags + new messages.
	prevRead, prevMod := parseFileTagsFromSummary(previousSummary)
	newRead, newMod := extractFileOpsFromMsgs(newMsgs)
	allMod := mergeFileLists(prevMod, newMod)
	// read-only = merged reads minus anything in modified
	allReadRaw := mergeFileLists(prevRead, newRead)
	modSet := make(map[string]struct{}, len(allMod))
	for _, f := range allMod {
		modSet[f] = struct{}{}
	}
	var allRead []string
	for _, f := range allReadRaw {
		if _, inMod := modSet[f]; !inMod {
			allRead = append(allRead, f)
		}
	}

	summary += formatFileOps(allRead, allMod)
	return summary, nil
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
	pushErr := yieldCommitAndPush(ctx, ws.dir, branch, editedFiles, session.Issue.Number, session.Issue.Title, o.logger)
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
	prBody := o.buildDraftPRBody(ws.dir, iterations, editedFiles, session)
	if o.ghClient != nil {
		pr, err := o.ghClient.CreatePullRequest(ctx, session.Issue.RepoOwner, session.Issue.RepoName, gh.PullRequestOptions{
			Title: fmt.Sprintf("WIP: %s (draft — needs human review)", session.Issue.Title),
			Body:  prBody,
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
		"**Draft PR created** — session `%s`\n\n"+
			"The agent made partial progress but could not reach an APPROVE verdict after %d iterations.\n",
		session.ID, iterations,
	)
	if len(editedFiles) > 0 {
		body += "\n**Changed files:**\n"
		for _, f := range editedFiles {
			body += "- `" + f + "`\n"
		}
		body += "\n"
	}
	if prURL != "" {
		body += fmt.Sprintf("**Draft PR:** %s\n\nA human can review the partial work, continue on branch `%s`, or close the draft.", prURL, branch)
	} else {
		body += fmt.Sprintf("Branch `%s` was pushed with partial changes. Open a draft PR manually to continue.", branch)
	}
	o.postIssueComment(ctx, session.Issue, body)
}

// buildDraftPRBody generates a descriptive PR body using git diff --stat and
// the list of edited files. Falls back to a generic message when git is unavailable.
func (o *Orchestrator) buildDraftPRBody(workspaceDir string, iterations int, editedFiles []string, session *Session) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Draft PR — #%d: %s\n\n", session.Issue.Number, session.Issue.Title)
	fmt.Fprintf(&b, "The implementation agent could not achieve an APPROVE verdict after %d iterations.\n", iterations)
	b.WriteString("This draft contains partial work ready for human review.\n\n")

	// Try to get a diff stat summary.
	diffStat := o.getGitDiffStat(workspaceDir)
	if diffStat != "" {
		b.WriteString("### Changes\n\n```\n" + diffStat + "\n```\n\n")
	}

	if len(editedFiles) > 0 {
		b.WriteString("### Modified Files\n\n")
		for _, f := range editedFiles {
			b.WriteString("- `" + f + "`\n")
		}
		b.WriteString("\n")
	}

	fmt.Fprintf(&b, "Closes #%d", session.Issue.Number)
	return b.String()
}

// getGitDiffStat runs git diff --stat against the base branch and returns the
// output. Returns empty string on any error.
func (o *Orchestrator) getGitDiffStat(workspaceDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	baseBranch := o.config.BaseBranch
	if baseBranch == "" {
		baseBranch = "main"
	}

	cmd := exec.CommandContext(ctx, "git", "-C", workspaceDir, "diff", "--stat", baseBranch+"...HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		o.logger.Debug("warden: git diff --stat failed, trying against origin/"+baseBranch,
			"error", err, "output", string(out))
		cmd = exec.CommandContext(ctx, "git", "-C", workspaceDir, "diff", "--stat", "origin/"+baseBranch+"...HEAD")
		out, err = cmd.CombinedOutput()
		if err != nil {
			o.logger.Debug("warden: git diff --stat against origin/main also failed",
				"error", err, "output", string(out))
			return ""
		}
	}

	stat := strings.TrimSpace(string(out))
	// Limit to prevent excessively large PR bodies.
	if len(stat) > 4000 {
		stat = stat[:4000] + "\n... (truncated)"
	}
	return stat
}

// formatProject runs the project's format_command once before the review phase.
// The command is configured in .code-warden.yml (e.g. "npm run format", "ruff format .").
// No-op if no format_command is set. Controlled independently of DisableFormatOnWrite.
// Returns a human-readable note for the review context if formatting ran, or "".
func (o *Orchestrator) formatProject(ctx context.Context, ws *agentWorkspace) string {
	if o.repoConfig == nil || o.repoConfig.FormatCommand == "" {
		return ""
	}
	formatter := NewFormatter(o.logger)
	if !formatter.FormatProject(ctx, ws.dir, o.repoConfig.FormatCommand) {
		return ""
	}
	return fmt.Sprintf("Note: project format command ran before review (\"%s\"). Some files may have formatting changes you did not make.", o.repoConfig.FormatCommand)
}

// yieldCommitAndPush stages all pending changes, commits, and pushes the branch.
// Returns errNoChanges when the workspace is clean (nothing to commit or push).
// The workspace remote URL already contains authentication (set up by
// prepareAgentWorkspace), so no explicit token injection is needed.
func yieldCommitAndPush(ctx context.Context, workspaceDir, branch string, editedFiles []string, issueNumber int, issueTitle string, logger *slog.Logger) error {
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
	commitMsg := gitutil.SanitizeCommitMsg(
		fmt.Sprintf("WIP: #%d — %s", issueNumber, issueTitle),
		"WIP: automated partial implementation",
	)
	out, err := run("commit", "-m", commitMsg)
	if err != nil {
		if strings.Contains(out, "nothing to commit") {
			logger.Info("yieldCommitAndPush: nothing to commit, skipping push")
			return errNoChanges
		}
		logger.Error("yieldCommitAndPush: git commit failed", "error", err, "output", out)
		return fmt.Errorf("git commit failed: %w (output: %s)", err, out)
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
