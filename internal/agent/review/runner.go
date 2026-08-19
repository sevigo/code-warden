package review

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/gitutil"
	"github.com/sevigo/goframe/llms"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	llmpkg "github.com/sevigo/code-warden/internal/llm"
)

// ToolBuilder constructs the read-only investigation tools (grep, find,
// read_file, list_dir) wired to a workspace root. It is provided by the caller
// to avoid an import cycle between this package and internal/agent.
type ToolBuilder func(workspaceRoot string) []goframeagent.Tool

// Runner executes the multi-angle agent-based code review.
type Runner struct {
	llm       llms.Model
	promptMgr *llmpkg.PromptManager
	tools     ToolBuilder
	angles    []Angle
	logger    *slog.Logger
}

// Params holds the inputs for a single review run.
type Params struct {
	// Diff is the unified diff of the PR being reviewed.
	Diff string
	// ChangedFiles are the per-file patches of the PR.
	ChangedFiles []internalgithub.ChangedFile
	// RepoURL is the git URL (credentials embedded) to clone for investigation.
	// Ignored when WorkspaceDir is set.
	RepoURL string
	// WorkspaceDir is an existing checkout to investigate. When set, the runner
	// uses it directly instead of cloning RepoURL (used by /implement's local
	// workspace, which is already at the modified state).
	WorkspaceDir string
	// RepoFullName is "owner/name" used for logging.
	RepoFullName string
}

// Result is the outcome of a review run.
type Result struct {
	Review *core.StructuredReview
	Raw    string
}

// NewRunner creates a Runner. angles defaults to DefaultAngles when nil.
// tools is the read-only tool builder; when nil it defaults to no tools.
func NewRunner(model llms.Model, promptMgr *llmpkg.PromptManager, tools ToolBuilder, logger *slog.Logger, angles []Angle) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	if angles == nil {
		angles = DefaultAngles
	}
	return &Runner{
		llm:       model,
		promptMgr: promptMgr,
		tools:     tools,
		angles:    angles,
		logger:    logger,
	}
}

// Run clones the repo, dispatches the angle passes in parallel (quorum 0.75,
// per-angle timeout 5m), merges + dedups findings, and returns a structured review.
func (r *Runner) Run(ctx context.Context, params Params) (*Result, error) {
	if params.Diff == "" {
		return &Result{Review: &core.StructuredReview{
			Summary:     "This pull request contains no code changes. Looks good to me!",
			Suggestions: []core.Suggestion{},
		}, Raw: "No code changes."}, nil
	}

	// 1. Determine the workspace to investigate: use the provided local checkout
	// if given, otherwise clone the repo to an isolated workspace.
	var workspace string
	var cleanup func()
	if params.WorkspaceDir != "" {
		workspace = params.WorkspaceDir
	} else {
		cloner := gitutil.NewCloner(r.logger)
		var err error
		workspace, cleanup, err = cloner.Clone(ctx, params.RepoURL)
		if err != nil {
			return nil, fmt.Errorf("agent review: clone failed: %w", err)
		}
		defer cleanup()
	}

	// Build the shared task context once (diff + changed files).
	taskCtx := r.buildTaskContext(params)
	diffFilter := newDiffFilter(params.ChangedFiles)

	chain := chains.NewMapReduceChain(
		func(ctx context.Context, angle Angle) ([]core.Suggestion, error) {
			return r.runAngle(ctx, angle, workspace, taskCtx)
		},
		func(_ context.Context, results [][]core.Suggestion) (*core.StructuredReview, error) {
			// Flatten, dedup, and filter to the diff hunks.
			var suggestions []core.Suggestion
			for _, group := range results {
				suggestions = append(suggestions, group...)
			}
			suggestions = dedupAndFilter(suggestions, diffFilter)
			return r.buildReview(suggestions), nil
		},
		chains.WithMaxConcurrency[Angle, []core.Suggestion, *core.StructuredReview](len(r.angles)),
		chains.WithMapTimeout[Angle, []core.Suggestion, *core.StructuredReview](5*time.Minute),
		chains.WithQuorum[Angle, []core.Suggestion, *core.StructuredReview](0.75),
	)

	review, err := chain.Call(ctx, r.angles)
	if err != nil {
		return nil, fmt.Errorf("agent review: failed: %w", err)
	}

	return &Result{Review: review}, nil
}

// runAngle runs a single agent pass for one angle and returns its findings.
func (r *Runner) runAngle(ctx context.Context, angle Angle, workspace, taskCtx string) ([]core.Suggestion, error) {
	r.logger.Info("agent review: starting angle", "angle", angle.Name, "workspace", workspace)

	systemPrompt, err := r.promptMgr.Raw(angle.PromptKey)
	if err != nil {
		return nil, fmt.Errorf("agent review angle %s: %w", angle.Name, err)
	}

	registry := goframeagent.NewRegistry()
	if r.tools != nil {
		for _, t := range r.tools(workspace) {
			if err := registry.Register(t); err != nil {
				r.logger.Warn("agent review: failed to register tool",
					"angle", angle.Name, "tool", t.Name(), "error", err)
			}
		}
	}

	loop, err := goframeagent.NewAgentLoop(r.llm, registry,
		goframeagent.WithLoopSystemPrompt(systemPrompt),
		goframeagent.WithLoopMaxIterations(10),
	)
	if err != nil {
		return nil, fmt.Errorf("agent review angle %s: new loop: %w", angle.Name, err)
	}

	task := goframeagent.Task{
		ID:          angle.Name,
		Description: fmt.Sprintf("Review the PR for %s", angle.Description),
		Context:     taskCtx,
	}

	result, err := loop.Run(ctx, task, nil)
	if err != nil {
		return nil, fmt.Errorf("agent review angle %s: loop failed: %w", angle.Name, err)
	}

	parser := NewStructuredReviewParser(r.logger)
	review, parseErr := parser.Parse(ctx, result.Response)
	if parseErr != nil {
		r.logger.Warn("agent review: angle returned unparseable review", "angle", angle.Name, "error", parseErr)
		return nil, nil
	}
	return review.Suggestions, nil
}

// buildTaskContext renders the diff and changed-file list for the agent task.
func (r *Runner) buildTaskContext(params Params) string {
	var b strings.Builder
	b.WriteString("Repository: ")
	b.WriteString(params.RepoFullName)
	b.WriteString("\n\nChanged files:\n")
	for _, cf := range params.ChangedFiles {
		b.WriteString("- " + cf.Filename + "\n")
	}
	b.WriteString("\n--- DIFF ---\n")
	b.WriteString(params.Diff)
	return b.String()
}

// dedupAndFilter deduplicates findings by file:line and drops those outside the
// diff hunks.
func dedupAndFilter(suggestions []core.Suggestion, filter *diffFilter) []core.Suggestion {
	// Dedup first.
	deduped := dedupSuggestions(suggestions)
	out := make([]core.Suggestion, 0, len(deduped))
	for _, s := range deduped {
		if filter.Allow(s) {
			out = append(out, s)
		}
	}
	return out
}

// dedupSuggestions merges duplicates by file:line keeping the highest severity.
func dedupSuggestions(suggestions []core.Suggestion) []core.Suggestion {
	seen := make(map[string]int)
	out := make([]core.Suggestion, 0, len(suggestions))
	for _, s := range suggestions {
		key := findingKey(s)
		if idx, ok := seen[key]; ok {
			if rank(out[idx].Severity) < rank(s.Severity) {
				out[idx] = s
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, s)
	}
	return out
}

// buildReview assembles a StructuredReview from the filtered suggestions.
func (r *Runner) buildReview(suggestions []core.Suggestion) *core.StructuredReview {
	verdict := core.VerdictApprove
	for _, s := range suggestions {
		if rank(s.Severity) >= severityRank["high"] {
			verdict = core.VerdictRequestChanges
			break
		}
	}

	summary := fmt.Sprintf("Agent review completed with %d finding(s) across %d angle(s).",
		len(suggestions), len(r.angles))

	return &core.StructuredReview{
		Summary:     summary,
		Verdict:     verdict,
		Suggestions: suggestions,
	}
}
