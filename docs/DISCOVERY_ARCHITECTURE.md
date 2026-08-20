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
// internal/reviewapp/contracts.go
type ReviewInput struct {
	Repository     string
	Diff           string
	ChangedFiles   []ChangedFile
	CommitMessages []string
	WorkspaceDir   string
}

type ReviewSource interface {
	Load(context.Context) (ReviewInput, error)
}

type ReviewReporter interface {
	Publish(context.Context, ReviewInput, *core.StructuredReview) error
}
```

`ChangedFile` should move to a neutral package when this extraction starts; until then, do not create conversion layers just for a future integration.

The first implementations are deliberately concrete:

| Workflow | Source | Reporter |
|---|---|---|
| `review --local` | `LocalSource` | `TerminalReporter` |
| GitHub App `/review` | `GitHubSource` | `GitHubReporter` |

The GitHub reporter may receive publication metadata not needed by the terminal (head SHA, PR number, check-run ID). Keep that metadata in its constructor or a GitHub-specific request; do not pollute `StructuredReview`.

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

1. Move model construction into `internal/llm`; cover it with unit tests.
2. Introduce neutral `ReviewInput` in a new application package and adapt the current CLI first.
3. Adapt the GitHub review job to the same application service, preserving its existing status and duplicate-review safeguards.
4. Move GitHub-specific rendering from the job workflow into `GitHubReporter`.
5. Only then decide whether a second integration earns an adapter.

Each step must leave both existing commands working. Avoid directory moves and generated mock churn until a compiler-enforced interface requires them.
