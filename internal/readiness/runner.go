package readiness

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	goframeagent "github.com/sevigo/goframe/agent"
	"github.com/sevigo/goframe/gitutil"
	"github.com/sevigo/goframe/llms"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/llm"
)

// ToolBuilder builds the toolchain of read-only investigation tools for a workspace.
type ToolBuilder func(workspaceRoot string) []goframeagent.Tool

// Runner runs the operational readiness review for a change.
type Runner struct {
	model     llms.Model
	promptMgr *llm.PromptManager
	tools     ToolBuilder
	detector  Detector
	logger    *slog.Logger
}

// NewRunner creates a readiness Runner.
func NewRunner(model llms.Model, promptMgr *llm.PromptManager, tools ToolBuilder, detector Detector, logger *slog.Logger) *Runner {
	if logger == nil {
		logger = slog.Default()
	}
	if detector == nil {
		detector = NewDetector()
	}
	return &Runner{model: model, promptMgr: promptMgr, tools: tools, detector: detector, logger: logger}
}

// Input is the provider-neutral data needed to run a readiness review, mirroring
// the existing review input.
type Input struct {
	Diff           string
	ChangedFiles   []core.ChangedFile
	WorkspaceDir   string
	CloneURL       string
	RepoFullName   string
	CommitMessages []string
}

// Result is the outcome of a readiness run.
type Result struct {
	Review   *core.StructuredReview
	Raw      string
	Detected []Detection
	Partial  bool
}

// Run detects applicable readiness categories and investigates each with a
// focused agent pass, then merges findings into one structured review. A partial
// run is flagged so a clean result is never mistaken for a full pass.
func (r *Runner) Run(ctx context.Context, input Input, cfg Config) (*Result, error) {
	if !cfg.Enabled {
		return &Result{Review: &core.StructuredReview{
			Summary:     "Operational readiness review is disabled by repository configuration.",
			Suggestions: []core.Suggestion{},
		}}, nil
	}

	detected := r.filterDetections(cfg, r.detector.Detect(input.ChangedFiles))
	if len(detected) == 0 {
		return &Result{Review: &core.StructuredReview{
			Summary:     "No production-facing change categories detected.",
			Suggestions: []core.Suggestion{},
		}, Detected: detected}, nil
	}

	workspace, cleanup, err := r.prepareWorkspace(ctx, input)
	if err != nil {
		return nil, err
	}
	if cleanup != nil {
		defer cleanup()
	}

	allSuggestions := []core.Suggestion{}
	partial := false
	completedCats := []Category{}

	for _, det := range detected {
		catResult, err := r.runCategory(ctx, input, workspace, det, cfg)
		if err != nil {
			r.logger.Error("readiness category failed", "category", det.Category, "error", err)
			partial = true
			continue
		}
		allSuggestions = append(allSuggestions, catResult.suggestions...)
		completedCats = append(completedCats, det.Category)
		if catResult.partial {
			partial = true
		}
	}

	allSuggestions = dedupe(allSuggestions)

	summary := buildSummary(allSuggestions, len(detected), len(completedCats))
	if partial {
		summary = "[PARTIAL] " + summary
	}

	review := &core.StructuredReview{
		Summary:     summary,
		Verdict:     verdictFor(allSuggestions),
		Suggestions: allSuggestions,
	}

	raw, err := agentreview.MarshalStructuredReview(review)
	if err != nil {
		return nil, fmt.Errorf("marshal readiness review: %w", err)
	}

	return &Result{Review: review, Raw: raw, Detected: detected, Partial: partial}, nil
}

// category is a single applicable readiness category with its evidence.
type category = Detection

// categoryRun is the outcome of one category agent pass.
type categoryRun struct {
	suggestions []core.Suggestion
	partial     bool
}

// runCategory runs one focused agent pass for a single readiness category.
func (r *Runner) runCategory(ctx context.Context, input Input, workspace string, det category, cfg Config) (*categoryRun, error) {
	systemPrompt, err := r.renderPrompt(cfg, det.Category)
	if err != nil {
		return nil, err
	}

	registry := goframeagent.NewRegistry()
	if r.tools != nil {
		for _, tool := range r.tools(workspace) {
			if err := registry.Register(tool); err != nil {
				r.logger.Warn("readiness: failed to register tool", "category", det.Category, "tool", tool.Name(), "error", err)
			}
		}
	}
	fileLines := buildLineMap(input.ChangedFiles)
	if err := registry.Register(newDiffTool(input.Diff, fileLines)); err != nil {
		return nil, err
	}

	loop, err := goframeagent.NewAgentLoop(r.model, registry,
		goframeagent.WithLoopSystemPrompt(systemPrompt),
		goframeagent.WithLoopMaxIterations(8),
	)
	if err != nil {
		return nil, err
	}

	task := goframeagent.Task{
		ID:          string(det.Category),
		Description: fmt.Sprintf("Review the PR for %s operational readiness", det.Category),
		Context:     r.buildTaskContext(input, det),
	}

	loopResult, runErr := loop.Run(ctx, task, nil)
	if runErr != nil && !errors.Is(runErr, goframeagent.ErrMaxIterations) {
		return nil, runErr
	}
	partial := runErr != nil
	if loopResult == nil || strings.TrimSpace(loopResult.Response) == "" {
		return nil, fmt.Errorf("readiness category %s: agent returned no response", det.Category)
	}

	parser := agentreview.NewStructuredReviewParser(r.logger)
	review, err := parser.Parse(ctx, loopResult.Response)
	if err != nil {
		return nil, fmt.Errorf("readiness category %s: %w", det.Category, err)
	}
	if review == nil {
		return nil, fmt.Errorf("readiness category %s: parser returned no review", det.Category)
	}
	return &categoryRun{suggestions: review.Suggestions, partial: partial}, nil
}

// buildTaskContext renders the changed-files, diff, and detection evidence for
// one category agent task.
func (r *Runner) buildTaskContext(input Input, det category) string {
	var b strings.Builder
	b.WriteString("Repository: ")
	b.WriteString(input.RepoFullName)
	b.WriteString("\n\nDetected category: ")
	b.WriteString(string(det.Category))
	b.WriteString("\n\nChanged files:\n")
	for _, cf := range input.ChangedFiles {
		b.WriteString("- " + cf.Filename + "\n")
	}
	b.WriteString("\nDetection evidence:\n")
	for _, ev := range det.Evidence {
		fmt.Fprintf(&b, "- %s: %s\n", ev.File, ev.Reason)
	}
	if len(input.CommitMessages) > 0 {
		b.WriteString("\nCommit messages:\n")
		for _, m := range input.CommitMessages {
			b.WriteString("- " + m + "\n")
		}
	}
	b.WriteString("\n--- DIFF ---\n")
	b.WriteString(input.Diff)
	b.WriteString("\n\nTip: Call get_diff to re-fetch this diff and the changed-line list at any time.")
	return b.String()
}

// renderPrompt renders the readiness prompt for a category with repo config.
func (r *Runner) renderPrompt(cfg Config, cat Category) (string, error) {
	return r.promptMgr.Render(llm.ReadinessPrompt, map[string]any{
		"Category":     cat,
		"Context":      renderContext(cfg.Context),
		"Instructions": strings.Join(cfg.Instructions, "\n"),
	})
}

// prepareWorkspace resolves the workspace to investigate, cloning when needed.
func (r *Runner) prepareWorkspace(ctx context.Context, input Input) (string, func(), error) {
	if input.WorkspaceDir != "" {
		return input.WorkspaceDir, nil, nil
	}
	if input.CloneURL == "" {
		return "", nil, fmt.Errorf("readiness: no workspace and no clone URL")
	}
	cloner := gitutil.NewCloner(r.logger)
	workspace, cleanup, err := cloner.Clone(ctx, input.CloneURL)
	if err != nil {
		return "", nil, fmt.Errorf("readiness: clone failed: %w", err)
	}
	return workspace, cleanup, nil
}

// filterDetections drops categories disabled in the config.
func (r *Runner) filterDetections(cfg Config, dets []Detection) []Detection {
	out := make([]Detection, 0, len(dets))
	for _, d := range dets {
		if enabled, ok := cfg.EnabledCats[d.Category]; ok && enabled {
			out = append(out, d)
		}
	}
	return out
}

// buildLineMap computes valid new-side line numbers per changed file from its patch.
func buildLineMap(files []core.ChangedFile) map[string]map[int]struct{} {
	out := make(map[string]map[int]struct{}, len(files))
	for _, f := range files {
		out[f.Filename] = parseValidLines(f.Patch)
	}
	return out
}

var hunkHeaderRe = regexp.MustCompile(`@@ -[0-9]+(,[0-9]+)? \+([0-9]+)(,[0-9]+)? @@`)

// parseValidLines extracts new-side line numbers from a unified patch.
func parseValidLines(patch string) map[int]struct{} {
	lines := make(map[int]struct{})
	cur := 0
	for _, raw := range strings.Split(patch, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if m := hunkHeaderRe.FindStringSubmatch(line); m != nil {
			cur, _ = strconv.Atoi(m[2])
			continue
		}
		if cur == 0 {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			lines[cur] = struct{}{}
			cur++
		case strings.HasPrefix(line, "-"):
		default:
			cur++
		}
	}
	return lines
}

// dedupe removes duplicate suggestions by file:line + rule.
func dedupe(suggestions []core.Suggestion) []core.Suggestion {
	seen := map[string]struct{}{}
	out := make([]core.Suggestion, 0, len(suggestions))
	for _, s := range suggestions {
		key := fmt.Sprintf("%s:%d:%s", s.FilePath, s.LineNumber, s.RuleID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
	}
	return out
}

// buildSummary summarizes the readiness findings by severity.
func buildSummary(suggestions []core.Suggestion, detected, completed int) string {
	if len(suggestions) == 0 {
		return fmt.Sprintf("Operational readiness: no findings across %d detected category/categories (%d fully investigated).",
			detected, completed)
	}
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
	parts := []string{}
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
	return fmt.Sprintf("Operational readiness: %d finding(s) (%s) across %d candidate(s).",
		len(suggestions), strings.Join(parts, ", "), detected)
}

// verdictFor derives a programmatic verdict from the readiness findings.
func verdictFor(suggestions []core.Suggestion) string {
	for _, s := range suggestions {
		switch strings.ToLower(s.Severity) {
		case "critical", "high":
			return core.VerdictRequestChanges
		}
	}
	return core.VerdictApprove
}

func renderContext(m map[string]string) string {
	var b strings.Builder
	for k, v := range m {
		fmt.Fprintf(&b, "%s: %s\n", k, v)
	}
	return strings.TrimSpace(b.String())
}
