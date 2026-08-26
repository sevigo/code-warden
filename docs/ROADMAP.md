# Code-Warden Roadmap

## Product direction

Code-Warden is a configurable, self-hosted review assistant. Its differentiator is moving from a **generic AI code reviewer** to a **skill engine**: a PR is examined through several focused lenses, each of which is a *skill*. A skill is either

- **agent mode** — the existing LLM-driven pass (system prompt + read-only workspace tools) that investigates a diff and reports findings; or
- **analyzer mode** — a deterministic parser (e.g. Terraform HCL, Kubernetes YAML, GitHub Actions) that extracts a diff into a typed model, applies explicit Go rule checks, and produces **grounded** findings with a stable rule ID and evidence. An LLM is used only to explain and prioritize the deterministic results, never to invent facts.

Both modes emit the same `core.StructuredReview` / `core.Suggestion`, so the webhook, storage, check-run, PR-comment, and CLI surfaces are unchanged. The differentiator is **defensible, domain-specific review**: deterministic rules + cloud/platform semantics + policy, with AI used for reasoning.

The near-term product:

- review a local working tree and a GitHub pull request with the same skill engine;
- auto-run every skill whose `Detect(changedFiles)` matches the PR's changed files;
- let users override with explicit commands (`/review`, and later `/infra`, `/policy`, `/readiness`);
- keep model backends and output surfaces decoupled from skill logic.

## Architecture decision: skills, not provider plugins

The skill engine generalizes the existing "review angle" into a `Skill` with two modes. The registry owns ordering and applicability:

| Concern | Boundary | First implementations | Owns |
|---|---|---|---|
| Review source | `ReviewSource` | local checkout, GitHub PR | obtain diff, files, context, workspace |
| Review presentation | `ReviewReporter` | terminal, GitHub PR | render and publish one `StructuredReview` |
| Model backend | `llms.Model` + `llm.Factory` | Ollama, OpenAI-compatible, Gemini | construct a model from configuration |
| Review lens | `skills.Skill` | agent (bug/security/performance/conventions), infra risk, policy guard, operational readiness | detect applicability and emit `core.Suggestion`s |

The model abstraction already exists at runtime (`llms.Model`). A skill must only produce findings; it stays provider-neutral. Deterministic analyzer skills run their Go rules directly and call the LLM only for the explanation pass, reusing `llm.PromptManager`.

The existing GitHub App remains the only remote integration. Keep its webhook validation, installation authentication, check-run semantics, and PR comment formatting inside the GitHub adapter; do not force those concepts into generic core interfaces.

## Milestone 0 — Skill engine foundation

**Outcome:** the `Skill` abstraction exists, the current review engine is a skill, and behavior is unchanged.

1. Introduce `internal/skills.Skill`: `Name`, `Mode` (`agent` | `analyzer`), `Detect(changedFiles) bool`, `Run(ctx, RunContext) (*core.StructuredReview, error)`.
2. Refactor `internal/agent/review.Angle` into the `agent` skill mode. `DefaultAngles` (bug, security, performance, conventions) become agent-mode skills; the existing `Runner` is the agent-mode executor.
3. Add a `skills.Registry`: an ordered list of skills plus `RunApplicable(ctx, changedFiles, overrides)`.
4. Extend `core.Suggestion` with optional deterministic fields — `Resource`, `Change`, `RuleID`, `Evidence` — all `omitempty` so existing renderers are unaffected.
5. Introduce the skill-command table in `internal/core/events.go` (`/review`, `/rereview`, optional `/infra`, `/policy`, `/readiness`) mapping to skill overrides.

**Exit criteria:** all existing tests pass; `/review` behaves identically; a PR touching only `.tf`/`k8s` files runs the applicable skills.

## Milestone 1 — Deterministic analyzers: infra risk, blast radius, policy guard (ideas 1, 2, 5)

**Outcome:** a PR changing Terraform/Kubernetes/Helm produces grounded, rule-driven production-risk findings with a human explanation.

1. `internal/skills/parse` — Terraform (HCL) and Kubernetes (YAML) diff parsers that produce typed resource models from changed files.
2. `internal/skills/rules` — shared deterministic rule primitives as pure Go checks, each unit-tested:
   - deployment replicas shrink (`replicas 4 -> 1`);
   - `PodDisruptionBudget` no longer valid;
   - `deletion_protection` disabled;
   - public ingress introduced;
   - IAM wildcard added;
   - S3 bucket encryption removed;
   - queue visibility timeout < worker processing time;
   - DB instance replacement likely.
3. `internal/skills/infra` — the `infra` skill = production-risk (idea 2) + blast-radius (idea 1): which resources change, what depends on them, state/rollback implications, expected downtime risk.
4. `internal/skills/policy` — the `policy` guard (idea 5): reusable rule primitives + a `policies:` block in `.code-warden.yml` (`production databases must have backups`, `no public RDS`, `all queues need DLQ`, `no unencrypted buckets`, `destructive changes require approval`). Report exactly what rule is violated and why.
5. `internal/skills/explainer` — one LLM pass over deterministic findings to write the narrative and severity, reusing `PromptManager` (new `infra_explain.prompt`, `policy_explain.prompt`).

**Exit criteria:** `review --local ./ops --skill infra` on a fixture Terraform/K8s repo emits grounded findings with rule IDs and an explanation; each rule has table-driven tests; no live model required for the deterministic path.

## Milestone 2 — PR-level operational readiness (idea 6)

**Outcome:** for every backend/infra change, a "is this safe to operate?" scorecard.

1. `internal/skills/readiness` — an agent-mode skill that investigates the diff for operational patterns: timeout configured, retries bounded, idempotency, metrics, alerting, circuit breaker, queue + DLQ, max retry count, failed-job metric.
2. Emit a `ReadinessScore` block in the review (percents, missing items, warnings). Deterministic for the parts that are parseable; LLM for ambiguous patterns.

**Exit criteria:** a PR adding a background worker or external API integration yields a scorecard with missing-items listed and a composite score.

## Milestone 3 — Configurable per-repo skill & policy surface

**Outcome:** teams can tune which skills run and which policy rules apply, without editing code.

1. Extend `.code-warden.yml`: `skills:` (enable/disable per skill) and `policies:` (enable/parameterize deterministic rules).
2. Render per-skill result sections in the PR comment and check-run summary.
3. Auto-detect by changed files remains the default; explicit `/infra`, `/policy`, `/readiness` commands override.

**Exit criteria:** a repo can disable the security agent skill, enable infra risk with a custom "no public RDS" policy, and see a labeled section per skill in the PR output.

## Later, after the core skill loop is proven

- **CI cost analyzer (idea 3):** GitHub Actions workflow parser + runner cost model projecting pre-merge cost increase and optimization suggestions.
- **CI flakiness intelligence (idea 4):** a new webhook event source + history store that ingests check-run/workflow-run history over time, learns flaky failures and slowdowns, then correlates a PR's touched modules with known flaky areas.
- second Git host implemented as a second source/reporter pair;
- a durable worker/outbox if a single process demonstrably loses or blocks jobs;
- extra model backends;
- optional developer integrations such as a pre-push helper.

## Explicit non-goals for the first presentable release

- multi-tenant SaaS, SSO, billing, dashboards, and organization analytics;
- autonomous issue implementation as a primary product workflow;
- automatic code changes or merge approvals;
- a third-party plugin marketplace / dynamic plugin loader (skills are registered in code; configuration only toggles them);
- language-wide semantic graphs.
