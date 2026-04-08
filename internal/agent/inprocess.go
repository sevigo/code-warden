package agent

import (
	"context"
	"fmt"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"

	"github.com/sevigo/code-warden/internal/mcp"
	"github.com/sevigo/code-warden/internal/mcp/tools"
)

// loopBuilderFn constructs a goframe AgentLoop for a given session and workspace.
type loopBuilderFn func(agentLLM llms.Model, session *Session, ws *agentWorkspace) (*goframeagent.AgentLoop, error)

// runInProcessAgent executes the agent workflow using the native in-process
// goframe AgentLoop — no external process or external LLM provider required.
//
// If agent.model is set and differs from the review model, it is resolved via
// ragService.GetLLM and used for the implementation loop.  Otherwise the review
// LLM (o.llm) is used as fallback.
func (o *Orchestrator) runInProcessAgent(ctx context.Context, session *Session, branch string) {
	o.runNativeLoop(ctx, session, branch, "native in-process", o.buildNativeLoop)
}

// runNativeLoop is the shared driver for native and warden agent modes.
// It prepares the workspace, invokes buildLoop to create the AgentLoop, runs
// the task, and handles result/failure bookkeeping.
func (o *Orchestrator) runNativeLoop(ctx context.Context, session *Session, branch, label string, buildLoop loopBuilderFn) {
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
	// Unregister from the per-session MCP workspace registry (different from
	// globalMCPRegistry, which is handled in cleanupNativeSession).
	defer o.mcpServer.UnregisterWorkspace(session.ID)

	o.logger.Info("🛠️ IMPLEMENTATION: Starting "+label+" agent",
		"session_id", session.ID,
		"working_dir", ws.dir,
		"timeout", o.config.Timeout,
		"model", agentLLM,
	)

	loop, err := buildLoop(agentLLM, session, ws)
	if err != nil {
		o.failSession(ctx, session, err.Error())
		return
	}

	task := goframeagent.Task{
		ID:          session.ID,
		Description: fmt.Sprintf("Implement GitHub issue #%d: %s", session.Issue.Number, session.Issue.Title),
		Context:     o.buildNativeTaskContext(session.Issue, branch),
		Priority:    5,
	}

	loopResult, err := loop.Run(ctx, task, nil)

	session.mu.Lock()
	session.CompletedAt = time.Now()
	session.mu.Unlock()

	if err != nil {
		errMsg := fmt.Sprintf("%s agent loop failed: %v", label, err)
		o.logger.Error("runNativeLoop: loop error",
			"session_id", session.ID,
			"label", label,
			"iterations", loopResult.Iterations,
			"error", err)
		o.failSession(ctx, session, errMsg)
		return
	}

	verdict, _, _ := o.mcpServer.GetReviewBySession(session.ID)
	result := &Result{
		Branch:     branch,
		Verdict:    verdict,
		Iterations: loopResult.Iterations,
	}
	if prInfo := extractPRInfo(loopResult.Response); prInfo != nil {
		result.PRNumber = prInfo.PRNumber
		result.PRURL = prInfo.PRURL
	}
	// Use session-scoped file list to avoid reading another concurrent session's files.
	if files := o.mcpServer.GetReviewFilesBySession(session.ID); files != nil {
		result.FilesChanged = files
	}

	session.SetResult(result)
	session.SetStatus(StatusCompleted)
	o.postSessionCompleted(ctx, session, result)

	o.logger.Info("runNativeLoop: completed",
		"session_id", session.ID,
		"label", label,
		"verdict", result.Verdict,
		"iterations", result.Iterations,
		"pr_url", result.PRURL,
		"tokens_in", loopResult.Tokens.Input,
		"tokens_out", loopResult.Tokens.Output)
}

// resolveAgentLLM returns the LLM to use for the native agent.
// If agent.model is set and can be resolved, that model is used.
// Otherwise falls back to the review LLM (o.llm).
func (o *Orchestrator) resolveAgentLLM(ctx context.Context) (llms.Model, error) {
	if o.config.Model != "" && o.ragService != nil {
		agentLLM, err := o.ragService.GetLLM(ctx, o.config.Model)
		if err != nil {
			o.logger.Warn("runInProcessAgent: could not load agent.model, falling back to review LLM",
				"model", o.config.Model, "error", err)
		} else {
			o.logger.Info("runInProcessAgent: using dedicated agent model", "model", o.config.Model)
			return agentLLM, nil
		}
	}
	if o.llm == nil {
		return nil, fmt.Errorf("native agent mode requires a configured LLM (llm is nil)")
	}
	return o.llm, nil
}

// buildNativeLoop constructs the goframe AgentLoop with all MCP tools injected.
func (o *Orchestrator) buildNativeLoop(agentLLM llms.Model, session *Session, ws *agentWorkspace) (*goframeagent.AgentLoop, error) {
	registry := goframeagent.NewRegistry()
	allowedTools := make(map[string]bool)

	for _, t := range o.mcpServer.Tools() {
		wrapped := &contextInjectingTool{
			inner:       t,
			projectRoot: ws.dir,
			sessionID:   session.ID,
		}
		if err := registry.Register(wrapped); err != nil {
			o.logger.Warn("runInProcessAgent: failed to register tool",
				"tool", t.Name(), "error", err)
			continue
		}
		allowedTools[t.Name()] = true
	}

	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})

	maxIter := max(o.config.MaxIterations*10, 30)

	return goframeagent.NewAgentLoop(agentLLM, registry,
		goframeagent.WithLoopSystemPrompt(o.buildNativeSystemPrompt(session.Issue, ws.dir)),
		goframeagent.WithLoopMaxIterations(maxIter),
		goframeagent.WithLoopGovernance(governance),
	)
}

// cleanupNativeSession deregisters the session workspace from global registry.
func (o *Orchestrator) cleanupNativeSession(_ context.Context, session *Session) {
	if err := o.cleanupWorkspace(session.ID); err != nil {
		o.logger.Error("runInProcessAgent: cleanup failed", "session_id", session.ID, "error", err)
	}
	if o.globalMCPRegistry != nil {
		if err := o.globalMCPRegistry.UnregisterWorkspaceBySessionID(session.ID); err != nil {
			o.logger.Warn("runInProcessAgent: failed to unregister global workspace",
				"session_id", session.ID, "error", err)
		}
	}
}

// failSession marks a session as failed and posts a GitHub comment.
func (o *Orchestrator) failSession(ctx context.Context, session *Session, errMsg string) {
	o.logger.Error("runInProcessAgent: "+errMsg, "session_id", session.ID)
	session.SetStatus(StatusFailed)
	session.SetError(errMsg)
	o.postSessionFailed(ctx, session, errMsg)
}

// buildNativeSystemPrompt returns the system prompt for the native agent.
func (o *Orchestrator) buildNativeSystemPrompt(issue Issue, workspaceDir string) string {
	return fmt.Sprintf(`You are an expert software engineer implementing GitHub issue #%d.

## Task
Title: %s
Description:
%s

## Workspace
Working directory: %s

## Workflow
1. **Explore** — use search_code, get_symbol, get_structure to understand the codebase.
2. **Plan** — identify which files need changing.
3. **Implement** — write the code changes.
4. **Verify** — run run_command("make lint") and run_command("make test").
5. **Review** — call review_code with the full diff; fix any REQUEST_CHANGES findings.
6. **Push** — call push_branch to push your changes.
7. **PR** — call create_pull_request; you MUST have an APPROVE verdict first.

## Rules
- Always verify with lint and tests before calling review_code.
- You MUST receive APPROVE from review_code before creating a PR.
- Keep changes minimal and focused on the issue.`,
		issue.Number, issue.Title, truncateString(issue.Body, 2000), workspaceDir)
}

// buildNativeTaskContext builds task context for the native agent.
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
	inner       mcp.Tool
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
