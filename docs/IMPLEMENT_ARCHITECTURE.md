# `/implement` Command Architecture

This document describes the architecture and data-flow of the `/implement` command, which drives autonomous code implementation via the **Warden agent harness**.

---

## Overview

When a GitHub issue comment contains `/implement`, Code-Warden:

1. Spawns a `Session` and persists it to PostgreSQL.
2. Clones the repository to an isolated workspace.
3. Runs the **Warden phased loop** (plan → implement → publish).
4. Posts real-time GitHub progress comments throughout.
5. Opens a draft PR once the implementation is approved.

The harness is entirely in-process: no external agent binary, no OpenCode subprocess.  
Tools are called as direct Go function calls inside `goframe.AgentLoop`.

---

## High-Level Flow

```
GitHub issue comment "/implement"
        │
        ▼
WebhookHandler  →  ReviewJob.RunImplementIssue
        │
        ▼
Orchestrator.SpawnAgent(ctx, issue)
  ├── create Session (status=pending, persist to DB)
  ├── go runAgent(ctx, session)          ← goroutine
  └── return Session immediately

runAgent  dispatches by mode:
  ├── "warden"  →  runWardenAgent()      ← current production mode
  └── "native"  →  runInProcessAgent()  ← simpler legacy mode
```

---

## Warden Agent — Three-Phase Loop

```
runWardenAgent(ctx, session, branch)
│
├─ prepareAgentWorkspace()
│    ├── git clone projectRoot → /tmp/code-warden-agents/<id>
│    ├── git remote set-url origin  (with token for push)
│    ├── RegisterWorkspace(session.ID, dir)  ← MCP routing
│    ├── open agent.log
│    └── lsp.NewManager().Start()  ← gopls + language servers
│
├─ persistSessionRunning(branch)
│
├─ progressTracker.start()          ← 30s GitHub comment ticker
│
├─ ── Phase 1: Plan ──────────────────────────────────────────────────
│   tracker.setPhase("planning")
│   buildPlan()  →  buildPlannerLoop()
│     tools: search_code, get_symbol, get_structure, get_arch_context,
│            find_usages, get_callers, get_callees, read_file, list_dir
│     max_iterations: 5
│     output: markdown "## Implementation Plan" injected into implement prompt
│
├─ ── Phase 2: Implement ─────────────────────────────────────────────
│   tracker.setPhase("implementing")
│   buildImplementLoop()
│     tools: ALL MCP tools except push_branch / create_pull_request
│            + read_file, write_file, edit_file, list_dir  (file tools)
│            + lsp_diagnostics, lsp_definition, lsp_references, lsp_hover
│     max_iterations: max(MaxIterations*10, 30)
│     compactionHook: fires at 70% of 128K tokens, summarises history
│
│   loop.Run()  →  Think → call tools → Observe → repeat
│     agent explores, writes code, runs lint/tests, calls review_code
│     loop exits when agent produces a final response (no more tool calls)
│
│   HARD GATE: GetReviewBySession(session.ID) must be "APPROVE"
│              → if not: failSession()
│
├─ ── Phase 3: Publish ───────────────────────────────────────────────
│   tracker.setPhase("publishing")
│   buildPublishLoop()
│     tools: push_branch, create_pull_request  ONLY
│     max_iterations: 5
│
│   loop.Run()  →  push branch → open draft PR
│
├─ persistSessionCompleted(result)
├─ postSessionCompleted(result)    ← GitHub comment with PR link
└─ cleanupNativeSession()
     ├── cleanupWorkspace()        ← rm -rf /tmp/code-warden-agents/<id>
     └── UnregisterWorkspace()
```

---

## LSP Integration (`internal/agent/lsp/`)

The Language Server Protocol client provides precise, always-current code navigation during the implement phase — complementing the RAG-based `search_code` tools that are better for open-ended exploration.

### Architecture

```
lsp.Manager
  ├── detectLanguages()        ← walks workspace, collects extensions
  ├── Start(ctx)               ← starts one Client per detected language
  │     GoServer       (.go)   → gopls
  │     TypeScriptServer (.ts) → typescript-language-server
  │     PythonServer    (.py)  → pylsp
  │     RustServer      (.rs)  → rust-analyzer
  └── clientForFile(absPath)   ← routes tool calls to correct client

lsp.Client (per language)
  ├── starts server subprocess via stdio
  ├── JSON-RPC 2.0 with Content-Length framing
  ├── readLoop()               ← goroutine: routes responses + notifications
  ├── Diagnostics()            ← pull (textDocument/diagnostic) with push fallback
  ├── Definition() / References() / Hover()
  └── DidOpen() / DidChange()  ← keeps server in sync with file edits
```

### LSP hook on file writes

Every `write_file` and `edit_file` call automatically:
1. Sends `DidChange` to the language server.
2. Waits 700 ms for diagnostics to settle.
3. Calls `Diagnostics()` and appends results to the tool's return value.

The agent sees compiler errors in the **same turn** it made the change — no extra tool call needed.

### Agent-facing LSP tools

| Tool | Parameters | Description |
|------|-----------|-------------|
| `lsp_diagnostics` | `path` | Compiler errors and warnings for a file |
| `lsp_definition` | `path, line, column` | Jump-to-definition (0-based) |
| `lsp_references` | `path, line, column` | All usages of a symbol |
| `lsp_hover` | `path, line, column` | Type signature and doc comment |

LSP tools are registered only when at least one language server started successfully. If `gopls` is not on PATH, the agent falls back to RAG-based `search_code` transparently.

---

## Phase-Based Tool Scoping

Tools are assigned to loops architecturally — the model literally cannot call a publish tool during implementation.

| Phase | Loop | Tools available |
|-------|------|----------------|
| Plan | `buildPlannerLoop` | `search_code`, `get_symbol`, `get_structure`, `get_arch_context`, `find_usages`, `get_callers`, `get_callees`, `read_file`, `list_dir` |
| Implement | `buildImplementLoop` | All MCP tools except `push_branch` / `create_pull_request`, plus file tools and LSP tools |
| Publish | `buildPublishLoop` | `push_branch`, `create_pull_request` **only** |

`publishToolNames` is the single source of truth for what is withheld during implementation (see `warden.go`).

---

## Progress Tracking (`internal/agent/progress.go`)

```
progressTracker
  ├── start(ctx)          ← goroutine: ticks every 30 s
  ├── setPhase(phase)     ← called at each phase boundary
  ├── record(tool, ok)    ← called by progressTool.Execute after every tool
  │     writes timestamped line to agent.log immediately
  │     appends to in-memory entries list
  └── maybePostComment()  ← posts GitHub comment if new entries since last post
        buildCommentBody() → table: phase, tool count, recent activity list

progressTool (wraps every registered tool)
  Execute(ctx, args):
    1. inner.Execute(ctx, args)
    2. tracker.record(name, err==nil)
    return result, err
```

GitHub receives a progress comment every 30 seconds showing:
- Current phase (planning / implementing / publishing)
- Total tool calls so far
- Last 6 tool names with ✓/✗ status

---

## Context Compaction

Long implement loops (GLM-5.1 / MiniMax M2.7 run 30–100+ iterations) accumulate conversation history that can approach the model's context window.

The compaction hook fires when `tokens.Input + tokens.Output > 128_000 * 0.70`:

```
buildCompactionHook(session) → func(ctx, msgs, tokens) []schema.MessageContent
  if used < threshold: return nil  ← no-op, loop continues unchanged
  
  build plain-text transcript of msgs[1:]  ← exclude system prompt
  call LLM with summarization prompt (max 400 words)
  
  rebuild history:
    [0] system prompt           (preserved verbatim)
    [1] "## Context Summary…"  (LLM summary)
    [2..5] last 4 messages     (current iteration context)
  
  return compacted  ← goframe replaces messages, increments result.Compactions
```

If the summarization call fails, the hook returns `nil` and the loop continues with the full history (graceful degradation).

The hook is wired via `goframeagent.WithLoopCompactionHook` added in goframe v0.36.6.

---

## Session Persistence (`internal/storage/agent_session.go`)

Every session is persisted to the `agent_sessions` PostgreSQL table defined in `agent_schema.sql`.

### State transitions

| Event | Status written | Method |
|-------|---------------|--------|
| `SpawnAgent` creates session | `pending` | `persistSessionCreated` |
| Workspace ready, branch set | `running` | `persistSessionRunning` |
| `postSessionCompleted` called | `completed` | `persistSessionCompleted` |
| `failSession` called | `failed` | `persistSessionFailed` |

All persist calls are nil-safe: when `store == nil` (tests, DB unavailable) they log a warning and return — the session continues normally.

### Schema (abbreviated)

```sql
CREATE TABLE agent_sessions (
    id            UUID PRIMARY KEY,
    task_type     VARCHAR(50),     -- "implement"
    repo_owner    VARCHAR(255),
    repo_name     VARCHAR(255),
    branch        VARCHAR(255),
    issue_number  INTEGER,
    status        VARCHAR(50),     -- pending|running|completed|failed
    created_at    TIMESTAMPTZ,
    updated_at    TIMESTAMPTZ,     -- updated via trigger
    completed_at  TIMESTAMPTZ,
    task_inputs   JSONB,           -- issue title + body excerpt
    result        JSONB,           -- Result struct (PR URL, verdict, iterations)
    error         TEXT,
    iterations    INTEGER,
    final_verdict VARCHAR(50)      -- APPROVE | REQUEST_CHANGES | COMMENT
);
```

### Store interface

`storage.AgentSessionStore` is embedded in `storage.Store`:

```go
type AgentSessionStore interface {
    CreateAgentSession(ctx, *AgentSession) error
    UpdateAgentSession(ctx, *AgentSession) error
    GetAgentSession(ctx, id string) (*AgentSession, error)
    ListAgentSessions(ctx, owner, repo string, limit int) ([]*AgentSession, error)
}
```

---

## MCP Tools Reference

Tools used by the implement loop (see `internal/mcp/tools/` and `internal/agent/file_tools.go`):

### Code exploration (RAG-backed, read-only)

| Tool | Description |
|------|-------------|
| `search_code` | Semantic search over the indexed codebase |
| `get_symbol` | Look up a symbol definition |
| `get_structure` | Project directory tree |
| `get_arch_context` | Architecture summary for a directory |
| `find_usages` | Call sites for a symbol |
| `get_callers` / `get_callees` | Call graph navigation |

### File operations (workspace-scoped)

| Tool | Description |
|------|-------------|
| `read_file` | Read a file, optionally paginated |
| `write_file` | Create or overwrite (triggers LSP diagnostics) |
| `edit_file` | Exact-string replace (triggers LSP diagnostics) |
| `list_dir` | List directory contents |

### LSP (live compiler feedback)

| Tool | Description |
|------|-------------|
| `lsp_diagnostics` | Errors/warnings from language server |
| `lsp_definition` | Jump to definition |
| `lsp_references` | Find all usages |
| `lsp_hover` | Type info and docs |

### Verification

| Tool | Description |
|------|-------------|
| `run_command` | Run whitelisted commands (`make lint`, `make test`) |
| `review_code` | RAG-based code review returning APPROVE / REQUEST_CHANGES |

### Publish (Phase 3 only)

| Tool | Description |
|------|-------------|
| `push_branch` | Commit pending changes and push to origin |
| `create_pull_request` | Open a draft PR (requires prior APPROVE) |

---

## Key Types

```go
// internal/agent/orchestrator.go
type Result struct {
    PRNumber     int
    PRURL        string
    Branch       string
    FilesChanged []string
    Verdict      string  // "APPROVE" | "REQUEST_CHANGES" | "COMMENT"
    Iterations   int     // implement + publish combined
}

// internal/agent/session.go
type Session struct {
    ID          string
    Issue       Issue
    status      SessionStatus   // pending | running | completed | failed | cancelled
    StartedAt   time.Time
    CompletedAt time.Time
    Result      *Result
    // ...
}
```

---

## Configuration

```yaml
agent:
  enabled: true
  mode: warden                          # "warden" (phased) | "native" (legacy single-loop)
  model: "glm-4-9b"                    # Override LLM for implementation (empty = use review LLM)
  timeout: 60m                          # Hard session timeout
  max_iterations: 3                     # implement loop cap = max(this*10, 30)
  max_concurrent_sessions: 3
  working_dir: "/tmp/code-warden-agents"

ai:
  llm_provider: ollama
  generator_model: glm-4-9b            # Used for implementation when agent.model is unset
  embedder_model: nomic-embed-text
```

**Model resolution order for the implement loop:**
1. `agent.model` (if set and different from review model)
2. `ragService.GeneratorLLM()` (the review model)

---

## Execution Modes

| Mode | Description | When to use |
|------|-------------|-------------|
| `warden` | Three-phase loop: plan → implement → publish. LSP, progress tracking, compaction, PostgreSQL persistence. | Production |
| `native` | Single-phase in-process loop. All tools available at once, no planning, no compaction. | Simpler tasks, debugging |
| `server` | goframe SDK connects to OpenCode HTTP server. | External OpenCode server |
| `cli` | Spawns OpenCode binary as subprocess. | Legacy / testing |

---

## Limitations and Known Issues

| Issue | Status |
|-------|--------|
| Sessions lost on server restart | **Mitigated** — `agent_sessions` table persists key state; full recovery (re-attach workspace) not yet implemented |
| No auto-review on agent PRs | Open — human reviewers must manually `/review` agent-created PRs |
| Single language server per extension | LSP manager starts one server per language; multi-root workspaces not supported |
| Compaction ceiling is fixed at 128K | Models support 198K+; ceiling is conservative to leave room for tool outputs |
