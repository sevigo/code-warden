package review

import (
	"context"

	"github.com/sevigo/code-warden/internal/core"
)

// AngleStatus describes how an individual review angle completed.
type AngleStatus string

const (
	AngleStatusCompleted AngleStatus = "completed"
	AngleStatusPartial   AngleStatus = "partial"
)

// AngleRequest contains everything an executor needs for one review angle.
type AngleRequest struct {
	Angle         Angle
	Workspace     string
	TaskContext   string
	Diff          string
	ChangedLines  string
	FilenameIndex string
	MaxIterations int
	ContextWindow int
}

// AngleResult is the observable outcome of one review angle.
type AngleResult struct {
	Angle       string
	Suggestions []core.Suggestion
	Raw         string
	Iterations  int
	TokensIn    int
	TokensOut   int
	Status      AngleStatus
}

// AngleExecutor runs one isolated review angle. Runner depends on this narrow
// boundary so orchestration can be tested without invoking an LLM.
type AngleExecutor interface {
	Execute(context.Context, AngleRequest) (AngleResult, error)
}
