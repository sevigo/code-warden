package skills

import (
	"context"
	"fmt"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/core"
)

// AgentRunner is the narrow executor boundary the agent skill depends on,
// mirroring the existing multi-angle review runner so it can be tested without
// invoking an LLM.
type AgentRunner interface {
	Run(context.Context, agentreview.Params) (*agentreview.Result, error)
}

// ReviewSkill is the general-purpose, LLM-driven review lens. It wraps the
// existing multi-angle agent runner and exposes it through the Skill interface
// so the engine treats it like any other skill. Its Detect returns true because
// a general code review applies to any changed set of files.
type ReviewSkill struct {
	runner AgentRunner
}

// NewReviewSkill creates an agent-mode skill backed by the given runner.
func NewReviewSkill(runner AgentRunner) *ReviewSkill {
	return &ReviewSkill{runner: runner}
}

// Name implements Skill.
func (s *ReviewSkill) Name() string { return "review" }

// Description implements Skill.
func (s *ReviewSkill) Description() string {
	return "general agent-based code review (bug, security, performance, conventions)"
}

// Mode implements Skill.
func (s *ReviewSkill) Mode() Mode { return ModeAgent }

// Detect implements Skill. A general review applies to any changed file.
func (s *ReviewSkill) Detect(_ []core.ChangedFile) bool { return true }

// Run implements Skill.
func (s *ReviewSkill) Run(ctx context.Context, rc RunContext) (*core.StructuredReview, error) {
	result, err := s.runner.Run(ctx, agentreview.Params{
		Diff:           rc.Diff,
		ChangedFiles:   rc.ChangedFiles,
		RepoURL:        rc.CloneURL,
		WorkspaceDir:   rc.Workspace,
		RepoFullName:   rc.RepoFullName,
		CommitMessages: rc.CommitMessages,
	})
	if err != nil {
		return nil, err
	}
	if result == nil || result.Review == nil {
		return nil, fmt.Errorf("skill review: agent runner returned no review")
	}
	return result.Review, nil
}
