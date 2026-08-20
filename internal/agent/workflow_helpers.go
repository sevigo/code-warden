package agent

import (
	"context"
	"fmt"

	"github.com/sevigo/goframe/llms"

	"github.com/sevigo/code-warden/internal/mcp"
	"github.com/sevigo/code-warden/internal/mcp/tools"
)

// resolveAgentLLM returns the LLM used by the implementation workflow.
// It uses the injected model (o.llm). The dedicated agent.model resolution that
// previously routed through the RAG service is gone — the generator model is used.
func (o *Orchestrator) resolveAgentLLM(_ context.Context) (llms.Model, error) {
	if o.llm == nil {
		return nil, fmt.Errorf("agent workflow requires a configured LLM (llm is nil)")
	}
	return o.llm, nil
}

// cleanupAgentSession deregisters the session workspace from the global registry.
func (o *Orchestrator) cleanupAgentSession(_ context.Context, session *Session) {
	if err := o.cleanupWorkspace(session.ID); err != nil {
		o.logger.Error("agent session cleanup failed", "session_id", session.ID, "error", err)
	}
	if o.globalMCPRegistry != nil {
		if err := o.globalMCPRegistry.UnregisterWorkspaceBySessionID(session.ID); err != nil {
			o.logger.Warn("failed to unregister global workspace",
				"session_id", session.ID, "error", err)
		}
	}
}

// failSession marks a session as failed and posts a GitHub comment.
func (o *Orchestrator) failSession(ctx context.Context, session *Session, errMsg string) {
	o.logger.Error("agent session failed", "session_id", session.ID, "error", errMsg)
	session.SetStatus(StatusFailed)
	session.SetError(errMsg)
	o.persistSessionFailed(ctx, session, errMsg)
	o.postSessionFailed(ctx, session, errMsg)
}

// buildAgentTaskContext builds task context for an implementation agent.
func (o *Orchestrator) buildAgentTaskContext(issue Issue, branch string) string {
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
