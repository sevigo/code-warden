package review

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/gitutil"

	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
)

const (
	// DefaultAngleTimeout is the maximum duration of one review angle.
	DefaultAngleTimeout = 3 * time.Minute
	// DefaultMaxIterations is the agent-loop iteration budget for one angle.
	DefaultMaxIterations = 8
	// DefaultContextWindow is the assumed model context capacity in tokens.
	DefaultContextWindow = 128000
)

// Runner executes the multi-angle agent-based code review.
type Runner struct {
	executor AngleExecutor
	angles   []Angle
	logger   *slog.Logger
}

// Params holds the inputs for a single review run.
type Params struct {
	// Diff is the unified diff of the PR being reviewed.
	Diff string
	// ChangedFiles are the per-file patches of the PR.
	ChangedFiles []core.ChangedFile
	// RepoURL is the git URL (credentials embedded) to clone for investigation.
	// Ignored when WorkspaceDir is set.
	RepoURL string
	// WorkspaceDir is an existing checkout to investigate. When set, the runner
	// uses it directly instead of cloning RepoURL (used by /implement's local
	// workspace, which is already at the modified state).
	WorkspaceDir string
	// RepoFullName is "owner/name" used for logging.
	RepoFullName string
	// CommitMessages are the commit messages for the PR/branch being reviewed.
	// They give the agent context on the intent of the changes.
	CommitMessages []string
	// Timeout is the per-angle timeout. When zero, defaults to 3 minutes.
	// Local CPU models (e.g. gemma on Ollama) may need longer.
	Timeout time.Duration
	// MaxIterations is the per-angle agent-loop iteration cap. When zero,
	// defaults to 8. A review angle needs to read the diff, grep for symbols,
	// read surrounding code, then emit a <review>.
	MaxIterations int
	// ContextWindow is the model's maximum context size in tokens. When zero,
	// defaults to 128000 (modern models: Claude 200K, GPT-4o 128K, Gemini 1M+,
	// local models like gemma3/llama3 128K). The compaction hook triggers when
	// input tokens reach 60% of this value, preserving headroom for the
	// model's response. Override with --context-window for unusual models.
	ContextWindow int
	// Config controls noise filtering: minimum severity, ignored paths,
	// category toggles, and file limits. When nil, DefaultConfig is used.
	// Pass an empty Config{} to disable all filtering.
	Config *Config
}

// Result is the outcome of a review run.
type Result struct {
	Review *core.StructuredReview
	Raw    string
	Angles []AngleResult
}

// NewRunner creates a Runner using executor for individual agent passes.
// angles defaults to DefaultAngles when nil.
func NewRunner(executor AngleExecutor, logger *slog.Logger, angles []Angle) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	if angles == nil {
		angles = DefaultAngles
	}
	return &Runner{
		executor: executor,
		angles:   angles,
		logger:   logger,
	}
}

// Run clones the repo, dispatches all selected angle passes in parallel,
// merges and deduplicates their findings, and returns a structured review.
func (r *Runner) Run(ctx context.Context, params Params) (*Result, error) {
	if r.executor == nil {
		return nil, fmt.Errorf("agent review: angle executor is required")
	}
	if params.Diff == "" {
		return &Result{Review: &core.StructuredReview{
			Summary:     "This pull request contains no code changes. Looks good to me!",
			Suggestions: []core.Suggestion{},
		}, Raw: "No code changes."}, nil
	}

	// Resolve review config — nil means defaults, empty struct means no filtering.
	rc := params.Config
	if rc == nil {
		defaults := DefaultConfig()
		rc = &defaults
	}

	// Filter changed files and check skip conditions before spending tokens.
	filteredFiles, filteredDiff := r.prepareFiles(params, rc)
	if filteredDiff == "" {
		return &Result{Review: &core.StructuredReview{
			Summary:     "Review skipped: all changed files match ignore patterns",
			Suggestions: []core.Suggestion{},
		}, Raw: "all files ignored"}, nil
	}

	// Clone or use the provided workspace.
	workspace, cleanup, err := r.prepareWorkspace(ctx, params)
	if err != nil {
		return nil, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	return r.dispatch(ctx, params, rc, filteredFiles, filteredDiff, workspace)
}

// prepareFiles filters changed files by ignore patterns and checks skip
// conditions. Returns the filtered files and the filtered diff. When the
// review should be skipped, filteredDiff is empty.
func (r *Runner) prepareFiles(params Params, rc *Config) ([]core.ChangedFile, string) {
	filteredFiles, ignoredCount := rc.FilterChangedFiles(params.ChangedFiles)
	if ignoredCount > 0 {
		r.logger.Info("agent review: ignored files",
			"ignored", ignoredCount,
			"remaining", len(filteredFiles),
		)
	}
	if skip, _ := rc.ShouldSkipReview(params.ChangedFiles); skip {
		return nil, ""
	}
	var filteredDiff string
	if ignoredCount > 0 {
		filteredDiff = rebuildDiff(params.ChangedFiles, filteredFiles)
	} else {
		filteredDiff = params.Diff
	}
	return filteredFiles, filteredDiff
}

// prepareWorkspace resolves the workspace to investigate: use the provided
// local checkout if given, otherwise clone the repo to an isolated workspace.
func (r *Runner) prepareWorkspace(ctx context.Context, params Params) (string, func(), error) {
	if params.WorkspaceDir != "" {
		return params.WorkspaceDir, nil, nil
	}
	cloner := gitutil.NewCloner(r.logger)
	workspace, cleanup, err := cloner.Clone(ctx, params.RepoURL)
	if err != nil {
		return "", nil, fmt.Errorf("agent review: clone failed: %w", err)
	}
	return workspace, cleanup, nil
}

// dispatch builds the task context, filters angles, and runs the map-reduce chain.
func (r *Runner) dispatch(ctx context.Context, params Params, rc *Config, filteredFiles []core.ChangedFile, filteredDiff, workspace string) (*Result, error) {
	fileLines := internalgithub.BuildValidLineMap(filteredFiles)
	changedLinesSummary := buildChangedLinesSummary(fileLines)
	filenameIndex := buildFilenameIndex(fileLines)
	taskCtx := r.buildTaskContext(params, filteredDiff, changedLinesSummary, filteredFiles)
	diffFilter := newDiffFilter(filteredFiles)

	// Filter angles: category toggles + file-relevance scoping.
	categorizedAngles := rc.FilterAngles(r.angles)
	scopedAngles := scopeAngles(categorizedAngles, filteredFiles)
	if len(scopedAngles) < len(r.angles) {
		r.logger.Info("agent review: scoped angles",
			"total", len(r.angles),
			"scoped", len(scopedAngles),
		)
	}

	timeout := params.Timeout
	if timeout <= 0 {
		timeout = DefaultAngleTimeout
	}
	maxIter := params.MaxIterations
	if maxIter <= 0 {
		maxIter = DefaultMaxIterations
	}
	contextWindow := params.ContextWindow
	if contextWindow <= 0 {
		contextWindow = DefaultContextWindow
	}

	chain := chains.NewMapReduceChain(
		func(ctx context.Context, angle Angle) (AngleResult, error) {
			return r.executor.Execute(ctx, AngleRequest{
				Angle:         angle,
				Workspace:     workspace,
				TaskContext:   taskCtx,
				Diff:          filteredDiff,
				ChangedLines:  changedLinesSummary,
				FilenameIndex: filenameIndex,
				MaxIterations: maxIter,
				ContextWindow: contextWindow,
			})
		},
		func(_ context.Context, results []AngleResult) (*Result, error) {
			r.logger.Info("agent review: reduce phase",
				"angles_dispatched", len(scopedAngles),
				"angles_collected", len(results),
			)
			var suggestions []core.Suggestion
			for _, result := range results {
				suggestions = append(suggestions, result.Suggestions...)
			}
			suggestions = dedupAndFilter(suggestions, diffFilter, r.logger)
			suggestions = rc.FilterBySeverity(suggestions)
			return &Result{Review: r.buildReview(suggestions), Angles: results}, nil
		},
		chains.WithMaxConcurrency[Angle, AngleResult, *Result](len(scopedAngles)),
		chains.WithMapTimeout[Angle, AngleResult, *Result](timeout),
		// Quorum 1.0 = wait for all angles. With 0.5, the slowest angle
		// (often bug, the most important) gets cut off when 3 of 4 finish.
		chains.WithQuorum[Angle, AngleResult, *Result](1.0),
	)

	result, err := chain.Call(ctx, scopedAngles)
	if err != nil {
		return nil, fmt.Errorf("agent review: failed: %w", err)
	}
	raw, err := MarshalStructuredReview(result.Review)
	if err != nil {
		return nil, err
	}
	result.Raw = raw

	r.logger.Info("agent review: complete",
		"repo", params.RepoFullName,
		"angles", len(r.angles),
		"findings", len(result.Review.Suggestions),
		"verdict", result.Review.Verdict,
	)

	return result, nil
}

// buildTaskContext renders the diff, changed-file list, changed-line numbers,
// and commit messages for the agent task. The changed-lines section gives the
// agent the exact valid <line> values up front so it doesn't guess from @@ headers.
func (r *Runner) buildTaskContext(params Params, diff, changedLinesSummary string, changedFiles []core.ChangedFile) string {
	var b strings.Builder
	b.WriteString("Repository: ")
	b.WriteString(params.RepoFullName)
	b.WriteString("\n\nChanged files:\n")
	for _, cf := range changedFiles {
		b.WriteString("- " + cf.Filename + "\n")
	}
	if changedLinesSummary != "" {
		b.WriteString("\nChanged line numbers (use these for <line> tags):\n")
		b.WriteString(changedLinesSummary)
		b.WriteString("\n")
	}
	if len(params.CommitMessages) > 0 {
		b.WriteString("\nCommit messages:\n")
		for _, m := range params.CommitMessages {
			b.WriteString("- " + m + "\n")
		}
	}
	b.WriteString("\n--- DIFF ---\n")
	b.WriteString(diff)
	b.WriteString("\n\nTip: Call get_diff to re-fetch this diff and the changed-line list at any time.")
	return b.String()
}

// rebuildDiff reconstructs a unified diff containing only the files that
// passed the ignore filter. Each changed file's patch is concatenated with
// the standard diff --git header.
func rebuildDiff(allFiles, keptFiles []core.ChangedFile) string {
	keptSet := make(map[string]bool, len(keptFiles))
	for _, f := range keptFiles {
		keptSet[f.Filename] = true
	}
	var b strings.Builder
	for _, f := range allFiles {
		if !keptSet[f.Filename] {
			continue
		}
		b.WriteString(f.Patch)
	}
	return b.String()
}

// dedupAndFilter deduplicates findings by file:line, snaps off-by-a-few-lines
// findings to the nearest diff hunk, and drops those that can't be snapped.
// Filtered findings are logged so "0 findings" is debuggable.
func dedupAndFilter(suggestions []core.Suggestion, filter *diffFilter, logger *slog.Logger) []core.Suggestion {
	// Dedup first.
	deduped := Deduplicate([]*core.StructuredReview{{Suggestions: suggestions}})
	out := make([]core.Suggestion, 0, len(deduped))
	for _, s := range deduped {
		snapped := filter.Snap(s)
		if snapped != nil {
			out = append(out, *snapped)
			continue
		}
		logger.Warn("agent review: filtered finding outside diff hunks",
			"file", s.FilePath,
			"line", s.LineNumber,
			"severity", s.Severity,
			"category", s.Category,
		)
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

	summary := buildSummary(suggestions, len(r.angles))

	return &core.StructuredReview{
		Summary:     summary,
		Verdict:     verdict,
		Suggestions: suggestions,
	}
}

// buildSummary renders a concise summary of the findings.
func buildSummary(suggestions []core.Suggestion, angleCount int) string {
	if len(suggestions) == 0 {
		return fmt.Sprintf("No issues found across %d review angle(s).", angleCount)
	}
	// Count by severity.
	var crit, high, med, low int
	for _, s := range suggestions {
		switch strings.ToLower(s.Severity) {
		case "critical":
			crit++
		case "high":
			high++
		case "medium":
			med++
		case "low":
			low++
		}
	}
	var parts []string
	if crit > 0 {
		parts = append(parts, fmt.Sprintf("%d critical", crit))
	}
	if high > 0 {
		parts = append(parts, fmt.Sprintf("%d high", high))
	}
	if med > 0 {
		parts = append(parts, fmt.Sprintf("%d medium", med))
	}
	if low > 0 {
		parts = append(parts, fmt.Sprintf("%d low", low))
	}
	return fmt.Sprintf("%d finding(s): %s — across %d angle(s).",
		len(suggestions), strings.Join(parts, ", "), angleCount)
}
