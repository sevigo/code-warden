# `/implement` Command Architecture

How the `/implement` command works — the Warden agent harness, its phases, and why certain design decisions were made.

---

## Overview

When a GitHub issue comment contains `/implement`, Code-Warden:

1. Spawns a `Session` and persists it to PostgreSQL.
2. Clones the repository to an isolated workspace.
3. Runs the **Warden phased loop** (plan → edit → review → publish).
4. Posts real-time GitHub progress comments throughout.
5. Opens a draft PR once the implementation is approved.

The harness runs entirely in-process — no external agent binary needed. Tools are called as direct Go function calls inside `goframe.AgentLoop`.

---

## Flow

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
```

`runAgent` dispatches by mode:
- `"warden"` → `runWardenAgent()` (production)
- `"native"` → `runInProcessAgent()` (simpler, single-loop)

---

## Four-Phase Architecture

```
runWardenAgent(ctx, session, branch)
│
├─ prepareAgentWorkspace()
│    ├── git clone projectRoot → /tmp/code-warden-agents/<id>
│    ├── git remote set-url origin  (with token for push)
│    ├── RegisterWorkspace(session.ID, dir)
│    └── open agent.log + trace file
│
├─ persistSessionRunning(branch)
│
├─ progressTracker.start()          ← 30s GitHub comment ticker
│
├─ ── Phase 1: Plan ──────────────────────────────────────────
│   buildPlan()  →  buildPlannerLoop()
│   tools: search_code, get_symbol, get_structure, get_arch_context,
│          find_usages, get_callers, get_callees, read_file, list_dir,
│          grep, find
│   max_iterations: 8
│   output: markdown plan injected into edit prompt
│
├─ ── Phase 2: Edit ──────────────────────────────────────────
│   buildEditLoop()
│   tools: ALL MCP tools except push_branch/create_pull_request/review_code
│          + read_file, write_file, edit_file, list_dir, grep, find
│          + run_command (make build/lint/test)
│   max_iterations: 50
│   compaction: fires at 70% of 128K tokens
│
├─ ── Phase 3: Review (orchestrator-driven state machine) ────
│   for round := 1..maxRounds:
│     diff = git diff HEAD
│     result = review.Executor.Execute(diff)    ← same RAG pipeline as /review
│     if APPROVE → break → Phase 4
│     buildFixLoop() ← restricted tools, exact findings in task context
│
├─ ── Phase 4: Publish ───────────────────────────────────────
│   tools: push_branch, create_pull_request ONLY
│   max_iterations: 8
```

---

## Why the Review Phase is Orchestrator-Driven

The review phase is run by Go code, not an LLM agent loop. Two problems made the previous LLM-driven approach unworkable:

1. **`review_code` requires a diff the agent couldn't obtain.** The `run_command` whitelist only allows `make build/lint/test` — no git commands. An LLM agent trying to call `review_code` would fail or stall because it had no way to produce a diff.

2. **Cold-start with nil history.** The previous design passed `nil` history to the review loop. The agent started with just system prompt + task and no memory of what the edit loop did. In practice it spent all its iterations re-exploring the codebase instead of reviewing.

The fix: Go runs `git diff HEAD` directly, calls the proven `review.Executor` (the same RAG pipeline `/review` uses), and feeds the exact findings to a small "fix loop." Review always runs regardless of edit-loop behavior. The fix loop only reads/writes the specific reported files.

---

## Tool Scoping per Phase

Tools are assigned architecturally — the model literally cannot call an out-of-phase tool.

| Phase | Tools |
|-------|-------|
| Plan | `search_code`, `get_symbol`, `get_structure`, `get_arch_context`, `find_usages`, `get_callers`, `get_callees`, `read_file`, `list_dir`, `grep`, `find` |
| Edit | All MCP tools except `push_branch`, `create_pull_request`, `review_code`; plus `read_file`, `write_file`, `edit_file`, `list_dir`, `grep`, `find`, `run_command` |
| Fix (per round) | `run_command`, `read_file`, `write_file`, `edit_file`, `list_dir`, `grep`, `find` — no MCP exploration tools |
| Publish | `push_branch`, `create_pull_request` only |

---

## Context Compaction

Long edit loops accumulate conversation history approaching the model's context window. The compaction hook fires at 70% of a 128K token ceiling.

- If `msgs[1]` already contains a prior summary, the LLM updates it rather than re-summarizing from scratch.
- `<read-files>` and `<modified-files>` XML blocks are tracked cumulatively across re-compactions.
- `findTailStart` ensures the preserved tail always starts on a human message, so tool results are never orphaned.
- If the summarization call fails, the hook returns `nil` and the loop continues with full history.

---

## Session Persistence

Every session is persisted to the `agent_sessions` PostgreSQL table.

| Event | Status |
|-------|--------|
| `SpawnAgent` creates session | `pending` |
| Workspace ready, branch set | `running` |
| `postSessionCompleted` | `completed` |
| `failSession` | `failed` |

---

## MCP Tools Reference

### Code exploration (RAG-backed, read-only)

| Tool | Description |
|------|-------------|
| `search_code` | Semantic search over the indexed codebase |
| `get_symbol` | Look up a symbol definition |
| `get_structure` | Project directory tree |
| `get_arch_context` | Architecture summary for a directory |
| `find_usages` | Call sites for a symbol |
| `get_callers` / `get_callees` | Call graph navigation |

### Code exploration (exact search, read-only)

| Tool | Description |
|------|-------------|
| `grep` | Search file contents by pattern (regex or literal) |
| `find` | Find files by glob pattern |

### File operations (workspace-scoped)

| Tool | Description |
|------|-------------|
| `read_file` | Read a file, optionally paginated |
| `write_file` | Create or overwrite a file |
| `edit_file` | Exact-string replace with fuzzy fallback; supports single or atomic multi-edit |
| `list_dir` | List directory entries |

### Verification

| Tool | Description |
|------|-------------|
| `run_command` | Run whitelisted commands (`make build`, `make lint`, `make test`) |

### Publish (Phase 4 only)

| Tool | Description |
|------|-------------|
| `push_branch` | Commit and push to origin |
| `create_pull_request` | Open a draft PR (requires prior APPROVE) |

---

## Configuration

```yaml
agent:
  enabled: true
  mode: warden                    # "warden" (phased) | "native" (legacy single-loop)
  model: ""                      # Override LLM for implementation (empty = use review LLM)
  timeout: 60m
  max_concurrent_sessions: 3
  working_dir: "/tmp/code-warden-agents"

  plan_iterations: 8
  edit_iterations: 50
  review_rounds: 10
  fix_iterations: 8
  publish_iterations: 8
```

Model resolution: `agent.model` (if set) → `ragService.GeneratorLLM()` (the review model).

---

- [ARCHITECTURE.md](./ARCHITECTURE.md) — Component relationships and system design
- [INDEXING.md](./INDEXING.md) — Chunk types, metadata, debugging retrieval
- [TROUBLESHOOTING.md](./TROUBLESHOOTING.md) — Common issues and fixes