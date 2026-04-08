package agent

// warden.go — "pi" / "warden" agent mode.
//
// Extends the native in-process AgentLoop with workspace file manipulation
// tools (read_file, write_file, edit_file, list_dir) so the LLM can directly
// create and modify files in the cloned repository without relying on an
// external binary.
//
// The concurrency limit (MaxConcurrentSessions) is already enforced by
// SpawnAgent in orchestrator.go via the sessions map length check.

import (
	"context"
	"fmt"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"
)

// runWardenAgent executes the agent workflow using the native goframe AgentLoop
// augmented with workspace file tools (read_file, write_file, edit_file, list_dir).
func (o *Orchestrator) runWardenAgent(ctx context.Context, session *Session, branch string) {
	o.runNativeLoop(ctx, session, branch, "warden", o.buildWardenLoop)
}

// buildWardenLoop constructs the goframe AgentLoop with all MCP tools AND the
// workspace file manipulation tools (read_file, write_file, edit_file, list_dir).
func (o *Orchestrator) buildWardenLoop(agentLLM llms.Model, session *Session, ws *agentWorkspace) (*goframeagent.AgentLoop, error) {
	registry := goframeagent.NewRegistry()
	allowedTools := make(map[string]bool)

	// Register existing MCP tools (search_code, get_symbol, review_code, etc.)
	for _, t := range o.mcpServer.Tools() {
		wrapped := &contextInjectingTool{
			inner:       t,
			projectRoot: ws.dir,
			sessionID:   session.ID,
		}
		if err := registry.Register(wrapped); err != nil {
			o.logger.Warn("buildWardenLoop: failed to register MCP tool",
				"tool", t.Name(), "error", err)
			continue
		}
		allowedTools[t.Name()] = true
	}

	// Register file manipulation tools — sandbox restricted to workspace dir
	for _, t := range fileTools() {
		wrapped := &contextInjectingTool{
			inner:       t,
			projectRoot: ws.dir,
			sessionID:   session.ID,
		}
		if err := registry.Register(wrapped); err != nil {
			o.logger.Warn("buildWardenLoop: failed to register file tool",
				"tool", t.Name(), "error", err)
			continue
		}
		allowedTools[t.Name()] = true
	}

	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})

	maxIter := max(o.config.MaxIterations*10, 30)

	return goframeagent.NewAgentLoop(agentLLM, registry,
		goframeagent.WithLoopSystemPrompt(o.buildWardenSystemPrompt(session.Issue, ws.dir)),
		goframeagent.WithLoopMaxIterations(maxIter),
		goframeagent.WithLoopGovernance(governance),
	)
}

// buildWardenSystemPrompt returns the system prompt for warden mode, extending
// the native prompt with file tool instructions.
func (o *Orchestrator) buildWardenSystemPrompt(issue Issue, workspaceDir string) string {
	return fmt.Sprintf(`You are an expert software engineer implementing GitHub issue #%d.

## Task
Title: %s
Description:
%s

## Workspace
Working directory: %s

## Available Tools
**Code navigation** (read-only, repository-indexed):
- search_code, get_arch_context, get_symbol, get_structure
- find_usages, get_callers, get_callees

**File operations** (workspace-scoped):
- read_file(path, offset?, limit?) — read a file (optionally paginated)
- write_file(path, content) — write/create a file
- edit_file(path, old_string, new_string) — targeted in-place edit
- list_dir(path?) — list directory

**Verification**:
- run_command(command) — run whitelisted commands (make lint, make test)
- review_code — automated code review

**Publishing** (requires APPROVE from review_code):
- push_branch — push the branch
- create_pull_request — open a draft PR

## Workflow
1. **Explore** — use search_code / get_symbol / list_dir / read_file to understand the codebase.
2. **Plan** — identify which files need changing.
3. **Implement** — use write_file / edit_file to make changes. Prefer edit_file for targeted changes.
4. **Verify** — run run_command("make lint") then run_command("make test").
5. **Review** — call review_code with the full diff; fix any REQUEST_CHANGES findings.
6. **Push** — call push_branch.
7. **PR** — call create_pull_request (requires APPROVE verdict first).

## Rules
- All file paths are relative to the workspace root.
- Always verify with lint and tests before calling review_code.
- You MUST receive APPROVE from review_code before creating a PR.
- Keep changes minimal and focused on the issue.`,
		issue.Number, issue.Title, truncateString(issue.Body, 2000), workspaceDir)
}
