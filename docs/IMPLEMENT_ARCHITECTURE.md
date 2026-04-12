# `/implement` Command Architecture

This document describes the architecture and data-flow of the `/implement` command, which drives autonomous code implementation via the **Warden agent harness**.

---

## Overview

When a GitHub issue comment contains `/implement`, Code-Warden:

1. Spawns a `Session` and persists it to PostgreSQL.
2. Clones the repository to an isolated workspace.
3. Runs the **Warden phased loop** (plan → edit → review → publish).
4. Posts real-time GitHub progress comments throughout.
5. Opens a draft PR once the implementation is approved.

The harness is entirely in-process: no external agent binary needed.
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

## Warden Agent — Four-Phase Architecture

```
runWardenAgent(ctx, session, branch)
│
├─ prepareAgentWorkspace()
│    ├── git clone projectRoot → /tmp/code-warden-agents/<id>
│    ├── git remote set-url origin  (with token for push)
│    ├── RegisterWorkspace(session.ID, dir)  ← MCP routing
│    └── open agent.log + trace file
│
├─ persistSessionRunning(branch)
│
├─ progressTracker.start()          ← 30s GitHub comment ticker
│
├─ ── Phase 1: Plan ──────────────────────────────────────────────────
│   tracker.setPhase("planning")
│   buildPlan()  →  buildPlannerLoop()
│   tools: search_code, get_symbol, get_structure, get_arch_context,
│          find_usages, get_callers, get_callees, read_file, list_dir,
│          grep, find
│     max_iterations: plan_iterations (default 8)
│     output: markdown "## Implementation Plan" injected into edit prompt
│
├─ ── Phase 2: Edit (implement) ──────────────────────────────────────
│   tracker.setPhase("editing")
│   buildEditLoop()
│     tools: ALL MCP tools except push_branch / create_pull_request / review_code
│            + read_file, write_file, edit_file, list_dir  (file tools)
│            + grep, find  (search tools)
│            + run_command  (make build/lint/test)
│     max_iterations: edit_iterations (default 50)
│     compactionHook: fires at 70% of 128K tokens
│
│   loop.Run()  →  Think → call tools → Observe → repeat
│     agent explores codebase, writes code, runs lint/tests
│     review_code is NOT available here — review happens in Phase 3
│
├─ ── Phase 3: Review state machine (orchestrator-driven) ───────────
│   tracker.setPhase("reviewing (round N/M)")
│   Go code runs up to review_rounds (default 10) rounds:
│
│   for round := 1; round <= maxRounds; round++ {
│     diff = git diff HEAD           ← Go runs this directly
│     result = review.Executor.Execute(diff)   ← proven RAG pipeline
│     RecordReviewBySession(verdict, diffHash)
│
│     if verdict == "APPROVE" → break → go to Phase 4
│
│     buildFixLoop()   ← restricted tool set (no MCP exploration)
│       tools: run_command, read_file, write_file, edit_file, list_dir,
│              grep, find
│       max_iterations: fix_iterations (default 8)
│       task context: exact review findings (file:line, severity, comment,
│                     suggested fix) — no codebase exploration needed
│     fixLoop.Run()
│   }
│   if never APPROVE → yieldDraftPR()  ← human fallback
│
├─ ── Phase 4: Publish ───────────────────────────────────────────────
│   tracker.setPhase("publishing")
│   buildPublishLoop()
│     tools: push_branch, create_pull_request  ONLY
│     max_iterations: publish_iterations (default 8)
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

## Why the Review Phase is Orchestrator-Driven (Not LLM-Driven)

The review phase is run entirely by Go code, not by an LLM agent loop. This was a deliberate architectural choice to fix two root causes:

**Root cause 1 — `review_code` required a diff the agent couldn't obtain.**
The `review_code` MCP tool schema has `"required": ["diff"]`. The `run_command`
whitelist only allows `make build/lint/test` — no git commands. Even if the agent
tried to call `review_code`, the call would fail or stall because it had no way
to produce a diff.

**Root cause 2 — Cold-start with nil history.**
The previous design passed `nil` history to `reviewLoop.Run()`. The review agent
started with 2 messages (system prompt + task) and no memory of what the edit loop
did. In practice it spent all 15 iterations re-exploring the codebase — never
reaching `review_code`.

**The fix:** Go runs `git diff HEAD` directly, calls the proven `review.Executor`
(the same RAG pipeline used by the `/review` PR command), and feeds the exact
review findings to a small "fix loop". The review is guaranteed to run regardless
of edit-loop behavior. The fix loop only reads/writes specific reported files —
no codebase re-exploration.

---

## Phase-Based Tool Scoping

Tools are assigned to loops architecturally — the model literally cannot call an
out-of-phase tool. `publishToolNames` and the `review_code` exclusion in
`buildEditLoop` are the single source of truth.

| Phase | Loop | Tools available |
|-------|------|----------------|
| Plan | `buildPlannerLoop` | `search_code`, `get_symbol`, `get_structure`, `get_arch_context`, `find_usages`, `get_callers`, `get_callees`, `read_file`, `list_dir`, `grep`, `find` |
| Edit | `buildEditLoop` | All MCP tools except `push_branch`, `create_pull_request`, `review_code`; plus file tools (`read_file`, `write_file`, `edit_file`, `list_dir`), search tools (`grep`, `find`), `run_command` |
| Fix (per round) | `buildFixLoop` | `run_command`, `read_file`, `write_file`, `edit_file`, `list_dir`, `grep`, `find` — NO MCP exploration tools |
| Publish | `buildPublishLoop` | `push_branch`, `create_pull_request` **only** |

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
- Current phase (planning / editing / reviewing round N/M / publishing)
- Total tool calls so far
- Last 6 tool names with ✓/✗ status

---

## Context Compaction

Long edit loops accumulate conversation history that can approach the model's context window.

The compaction hook fires when `tokens.Input + tokens.Output > 128_000 * 0.70`:

```
buildCompactionHook(session) → func(ctx, msgs, tokens) []schema.MessageContent
  if used < threshold: return nil  ← no-op, loop continues unchanged

  previousSummary := extract prior "## Context Summary" from msgs[1] (if any)

  build plain-text transcript of newMsgs (messages after the prior summary)
  call LLM with summarization prompt (iterative: update summary vs. fresh start)
    max 400 words; if previousSummary != "": "Update this summary: …" prompt

   extract file ops from newMsgs:
     - parse tool result messages for read_file / write_file / edit_file paths
     - merge with file lists already in previousSummary (<read-files>/<modified-files> XML)

  find tail boundary via findTailStart(msgs, 8):
    - walks backward from end until landing on a ChatMessageTypeHuman message
    - ensures tool-result messages are never orphaned from the AI turn that called them

  rebuild history:
    [0] system prompt                     (preserved verbatim)
    [1] "## Context Summary\n…<read-files>…<modified-files>…"
    [2..] msgs[tailStart:]                (≥8 messages from last clean turn boundary)

  return compacted  ← goframe replaces messages, increments result.Compactions
```

**Iterative summarization** — if `msgs[1]` already contains a prior summary the hook
asks the LLM to update rather than re-summarise from scratch.

**File footprint tracking** — `<read-files>` and `<modified-files>` XML blocks are
appended to every summary and merged cumulatively.

**Turn boundary safety** — `findTailStart` ensures the preserved tail always begins on
a human message, so a `tool` role result is never the first message in the compacted
history.

If the summarization call fails, the hook returns `nil` and the loop continues with the
full history (graceful degradation).

---

## Session Persistence (`internal/storage/agent_session.go`)

Every session is persisted to the `agent_sessions` PostgreSQL table.

### State transitions

| Event | Status written | Method |
|-------|---------------|--------|
| `SpawnAgent` creates session | `pending` | `persistSessionCreated` |
| Workspace ready, branch set | `running` | `persistSessionRunning` |
| `postSessionCompleted` called | `completed` | `persistSessionCompleted` |
| `failSession` called | `failed` | `persistSessionFailed` |

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
| `grep` | Search file contents by pattern (regex or literal). Uses ripgrep with grep fallback. |
| `find` | Find files by glob pattern (e.g. `*.go`, `**/*_test.go`). Pure Go, skips `.git`/`node_modules`/`vendor`. |

### File operations (workspace-scoped)

| Tool | Description |
|------|-------------|
| `read_file` | Read a file, optionally paginated; returns `{content, lines, path}`. Includes `hint` with next offset when truncated. |
| `write_file` | Create or overwrite; returns `{ok, path, bytes}` |
| `edit_file` | Exact-string replace with fuzzy fallback and CRLF/BOM handling; returns `{ok, path, diff, fuzzy_match?}`. Supports single `{old_string, new_string}` or atomic multi-edit `{edits:[…]}`. |
| `list_dir` | List directory entries with name, type, size |

### Verification

| Tool | Description |
|------|-------------|
| `run_command` | Run whitelisted commands (`make build`, `make lint`, `make test`) |

### Publish (Phase 4 only)

| Tool | Description |
|------|-------------|
| `push_branch` | Commit pending changes and push to origin |
| `create_pull_request` | Open a draft PR (requires prior APPROVE recorded by orchestrator) |

---

## Configuration

```yaml
agent:
  enabled: true
  mode: warden                          # "warden" (phased) | "native" (legacy single-loop)
  model: "glm-4-9b"                    # Override LLM for implementation (empty = use review LLM)
  timeout: 60m                          # Hard session timeout
  max_concurrent_sessions: 3
  working_dir: "/tmp/code-warden-agents"

  # Per-phase iteration budgets (0 = use built-in default)
  plan_iterations: 8      # Planning loop — read-only exploration
  edit_iterations: 50     # Edit/implement loop — main coding budget
  review_rounds: 10       # Max orchestrator-driven review+fix cycles
  fix_iterations: 8       # Fix loop iterations per review round
  publish_iterations: 8   # Publish loop — push branch + open PR
```

**Model resolution order for the implement loop:**
1. `agent.model` (if set)
2. `ragService.GeneratorLLM()` (the review model)

---

## Execution Modes

| Mode | Description | When to use |
|------|-------------|-------------|
| `warden` | Four-phase loop: plan → edit → review (orchestrator) → publish. Progress tracking, compaction, PostgreSQL persistence. | Production |
| `native` | Single-phase in-process loop. All tools available at once, no planning, no compaction. | Simpler tasks, debugging |

---

## Limitations and Known Issues

| Issue | Status |
|-------|--------|
| Sessions lost on server restart | **Mitigated** — `agent_sessions` table persists key state; full recovery (re-attach workspace) not yet implemented |
| No auto-review on agent PRs | Open — human reviewers must manually `/review` agent-created PRs |
| Compaction ceiling is fixed at 128K | Models support 198K+; ceiling is conservative to leave room for tool outputs |
| Review executor uses single-model mode | `ComparisonModels` is always nil in the agent; consensus review not yet wired |
