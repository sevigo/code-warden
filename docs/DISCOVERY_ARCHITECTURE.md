# Discovery Architecture

This note guides the first small, open architecture for Code-Warden. It is a design constraint, not a promise to implement every interface immediately.

## The review pipeline

```
ReviewSource -> ReviewInput -> ReviewService -> StructuredReview -> ReviewReporter
                     |                                  |
                 workspace path                       review evidence
```

The review service owns orchestration, prompt construction, multi-angle review, deduplication, filtering, and line validation. Sources own data acquisition. Reporters own formatting and delivery.

```go
// internal/reviewapp/service.go
type ReviewInput struct {
	Repository     string
	Diff           string
	ChangedFiles   []core.ChangedFile
	CommitMessages []string
	WorkspaceDir   string
	CloneURL       string
}

type ReviewSource interface {
	Load(context.Context) (ReviewInput, error)
}

type Reviewer interface {
	Review(context.Context, ReviewInput, ReviewOptions) (*ReviewResult, error)
}
```

`core.ChangedFile` is the neutral diff-file type. The existing GitHub type is a compatibility alias, so integrations and the engine share it without conversion layers.

The first implementation is deliberately concrete and CLI-only:

| Workflow | Source | Reporter |
|---|---|---|
| `review --local` / `review --pr` | `LocalSource` / `PRSource` (`internal/reviewcli/`) | rendered directly by `reviewcli/render/` |
| GitHub App `/review` | not yet on `reviewapp` — still calls the review runner directly from `internal/jobs/review.go` | posted directly by the job |

A `ReviewReporter` interface does not exist yet. It is still the planned seam for step 4 below, once the GitHub job is migrated onto `reviewapp.Service` and needs to share rendering with the CLI. When it lands, expect the GitHub implementation to carry publication metadata the terminal does not need (head SHA, PR number, check-run ID) in its constructor or a GitHub-specific request; do not pollute `StructuredReview`.

## What does not belong in this interface

Webhook verification, installation-token creation, cloning credentials, GitHub check runs, comment edit IDs, and issue implementation are integration details. They must not become methods on a generic review source or reporter.

Likewise, the terminal is not a Git provider. It is a source and reporter pair. Calling both GitHub and terminal "providers" leads to interfaces that either leak GitHub vocabulary or become too vague to be useful.

## Model factory

The engine consumes `llms.Model`; this is already the essential model-provider abstraction. The missing piece is construction:

```go
// internal/llm/factory.go
func NewGenerator(ctx context.Context, cfg config.AIConfig, log *slog.Logger) (llms.Model, error)
```

Both the Wire provider and standalone CLI call this function. The factory owns the named backend switch and backend-specific options. It returns clear errors for unavailable credentials and unsupported names. It does not choose prompts or add a second model interface.

Add a registry only if model backends begin to require independently tested capabilities or dynamically configured aliases. A static map of named factory functions is enough at that point; loading binary plugins is out of scope.

## Migration order

1. **Complete:** move model construction into `internal/llm`; cover it with unit tests.
2. **Complete:** introduce neutral `ReviewInput` in a new application package and adapt the current CLI first.
3. Adapt the GitHub review job to the same application service, preserving its existing status and duplicate-review safeguards.
4. Move GitHub-specific rendering from the job workflow into `GitHubReporter`.
5. Only then decide whether a second integration earns an adapter.

Each step must leave both existing commands working. Avoid directory moves and generated mock churn until a compiler-enforced interface requires them.
