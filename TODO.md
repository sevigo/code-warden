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
