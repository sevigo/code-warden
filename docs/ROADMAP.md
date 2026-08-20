# Code-Warden Roadmap

## Product direction

Code-Warden is a focused, self-hosted review assistant for teams that want credible AI-assisted review without handing their entire engineering workflow to a hosted platform. Its differentiator is **investigable reviews**: each finding is tied to a changed line, reviewed against repository context, and available in the developer's normal workflow (terminal first, pull request second).

The near-term product is deliberately small:

- review a local working tree before code leaves a developer's machine;
- review a GitHub pull request with the same engine and findings; and
- choose a supported model backend through configuration.

Do not add another Git host, a marketplace, automatic fixes, analytics, or a new queue until these paths are dependable and demonstrably useful.

## Architecture decision: review surfaces, not provider plugins

The word "provider" hides three different extension points. They should remain separate:

| Concern | Boundary | First implementations | Owns |
|---|---|---|---|
| Review source | `ReviewSource` | local checkout, GitHub PR | obtain diff, files, context, workspace |
| Review presentation | `ReviewReporter` | terminal, GitHub PR | render and publish one `StructuredReview` |
| Model backend | `llms.Model` + `llm.Factory` | Ollama, OpenAI-compatible, Gemini | construct a model from configuration |

`ReviewSource` and `ReviewReporter` may be paired in a convenience command, but are intentionally independent. A local review obtains its diff through git and reports to the terminal; a GitHub review obtains PR data and publishes a check run and comments. Both call the same `agent/review.Runner` and pass the same `core.StructuredReview` to their reporter.

This is a registry only after there are at least two implementations at one boundary. Use explicit constructors and configuration names now. A dynamic plugin loader would add versioning, security, configuration, and lifecycle problems without serving the first release.

The existing GitHub App remains the only remote integration. Keep its webhook validation, installation authentication, check-run semantics, and PR comment formatting inside the GitHub adapter; do not force those concepts into generic core interfaces. A future GitLab adapter can translate its merge-request concepts into neutral `ReviewInput` and `ReviewPublication` types.

The model abstraction already exists at runtime (`llms.Model`). Consolidate the two construction switches (server and CLI) behind one `llm.Factory` before adding a backend. A provider must only construct a model; prompts, review angles, parsing, filtering, and output formatting stay provider-neutral.

The detailed proposed types and migration sequence are in [DISCOVERY_ARCHITECTURE.md](./DISCOVERY_ARCHITECTURE.md).

## Milestone 0 — Stabilize the current review engine

**Outcome:** a repeatable quality baseline for the engine that both surfaces will share.

1. Finish and run the committed eval harness in mock mode in CI.
2. Keep six small golden cases: bugs, performance, conventions, and clean diffs. Add a live-model suite only when its cost and pass thresholds are agreed.
3. Correct the eval workspaces with required surrounding source files, so context-dependent findings are meaningful.
4. Record a short release checklist: no duplicate findings, valid diff anchors, useful summary, nonzero exit code for requested changes.

**Exit criteria:** `go test ./...` and `go run ./evals --mock` pass in CI; the CLI produces valid JSON and prompt-only output for a fixture repository.

## Milestone 1 — Make local review the reference experience

**Outcome:** one command gives a developer a useful review before push.

1. Treat `cmd/review --local` as the canonical path and document it with a minimal config example.
2. Extract a `LocalSource` from the CLI option-building code. It returns `ReviewInput` (diff, changed files, commit messages, workspace path, and repository identity).
3. Extract the terminal renderers into a `TerminalReporter`; preserve JSON, prompt-only, colour, and exit-code modes as presentation options.
4. Load `.code-warden/config.yml` from a local repository, merged over global defaults and under command-line flags. Start with severity, ignored paths, categories, and max files.
5. Add fixture-based end-to-end tests that never call a live model.

**Exit criteria:** a new user can configure one model, run a local review, interpret findings, and use the exit code in a pre-push or CI script.

## Milestone 2 — Deliver the GitHub path using the same engine

**Outcome:** a GitHub App webhook produces the same review quality and a presentable PR result.

1. Define neutral review input/output types next to the review application service; migrate the job workflow to call that service.
2. Introduce `GitHubSource` and `GitHubReporter` behind narrow interfaces, retaining the current App client internally.
3. Preserve duplicate-SHA protection and check-run lifecycle. Publish one review summary plus validated inline comments; use suggestion blocks when a safe code replacement is supplied.
4. Support explicit `/review` and `/rereview` commands. Automatic cadence, reactions, and comment directives wait until manual operation is reliable.
5. Add contract tests around GitHub source data conversion and reporter output.

**Exit criteria:** installing the app, commenting `/review`, and receiving a single completed check plus inline findings works against a test repository.

## Milestone 3 — Consolidate model backends

**Outcome:** CLI and server select models identically.

- [x] Move model construction to `internal/llm.NewGenerator` and use it from Wire, the standalone CLI, and live evals.
- [x] Keep the configuration schema explicit: `ollama`, `openai` for compatible endpoints, and `gemini`. Validate credentials per selected backend.
- [x] Test backend selection, invalid configuration, and shared timeout defaults without exposing runtime plugins or user-supplied Go code.
- [ ] Add model capability declarations only when a real backend needs them (for example structured output or reasoning controls).

**Exit criteria:** server, CLI, and eval live mode use the same model factory and fail with the same actionable configuration errors.

## Later, only after the core loop is proven

- repository rule files and per-repository configuration expansion;
- a durable worker/outbox if a single process demonstrably loses or blocks jobs;
- a second Git host, implemented as a second source/reporter pair;
- extra model backends; and
- optional developer integrations such as a pre-push helper.

## Explicit non-goals for the first presentable release

- multi-tenant SaaS, SSO, billing, dashboards, and organization analytics;
- autonomous issue implementation as a primary product workflow;
- automatic code changes or merge approvals;
- a third-party plugin marketplace; and
- language-wide semantic graphs.
