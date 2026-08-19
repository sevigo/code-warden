package review

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/chains"
	"github.com/sevigo/goframe/gitutil"
	"github.com/sevigo/goframe/llms"
	"github.com/sevigo/goframe/schema"

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

// Run clones the repo, dispatches the angle passes in parallel (quorum 0.5,
// per-angle timeout 3m), merges + dedups findings, and returns a structured review.
func (r *Runner) Run(ctx context.Context, params Params) (*Result, error) {
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
func (r *Runner) prepareFiles(params Params, rc *Config) ([]internalgithub.ChangedFile, string) {
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
func (r *Runner) dispatch(ctx context.Context, params Params, rc *Config, filteredFiles []internalgithub.ChangedFile, filteredDiff, workspace string) (*Result, error) {
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
		timeout = 3 * time.Minute
	}
	maxIter := params.MaxIterations
	if maxIter <= 0 {
		maxIter = 8
	}
	contextWindow := params.ContextWindow
	if contextWindow <= 0 {
		contextWindow = 128000
	}

	chain := chains.NewMapReduceChain(
		func(ctx context.Context, angle Angle) ([]core.Suggestion, error) {
			return r.runAngle(ctx, angle, workspace, taskCtx, filteredDiff, changedLinesSummary, filenameIndex, maxIter, contextWindow)
		},
		func(_ context.Context, results [][]core.Suggestion) (*core.StructuredReview, error) {
			r.logger.Info("agent review: reduce phase",
				"angles_dispatched", len(scopedAngles),
				"angles_collected", len(results),
			)
			var suggestions []core.Suggestion
			for _, group := range results {
				suggestions = append(suggestions, group...)
			}
			suggestions = dedupAndFilter(suggestions, diffFilter, r.logger)
			suggestions = rc.FilterBySeverity(suggestions)
			return r.buildReview(suggestions), nil
		},
		chains.WithMaxConcurrency[Angle, []core.Suggestion, *core.StructuredReview](len(scopedAngles)),
		chains.WithMapTimeout[Angle, []core.Suggestion, *core.StructuredReview](timeout),
		// Quorum 1.0 = wait for all angles. With 0.5, the slowest angle
		// (often bug, the most important) gets cut off when 3 of 4 finish.
		chains.WithQuorum[Angle, []core.Suggestion, *core.StructuredReview](1.0),
	)

	review, err := chain.Call(ctx, scopedAngles)
	if err != nil {
		return nil, fmt.Errorf("agent review: failed: %w", err)
	}

	r.logger.Info("agent review: complete",
		"repo", params.RepoFullName,
		"angles", len(r.angles),
		"findings", len(review.Suggestions),
		"verdict", review.Verdict,
	)

	return &Result{Review: review}, nil
}

// runAngle runs a single agent pass for one angle and returns its findings.
func (r *Runner) runAngle(ctx context.Context, angle Angle, workspace, taskCtx, diff, changedLines, filenameIndex string, maxIter, contextWindow int) ([]core.Suggestion, error) {
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
	// Register the get_diff tool so the agent can re-anchor on the diff at any
	// time. This prevents the "wandering" failure mode where the diff scrolls
	// out of context and the agent starts exploring the whole repo.
	getDiff := newGetDiffTool(diff, changedLines, filenameIndex)
	if err := registry.Register(getDiff); err != nil {
		r.logger.Warn("agent review: failed to register get_diff tool",
			"angle", angle.Name, "error", err)
	}

	loop, err := goframeagent.NewAgentLoop(r.llm, registry,
		goframeagent.WithLoopSystemPrompt(systemPrompt),
		goframeagent.WithLoopMaxIterations(maxIter),
		goframeagent.WithLoopObserver(newReviewObserver(r.logger, angle.Name)),
		goframeagent.WithLoopCompactionHook(r.compactionHook(angle.Name, contextWindow)),
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
	if err != nil && !errors.Is(err, goframeagent.ErrMaxIterations) {
		return nil, fmt.Errorf("agent review angle %s: loop failed: %w", angle.Name, err)
	}
	if err != nil {
		r.logger.Warn("agent review: angle hit iteration cap, parsing partial response",
			"angle", angle.Name, "error", err)
	}

	if result != nil && result.Response != "" {
		r.logger.Info("agent review: angle raw response",
			"angle", angle.Name,
			"response", result.Response,
		)
	}

	parser := NewStructuredReviewParser(r.logger)
	review, parseErr := parser.Parse(ctx, result.Response)
	if parseErr != nil {
		r.logger.Warn("agent review: angle returned unparseable review", "angle", angle.Name, "error", parseErr)
		return nil, nil
	}
	return review.Suggestions, nil
}

// buildTaskContext renders the diff, changed-file list, changed-line numbers,
// and commit messages for the agent task. The changed-lines section gives the
// agent the exact valid <line> values up front so it doesn't guess from @@ headers.
func (r *Runner) buildTaskContext(params Params, diff, changedLinesSummary string, changedFiles []internalgithub.ChangedFile) string {
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
func rebuildDiff(allFiles, keptFiles []internalgithub.ChangedFile) string {
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

// compactionHook returns a hook that compacts the conversation history when the
// cumulative input tokens exceed a threshold. It preserves the system prompt,
// the initial task (diff + changed lines), and the last two tool results, and
// inserts a short reminder so the agent knows history was summarized. This
// prevents the unbounded context growth that otherwise leads to 1M+ token
// reviews where the model spends its budget re-reading its own history.
func (r *Runner) compactionHook(angle string, contextWindow int) func(ctx context.Context, msgs []schema.MessageContent, tokens goframeagent.TokenUsage) []schema.MessageContent {
	// Compact at 60% of the context window to leave headroom for the model's
	// response and system prompt.
	compactAtTokens := int(float64(contextWindow) * 0.6)
	return func(_ context.Context, msgs []schema.MessageContent, tokens goframeagent.TokenUsage) []schema.MessageContent {
		if int(tokens.Input) < compactAtTokens {
			return nil
		}
		r.logger.Info("agent review: compacting context",
			"angle", angle,
			"tokens_in", int(tokens.Input),
			"context_window", contextWindow,
			"threshold", compactAtTokens,
			"messages_before", len(msgs),
		)
		compacted := make([]schema.MessageContent, 0, 6)
		// Keep the system prompt (first message).
		for _, m := range msgs {
			if m.Role == schema.ChatMessageTypeSystem {
				compacted = append(compacted, m)
				break
			}
		}
		// Keep the initial user task message (diff + context).
		for _, m := range msgs {
			if m.Role == schema.ChatMessageTypeHuman {
				compacted = append(compacted, m)
				break
			}
		}
		// Append a reminder that older tool results were dropped.
		compacted = append(compacted, schema.NewHumanMessage(
			"[Context compacted: earlier tool results were summarized to save space. "+
				"Call get_diff to re-fetch the diff if needed. Emit your <review> now if you have enough evidence.]",
		))
		// Keep the last two tool results so the agent has its most recent findings.
		var tail []schema.MessageContent
		for i := len(msgs) - 1; i >= 0 && len(tail) < 2; i-- {
			if msgs[i].Role == schema.ChatMessageTypeTool {
				tail = append(tail, msgs[i])
			}
		}
		// Reverse tail back to chronological order.
		for i := len(tail) - 1; i >= 0; i-- {
			compacted = append(compacted, tail[i])
		}
		return compacted
	}
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
