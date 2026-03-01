# `/implement` Command Architecture

This document describes the architecture and flow of the `/implement` command, which enables autonomous code implementation by AI agents.

## Overview

The `/implement` command allows users to request an AI agent to automatically implement a GitHub issue. The agent uses the same RAG (Retrieval-Augmented Generation) infrastructure as the `/review` command to understand the codebase, implements changes, runs internal code review, and creates a pull request.

## Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────────┐
│                           /implement COMMAND FLOW                                │
├─────────────────────────────────────────────────────────────────────────────────┤
│                                                                                  │
│  ┌──────────────┐     ┌──────────────┐     ┌──────────────────────────────────┐ │
│  │   GitHub     │────►│   Webhook    │────►│     ReviewJob.RunImplementIssue │ │
│  │   Issue      │     │   Handler    │     │     (internal/jobs/review.go)     │ │
│  │  /implement  │     │              │     │                                  │ │
│  └──────────────┘     └──────────────┘     └─────────────┬────────────────────┘ │
│                                                            │                     │
│                                                            ▼                     │
│  ┌─────────────────────────────────────────────────────────────────────────────┐│
│  │                        Orchestrator.SpawnAgent                              ││
│  │                        (internal/agent/orchestrator.go)                     ││
│  │                                                                             ││
│  │   ┌─────────────────┐    ┌─────────────────┐    ┌─────────────────────┐   ││
│  │   │ Create Session │───►│ Clone Repo to   │───►│ Start MCP Server    │   ││
│  │   │ (session ID)   │    │ Isolated WS     │    │ (port 8081)         │   ││
│  │   └─────────────────┘    └─────────────────┘    └─────────────────────┘   ││
│  │                                                                             ││
│  │   ┌─────────────────────────────────────────────────────────────────────┐ ││
│  │   │                      OpenCode Agent Process                          │ ││
│  │   │                                                                      │ ││
│  │   │   System Prompt:                                                     │ ││
│  │   │   1. Understand - Read issue                                        │ ││
│  │   │   2. Explore - Use MCP tools to understand codebase                 │ ││
│  │   │   3. Plan - Identify files to modify                                │ ││
│  │   │   4. Implement - Write code                                          │ ││
│  │   │   5. Verify - Run make lint && make test                            │ ││
│  │   │   6. Review - Call review_code on changes                           │ ││
│  │   │   7. Iterate - Fix issues if REQUEST_CHANGES                        │ ││
│  │   │   8. Sync - Call push_branch                                         │ ││
│  │   │   9. Submit - Create pull request                                    │ ││
│  │   └─────────────────────────────────────────────────────────────────────┘ ││
│  └─────────────────────────────────────────────────────────────────────────────┘│
│                                                            │                     │
│                                                            ▼                     │
│  ┌─────────────────────────────────────────────────────────────────────────────┐│
│  │                           MCP Server                                        ││
│  │                           (internal/mcp/server.go)                          ││
│  │                                                                             ││
│  │   Transport: HTTP/SSE at http://127.0.0.1:8081/sse                        ││
│  │   Protocol: JSON-RPC 2.0                                                    ││
│  │                                                                             ││
│  │   Available Tools:                                                          ││
│  │   ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐           ││
│  │   │ search_code     │  │ get_arch_context │  │ get_symbol      │           ││
│  │   │ (semantic       │  │ (directory       │  │ (type/function  │           ││
│  │   │  search)        │  │  summaries)      │  │  definitions)   │           ││
│  │   └─────────────────┘  └─────────────────┘  └─────────────────┘           ││
│  │   ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐           ││
│  │   │ get_structure   │  │ review_code      │  │ push_branch     │           ││
│  │   │ (project tree)  │  │ (5-stage RAG)    │  │ (git push)      │           ││
│  │   └─────────────────┘  └─────────────────┘  └─────────────────┘           ││
│  │   ┌─────────────────┐  ┌─────────────────┐                                 ││
│  │   │ create_pull     │  │ list_issues/    │                                 ││
│  │   │ _request        │  │ get_issue       │                                 ││
│  │   └─────────────────┘  └─────────────────┘                                 ││
│  └─────────────────────────────────────────────────────────────────────────────┘│
│                                                            │                     │
│                                                            ▼                     │
│  ┌─────────────────────────────────────────────────────────────────────────────┐│
│  │                    review_code Tool Flow                                    ││
│  │                    (internal/mcp/tools.go:401-517)                          ││
│  │                                                                             ││
│  │   Input: diff (string)                                                     ││
│  │   ┌─────────────────┐                                                      ││
│  │   │ 1. Parse Diff   │  ──► Extract changed files from unified diff        ││
│  │   │    (ParseDiff)  │      (no VectorStore modification)                   ││
│  │   └────────┬────────┘                                                      ││
│  │            ▼                                                                ││
│  │   ┌─────────────────────────────────────────────────────────────────────┐  ││
│  │   │ 2. Generate Review (5-Stage RAG Pipeline)                          │  ││
│  │   │                                                                     │  ││
│  │   │   Uses existing main-branch VectorStore (no pollution)              │  ││
│  │   │                                                                     │  ││
│  │   │   Stage 1: Architectural Context                                   │  ││
│  │   │     └─► Get directory summaries from VectorStore (chunk_type=arch) │  ││
│  │   │                                                                     │  ││
│  │   │   Stage 2: HyDE Context (if enabled)                               │  ││
│  │   │     └─► Generate hypothetical code, search for similar             │  ││
│  │   │                                                                     │  ││
│  │   │   Stage 3: Impact Context                                          │  ││
│  │   │     └─► Find dependents via DependencyRetriever                    │  ││
│  │   │                                                                     │  ││
│  │   │   Stage 4: Description Context                                     │  ││
│  │   │     └─► MultiQuery from PR description                             │  ││
│  │   │                                                                     │  ││
│  │   │   Stage 5: Definitions Context                                     │  ││
│  │   │     └─► Resolve symbols from diff via DefinitionRetriever          │  ││
│  │   └─────────────────────────────────────────────────────────────────────┘  ││
│  │            ▼                                                                ││
│  │   ┌─────────────────┐                                                      ││
│  │   │ 3. LLM Review   │  ──► Generate StructuredReview                       ││
│  │   │    (Verdict,    │      {verdict, summary, suggestions[]}               ││
│  │   │    Suggestions) │                                                      ││
│  │   └─────────────────┘                                                      ││
│  └─────────────────────────────────────────────────────────────────────────────┘│
│                                                                                  │
│  Result: StructuredReview returned to agent for iteration decision             │
│                                                                                  │
└─────────────────────────────────────────────────────────────────────────────────┘
```

## Key Components

### 1. Orchestrator (`internal/agent/orchestrator.go`)

Manages the agent lifecycle:

| Method | Purpose |
|--------|---------|
| `SpawnAgent()` | Creates a new agent session |
| `runAgent()` | Main agent execution loop |
| `runAgentAPI()` | Execute via OpenCode HTTP API |
| `runAgentCLI()` | Execute via OpenCode CLI binary |
| `buildSystemPrompt()` | Constructs instructions for the agent |
| `prepareWorkspace()` | Clones repo to isolated directory |
| `verifyMCPConfig()` | Validates MCP server connectivity |

**Configuration (`internal/config/config.go`)**:

```yaml
agent:
  enabled: true
  provider: opencode
  model: llama3.1:70b
  timeout: 30m
  max_iterations: 3
  mcp_addr: "127.0.0.1:8081"
  opencode_addr: "http://127.0.0.1:8000"
  working_dir: "/tmp/code-warden-agents"
```

### 2. MCP Server (`internal/mcp/server.go`)

Provides JSON-RPC 2.0 interface over HTTP/SSE:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/sse` | GET | SSE connection for streaming |
| `/message` | POST | JSON-RPC message handling |
| `/` | POST | Direct JSON-RPC (fallback) |

**Protocol Methods**:
- `initialize` - Server handshake
- `tools/list` - List available tools
- `tools/call` - Execute a tool
- `ping` - Health check

### 3. MCP Tools (`internal/mcp/tools.go`, `internal/mcp/github_tools.go`)

| Tool | Input | Output | Description |
|------|-------|--------|-------------|
| `search_code` | `{query, limit?, chunk_type?}` | `{results: [{content, score, metadata}]}` | Semantic search in VectorStore |
| `get_arch_context` | `{directory}` | `{found, summaries[]}` | Get architectural summary for directory |
| `get_symbol` | `{name}` | `{found, definitions[]}` | Get type/function definition |
| `get_structure` | `{}` | `{projectRoot, directories[]}` | Get project structure |
| `review_code` | `{diff, title?, description?}` | `{verdict, confidence, summary, suggestions}` | **5-stage RAG review** |
| `push_branch` | `{branch, force?}` | `{status, message}` | Push local branch to remote |
| `create_pull_request` | `{title, body, head, base?, draft?}` | `{number, url, state}` | Create GitHub PR |
| `list_issues` | `{state?, labels?, limit?}` | `{count, issues[]}` | List repository issues |
| `get_issue` | `{number}` | `{number, title, body, ...}` | Get issue details |

### 4. RAG Service (`internal/rag/`)

The 5-stage context building pipeline:

```
buildRelevantContext(ctx, collection, embedder, repoPath, changedFiles, prContext)
    │
    ├─── Stage 1: Architectural Context
    │    └─── GetArchContextForPaths(changedFiles)
    │         └─── Query VectorStore (chunk_type="arch")
    │
    ├─── Stage 2: HyDE Context (optional)
    │    └─── GenerateHyDEContext(patch)
    │         ├─── Generate hypothetical code snippet
    │         └─── Search with generated snippet
    │
    ├─── Stage 3: Impact Context
    │    └─── GetDependencyNetwork(changedFiles)
    │         └─── DependencyRetriever.GetContextNetwork()
    │
    ├─── Stage 4: Description Context
    │    └─── GetDescriptionContext(prTitle + prBody)
    │         └─── MultiQuery retrieval
    │
    └─── Stage 5: Definitions Context
         └─── gatherDefinitionsContext(changedFiles)
              ├─── Extract symbols from patch
              └─── DefinitionRetriever.GetDefinition(symbol)
```

## Session Lifecycle

```
┌─────────────────────────────────────────────────────────────────────┐
│                     Session State Machine                            │
├─────────────────────────────────────────────────────────────────────┤
│                                                                      │
│   ┌────────────┐     ┌────────────┐     ┌────────────┐            │
│   │  PENDING   │────►│  RUNNING   │────►│ REVIEWING   │            │
│   │            │     │            │     │            │            │
│   └────────────┘     └─────┬──────┘     └─────┬──────┘            │
│                            │                   │                    │
│                            │                   │                    │
│                            ▼                   ▼                    │
│                     ┌────────────┐     ┌────────────┐            │
│                     │  FAILED    │     │ COMPLETED  │            │
│                     │            │     │            │            │
│                     └────────────┘     └────────────┘            │
│                            │                   ▲                    │
│                            │                   │                    │
│                            ▼                   │                    │
│                     ┌────────────┐            │                    │
│                     │ CANCELLED  │────────────┘                    │
│                     │            │                                 │
│                     └────────────┘                                 │
│                                                                      │
└─────────────────────────────────────────────────────────────────────┘

Session States:
- PENDING:   Session created, waiting to start
- RUNNING:   Agent is implementing changes
- REVIEWING: Agent is running review_code (subset of RUNNING)
- COMPLETED: PR created successfully
- FAILED:    Agent encountered an error
- CANCELLED: Session was cancelled (timeout or manual)
```

## Data Flow

### Input Event

```go
// core/events.go
type GitHubEvent struct {
    Type           EventType      // ImplementIssue
    IssueNumber    int            // Issue to implement
    IssueTitle     string         // Issue title
    IssueBody      string         // Issue description
    UserInstructions string       // Additional instructions from comment
    RepoOwner      string         // Repository owner
    RepoName       string         // Repository name
    RepoFullName   string         // "owner/name"
    InstallationID int64          // GitHub App installation ID
}
```

### Session Result

```go
// internal/agent/orchestrator.go
type Result struct {
    PRNumber      int      // Created PR number
    PRURL         string   // PR URL
    Branch        string   // Branch name (e.g., "agent/issue-123")
    FilesChanged  []string // List of modified files
    ReviewSummary string   // Summary from review_code
    Verdict       string   // "APPROVE", "REQUEST_CHANGES", "COMMENT"
    Iterations    int      // Number of review iterations
}
```

### Review Output

```go
// internal/core/structured_review.go
type StructuredReview struct {
    Verdict     string       // APPROVE, REQUEST_CHANGES, COMMENT
    Confidence  int          // 0-100
    Summary     string       // Markdown summary
    Suggestions []Suggestion // Line-specific feedback
}

type Suggestion struct {
    FilePath    string // File path
    LineNumber  int    // Line number
    Severity    string // "critical", "warning", "info"
    Comment     string // Suggestion text
    Rationale   string // Why this change is needed
    Suggestion  string // Code suggestion (optional)
}
```

## Error Handling

### Session Failures

| Failure Mode | Handling |
|--------------|----------|
| MCP server startup | Return error, don't spawn agent |
| OpenCode process exit | Mark session FAILED, log output |
| Timeout exceeded | Cancel session, mark CANCELLED |
| PR creation fails | Mark session FAILED, report error |

### Recovery

```go
// Cleanup on session end
defer func() {
    if session.cancel != nil {
        session.cancel()
    }
}()

// Cleanup old sessions (every 5 minutes)
func (o *Orchestrator) cleanupOldSessions() {
    cutoff := time.Now().Add(-1 * time.Hour)
    for id, session := range o.sessions {
        if session.CompletedAt.Before(cutoff) {
            delete(o.sessions, id)
        }
    }
}
```

## Limitations and Considerations

### Current Limitations

1. **No Lint/Test Verification**: The system prompt instructs the agent to run `make lint && make test`, but there's no MCP tool to verify execution.

2. **No Post-PR Review**: After PR creation, no automatic `/review` is triggered on the actual PR for GitHub visibility.

3. **In-Memory Sessions**: Session state is stored in memory and lost on server restart.

### Resolved Issues

1. ~~**VectorStore Sharing**~~: ✅ Fixed - The `review_code` tool no longer indexes agent changes. It queries the existing main-branch VectorStore, preventing pollution from work-in-progress code.

2. ~~**No Concurrency Limits**~~: ✅ Fixed - `MaxConcurrentSessions` config (default: 3) limits parallel agents. New requests are rejected when limit is reached.

3. ~~**No Workspace Cleanup**~~: ✅ Fixed - Workspaces are cleaned up after session completion and during periodic cleanup of old sessions.

### Security Considerations

1. **Branch Name Validation**: `validBranchName` regex prevents command injection:
   ```go
   var validBranchName = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9/_\-\.]*[a-zA-Z0-9])?$`)
   ```

2. **Input Limits**: All MCP tools validate input lengths:
   ```go
   const (
       maxQueryLength   = 10000
       maxDiffLength    = 1000000  // 1MB
       maxTitleLength   = 500
       maxSymbolLength  = 200
   )
   ```

3. **Isolated Workspace**: Each agent works in `/tmp/code-warden-agents/<session-id>`

4. **MCP Server Binding**: Server binds to `127.0.0.1` only (localhost), not externally accessible.

## Configuration Reference

```yaml
# config.yaml
agent:
  enabled: true                    # Enable /implement functionality
  provider: opencode               # Agent provider (currently only opencode)
  model: llama3.1:70b              # Model for implementation
  timeout: 30m                     # Maximum session duration
  max_iterations: 3                # Max review iterations before failure
  max_concurrent_sessions: 3       # Max parallel agent sessions (default: 3)
  mcp_addr: "127.0.0.1:8081"       # MCP server bind address
  opencode_addr: "http://127.0.0.1:8000"  # OpenCode API endpoint
  working_dir: "/tmp/code-warden-agents"  # Workspace directory

ai:
  llm_provider: ollama              # Provider for RAG
  generator_model: gemma3:latest   # Model for reviews
  embedder_model: nomic-embed-text # Model for embeddings
  enable_hyde: true                # Enable HyDE context stage
  comparison_models: []            # Optional: multi-model consensus
```

## Future Improvements

1. ~~**Session-Scoped VectorStore**~~: ✅ Resolved - The `review_code` tool no longer indexes agent changes, using the existing main-branch VectorStore instead.

2. **Auto-PR Review**: Trigger standard `/review` workflow after PR creation for GitHub visibility.

3. **Command Verification Tool**: Add `run_command` MCP tool to verify lint/test execution.

4. **Session Persistence**: Store session state in database for recovery after restart.

5. **Iteration Logging**: Track each review iteration with detailed feedback.

6. ~~**Concurrency Controls**~~: ✅ Resolved - `MaxConcurrentSessions` config (default: 3) limits parallel agents.

7. ~~**Workspace Cleanup**~~: ✅ Resolved - Workspaces are cleaned up after session completion and during periodic cleanup.

8. **Progress Updates**: Comment on issue with implementation progress.