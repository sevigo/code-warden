# Warden Agent Harness — Design Reference

This document records the architectural decisions behind the Warden agent harness
(`internal/agent/warden.go` and related files).  It is a companion to
[IMPLEMENT_ARCHITECTURE.md](IMPLEMENT_ARCHITECTURE.md), which documents the
runtime flow.  Where that document answers *how it works*, this one answers *why*.

---

## Seven-Decision Framework

The harness was designed by answering seven core questions about any agent system.

| # | Decision | Choice | Rationale |
|---|----------|--------|-----------|
| 1 | **Agent count** | Single agent per task | Parallelism adds coordination overhead; the implement task is sequential by nature |
| 2 | **Reasoning strategy** | Plan-then-ReAct | Short read-only planning loop before implementation; gives the model a concrete roadmap and reduces wasted exploration in the implement loop |
| 3 | **Context strategy** | Rich context + compaction at 70% | GLM-5.1 and MiniMax M2.7 support 198K tokens; compaction extends useful run length without hitting the limit |
| 4 | **Verification** | Computational first, then LLM-as-judge | `gopls` diagnostics → `make lint` → `make test` → `review_code`; earlier stages are cheaper and faster |
| 5 | **Permissions** | Restrictive, phase-gated | Publish tools are withheld architecturally during implementation — the model cannot push before APPROVE even if it tries |
| 6 | **Tool scoping** | Minimal per phase | Plan phase: read-only. Implement phase: no publish tools. Publish phase: only push + PR. Fewer tools = fewer wrong tool calls |
| 7 | **Harness thickness** | Thin | Trust the model; harness enforces only hard gates (APPROVE check, tool scoping). No rewrite of tool results, no output validators beyond the review verdict |

---

## Harness Layers

```
┌─────────────────────────────────────────────────────────────────┐
│  runWardenAgent()                                                │
│  ├── Phase gate: APPROVE required before publish loop           │
│  ├── PostgreSQL persistence at each state transition            │
│  └── progressTracker: real-time GitHub comment ticker           │
├─────────────────────────────────────────────────────────────────┤
│  goframe.AgentLoop (per phase)                                  │
│  ├── WithLoopGovernance: allowedTools permit list               │
│  ├── WithLoopMaxIterations: phase-appropriate cap               │
│  └── WithLoopCompactionHook: 70% token ceiling compaction       │
├─────────────────────────────────────────────────────────────────┤
│  Tool registry (contextInjectingTool → progressTool → inner)    │
│  ├── contextInjectingTool: injects projectRoot + sessionID      │
│  └── progressTool: records call to tracker, writes to log       │
└─────────────────────────────────────────────────────────────────┘
```

---

## Phase Design

### Phase 1 — Plan

**Goal:** Produce a structured markdown implementation plan before any code is written.

**Why a separate phase?**
Plan-then-ReAct is 3–4× faster than pure ReAct on coding tasks: the model starts
the implement loop knowing which files to change instead of re-exploring from scratch
on every iteration. The planner uses only read-only tools so it cannot accidentally
mutate state.

**Fallback:** If the planner loop fails or returns an empty response, `fallbackPlan()`
returns a minimal plan and the implement loop continues. Planning failure never
blocks implementation.

**Cap:** 5 iterations. The planner should read a handful of files and produce a plan,
not implement anything.

### Phase 2 — Implement

**Goal:** Write and verify code until `review_code` returns APPROVE.

**Tool selection:**
LSP tools complement RAG tools. RAG (`search_code`) is better for "find things related
to X" open-ended exploration. LSP (`lsp_definition`, `lsp_references`) is better for
"where exactly is this symbol used" precision — and it is always current (no index lag).

**Verification ladder (cheapest first):**
1. `write_file` / `edit_file` return gopls diagnostics automatically.
2. `run_command("make lint")` — fast, catches style errors.
3. `run_command("make test")` — slower, catches regressions.
4. `review_code` — LLM-as-judge with full RAG context.

**Iteration cap:**
`max(config.MaxIterations * 10, 30)`. With `MaxIterations: 3` the cap is 30 loop
steps — enough for multi-file changes with several review cycles.

**Compaction:**
The compaction hook fires at 70% of a 128K token ceiling. It summarises the
conversation history to ≤400 words, then rebuilds the history as:
`[system prompt] + [summary] + [last 4 messages]`.
The 128K ceiling is conservative: models support 198K+ but we leave headroom for
tool outputs in the current iteration.

### Phase 3 — Publish

**Goal:** Push the branch and open a draft PR. Nothing else.

**Tools:** `push_branch` and `create_pull_request` only. The model cannot call any
code-reading or code-writing tool here — there is nothing to explore.

**Cap:** 5 iterations. Push + PR creation should complete in 1–2 turns.

**Gate:** The publish loop only runs if `GetReviewBySession` returns `"APPROVE"`. A
completed-without-APPROVE loop is treated as a failure (`failSession`).

---

## LSP Design

### Why LSP instead of more RAG?

RAG vector search has inherent lag (documents must be indexed before they are
searchable) and can return stale results when the agent is actively editing files.
LSP talks directly to the compiler and is always authoritative.

The two tools complement each other:
- **RAG** for broad, semantic exploration ("what handles authentication?")
- **LSP** for precise, structural navigation ("where is `handleAuth` called?")

### Language server lifecycle

Each `lsp.Client` starts its server as a subprocess over stdio with JSON-RPC 2.0
Content-Length framing. The manager starts one client per detected language and
routes tool calls by file extension. If a server binary is not found, that language
silently falls back to RAG — no error is surfaced to the agent.

### Extending to new languages

Add one struct implementing `LanguageServer` to `server.go` and add it to
`DefaultServers()`:

```go
type RustServer struct{}
func (s *RustServer) Name()       string   { return "rust-analyzer" }
func (s *RustServer) Extensions() []string { return []string{".rs"} }
func (s *RustServer) Command(dir string) []string { return []string{"rust-analyzer"} }
func (s *RustServer) Env()        []string { return nil }
func (s *RustServer) LanguageID() string   { return "rust" }
```

---

## Progress Tracking Design

### Why a wrapper rather than a goframe hook?

goframe's `AgentLoop` does not expose iteration callbacks. Instead of modifying
goframe, every registered tool is wrapped in a `progressTool` that calls
`tracker.record()` after each `Execute`. This achieves real-time logging without
any changes to the loop internals.

The call chain is:

```
goframe registry → progressTool.Execute()
                       ├── contextInjectingTool.Execute()
                       │       └── inner mcp.Tool.Execute()
                       └── tracker.record(name, err==nil)
                             ├── write timestamped line to agent.log
                             └── append to in-memory entries list
```

A background goroutine ticks every 30 seconds and posts a GitHub comment if new
entries have arrived since the last post. This gives live visibility into long
runs without polling.

---

## PostgreSQL Persistence Design

### Why persist at all?

The in-memory `sessions` map is lost on server restart. Any session active during a
restart becomes orphaned: no status visible to users, workspace left on disk. The
`agent_sessions` table provides a recoverable audit trail and enables:

- Listing recent sessions per repository (for a future sessions dashboard).
- Diagnosing failures after the fact (iterations count, final verdict, error text).
- Future: restart recovery — reattach to an in-progress session after a crash.

### Nil-safe store

`Orchestrator.store` is typed as `storage.AgentSessionStore` (an interface) and may
be `nil` when the database is unavailable (tests, dev without PostgreSQL). All
`persist*` helpers check `o.store == nil` first and log a warning instead of
failing. Session execution is never blocked by a database error.

### Why not store the full conversation log?

The session log (every tool call + response) can be hundreds of KB for a 30-iteration
run. Storing it as JSONB in PostgreSQL would make the row huge and slow to write.
Instead, the conversation is written to `agent.log` in the workspace directory and
cleaned up after the session completes. Future improvement: stream log lines to
object storage (S3 / GCS) for post-mortem inspection.

---

## Context Compaction Design

### The compaction hook (goframe v0.36.6)

`WithLoopCompactionHook(fn)` is called after every think-act-observe iteration.
The hook signature:

```go
func(ctx context.Context, msgs []schema.MessageContent, tokens TokenUsage) []schema.MessageContent
```

Return `nil` → no compaction (loop continues unchanged).
Return a new slice → replaces `messages` for all subsequent iterations.
`LoopResult.Compactions` counts how many times compaction triggered.

### Why 70% of 128K?

- Models support 198K+ but compaction logic itself requires a few thousand tokens.
- Tool outputs from the current iteration can be large (file contents, test output).
- 70% of 128K = ~90K tokens consumed before compaction — ample headroom.
- The ceiling is a conservative estimate, not the model's actual limit. We compact
  early to guarantee there is always room for the current iteration's tool outputs.

### Graceful degradation

If the LLM call for summarization fails (network error, timeout), the hook returns
`nil`. The loop continues with the full history. In the worst case the model sees a
truncated context at its hard limit, but the session does not fail.

---

## What Stays Unchanged

The following components were explicitly kept stable during the harness rework:

- **MCP tool implementations** — `search_code`, `review_code`, `push_branch`, etc.
  are unchanged; only their registration path changed.
- **RAG pipeline** — stays for initial exploration and code review.
- **goframe for non-agent work** — chains, vectorstores, embeddings.
- **Ollama / Gemini integration** — LLM provider unchanged.
- **Governance config structure** — `PermissionCheck.Allowed` map is still the
  enforcement mechanism; only the set of allowed tools per phase changed.

---

## Database Migration

Run `internal/storage/agent_schema.sql` once against your PostgreSQL instance to
create the `agent_sessions` and `agent_design_documents` tables. The migration is
idempotent (`CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`).

```bash
psql "$DATABASE_URL" -f internal/storage/agent_schema.sql
```
