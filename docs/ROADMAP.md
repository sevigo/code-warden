# Code-Warden Roadmap: Building Toward Kodus Parity

> Based on a deep analysis of [kodustech/kodus-ai](https://github.com/kodustech/kodus-ai)
> (5,965 commits, NestJS/Next.js monorepo, AGPL-3.0 + Enterprise) and
> code-warden's current state (Go, goframe, multi-angle agent review, CLI +
> GitHub App).

## What code-warden already has (don't rebuild)

| Capability | code-warden | Kodus equivalent |
|---|---|---|
| Multi-angle agent review | `internal/agent/review/` | `libs/code-review/infrastructure/agents/` |
| CLI standalone review | `cmd/review` | `libs/cli-review` |
| GitHub App integration | `internal/github/`, `internal/jobs/` | `apps/webhooks` + `apps/api` |
| LLM provider abstraction | `internal/llm/` (Ollama, OpenAI, Gemini) | `libs/llm/` + BYOK |
| MCP tool server | `internal/mcp/` | `libs/mcp-server` |
| Agent loop (think-act-observe) | goframe `agent/loop.go` | `libs/ai-engine` |
| Diff parsing + hunk validation | `internal/github/hunks.go` | `libs/code-review` |
| Dedup + severity ranking | `internal/agent/review/dedup.go` | `libs/code-review` dedup |
| Severity filter + ignore paths | `internal/agent/review/config.go` | `suggestionControl` + `ignorePaths` |
| Render output | `internal/reviewcli/render/` | terminal output |
| React dashboard | `ui/` | `apps/web` |
| Context-aware compaction | `runner.go` compaction hook | (not in Kodus — our advantage) |
| Snap-to-hunk line correction | `diff_filter.go` Snap() | `snapLinesToDiff` |
| Angle scoping by file relevance | `scope.go` | (heuristic in Kodus) |
| `get_diff` tool for re-anchoring | `diff_tool.go` | (Kodus uses AST graph instead) |
| `--prompt-only` mode for AI agents | `cmd/review/main.go` | `kodus review --prompt-only` |
| Exit codes for CI/CD | `cmd/review/main.go` | `kodus review` exit codes |
| Category toggles | `config.go` EnabledCategories | `reviewOptions` in config |

---

## Phase 1: Review Quality Foundation (weeks 1-3)

**Goal: make the review reliably good, not just functional.**

### 1.1 Eval harness (`evals/` directory)

This is the single most important item. Kodus has 12 eval suites. We have zero.
Without evals, we can't measure if a prompt change helps or hurts.

**What to build:**

```
evals/
  engine.go          # runner: loads cases, runs review, scores results
  cases/
    bug-negative-index.json      # PR with a negative slice index bug
    bug-nil-deref.json            # PR with a nil pointer dereference
    bug-error-swallow.json       # PR with a swallowed error
    security-sql-injection.json  # PR with string-concat SQL
    security-path-traversal.json # PR with filepath.Join user input
    perf-n-plus-1.json           # PR with a DB query in a loop
    perf-unbounded-alloc.json    # PR with unbounded slice growth
    convention-missing-tests.json # PR with new exported func, no tests
    convention-error-wrapping.json # PR with bare error return
    clean-no-issues.json         # PR with no bugs (false-positive test)
  scorer.go          # precision, recall, latency, token cost
  mock.go            # --mock mode: skip LLM, test pipeline only
```

**Each case file:**
```json
{
  "name": "bug-negative-index",
  "diff": "diff --git a/main.go ...",
  "changed_files": ["main.go"],
  "expected_findings": [
    {
      "file": "main.go",
      "line_range": [20, 25],
      "severity": "high",
      "category": "Bug",
      "description_contains": "negative"
    }
  ],
  "expected_verdict": "REQUEST_CHANGES",
  "expected_false_positives": 0
}
```

**Runner:**
```go
// evals/engine.go
type EvalResult struct {
    Case       string
    Passed     bool
    Precision  float64  // true positives / (true + false positives)
    Recall     float64  // true positives / expected findings
    Latency    time.Duration
    TokensIn   int
    TokensOut  int
}

func RunEvals(ctx context.Context, cases []EvalCase, opts EvalOptions) []EvalResult
```

**CLI:**
```bash
go run ./evals --model ollama/deepseek-v4-flash:cloud
go run ./evals --mock              # CI mode, no LLM keys
go run ./evals --suite bug        # run only bug cases
go run ./evals --verbose          # show per-case details
```

**Exit codes (from Kodus):**
- `0` = all gate evals passed
- `1` = a gate eval failed (quality regression)
- `2` = infrastructure error (not a quality issue)

**Metrics to track over time:**
- Precision: % of findings that are real (not false positives)
- Recall: % of planted bugs found
- Latency: p50, p95 time per review
- Token cost: input + output tokens per review
- Severity accuracy: did the model rate it correctly?

**Mock modes (from Kodus):**
- `--mock=identity` — dedup eval, identity dedup (no LLM)
- `--mock=perfect` — format eval, perfect input (no LLM)
- `--mock=heuristic` — severity eval, heuristic classifier (no LLM)
- `--mock` (default for CI) — pipeline-only, no LLM calls

**Implementation plan:**
1. Create `evals/` directory with `engine.go` and `scorer.go`
2. Write 5 golden cases (3 bugs, 1 security, 1 clean/no-issues)
3. Add `--mock` mode that tests diff parsing, line-map building, snap, dedup, severity filter — all without an LLM
4. Add `go run ./evals` to CI (`.github/workflows/`)
5. Track results in `evals/results/` as JSON for trend analysis
6. Expand to 15-20 cases covering all angles

### 1.2 PR summary generation (`internal/agent/summary/`)

Kodus generates a 2-3 sentence PR description. This is table stakes.

**What to build:**

```go
// internal/agent/summary/summary.go
type SummaryParams struct {
    Diff           string
    ChangedFiles   []string
    CommitMessages []string
    RepoFullName   string
}

func Generate(ctx context.Context, llm llms.Model, params SummaryParams) (string, error)
```

**No tools, no agent loop** — single LLM call with the diff. The prompt:
```
Summarize this pull request in 2-3 sentences. Focus on what changed and why.
Be concise. Do not list every file.

Diff:
{diff}
```

**Config:**
```yaml
review:
  summary:
    generate: true
    behavior: "replace"  # replace | append | complement
```

**Integration:**
- CLI: `--summary` flag prints the summary before findings
- GitHub App: post summary as a PR comment (separate from inline findings)
- `--prompt-only` mode: include summary in the structured output

### 1.3 Incremental review (skip when no new commits)

Kodus: "remembers the last analyzed commit to keep follow-ups incremental."

**What to build:**

```go
// internal/jobs/review_state.go
type ReviewState struct {
    RepoFullName string
    PRNumber     int
    LastReviewedSHA string
    LastReviewedAt time.Time
}

func ShouldSkipReview(state ReviewState, currentSHA string) bool {
    return state.LastReviewedSHA == currentSHA
}
```

**DB table:**
```sql
CREATE TABLE review_states (
    repo_full_name TEXT NOT NULL,
    pr_number INTEGER NOT NULL,
    last_reviewed_sha TEXT NOT NULL,
    last_reviewed_at TIMESTAMP NOT NULL,
    PRIMARY KEY (repo_full_name, pr_number)
);
```

**Flow:**
1. Webhook receives push event with new SHA
2. Check `review_states` — if SHA matches, skip (log "no new commits")
3. After review completes, update `review_states` with the new SHA
4. `@code-warden review --force` overrides the skip

### 1.4 Review cadence / debounce

Kodus: `reviewCadence: { type: "auto_pause", timeWindow: 15m, pushesToTrigger: 3 }`

**What to build:**

```go
// internal/jobs/cadence.go
type CadenceConfig struct {
    Type            string  // "automatic" | "auto_pause" | "manual"
    TimeWindow      time.Duration  // e.g. 15m
    PushesToTrigger int     // e.g. 3
}

type PushTracker struct {
    RepoFullName string
    PRNumber     int
    PushCount    int
    FirstPushAt  time.Time
}

func ShouldTriggerReview(tracker PushTracker, cfg CadenceConfig) bool {
    switch cfg.Type {
    case "automatic":
        return true  // review every push
    case "auto_pause":
        // wait for N pushes in the time window
        if tracker.PushCount >= cfg.PushesToTrigger {
            return true
        }
        if time.Since(tracker.FirstPushAt) > cfg.TimeWindow {
            return true  // window expired, review what we have
        }
        return false  // wait for more pushes
    case "manual":
        return false  // only @code-warden review triggers
    }
}
```

**Storage:** in-memory map with a mutex (simpler) or Redis (scalable).
For self-hosted single-instance, in-memory is fine.

**Config:**
```yaml
review:
  cadence:
    type: "auto_pause"
    time_window: "15m"
    pushes_to_trigger: 3
```

---

## Phase 2: Multi-Provider Git Support (weeks 4-7)

**Goal: don't be GitHub-only. GitLab is the #2 request.**

### 2.1 Git provider abstraction (`internal/platform/`)

**What to build:**

```go
// internal/platform/provider.go
type GitProvider interface {
    // Diff and files
    GetPullRequestDiff(ctx context.Context, owner, repo string, number int) (string, error)
    GetChangedFiles(ctx context.Context, owner, repo string, number int) ([]ChangedFile, error)
    GetPullRequestCommits(ctx context.Context, owner, repo string, number int) ([]string, error)

    // Comments and reviews
    PostInlineComment(ctx context.Context, repo string, number int, comment InlineComment) error
    PostReviewSummary(ctx context.Context, repo string, number int, summary string) error
    SetReviewStatus(ctx context.Context, repo string, number int, status ReviewStatus) error

    // Reactions (for status feedback)
    AddReaction(ctx context.Context, repo string, number int, reaction string) error

    // Identity
    Name() string
    ParseWebhook(payload []byte, headers http.Header) (*WebhookEvent, error)
}
```

**Providers to implement, in order:**
1. GitHub (already exists — extract interface from `internal/github/`)
2. GitLab (merge requests instead of pull requests)
3. Bitbucket (later)
4. Azure Repos (later)
5. Forgejo (later, easy since it's GitHub-compatible)

**File structure:**
```
internal/platform/
  provider.go           # interface
  github/
    provider.go         # implements GitProvider
    webhook.go
    comments.go
  gitlab/
    provider.go
    webhook.go
    comments.go
```

**Migration from current code:**
- `internal/github/` → `internal/platform/github/`
- `internal/jobs/review.go` → use `GitProvider` interface instead of concrete GitHub client
- `cmd/review/main.go` → `--provider github|gitlab` flag (auto-detect from `--pr` URL)

### 2.2 Webhook router (`internal/webhooks/`)

Kodus: fire-and-forget + outbox pattern. Return 200 immediately, process async.

**What to build:**

```go
// internal/webhooks/router.go
type WebhookRouter struct {
    providers map[string]GitProvider
    queue     Queue
}

func (r *WebhookRouter) Handle(w http.ResponseWriter, req *http.Request) {
    provider := r.detectProvider(req.Header)
    event, err := provider.ParseWebhook(body, req.Header)
    if err != nil {
        http.Error(w, "invalid webhook", 400)
        return
    }
    // Fire-and-forget: write to DB + enqueue, return 200
    r.queue.Enqueue(event)
    w.WriteHeader(200)
}
```

**Outbox pattern:**
```sql
CREATE TABLE webhook_events (
    id SERIAL PRIMARY KEY,
    provider TEXT NOT NULL,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMP NOT NULL,
    processed_at TIMESTAMP  -- NULL = pending
);
```

A relay goroutine polls `webhook_events` where `processed_at IS NULL`,
processes them, and marks as processed. This ensures no webhook is lost
even if the worker is down.

---

## Phase 3: Product Features (weeks 8-13)

**Goal: match Kodus's feature surface for self-hosted users.**

### 3.1 Code-warden rules (`internal/rules/`)

Kodus: "Kody Rules are customizable guidelines your team sets up to
automatically enforce code quality, consistency, security, and
maintainability."

**What to build:**

```
.code-warden/
  rules/
    no-global-state.md      # "Flag any new global variables"
    error-wrapping.md        # "All errors must be wrapped with context"
    test-coverage.md        # "New exported functions must have tests"
    security-checklist.md   # "Check for SQL injection in all DB queries"
```

**Rule file format:**
```markdown
---
name: "Error wrapping"
severity: "medium"
category: "conventions"
applies_to: "**/*.go"
---

All errors returned from internal calls must be wrapped with context
using `fmt.Errorf("...: %w", err)`. Bare `return err` is not acceptable
for internal package boundaries.
```

**Loading:**
```go
// internal/rules/loader.go
type Rule struct {
    Name        string
    Severity    string
    Category    string
    AppliesTo   string  // glob pattern
    Description string  // markdown body
}

func LoadRules(repoRoot string) ([]Rule, error) {
    // Walk .code-warden/rules/*.md
    // Parse YAML frontmatter + markdown body
}
```

**Integration into review:**
- Rules are loaded from the workspace
- Each rule's description is injected into the task context as
  "Repository rules to enforce:"
- The agent is instructed to check the diff against these rules
- Rules bypass the severity filter (like Kodus: `applyFiltersToKodyRules: false`)

**Config:**
```yaml
review:
  rules:
    enabled: true
    directory: ".code-warden/rules"
    apply_severity_filter: false  # rules always reported
```

### 3.2 Review directives (`@code-warden review` comments)

Kodus: `@kody start-review`, `@kody review --force`

**What to build:**

Parse PR comments for commands:
```
@code-warden review                  # trigger a review
@code-warden review --force          # force re-review even if no new commits
@code-warden review --focus auth.go  # focus on a specific file
@code-warden review --severity high  # only report high+ this time
```

```go
// internal/jobs/directive.go
type Directive struct {
    Command  string   // "review"
    Flags    map[string]string  // --force, --focus, --severity
}

func ParseDirective(comment string) (*Directive, error) {
    // Parse "@code-warden review --force --focus auth.go"
}
```

**Integration:**
- Webhook handler listens for `issue_comment` events
- If comment starts with `@code-warden`, parse the directive
- Enqueue a review job with the directive's flags overriding config

### 3.3 Committable suggestions

Kodus: `enableCommittableSuggestions: true`

**What to build:**

When a finding includes `CodeSuggestion`, post it as a GitHub suggestion block:
```
```suggestion
if n < 0 {
    n = 0
}
```
```

GitHub renders this as a one-click "Commit suggestion" button in the PR UI.

```go
// internal/platform/github/comments.go
func FormatSuggestionComment(s core.Suggestion) string {
    if s.CodeSuggestion == "" {
        return s.Comment
    }
    return fmt.Sprintf("%s\n\n```suggestion\n%s\n```", s.Comment, s.CodeSuggestion)
}
```

**Config:**
```yaml
review:
  enable_code_suggestions: true  # already in config.yaml.example
```

### 3.4 Status feedback (emoji reactions)

Kodus: 🚀 processing → 🎉 completed → 👀 skipped → 😕 error

**What to build:**

```go
// internal/platform/github/reactions.go
const (
    ReactionProcessing = "rocket"
    ReactionCompleted  = "tada"
    ReactionSkipped    = "eyes"
    ReactionError      = "confused"
)

func SetReviewStatus(ctx context.Context, provider GitProvider, repo string, prNumber int, status string) {
    // Remove previous reaction, add new one
    provider.AddReaction(ctx, repo, prNumber, status)
}
```

**Flow:**
1. Review starts → add 🚀 to PR description
2. Review completes → replace with 🎉
3. Review skipped → replace with 👀
4. Review errors → replace with 😕

### 3.5 Config file in repo (`.code-warden/config.yml`)

Kodus: `kodus-config.yml` with `kodusConfigFileOverridesWebPreferences`

**What to build:**

```yaml
# .code-warden/config.yml
version: "1.0"

review:
  cadence:
    type: "auto_pause"
    time_window: "15m"
    pushes_to_trigger: 3
  severity_filter: "medium"
  ignore_paths:
    - "vendor/**"
    - "**/*.gen.go"
  categories:
    bug: true
    security: true
    performance: true
    conventions: true
  summary:
    generate: true
    behavior: "replace"
  rules:
    enabled: true
    directory: ".code-warden/rules"
  code_suggestions: true
  auto_approve: true
  request_changes: true
  run_on_draft: true
```

**Loading:**
```go
// internal/config/repo_config.go
func LoadRepoConfig(repoRoot string) (*RepoConfig, error) {
    // Read .code-warden/config.yml
    // Merge with global config (repo overrides global)
}
```

**Config cascade:**
1. Global defaults (`config.yaml` on the server)
2. Repo config (`.code-warden/config.yml` in the repo)
3. CLI flags (highest priority)

---

## Phase 4: Scale & Polish (weeks 14-21)

**Goal: production-ready self-hosted deployment.**

### 4.1 Worker process (`cmd/worker`)

Currently `internal/jobs/` runs inline in the server process. For large
repos and multiple concurrent reviews, this needs to be async.

**What to build:**

```
cmd/worker/main.go    # standalone worker process
```

**Queue options (in order of complexity):**
1. **In-memory channel** (current, fine for single-instance)
2. **Redis streams** (simple, good for self-hosted)
3. **RabbitMQ** (what Kodus uses, more robust)

**For self-hosted, Redis is the sweet spot:**
```go
// internal/queue/redis.go
type RedisQueue struct {
    client *redis.Client
}

func (q *RedisQueue) Enqueue(job ReviewJob) error
func (q *RedisQueue) Dequeue(ctx context.Context) (ReviewJob, error)
```

**Inbox/outbox pattern (from Kodus):**
```sql
CREATE TABLE review_jobs (
    id SERIAL PRIMARY KEY,
    repo_full_name TEXT NOT NULL,
    pr_number INTEGER NOT NULL,
    sha TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',  -- pending | processing | done | failed
    created_at TIMESTAMP NOT NULL,
    processed_at TIMESTAMP,
    result JSONB
);
```

**Worker flow:**
1. Poll `review_jobs` for `status = 'pending'`
2. Mark as `processing`
3. Run the review
4. Mark as `done` (or `failed` with error)
5. Post results to GitHub

### 4.2 Go AST graph (`internal/graph/`)

Kodus uses `kodus-graph` (TypeScript AST parser) to build a call graph.
Go has `golang.org/x/tools/go/packages` — we can build a Go-specific
AST graph that's better than Kodus's generic parser for Go code.

**What to build:**

```go
// internal/graph/graph.go
type CallGraph struct {
    Functions map[string]*FunctionNode  // fully-qualified name → node
    Files    map[string]*FileNode      // path → file
}

type FunctionNode struct {
    Name       string
    File       string
    LineStart   int
    LineEnd     int
    Callers    []string  // function names that call this
    Callees    []string  // functions this calls
    Imports    []string
    Receivers  []string  // types that receive this method
}

func BuildGraph(ctx context.Context, repoRoot string) (*CallGraph, error)
```

**Integration into review:**
- When the agent investigates a changed function, feed it the callers
  and callees from the graph — no need to grep blindly
- `get_callers` tool: given a function name, return its callers
- `get_callees` tool: given a function name, return what it calls
- This is more precise than grep and saves 2-3 agent iterations

**Implementation:**
1. Use `golang.org/x/tools/go/packages` to parse the repo
2. Walk the AST to extract functions, calls, imports
3. Build the call graph in memory
4. Cache it in the DB (rebuild on merge to main, incremental on PR)
5. Expose `get_callers` and `get_callees` as agent tools

**Start Go-only.** Other languages can use a generic tree-sitter parser
later, but Go-native is our competitive advantage.

### 4.3 Cockpit metrics (`internal/metrics/`)

Kodus: DORA metrics, bug ratio, PR cycle time, review effectiveness.

**What to build:**

```go
// internal/metrics/collector.go
type Metrics struct {
    ReviewsRun         int
    FindingsPerReview  float64
    FalsePositiveRate  float64  // from user reactions (dismissals)
    AvgTokensPerReview int
    AvgLatency         time.Duration
    SeverityBreakdown  map[string]int  // critical/high/medium/low counts
}
```

**Track per-PR:**
- Review time, token cost, finding count
- User reactions (👍 accepted, 👎 dismissed) for false-positive tracking
- Verdict (APPROVE vs REQUEST_CHANGES)

**Dashboard:**
- Add a "Metrics" tab to the React UI
- Show trends over time (reviews/week, findings/review, FP rate)
- This is simpler than Kodus's full Cockpit but covers the basics

### 4.4 Learning from feedback

Kodus: reinforcement learning from user feedback.

**What to build (simple version):**

```sql
CREATE TABLE finding_feedback (
    id SERIAL PRIMARY KEY,
    repo_full_name TEXT NOT NULL,
    pr_number INTEGER NOT NULL,
    file TEXT NOT NULL,
    line INTEGER NOT NULL,
    severity TEXT NOT NULL,
    category TEXT NOT NULL,
    comment TEXT NOT NULL,
    dismissed BOOLEAN NOT NULL DEFAULT false,
    dismissed_by TEXT,
    dismissed_at TIMESTAMP
);
```

**Integration:**
- When a user reacts 👎 to a review comment, mark that finding as dismissed
- On the next review of the same repo, include past dismissals in the task context:
  ```
  Past false positives on this repo (do not repeat):
  - [file:line] severity category: "comment summary" (dismissed by user)
  ```
- This is a simple version of Kodus's reinforcement learning

### 4.5 Linked repositories (Enterprise)

Kodus: `libs/ee/linked-repositories` for cross-repo context.

**What to build:**

```go
// internal/config/linked_repos.go
type LinkedRepo struct {
    URL      string
    Ref      string  // branch/tag
    Paths    []string  // only index these paths
}
```

**Integration:**
- Clone linked repos alongside the main repo
- Index their public interfaces (exported functions, types)
- Feed to the agent as "sibling repository contracts" in the task context
- Gate behind a license flag (`internal/license/`)

---

## Phase 5: Competitive Edge (ongoing)

**Goal: where code-warden can be better than Kodus.**

### 5.1 Go-native performance

- Single binary deployment (no Node.js, no Docker compose with 5 services)
- Lower memory footprint for self-hosted
- Faster startup, simpler ops
- `go build` → one binary, `./code-warden` → running server + worker + webhooks

### 5.2 Local-first CLI

- `code-warden review --local .` already works — make it the best local review experience
- Pre-push hook: `code-warden hook install` → git pre-push hook that runs review
- `--fix` mode: auto-apply fixable suggestions (like Kodus `--fix`)
- Fully offline with local Ollama models — Kodus CLI requires auth

### 5.3 Go AST-aware review

- Use `golang.org/x/tools` to build a Go-specific review pass
- Catch things no LLM can: unused imports, unreachable code, type mismatches
- Feed AST findings to the LLM as pre-verified facts
- Kodus uses a generic TS AST parser; we can be Go-native and more precise

### 5.4 Plugin system (MCP-based)

- Let users add custom MCP tools (e.g. "check our internal API contract")
- `internal/mcp/` already has the infrastructure
- Config: `.code-warden/plugins/` directory with MCP server definitions
- Kodus: `libs/mcp-server` + Plugins UI

---

## What NOT to build (yet)

| Feature | Why skip |
|---|---|
| SSO/SAML | Enterprise-only, no self-hosted user needs this early |
| Multi-tenant org/team model | Adds complexity; start single-tenant |
| Fine-tuning pipeline | Too expensive, not enough data yet |
| Business logic validation (Jira/Linear) | Niche feature, build after core review is solid |
| Analytics worker | Cockpit is nice-to-have, not core |
| Bitbucket/Azure/Forgejo | GitLab first, others much later |
| Auto-rules generation | Need learning data first |
| IDE rules sync | Need rules system first |

---

## Priority order

```
Phase 1 (weeks 1-3):  Evals → PR summary → incremental review → debounce
Phase 2 (weeks 4-7):  Git provider abstraction → GitLab support → webhook router
Phase 3 (weeks 8-13): Rules → review directives → committable suggestions → status → config file
Phase 4 (weeks 14-21): Worker → AST graph → metrics → learning → linked repos
Phase 5 (ongoing):    Go-native advantages → local-first → plugins
```

## Kodus feature → code-warden mapping

| Kodus feature | Kodus location | code-warden equivalent | Status |
|---|---|---|---|
| Multi-angle agent review | `libs/code-review/infrastructure/agents/` | `internal/agent/review/` | done |
| CLI standalone review | `libs/cli-review` | `cmd/review` | done |
| LLM provider abstraction | `libs/llm/` + BYOK | `internal/llm/` | done |
| MCP tool server | `libs/mcp-server` | `internal/mcp/` | done |
| Diff parsing + hunk validation | `libs/code-review` | `internal/github/hunks.go` | done |
| Dedup + severity ranking | `libs/code-review` dedup | `internal/agent/review/dedup.go` | done |
| Severity filter | `suggestionControl.severityLevelFilter` | `config.go` FilterBySeverity | done |
| Ignore paths | `ignorePaths` in general config | `config.go` IgnorePaths | done |
| Category toggles | `reviewOptions` | `config.go` EnabledCategories | done |
| `--prompt-only` mode | CLI `--prompt-only` | `cmd/review/main.go` | done |
| Exit codes for CI/CD | CLI exit codes | `cmd/review/main.go` | done |
| Context compaction | (not in Kodus) | `runner.go` compaction hook | done (our advantage) |
| Snap-to-hunk | `snapLinesToDiff` | `diff_filter.go` Snap() | done |
| Angle scoping | (heuristic in Kodus) | `scope.go` | done |
| `get_diff` tool | (AST graph instead) | `diff_tool.go` | done |
| Eval harness | `evals/` (12 suites) | — | **todo (Phase 1)** |
| PR summary | `libs/code-review` summary | — | **todo (Phase 1)** |
| Incremental review | "remembers last commit" | — | **todo (Phase 1)** |
| Review cadence/debounce | `reviewCadence` config | — | **todo (Phase 1)** |
| Git provider abstraction | `libs/platform/` | — | **todo (Phase 2)** |
| GitLab support | `libs/platform/gitlab/` | — | **todo (Phase 2)** |
| Webhook router | `apps/webhooks/` | — | **todo (Phase 2)** |
| Kody Rules | `libs/kodyRules/` | — | **todo (Phase 3)** |
| Review directives | `@kody start-review` | — | **todo (Phase 3)** |
| Committable suggestions | `enableCommittableSuggestions` | — | **todo (Phase 3)** |
| Status feedback (emoji) | `showStatusFeedback` | — | **todo (Phase 3)** |
| Config file in repo | `kodus-config.yml` | — | **todo (Phase 3)** |
| Worker process | `apps/worker/` | — | **todo (Phase 4)** |
| AST graph | `kodus-graph` | — | **todo (Phase 4)** |
| Cockpit metrics | `libs/cockpit/` | — | **todo (Phase 4)** |
| Learning from feedback | `kodyLearningExcludedReviewers` | — | **todo (Phase 4)** |
| Linked repositories | `libs/ee/linked-repositories` | — | **todo (Phase 4)** |
| Pre-push hook | CLI `kodus hook install` | — | **todo (Phase 5)** |
| `--fix` auto-apply | CLI `--fix` | — | **todo (Phase 5)** |
| Plugin system | `libs/mcp-server` + Plugins UI | — | **todo (Phase 5)** |
| SSO/SAML | `libs/identity/` | — | skip |
| Multi-tenant | org/team model | — | skip |
| Fine-tuning | `libs/kodyFineTuning/` | — | skip |
| Business logic validation | `@kody -v business-logic` | — | skip |