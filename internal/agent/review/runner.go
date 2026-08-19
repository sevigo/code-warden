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

	// Build the shared task context once (diff + changed files + changed-line map).
	fileLines := internalgithub.BuildValidLineMap(params.ChangedFiles)
	changedLinesSummary := buildChangedLinesSummary(fileLines)
	filenameIndex := buildFilenameIndex(fileLines)
	taskCtx := r.buildTaskContext(params, changedLinesSummary)
	diffFilter := newDiffFilter(params.ChangedFiles)

	// Scope angles to those relevant for the changed files. This avoids
	// spending tokens on angles with no applicable findings (e.g. security
	// angle on a doc-only PR).
	scopedAngles := scopeAngles(r.angles, params.ChangedFiles)
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
			return r.runAngle(ctx, angle, workspace, taskCtx, params.Diff, changedLinesSummary, filenameIndex, maxIter, contextWindow)
		},
		func(_ context.Context, results [][]core.Suggestion) (*core.StructuredReview, error) {
			r.logger.Info("agent review: reduce phase",
				"angles_dispatched", len(scopedAngles),
				"angles_collected", len(results),
			)
			// Flatten, dedup, and snap/filter to the diff hunks.
			var suggestions []core.Suggestion
			for _, group := range results {
				suggestions = append(suggestions, group...)
			}
			suggestions = dedupAndFilter(suggestions, diffFilter, r.logger)
			return r.buildReview(suggestions), nil
		},
		chains.WithMaxConcurrency[Angle, []core.Suggestion, *core.StructuredReview](len(scopedAngles)),
		chains.WithMapTimeout[Angle, []core.Suggestion, *core.StructuredReview](timeout),
		chains.WithQuorum[Angle, []core.Suggestion, *core.StructuredReview](0.5),
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
func (r *Runner) buildTaskContext(params Params, changedLinesSummary string) string {
	var b strings.Builder
	b.WriteString("Repository: ")
	b.WriteString(params.RepoFullName)
	b.WriteString("\n\nChanged files:\n")
	for _, cf := range params.ChangedFiles {
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
	b.WriteString(params.Diff)
	b.WriteString("\n\nTip: Call get_diff to re-fetch this diff and the changed-line list at any time.")
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

	summary := fmt.Sprintf("Agent review completed with %d finding(s) across %d angle(s).",
		len(suggestions), len(r.angles))

	return &core.StructuredReview{
		Summary:     summary,
		Verdict:     verdict,
		Suggestions: suggestions,
	}
}
