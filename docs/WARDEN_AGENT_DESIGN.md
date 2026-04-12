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

**Cap:** `plan_iterations` (default 8). The planner should read a handful of files
and produce a plan, not implement anything.

### Phase 2 — Edit (Implement)

**Goal:** Write and verify code against `make build`, `make lint`, `make test`.
`review_code` is deliberately withheld — review happens in the separate
orchestrator-driven phase so it always runs regardless of the edit budget.

**Tool selection:**
RAG tools (`search_code`, `get_symbol`) are best for semantic exploration.
Search tools (`grep`, `find`) are best for exact pattern search and file discovery.
`run_command` runs `make build/lint/test` for verification.

**Verification ladder (cheapest first):**
1. `run_command("make build")` — fast compile check.
2. `run_command("make lint")` — style errors.
3. `run_command("make test")` — regression check.

**Iteration cap:** `edit_iterations` (default 50).

**Compaction:**
The compaction hook fires at 70% of a 128K token ceiling. It summarises the
conversation history to ≤400 words and rebuilds history as:
`[system prompt] + [summary] + [last 8 messages from a clean turn boundary]`.

### Phase 3 — Review (Orchestrator-Driven State Machine)

**Goal:** Validate the implementation using the proven RAG review pipeline.
Iterate (up to `review_rounds`) until the reviewer approves or the budget is
exhausted.

**Design rationale:**
The review phase is run by Go code, not an LLM agent. Two root causes made
the previous LLM-driven approach broken by construction:
1. `review_code` required a `diff` parameter the agent couldn't produce
   (run_command whitelist has no git commands).
2. The review loop started cold with nil history, causing the agent to
   re-explore rather than review.

**Per round:**
1. Go runs `git diff HEAD` to get the full workspace diff.
2. Go calls `review.Executor.Execute()` — the same RAG pipeline used by `/review`.
3. Verdict is recorded via `RecordReviewBySession` for PR enforcement.
4. `APPROVE` → move to Phase 4.
5. `REQUEST_CHANGES` → spawn a focused fix loop (`fix_iterations`, default 8)
   with the exact findings (file:line, severity, suggested fix) as task context.
   The fix loop has NO MCP exploration tools — it only reads/writes the
   specific reported files.

**Draft PR fallback:** If `review_rounds` are exhausted without APPROVE,
`yieldDraftPR()` opens a draft PR for human review.

### Phase 4 — Publish

**Goal:** Push the branch and open a draft PR. Nothing else.

**Tools:** `push_branch` and `create_pull_request` only.

**Cap:** `publish_iterations` (default 8). Push + PR creation should complete in 1–2 turns.

**Gate:** Phase 4 only runs after the orchestrator records an APPROVE verdict.
`create_pull_request` enforces this via `GetReviewBySession`.

---

## LSP Removal

The Language Server Protocol client was removed from agent sessions. It added
30–120 seconds of startup time per session and required per-language server
binaries (`gopls`, `typescript-language-server`, etc.) on the host. The agent
now uses `grep` for exact pattern search, `find` for file discovery, and
`run_command("go build ./...")` for compile verification — which is faster and
works across all programming languages without pre-installed servers.

The LSP package (`internal/agent/lsp/`) still exists for standalone use but is
no longer started during workspace preparation.

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
