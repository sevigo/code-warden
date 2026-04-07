package agent

import (
	"context"
	"fmt"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"

	"github.com/sevigo/code-warden/internal/mcp/tools"
)

// runInProcessAgent executes the agent workflow using the native in-process
// goframe AgentLoop — no external process is required.
//
// The same LLM used for code reviews drives a ReAct (Think-Act-Observe) loop.
// All MCP tools are registered directly in the loop registry, with the session
// workspace path injected into each tool call's context.
func (o *Orchestrator) runInProcessAgent(ctx context.Context, session *Session, branch string) {
	defer func() {
		if err := o.cleanupWorkspace(session.ID); err != nil {
			o.logger.Error("runInProcessAgent: cleanup failed", "session_id", session.ID, "error", err)
		}
		if o.globalMCPRegistry != nil {
			if err := o.globalMCPRegistry.UnregisterWorkspaceBySessionID(session.ID); err != nil {
				o.logger.Warn("runInProcessAgent: failed to unregister global workspace",
					"session_id", session.ID, "error", err)
			}
		}
	}()

	if o.llm == nil {
		errMsg := "native agent mode requires a configured LLM (llm is nil)"
		o.logger.Error("runInProcessAgent: "+errMsg, "session_id", session.ID)
		session.SetStatus(StatusFailed)
		session.SetError(errMsg)
		o.postSessionFailed(ctx, session, errMsg)
		return
	}

	// Apply timeout
	ctx, cancel := context.WithTimeout(ctx, o.config.Timeout)
	defer cancel()

	ws, err := o.prepareAgentWorkspace(ctx, session)
	if err != nil {
		o.logger.Error("runInProcessAgent: workspace setup failed", "session_id", session.ID, "error", err)
		session.SetStatus(StatusFailed)
		session.SetError(err.Error())
		o.postSessionFailed(ctx, session, err.Error())
		return
	}
	defer ws.logFile.Close()
	defer o.mcpServer.UnregisterWorkspace(session.ID)

	o.logger.Info("🛠️ IMPLEMENTATION: Starting native in-process agent",
		"session_id", session.ID,
		"working_dir", ws.dir,
		"timeout", o.config.Timeout)

	// Build tool registry — wrap every MCP tool so the workspace root and
	// session ID are transparently injected into each tool call's context.
	registry := goframeagent.NewRegistry()
	for _, t := range o.mcpServer.Tools() {
		wrapped := &contextInjectingTool{
			inner:       t,
			projectRoot: ws.dir,
			sessionID:   session.ID,
		}
		if regErr := registry.Register(wrapped); regErr != nil {
			o.logger.Warn("runInProcessAgent: failed to register tool",
				"tool", t.Name(), "error", regErr)
		}
	}

	// Governance: allow the full MCP tool set.
	allowedTools := make(map[string]bool)
	for _, t := range o.mcpServer.Tools() {
		allowedTools[t.Name()] = true
	}
	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})

	maxIter := o.config.MaxIterations * 10 // each review iteration has many think-act steps
	if maxIter < 30 {
		maxIter = 30
	}

	loop, err := goframeagent.NewAgentLoop(o.llm, registry,
		goframeagent.WithLoopSystemPrompt(o.buildNativeSystemPrompt(session.Issue, branch, ws.dir)),
		goframeagent.WithLoopMaxIterations(maxIter),
		goframeagent.WithLoopGovernance(governance),
	)
	if err != nil {
		errMsg := fmt.Sprintf("failed to create agent loop: %v", err)
		o.logger.Error("runInProcessAgent: "+errMsg, "session_id", session.ID)
		session.SetStatus(StatusFailed)
		session.SetError(errMsg)
		o.postSessionFailed(ctx, session, errMsg)
		return
	}

	task := goframeagent.Task{
		ID:          session.ID,
		Description: fmt.Sprintf("Implement GitHub issue #%d: %s", session.Issue.Number, session.Issue.Title),
		Context:     o.buildNativeTaskContext(session.Issue, branch),
		Priority:    5,
	}

	loopResult, err := loop.Run(ctx, task, nil)

	// Update completed time regardless of outcome
	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()

	if err != nil {
		errMsg := fmt.Sprintf("native agent loop failed: %v", err)
		session.SetStatus(StatusFailed)
		session.SetError(errMsg)
		o.logger.Error("runInProcessAgent: loop error",
			"session_id", session.ID,
			"iterations", loopResult.Iterations,
			"error", err)
		o.postSessionFailed(ctx, session, errMsg)
		return
	}

	// Extract result from the loop's final response and review tracker state.
	verdict, _, _ := o.mcpServer.GetReviewBySession(session.ID)
	result := &Result{
		Branch:     branch,
		Verdict:    verdict,
		Iterations: loopResult.Iterations,
	}

	// Try to parse PR info from the agent's final response.
	if prInfo := extractPRInfo(loopResult.Response); prInfo != nil {
		result.PRNumber = prInfo.PRNumber
		result.PRURL = prInfo.PRURL
	}

	// Collect files touched during this session from the review tracker.
	if files := o.mcpServer.GetLastReviewFiles(); files != nil {
		result.FilesChanged = files
	}

	session.SetResult(result)
	session.SetStatus(StatusCompleted)
	o.postSessionCompleted(ctx, session, result)

	o.logger.Info("runInProcessAgent: completed",
		"session_id", session.ID,
		"verdict", result.Verdict,
		"iterations", result.Iterations,
		"pr_url", result.PRURL,
		"tokens_in", loopResult.Tokens.Input,
		"tokens_out", loopResult.Tokens.Output)
}

// buildNativeSystemPrompt returns the system prompt for the native agent.
func (o *Orchestrator) buildNativeSystemPrompt(issue Issue, branch, workspaceDir string) string {
	return fmt.Sprintf(`You are an expert software engineer implementing GitHub issue #%d.

## Task
Title: %s
Description:
%s

## Workspace
Working directory: %s
Target branch: %s

## Workflow
1. **Explore** — use search_code, get_symbol, get_structure to understand the codebase.
2. **Plan** — identify which files need changing.
3. **Implement** — write the code changes.
4. **Verify** — run run_command("make lint") and run_command("make test") to check correctness.
5. **Review** — call review_code with the full diff; fix any REQUEST_CHANGES findings.
6. **Push** — call push_branch to push your changes to %s.
7. **PR** — call create_pull_request; you MUST have an APPROVE verdict first.

## Rules
- Always verify with lint and tests before calling review_code.
- You MUST receive APPROVE from review_code before creating a PR.
- Commit messages should be descriptive.
- Keep changes minimal and focused on the issue.`,
		issue.Number, issue.Title, issue.Body, workspaceDir, branch, branch)
}

// buildNativeTaskContext builds additional task context for the native agent.
func (o *Orchestrator) buildNativeTaskContext(issue Issue, branch string) string {
	ctx := fmt.Sprintf("Repository: %s/%s\nIssue #%d: %s\nBranch: %s",
		issue.RepoOwner, issue.RepoName, issue.Number, issue.Title, branch)
	if issue.Body != "" {
		ctx += fmt.Sprintf("\n\nIssue description:\n%s", truncateString(issue.Body, 2000))
	}
	return ctx
}

// contextInjectingTool wraps an MCP tool and injects the workspace root and
// session ID into every tool call's context before delegating to the inner tool.
type contextInjectingTool struct {
	inner       interface {
		Name() string
		Description() string
		ParametersSchema() map[string]any
		Execute(ctx context.Context, args map[string]any) (any, error)
	}
	projectRoot string
	sessionID   string
}

func (w *contextInjectingTool) Name() string                     { return w.inner.Name() }
func (w *contextInjectingTool) Description() string              { return w.inner.Description() }
func (w *contextInjectingTool) ParametersSchema() map[string]any { return w.inner.ParametersSchema() }

func (w *contextInjectingTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	ctx = tools.WithProjectRoot(ctx, w.projectRoot)
	ctx = tools.WithSessionID(ctx, w.sessionID)
	return w.inner.Execute(ctx, args)
}
