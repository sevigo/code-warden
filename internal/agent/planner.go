package agent

// planner.go — Plan-then-ReAct: a short read-only exploration loop that runs
// before the implement loop and produces a structured implementation plan.
//
// The planner gets a minimal tool set (search + read, no write/edit/publish),
// a tight iteration cap (max 5), and a prompt that asks for a plan in a
// specific markdown format. The resulting plan text is injected into the
// implement loop's system prompt so the model starts with a clear roadmap.
//
// This is the "Plan-and-Execute" reasoning strategy from the agent harness
// design framework — it separates exploration from execution and gives the
// implement loop a much better starting context than a cold start.

import (
	"context"
	"fmt"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/llms"

	"github.com/sevigo/code-warden/internal/mcp"
)

// plannerAllowedMCPTools is the set of MCP tools available during planning.
// All are read-only — no code changes, no publishing.
var plannerAllowedMCPTools = map[string]bool{
	"search_code":      true,
	"get_symbol":       true,
	"get_structure":    true,
	"get_arch_context": true,
	"find_usages":      true,
	"get_callers":      true,
	"get_callees":      true,
}

// buildPlan runs a short read-only agent loop to produce an implementation plan
// for the given issue. It returns the plan as a markdown string.
//
// If planning fails (timeout, loop error, empty output) a minimal fallback plan
// is returned so the implement loop always has some context.
func (o *Orchestrator) buildPlan(ctx context.Context, agentLLM llms.Model, session *Session, ws *agentWorkspace, tracker *progressTracker) string {
	loop, err := o.buildPlannerLoop(agentLLM, session, ws, tracker)
	if err != nil {
		o.logger.Warn("planner: failed to build loop, skipping planning phase",
			"session_id", session.ID, "error", err)
		return o.fallbackPlan(session.Issue)
	}

	task := goframeagent.Task{
		ID: session.ID + "-plan",
		Description: fmt.Sprintf(
			"Plan implementation of GitHub issue #%d: %s",
			session.Issue.Number, session.Issue.Title,
		),
		Priority: 5,
	}

	o.logger.Info("planner: starting planning loop", "session_id", session.ID)
	result, err := loop.Run(ctx, task, nil)
	if err != nil {
		o.logger.Warn("planner: loop error, using fallback plan",
			"session_id", session.ID, "error", err)
		return o.fallbackPlan(session.Issue)
	}

	o.logger.Info("planner: planning complete",
		"session_id", session.ID,
		"iterations", result.Iterations,
		"tokens_in", result.Tokens.Input,
		"tokens_out", result.Tokens.Output,
	)

	if result.Response == "" {
		o.logger.Warn("planner: empty response, using fallback plan", "session_id", session.ID)
		return o.fallbackPlan(session.Issue)
	}

	return result.Response
}

// buildPlannerLoop constructs the read-only agent loop for the planning phase.
func (o *Orchestrator) buildPlannerLoop(agentLLM llms.Model, session *Session, ws *agentWorkspace, tracker *progressTracker) (*goframeagent.AgentLoop, error) {
	registry := goframeagent.NewRegistry()
	allowedTools := make(map[string]bool)

	// Read-only MCP tools only.
	for _, t := range o.mcpServer.Tools() {
		if !plannerAllowedMCPTools[t.Name()] {
			continue
		}
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	// Read-only file tools: read_file and list_dir only (no write/edit).
	for _, t := range []mcp.Tool{
		&readFileTool{},
		&listDirTool{},
	} {
		registerTool(registry, allowedTools, t, ws, session.ID, tracker, o.logger)
	}

	governance := goframeagent.NewGovernance(&goframeagent.PermissionCheck{Allowed: allowedTools})

	loopLogger := o.logger.With("session_id", session.ID, "phase", "plan")
	return goframeagent.NewAgentLoop(agentLLM, registry,
		goframeagent.WithLoopSystemPrompt(o.buildPlannerSystemPrompt(session.Issue, ws.dir)),
		goframeagent.WithLoopMaxIterations(5), // tight cap — explore, then plan
		goframeagent.WithLoopGovernance(governance),
		goframeagent.WithLoopLogger(loopLogger),
	)
}

// buildPlannerSystemPrompt returns the system prompt for the planning loop.
func (o *Orchestrator) buildPlannerSystemPrompt(issue Issue, workspaceDir string) string {
	return fmt.Sprintf(`You are planning the implementation of GitHub issue #%d.

## Issue
Title: %s
Description:
%s

## Workspace
%s

## Your task
Explore the codebase to understand what needs to change, then output a concise
implementation plan. Use the available tools to read relevant code before planning.
Do NOT write or edit any files.

## Available tools
- search_code(query) — semantic search over the indexed codebase
- get_symbol(name) — look up a specific symbol definition
- get_structure() — see the full project layout
- get_arch_context(dir) — architecture summary for a directory
- find_usages(symbol) / get_callers(fn) / get_callees(fn) — navigation
- read_file(path) — read a specific file (paths relative to workspace)
- list_dir(path?) — list directory contents

## Output format
After exploring, output your plan using EXACTLY this structure:

## Implementation Plan

### Summary
<1-2 sentences describing the overall approach>

### Files to modify
- <relative/path/to/file.go> — <reason>

### Files to create
- <relative/path/to/new_file.go> — <reason>
(omit this section if no new files are needed)

### Approach
<numbered step-by-step implementation notes>

### Risks
<potential complications, breaking changes, or edge cases to watch for>
(omit this section if none identified)`,
		issue.Number, issue.Title, truncateString(issue.Body, 2000), workspaceDir)
}

// fallbackPlan returns a minimal plan when the planning loop fails or returns empty.
func (o *Orchestrator) fallbackPlan(issue Issue) string {
	return fmt.Sprintf(`## Implementation Plan

### Summary
Implement GitHub issue #%d: %s

### Approach
1. Explore the codebase to understand the relevant code
2. Identify files that need to change
3. Implement the changes
4. Run lint and tests to verify`, issue.Number, issue.Title)
}
