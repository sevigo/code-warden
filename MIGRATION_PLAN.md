# Plan: Migrate code-warden from OpenCode CLI to goframe/agent SDK

## Architecture Overview

### Current Flow

```
GitHub Issue (/implement comment)
        │
        ▼
┌─────────────────────────────────────────────────────┐
│                   code-warden                        │
│                                                      │
│  ┌─────────────┐    exec.Command      ┌───────────┐ │
│  │ Orchestrator├──────────────────────►│ OpenCode  │ │
│  │             │   "opencode run"      │   CLI     │ │
│  │             │                       │           │ │
│  │             │◄──────────────────────┤           │ │
│  │             │   Parse CLI output    │           │ │
│  └─────────────┘    AGENT_RESULT       └───────────┘ │
│         │                                        ▲   │
│         │ MCP HTTP Server                         │   │
│         │ (127.0.0.1:8081)                         │   │
│         ▼                                          │   │
│  ┌─────────────┐                                  │   │
│  │ MCP Server  │◄─────────────────────────────────┘   │
│  │             │        MCP SSE Connection            │
│  │ Tools:      │                                       │
│  │ - search_code        (RAG lookup)                 │
│  │ - get_arch_context   (Directory summary)          │
│  │ - get_symbol         (Definition lookup)           │
│  │ - get_structure      (Project layout)              │
│  │ - review_code        (Code review via RAG)         │
│  │ - push_branch         (Git operations)              │
│  │ - create_pull_request (GitHub API)                 │
│  │ - list_issues/get_issue                             │
│  └─────────────┘                                       │
│         │                                              │
│         ▼                                              │
│  ┌─────────────┐                                       │
│  │ RAG Service │◄─── Qdrant vector store               │
│  │             │    (indexed codebase)                 │
│  └─────────────┘                                       │
└─────────────────────────────────────────────────────┘
```

### Key Files

| File | Purpose |
|------|---------|
| `cmd/server/main.go` | Webhook server entry point |
| `cmd/cli/review.go` | CLI for `warden-cli review <pr-url>` |
| `internal/agent/orchestrator.go` | Spawns OpenCode CLI, manages sessions |
| `internal/agent/workspace.go` | Isolated git worktree setup |
| `internal/agent/session.go` | Session state (pending, running, completed) |
| `internal/mcp/server.go` | MCP HTTP/SSE server with tools |
| `internal/mcp/tools/*.go` | Individual MCP tools |
| `internal/rag/*.go` | RAG service for code search |

### Current Implementation Details

#### 1. Orchestrator spawns CLI (lines 411-649)
```go
// orchestrator.go:614-619
cmd := exec.CommandContext(ctx, "opencode",
    "run",
    "--model", o.config.Model,
    "--agent", "build",
    systemPrompt,  // Built with MCP server URL, tools, instructions
)
cmd.Env = append(os.Environ(),
    "OPENCODE_MAX_ITERATIONS="+fmt.Sprintf("%d", o.config.MaxIterations),
    "OPENCODE_BRANCH="+branch,
    // ... more env vars
)
```

#### 2. System Prompt Structure (lines 487-599)
The prompt includes:
- MCP server URL for tool access
- Tool descriptions (search_code, review_code, push_branch, create_pull_request)
- Step-by-step instructions (Explore → Plan → Implement → Verify → Review → Submit)
- **Critical**: Review iteration loop with `AGENT_ITERATION: X` markers
- **Critical**: Final `AGENT_RESULT: {...}` JSON output

#### 3. CLI Output Parsing (lines 694-746)
```go
// Parse AGENT_RESULT sentinel
for _, line := range lines {
    if strings.HasPrefix(line, "AGENT_RESULT:") {
        jsonStr := strings.TrimPrefix(line, "AGENT_RESULT:")
        var res Result
        json.Unmarshal([]byte(jsonStr), &res)
    }
}
```

#### 4. MCP Tools Available
- `search_code(query, limit, chunk_type)` - RAG search
- `get_arch_context(directory)` - Directory summary
- `get_symbol(name)` - Type/function definition
- `get_structure()` - Project structure
- `review_code(diff)` - Code review (uses RAG + LLM)
- `push_branch(branch)` - Git push
- `create_pull_request(title, body, head, base)` - GitHub PR
- `list_issues(state, labels)` / `get_issue(number)` - GitHub issues

---

## Target Architecture

### Using goframe/agent SDK

```
GitHub Issue (/implement comment)
        │
        ▼
┌─────────────────────────────────────────────────────┐
│                   code-warden                        │
│                                                      │
│  ┌─────────────┐    agent.FeedbackLoop   ┌────────┐ │
│  │ Orchestrator├──────────────────────────►│OpenCode│ │
│  │             │   SDK: agent.New()       │ Server │ │
│  │             │   WithMCPRegistry()      │        │ │
│  │             │   WithReviewHandler()     │        │ │
│  │             │◄──────────────────────────┤        │ │
│  │             │   session.Prompt()        │        │ │
│  └─────────────┘   FeedbackLoop.Result     └────────┘ │
│         │                                        ▲   │
│         │ MCP HTTP Server (UNCHANGED)            │   │
│         │ (127.0.0.1:8081)                        │   │
│         ▼                                         │   │
│  ┌─────────────┐                                  │   │
│  │ MCP Server  │◄─────────────────────────────────┘   │
│  │ (UNCHANGED) │                                       │
│  │             │                                       │
│  │ Tools:      │                                       │
│  │ - search_code, get_arch_context, get_symbol        │
│  │ - review_code (called by ReviewHandler)           │
│  │ - push_branch, create_pull_request                │
│  └─────────────┘                                       │
│         │                                              │
│         ▼                                              │
│  ┌─────────────┐                                       │
│  │ RAG Service │◄─── Qdrant (UNCHANGED)               │
│  └─────────────┘                                       │
└─────────────────────────────────────────────────────┘
```

### Key Concept: Keep MCP Server, Replace CLI with SDK

**What stays the same:**
- `internal/mcp/server.go` - MCP HTTP/SSE server unchanged
- `internal/mcp/tools/*.go` - All tools unchanged
- `internal/rag/*.go` - RAG service unchanged
- MCP server still runs on `127.0.0.1:8081`

**What changes:**
- Remove `exec.Command("opencode", ...)`
- Use `agent.New(WithBaseURL(openCodeServerURL))`
- Use `FeedbackLoop` with custom `ReviewHandler`
- Use `PRHandler` for PR creation
- Direct `Result` struct instead of output parsing

---

## Implementation Plan

### Phase 1: Add goframe Dependency (1 hour)

**`go.mod`:**
```go
require (
    // ... existing ...
    github.com/sevigo/goframe v0.31.0
)
```

```bash
cd ~/sevigo/code-warden
go get github.com/sevigo/goframe@v0.31.0
```

### Phase 2: Create AgentSDK Wrapper (3 hours)

**`internal/agent/sdk_client.go` (NEW):**

```go
package agent

import (
    "context"
    "fmt"
    
    goframeagent "github.com/sevigo/goframe/agent"
    "github.com/sevigo/code-warden/internal/core"
    "github.com/sevigo/code-warden/internal/mcp"
)

// SDKClient wraps goframe/agent for code-warden.
type SDKClient struct {
    agent      *goframeagent.Agent
    mcpServer  *mcp.Server
    config     Config
}

// NewSDKClient creates a new SDK-based agent client.
func NewSDKClient(
    openCodeURL string,  // e.g., "http://localhost:3000"
    mcpServer *mcp.Server,
    config Config,
) (*SDKClient, error) {
    // Create remote MCP server reference
    mcpRegistry := goframeagent.NewMCPRegistry(
        goframeagent.RemoteMCPServer("code-warden",
            fmt.Sprintf("http://%s/sse", config.MCPAddr),
            goframeagent.WithEnabled(true),
        ),
    )
    
    ag, err := goframeagent.New(
        goframeagent.WithBaseURL(openCodeURL),
        goframeagent.WithModel(config.Model),
        goframeagent.WithMCPRegistry(mcpRegistry),
        goframeagent.WithWorkingDir(config.WorkingDir),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to create agent: %w", err)
    }
    
    return &SDKClient{
        agent:     ag,
        mcpServer: mcpServer,
        config:    config,
    }, nil
}

// ImplementResult represents the result of implementation.
type ImplementResult struct {
    PRNumber     int
    PRURL        string
    Branch       string
    FilesChanged []string
    Verdict      string
    Iterations   int
}
```

### Phase 3: Implement Review Handler (2 hours)

**`internal/agent/review_handler.go` (NEW):**

```go
package agent

import (
    "context"
    "fmt"
    "strings"
    
    goframeagent "github.com/sevigo/goframe/agent"
)

// createReviewHandler creates a handler that uses MCP review_code tool.
func (c *SDKClient) createReviewHandler() goframeagent.ReviewHandler {
    return func(ctx context.Context, session *goframeagent.Session, implementation string) (*goframeagent.ReviewResult, error) {
        // The agent will call review_code MCP tool internally
        // We need to prompt it to use the tool
        
        reviewPrompt := fmt.Sprintf(`
Please use the review_code tool to review the implementation you just made.

After the review, respond with either:
- APPROVE: if the code looks good
- REQUEST_CHANGES: if there are issues to fix (list them)

Review feedback:
%s
`, implementation)

        response, err := session.Prompt(ctx, reviewPrompt)
        if err != nil {
            return nil, fmt.Errorf("review request failed: %w", err)
        }
        
        // Parse verdict from response
        approved := strings.Contains(strings.ToUpper(response.Content), "APPROVE") ||
                   strings.Contains(strings.ToUpper(response.Content), "COMMENT")
        
        return &goframeagent.ReviewResult{
            Approved: approved,
            Feedback: response.Content,
            Score:    80.0,
        }, nil
    }
}

// createPRHandler creates a handler that uses MCP create_pull_request tool.
func (c *SDKClient) createPRHandler(issue *core.GitHubEvent, branch string) goframeagent.PRHandler {
    return func(ctx context.Context, session *goframeagent.Session, implementation string, review *goframeagent.ReviewResult) error {
        if !review.Approved {
            return nil // Don't create PR if not approved
        }
        
        prPrompt := fmt.Sprintf(`
Please use the create_pull_request tool to create a pull request:
- Title: "Fix #%d: %s"
- Head: %s
- Base: main
- Body: Describe the changes you made
`, issue.PRNumber, issue.PRTitle, branch)
        
        _, err := session.Prompt(ctx, prPrompt)
        return err
    }
}
```

### Phase 4: Modify Orchestrator (4 hours)

**`internal/agent/orchestrator.go` changes:**

```go
// OLD: runAgentCLI - SPAWNS CLI SUBPROCESS
func (o *Orchestrator) runAgentCLI(ctx context.Context, session *Session, systemPrompt, branch string) {
    // ... exec.Command("opencode", ...) ...
}

// NEW: runAgentSDK - USES GOFRAME SDK
func (o *Orchestrator) runAgentSDK(ctx context.Context, session *Session, branch string) (*Result, error) {
    // Create SDK client
    client, err := NewSDKClient(
        o.config.OpenCodeURL,  // e.g., "http://localhost:3000"
        o.mcpServer,
        o.config,
    )
    if err != nil {
        return nil, fmt.Errorf("failed to create SDK client: %w", err)
    }
    
    // Create agent session
    agSession, err := client.agent.NewSession(ctx,
        goframeagent.WithTitle(fmt.Sprintf("Issue #%d", session.Issue.Number)),
    )
    if err != nil {
        return nil, fmt.Errorf("failed to create session: %w", err)
    }
    defer agSession.Close()
    
    // Build implementation request
    req := goframeagent.ImplementRequest{
        Task: fmt.Sprintf("Implement GitHub issue #%d: %s",
            session.Issue.Number, session.Issue.Title),
        Context: o.buildContext(session.Issue),
        Constraints: o.getConstraints(),
    }
    
    // Create feedback loop
    fl := goframeagent.NewFeedbackLoop(client.agent, agSession,
        goframeagent.WithMaxRetries(o.config.MaxIterations),
        goframeagent.WithReviewHandler(client.createReviewHandler()),
        goframeagent.WithPRHandler(client.createPRHandler(session.Issue, branch)),
    )
    
    // Run implementation
    result, err := fl.ImplementWithReview(ctx, req)
    if err != nil {
        return nil, fmt.Errorf("implementation failed: %w", err)
    }
    
    // Parse result
    return &Result{
        Branch:       branch,
        FilesChanged: extractFiles(result.Implementation),
        Verdict:      "APPROVED",
        Iterations:   result.Response.Tokens.Input,
    }, nil
}

// buildContext creates the context for implementation.
func (o *Orchestrator) buildContext(issue Issue) string {
    var builder strings.Builder
    
    // Add project context from RAG
    if o.repo != nil && o.repo.GeneratedContext != "" {
        builder.WriteString("## Project Context\n")
        builder.WriteString(o.repo.GeneratedContext)
        builder.WriteString("\n\n")
    }
    
    // Add issue details
    builder.WriteString("## Issue Details\n")
    builder.WriteString(fmt.Sprintf("Number: %d\n", issue.Number))
    builder.WriteString(fmt.Sprintf("Title: %s\n", issue.Title))
    builder.WriteString(fmt.Sprintf("Body: %s\n", issue.Body))
    
    if issue.Instructions != "" {
        builder.WriteString(fmt.Sprintf("\nAdditional Instructions: %s\n", issue.Instructions))
    }
    
    return builder.String()
}

// getConstraints returns implementation constraints.
func (o *Orchestrator) getConstraints() []string {
    constraints := []string{
        "Run verification commands before requesting review",
        "Use search_code tool to understand the codebase first",
        "All tests must pass before creating PR",
    }
    
    if o.repoConfig != nil && len(o.repoConfig.VerifyCommands) > 0 {
        for _, cmd := range o.repoConfig.VerifyCommands {
            constraints = append(constraints, fmt.Sprintf("Run: %s", cmd))
        }
    } else {
        constraints = append(constraints, "Run: make lint", "Run: make test")
    }
    
    return constraints
}
```

### Phase 5: Update runAgent Method (1 hour)

**`internal/agent/orchestrator.go`:**

```go
func (o *Orchestrator) runAgent(ctx context.Context, session *Session) {
    defer func() {
        if session.cancel != nil {
            session.cancel()
        }
    }()
    
    logSessionStart(o.logger, session)
    session.SetStatus(StatusRunning)
    
    branch := gitutil.SanitizeBranch(fmt.Sprintf("agent/%s", session.ID))
    
    // CHOOSE: Use CLI or SDK based on config
    var result *Result
    var err error
    
    if o.config.UseSDK {
        result, err = o.runAgentSDK(ctx, session, branch)
    } else {
        result, err = o.runAgentCLI(ctx, session, o.buildSystemPrompt(session.Issue, branch), branch)
    }
    
    if err != nil {
        session.SetStatus(StatusFailed)
        session.SetError(err.Error())
        return
    }
    
    session.SetResult(result)
    session.SetStatus(StatusCompleted)
}
```

### Phase 6: Config Changes (30 min)

**`internal/config/config.go`:**

```go
type AgentConfig struct {
    Enabled               bool          `yaml:"enabled"`
    Provider              string        `yaml:"provider"`        // "opencode" or "opencode-sdk"
    OpenCodeURL           string        `yaml:"opencode_url"`     // NEW: for SDK mode
    Model                 string        `yaml:"model"`
    Timeout               time.Duration `yaml:"timeout"`
    MaxIterations         int           `yaml:"max_iterations"`
    MaxConcurrentSessions int           `yaml:"max_concurrent_sessions"`
    MCPAddr               string        `yaml:"mcp_addr"`
    WorkingDir            string        `yaml:"working_dir"`
    UseSDK                bool          `yaml:"use_sdk"`          // NEW: toggle CLI vs SDK
    // ... existing fields
}

func DefaultAgentConfig() AgentConfig {
    return AgentConfig{
        Enabled:               false,
        Provider:              "opencode-sdk",  // NEW default
        OpenCodeURL:           "http://localhost:3000",  // NEW
        Model:                 "ollama/llama3.1:70b",
        Timeout:               30 * time.Minute,
        MaxIterations:         3,
        MaxConcurrentSessions: 3,
        MCPAddr:               "127.0.0.1:8081",
        WorkingDir:            "/tmp/code-warden-agents",
        UseSDK:                true,  // NEW: default to SDK
    }
}
```

### Phase 7: Remove Obsolete Code (1 hour)

After SDK mode is stable:

**Remove:**
- `buildOpenCodeCommand()` - No longer needed
- `createOpenCodeConfig()` - Temp config files not needed
- `parseAgentOutput()` - SDK returns structured Result
- `buildSystemPrompt()` - Replaced by `buildContext()` + SDK prompt building

**Simplify:**
- `workspace.go` - Keep for isolated worktrees
- `session.go` - Keep for state tracking

---

## Docker Integration

### Update `docker-compose.yml`

```yaml
services:
  # ... existing services (qdrant, ollama, etc.)
  
  opencode:
    image: ghcr.io/anomalyco/opencode:latest
    ports:
      - "3000:3000"
    environment:
      - OPENCODE_WORKING_DIR=/app/workspace
      - OPENCODE_MODEL=${OPENCODE_MODEL:-ollama/llama3.1:70b}
    volumes:
      - .:/app/workspace
    command: ["serve", "--port", "3000", "--hostname", "0.0.0.0"]
    depends_on:
      - qdrant
      - ollama
  
  code-warden:
    build: .
    ports:
      - "8080:8080"  # Webhook server
    environment:
      - CW_AGENT_ENABLED=true
      - CW_AGENT_PROVIDER=opencode-sdk
      - CW_AGENT_OPENCODE_URL=http://opencode:3000
      - CW_AGENT_MCP_ADDR=0.0.0.0:8081
    depends_on:
      - opencode
      - qdrant
```

---

## Testing Strategy

### Unit Tests

```go
// internal/agent/sdk_client_test.go
func TestSDKClientImplementIssue(t *testing.T) {
    mockAgent := &MockAgent{}  // Mock goframe/agent
    client := &SDKClient{agent: mockAgent}
    
    result, err := client.ImplementIssue(ctx, ImplementRequest{
        Task: "Test issue",
    })
    
    assert.NoError(t, err)
    assert.NotNil(t, result)
}
```

### Integration Tests

```go
// internal/agent/sdk_integration_test.go
//go:build integration

func TestSDKWithRealOpenCode(t *testing.T) {
    if testing.Short() {
        t.Skip("Skipping integration test")
    }
    
    // Requires OpenCode server running
    client, _ := NewSDKClient("http://localhost:3000", ...)
    
    result, err := client.ImplementIssue(ctx, req)
    // ...
}
```

---

## Migration Path

### Stage 1: Parallel Implementation (1 week)
- Keep CLI mode working
- Add SDK mode behind config flag
- Test SDK mode with simple issues

### Stage 2: Optimize SDK Mode (1 week)
- Fine-tune prompts
- Optimize ReviewHandler and PRHandler
- Add error handling and retries

### Stage 3: Deprecate CLI Mode (1 week)
- Make SDK the default
- Remove CLI-specific code
- Update documentation

---

## Files to Create/Modify

### New Files
| File | Purpose |
|------|---------|
| `internal/agent/sdk_client.go` | SDKClient wrapper |
| `internal/agent/review_handler.go` | ReviewHandler implementation |
| `internal/agent/sdk_integration_test.go` | Integration tests |

### Modified Files
| File | Changes |
|------|---------|
| `internal/agent/orchestrator.go` | Add `runAgentSDK()`, modify `runAgent()` |
| `internal/config/config.go` | Add `OpenCodeURL`, `UseSDK` fields |
| `docker-compose.yml` | Add OpenCode server |
| `go.mod` | Add goframe dependency |

### Deprecated Files (remove after Stage 3)
| File | Reason |
|------|--------|
| `buildOpenCodeCommand()` in orchestrator.go | Replaced by SDK |
| `createOpenCodeConfig()` in orchestrator.go | Not needed for SDK |
| `parseAgentOutput()` in orchestrator.go | SDK returns Result struct |

---

## Benefits

| Current (CLI) | New (SDK) |
|--------------|-----------|
| Subprocess spawn overhead | Direct API calls |
| Parse CLI output for sentinels | Structured Result type |
| String formatting for prompts | `ImplementRequest{}` struct |
| Custom feedback loop logic | Built-in `FeedbackLoop` |
| Manual session cleanup | `io.Closer` with defer |
| Temp config files | In-memory config |

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| OpenCode server not running | Health check before operations |
| SDK API changes | Vendor goframe v0.31.0 |
| MCP connection issues | Retry logic with backoff |
| Both modes needed | Config flag to toggle CLI/SDK |

---

## Estimated Effort

| Phase | Hours | Priority |
|-------|-------|----------|
| Phase 1: Dependency | 1 | P0 |
| Phase 2: SDK Client | 3 | P0 |
| Phase 3: Review Handler | 2 | P0 |
| Phase 4: Orchestrator | 4 | P0 |
| Phase 5: Run Method | 1 | P0 |
| Phase 6: Config | 0.5 | P1 |
| Phase 7: Cleanup | 1 | P2 |
| **Total** | **12.5 hours** | |

---

## Questions to Resolve

1. **OpenCode deployment**: Should OpenCode run in the same container or separate?
2. **MCP URL**: Is the MCP server URL correctly formatted for SDK's RemoteMCPServer?
3. **Branch management**: Does the agent handle branch creation or should code-warden create it first?
4. **Result extraction**: How to extract PR number/URL from agent response?

## Next Steps

1. Verify OpenCode server setup in docker-compose
2. Create SDKClient wrapper with basic New/Implement methods
3. Test ReviewHandler with existing MCP tools
4. Modify Orchestrator.runAgent to use SDK
5. Deploy and test with real issues