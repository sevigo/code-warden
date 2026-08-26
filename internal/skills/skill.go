// Package skills defines the extensible review-lens engine. A Skill examines a
// changed set of files and produces review findings. Skills come in two modes:
// agent-mode (an LLM-driven investigation loop) and analyzer-mode (deterministic
// parsing plus Go rule checks, with an optional LLM explanation pass).
package skills

import (
	"context"

	"github.com/sevigo/code-warden/internal/core"
)

// Mode describes how a Skill produces findings.
type Mode string

const (
	// ModeAgent runs an LLM-driven investigation loop against a workspace,
	// prompting a model to reason over a diff and its surrounding code.
	ModeAgent Mode = "agent"
	// ModeAnalyzer deterministically parses changed files and applies Go rule
	// checks. Findings are grounded in evidence rather than inferred by a model.
	ModeAnalyzer Mode = "analyzer"
)

// RunContext carries everything a Skill needs to review one change. It mirrors
// the data integrations already load, decoupled from any single source.
type RunContext struct {
	// Diff is the unified diff of the change being reviewed.
	Diff string
	// ChangedFiles are the per-file patches of the change.
	ChangedFiles []core.ChangedFile
	// Workspace is an existing checkout of the repository to investigate.
	// Empty when the skill does not need a workspace.
	Workspace string
	// CloneURL is the git URL used to clone the repository when Workspace is
	// unset and an agent-mode skill needs a checkout.
	CloneURL string
	// RepoFullName is "owner/name" used for logging.
	RepoFullName string
	// CommitMessages are the commit messages for the change under review.
	CommitMessages []string
}

// Skill is one review lens. Deterministic analyzer skills should perform no
// network or model work inside Detect; they use Detect only to decide whether
// the changed files are in scope.
type Skill interface {
	// Name is the stable, command-invokable identifier of the skill.
	Name() string
	// Description is a short human-readable summary of the skill.
	Description() string
	// Mode reports whether the skill is agent-driven or deterministic.
	Mode() Mode
	// Detect reports whether this skill applies to the given changed files.
	Detect(changedFiles []core.ChangedFile) bool
	// Run executes the skill and returns its structured findings.
	Run(ctx context.Context, rc RunContext) (*core.StructuredReview, error)
}
