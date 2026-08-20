// Package reviewcli provides a standalone entry point for agent-based code
// reviews without requiring the GitHub App dependency graph.
package reviewcli

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sevigo/code-warden/internal/agent"
	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/config"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/llm"
)

// Options holds the inputs needed to run a standalone review.
type Options struct {
	// RepoFullName is "owner/name" used for logging and task context.
	RepoFullName string
	// Diff is the unified diff to review.
	Diff string
	// ChangedFiles are the per-file patches.
	ChangedFiles []internalgithub.ChangedFile
	// WorkspaceDir is an existing local checkout to investigate. When set, the
	// runner uses it directly instead of cloning RepoURL.
	WorkspaceDir string
	// RepoURL is a git URL (with credentials embedded) to clone for investigation,
	// used when WorkspaceDir is empty.
	RepoURL string
	// CommitMessages are the commit messages for the changes being reviewed.
	CommitMessages []string
	// Timeout is the per-angle timeout. When zero, the runner default (5m) is used.
	Timeout time.Duration
	// MaxIterations is the per-angle agent-loop iteration cap. When zero, the
	// runner default (8) is used.
	MaxIterations int
	// ContextWindow is the model's max context size in tokens. When zero,
	// the runner default (128000) is used. Compaction triggers at 60%.
	ContextWindow int
	// Config controls noise filtering. When nil, the runner uses
	// DefaultConfig.
	Config *agentreview.Config
}

// Run executes the agent-based review and returns the structured result.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger, opts Options) (*agentreview.Result, error) {
	if logger == nil {
		logger = slog.Default()
	}

	model, err := llm.NewGenerator(ctx, cfg.AI, logger)
	if err != nil {
		return nil, fmt.Errorf("build LLM: %w", err)
	}

	promptMgr, err := llm.NewPromptManager()
	if err != nil {
		return nil, fmt.Errorf("load prompts: %w", err)
	}

	runner := agentreview.NewRunner(model, promptMgr, agent.ReadOnlyReviewTools, logger, nil)

	params := agentreview.Params{
		Diff:           opts.Diff,
		ChangedFiles:   opts.ChangedFiles,
		WorkspaceDir:   opts.WorkspaceDir,
		RepoURL:        opts.RepoURL,
		RepoFullName:   opts.RepoFullName,
		CommitMessages: opts.CommitMessages,
		Timeout:        opts.Timeout,
		MaxIterations:  opts.MaxIterations,
		ContextWindow:  opts.ContextWindow,
		Config:         opts.Config,
	}

	return runner.Run(ctx, params)
}
