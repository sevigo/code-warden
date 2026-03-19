# TODO

This document outlines the development roadmap for Code-Warden. It tracks pending features and future ideas.

---

## 🚀 Immediate Priorities

### 1. Create a Simple Web UI for Status & Onboarding

Provide a user-friendly way to see what the app is doing and what repositories are managed.

- Add frontend routes in `internal/server/router.go`
- Build a status page listing all repositories with last indexed SHA
- Show job history with status and PR links
- **Benefit:** Improves transparency and user experience.

### 2. Implement Resource Lifecycle Management

Ensure long-term stability with garbage collection.

- Create a "Janitor" background service
- TTL-based cleanup for old repositories (Qdrant collections, disk files, DB records)
- Handle GitHub App uninstallation events
- **Benefit:** Prevents resource leaks and controls operational costs.

### 3. Add Godoc Documentation

- Add godoc comments to `internal/storage/` interfaces
- Document `internal/rag/` service methods
- Document `internal/jobs/` dispatcher and worker

---

## 🐙 GitHub Interactions

These expand what users can do directly from PR/issue comments, making Code-Warden feel like a first-class GitHub citizen.

### Commands in PR Comments

Users trigger these by commenting on a PR:

| Command | Description |
|---|---|
| `/review` | Full review (already implemented) |
| `/rereview` | Re-review after updates (already implemented) |
| `/implement` | Agent-driven issue implementation (already implemented) |
| `/review focus=security` | Scoped review — security, performance, naming, tests, etc. |
| `/review file=internal/rag/service.go` | Review a single file only |
| `/explain <symbol>` | Look up a symbol in the RAG index and explain it in context |
| `/why <line or snippet>` | Explain why a piece of code was written this way (git blame + RAG) |
| `/suggest` | Generate concrete code fix suggestions for flagged issues |
| `/ask <free text>` | Free-form question answered with RAG context (no GitHub comment posted) |

### Reacting to Review Feedback

Allow users to close the feedback loop directly in GitHub comments:

- **`/feedback wrong`** or **`/feedback not relevant`** on a review comment — marks that finding as a false positive; stored in DB and used to suppress similar findings in future reviews on this repo
- **`/feedback good`** on a review comment — positive signal; boosts similar findings in future reviews
- **`/accept`** — signals the user agrees with the finding and intends to fix it; can optionally open an issue automatically
- **`/ignore`** — permanently mutes a specific finding class for this file/repo (written to `.code-warden.yml`)
- Reaction emoji on the bot's top-level review comment (👍/👎) as lightweight overall rating — stored per PR for trend analysis

### PR Lifecycle Events

Subscribe to more GitHub webhook events to automate indexing:

- **PR merged** → trigger incremental re-index of changed files on the target branch automatically (currently only done on `/review`)
- **Push to default branch** → re-index changed files so the vector store stays fresh without needing a PR
- **PR closed without merge** → no re-index, but clean up any ephemeral data stored for that PR
- **PR review requested** → optionally auto-trigger a `/review` without waiting for a comment (configurable per repo via `.code-warden.yml`)
- **Repository created/transferred** → auto-register and start initial prescan
- **GitHub App uninstalled** → remove Qdrant collection, DB records, cloned repo

### Issue Interactions

- **`/implement`** on an issue comment — already supported; expand to handle sub-tasks within issues
- **Auto-link** issues referenced in PR description to the review context (include issue body in RAG query)
- **Post review summary as issue** when a PR has >= N critical findings (configurable threshold)

---

## 🧠 Using User Feedback to Improve Reviews

The feedback signals above need a storage and application layer:

### Feedback Storage
- Add `review_feedback` table: `(repo_id, pr_number, comment_id, finding_hash, signal: positive|negative, created_at)`
- `finding_hash` = stable hash of `(file_path, rule/category, brief description)` so it survives across PRs

### Suppression Rules (Negative Feedback)
- After N negative signals for a finding pattern in a repo, auto-add a suppression rule to `.code-warden.yml`
- Surface suppression rules in the web UI so users can review/remove them
- Pass active suppression rules into the system prompt so the LLM avoids flagging them

### Boosting (Positive Feedback)
- Track which finding categories get positive signals per repo
- Weight those categories higher in the prompt instructions for that repo
- Example: if "missing error handling" findings consistently get 👍 in a Go repo, emphasize it

### Learning from Merged PRs
- After a PR merges, check if any flagged issues were actually fixed in the diff
- If yes → positive reinforcement for that finding category
- If no → mild negative signal (finding was reviewed and ignored)
- Store per-repo finding acceptance rates over time

### Review Quality Metrics
- Track per-repo: total reviews, average 👍/👎 rate, most/least accepted finding categories
- Expose in web UI dashboard so the owner can tune `.code-warden.yml` custom instructions based on data

---

## 📊 UI / Dashboard

### Status Page (Phase 1 — MVP)
- List of installed repositories with: last indexed SHA, index size, last review date
- Recent job history table: PR link, status (pending/running/done/error), duration
- Trigger manual prescan or re-index from the UI

### Review Explorer (Phase 2)
- Browse past reviews per repository
- Filter by finding category, severity, file
- See which findings were accepted vs ignored (from feedback signals)
- Diff viewer showing the original PR diff alongside the review comments

### Analytics Dashboard (Phase 3)
- Review quality trend over time (acceptance rate, 👍/👎 per repo)
- Most common finding categories per repo / across all repos
- Index freshness graph (SHA staleness per collection)
- Token usage and LLM cost estimates per review

### Onboarding Wizard
- Step-by-step setup: GitHub App installation → first repo detected → prescan triggered → first review ready
- Show progress bar during prescan/indexing
- Display estimated time to first review

---

## 💬 Developer Onboarding Assistant (Terminal → Web)

The `cmd/terminal` binary is a working POC for this: a Bubbletea TUI that lets you point it at any indexed repo and ask free-form questions ("how does auth work?", "what's the pattern for adding a new endpoint?"). The foundation is solid but there are concrete gaps before this is genuinely useful for onboarding.

### What's Already Working

- Bubbletea TUI with themes, `/add`, `/list`, `/select`, `/rescan` commands
- Free-form questions answered via `RAGService.AnswerQuestion` with hybrid search
- Arch summaries (`chunk_type=arch`) and project context (`chunk_type=project_context`) are already generated at index time — this is the core onboarding data
- ValidatingRetrievalQA (optional fast model pre-filters irrelevant chunks before main LLM)

### Gaps to Fix

**1. Conversation history is not reaching the LLM**

`answerWithValidation` ignores the history parameter entirely. `answerWithoutValidation` passes it to the prompt builder, but `question.prompt` has no `{{.History}}` field in its template — the history is built but never rendered. Multi-turn conversation does not work. Fix: add `{{.History}}` to `question.prompt` and wire it through `answerWithValidation`.

**2. QA retrieval doesn't use the arch summaries**

`AnswerQuestion` does a flat similarity search with k=5 across all chunk types. Onboarding questions ("how is this codebase structured?", "where do I add a new service?") are best answered from `chunk_type=arch` and `chunk_type=project_context` chunks — but those have equal weight with individual function chunks in retrieval. Add a two-pass retrieval: always include the relevant arch summary for the directories mentioned in the question, then fill remaining slots with regular similarity results.

**3. No source citations in answers**

Answers say "the auth service does X" but don't say "see `internal/auth/service.go:45`". For onboarding, knowing *where* to look is as important as the explanation. Add source file + line number to the rendered answer, the same way the review comments include file references.

**4. No streaming**

Answers appear all at once after potentially 30+ seconds of silence. The TUI looks frozen. Add streaming output — Bubbletea supports this via `tea.Cmd` ticks. GoFrame's `GenerateContent` supports streaming already.

**5. No `/explain [path]` command**

A dedicated command that retrieves the arch summary and TOC for a specific directory or file and explains it in plain language. More useful than a generic question for "show me what `internal/rag/` does."

**6. Onboarding-specific prompt**

The current `question.prompt` answers like a generic assistant. For onboarding, the tone should be: "here's the pattern used in this codebase, here's where to find it, here's what you'd need to change to do X." Separate prompt key `onboarding_question` or add a mode flag.

### Integration into the Web UI

The terminal is useful for local/debug use but the real onboarding use case is in-browser. Once the web UI exists (see UI / Dashboard below), add a chat interface per repository: select repo → ask questions → get answers with source links. Same `RAGService.AnswerQuestion` backend, different frontend.

### Additional Indexing Data Needed for Onboarding

The arch summaries and project context already cover most of what a new engineer needs. The gaps:

| Data | How to get it | Value |
|---|---|---|
| **CODEOWNERS** | Parse `.github/CODEOWNERS` at index time | "Who owns `internal/storage/`?" → route to right person |
| **File hotspots** | `git log --follow --format=%H -- <file>` → count commits | Surface most-changed files; those are the ones to understand first |
| **README.md priority** | Index directory READMEs with `chunk_type=docs` and higher retrieval weight | Direct human-written explanations should rank above inferred arch summaries |
| **Commit messages for context** | Already fetched per-PR; extend to index recent commit messages per file as `chunk_type=history` | "What's been happening in `internal/jobs/`?" |

CODEOWNERS and README files are the highest-value additions — low effort, immediately useful for onboarding questions about ownership and intent.

---

## 🤖 Agent Integration (`/implement`)

The `/implement` command works end-to-end but has several gaps that significantly affect usability and reliability. These are ordered by impact.

### 1. Post GitHub Comments During Session (Highest Impact)

Currently the user triggers `/implement` on an issue, the server acknowledges with nothing, and the agent works silently for up to 30 minutes. The user has no way to know whether the session started, what it's doing, or whether it failed — without reading server logs.

What should happen:
- **On trigger**: Post a comment on the issue: "Started implementation session `<session-id>`. I'll update you here."
- **Each iteration**: Post an update: "Iteration 2/3 — review returned `REQUEST_CHANGES`, addressing feedback..."
- **On completion**: Post: "PR created: <url> — <one-line summary>"
- **On failure/timeout**: Post: "Session failed after N iterations. Last error: <message>"

This is the single highest-leverage UX improvement. Without it, the feature feels broken even when it works correctly.

### 2. Add `run_command` MCP Tool

The agent system prompt instructs the agent to run `make lint && make test` to verify its changes before calling `review_code`. But Code-Warden has no MCP tool to actually execute commands in the workspace — so the agent can either skip this step or hallucinate a success.

Required: a `run_command` tool that:
- Executes a whitelisted command (e.g. `make lint`, `make test`, `go build ./...`) inside the session workspace
- Returns stdout/stderr and exit code
- Whitelist is configurable via `verify_commands` in `.code-warden.yml` or global config
- Has a timeout (e.g. 5 minutes) to avoid blocking the session

Without this, "Verify" step 5 in the system prompt is fiction.

### 3. Fix `GetLastReview()` Race Condition

In `internal/agent/orchestrator.go`, the `createReviewHandler` reads the review verdict via `o.mcpServer.GetLastReview()` — a method that returns the **last review stored on the global MCP server**, not the current session's review. With two or more concurrent agent sessions, each session can accidentally pick up the verdict from the other session's `review_code` call.

Fix: scope review results to session ID. `review_code` should store its result keyed by session ID, and the orchestrator should retrieve it by that same key.

### 4. Persist Session State to PostgreSQL

Sessions are stored in memory (`sessions map[string]*Session`). A server restart orphans all active sessions — they show no status, GitHub gets no notification, and the workspaces are left on disk.

Add an `agent_sessions` table:
```
id, issue_number, repo_id, status, branch, pr_url, iterations, started_at, completed_at, error
```

On startup, load active sessions and mark any that were interrupted as FAILED (and post a GitHub comment explaining the restart).

### 5. Auto-Trigger Full `/review` After PR Creation

When the agent creates a PR, it runs its own lightweight `review_code` (single model, no consensus) and submits. But the actual PR has no Code-Warden review posted to GitHub — reviewers see no AI feedback.

After `create_pull_request` succeeds, enqueue a standard review job on the new PR. This gives the team the same full 6-stage consensus review on agent-created PRs that they get on human PRs.

### 6. Replace Fragile Output Parsing

`extractFilesFromImplementation` and `extractPRInfo` use string matching and regex on free-text output from the agent to determine which files changed and what the PR title/body should be. This breaks if the agent formats its output slightly differently.

Better approach: after `push_branch` succeeds, query the GitHub API for the branch's diff relative to base to get the actual changed file list. After `create_pull_request` succeeds, use the returned PR number/URL rather than parsing agent output.

---

## 🏗 RAG Pipeline Improvements

### Query Decomposition / Multi-Query Retrieval
- Break complex PR descriptions into sub-queries (e.g. "auth change + rate limiting + logging")
- Run each sub-query independently, merge and deduplicate results
- Already discussed; implement after feedback loop is stable

### Contextual Chunk Compression
- Before inserting retrieved chunks into the prompt, run a fast model pass to strip boilerplate and keep only the relevant lines
- Reduces token usage without losing signal

### Retrieval Evaluation (Offline)
- Build a small eval set: PR diffs + expected relevant files/symbols
- Run retrieval pipeline against it and measure recall@k
- Use it to tune retrieval parameters (k, rerank threshold, hybrid weights)

### Staleness Detection
- Before generating a review, check if any retrieved chunks are from commits older than the file's last modification
- Flag or re-fetch stale chunks to avoid reviewing against outdated context

---

## 🛒 Product & Competitive Positioning

To be viable as a service for teams with larger repos:

### Must-Have for Enterprise
- **Multi-tenant isolation** — each GitHub org gets its own Qdrant namespace and DB schema
- **SSO / GitHub OAuth login** for the web UI (no separate user management needed)
- **Audit log** — who triggered what review, when, what model was used
- **Cost controls** — per-repo token budget limits, model selection per tier

### Differentiators Worth Doubling Down On
- **Repo-aware context** via RAG is the main moat — competitors do line-by-line diffs; Code-Warden understands the whole codebase
- **Feedback loop** (accept/ignore/wrong signals) — no major competitor has this; it compounds over time
- **Consensus review** (multiple models → synthesis) — unique quality signal, especially for critical PRs
- **Agent-driven implementation** — closes the loop from "review finding" to "code fix" without leaving GitHub

### Nice-to-Have Additions
- **Slack/Teams integration** — post review summaries to a channel on PR open
- **JIRA/Linear integration** — auto-create tickets from critical findings
- **Custom rule engine** — repo owners define structured rules (beyond free-text instructions) that are always checked
- **Severity scoring** — assign P0/P1/P2 to findings based on category + repo config; only comment P0/P1 by default
- **PR size guard** — warn when a PR exceeds a configurable line/file threshold before review runs

---

## 🔧 Operational & Infrastructure

- **Metrics endpoint** (`/metrics` Prometheus) — track review latency, queue depth, error rates
- **Health check** (`/healthz`) — Qdrant + DB + Ollama reachability
- **Graceful shutdown** — drain job queue before exit
- **Docker Compose for production** — single `docker-compose.yml` covering Qdrant, Postgres, Ollama, and Code-Warden
- **Helm chart** — for teams running Kubernetes
- **Rate limiting** — per-repo review rate cap to prevent runaway costs

---

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development guidelines.
