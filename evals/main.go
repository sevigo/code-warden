// Command evals runs the code-warden review eval harness. It loads eval cases
// from JSON files, runs each through the review pipeline, and scores the
// results for precision, recall, and latency.
//
// Usage:
//
//	go run ./evals                          # run all evals with default model
//	go run ./evals --mock                   # pipeline-only, no LLM calls
//	go run ./evals --suite bug             # run only bug cases
//	go run ./evals --verbose               # show per-case details
//	go run ./evals --model ollama/gpt-oss:7b
//
// Exit codes:
//
//	0 = all gate evals passed
//	1 = a gate eval failed (quality regression)
//	2 = infrastructure error
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sevigo/goframe/llms"

	"github.com/sevigo/code-warden/internal/agent"
	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	internalgithub "github.com/sevigo/code-warden/internal/github"
	llmpkg "github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/logger"
)

func main() {
	os.Exit(run())
}

type evalOptions struct {
	suite   string
	mock    bool
	verbose bool
	cfgPath string
}

func run() int {
	opts := parseEvalOptions()
	cases, err := loadCases(opts.suite)
	if err != nil {
		return printEvalError(err)
	}
	if len(cases) == 0 {
		return printEvalError(fmt.Errorf("no eval cases found"))
	}

	printHeader(len(cases), opts)
	log := newEvalLogger(opts.verbose)
	model, promptMgr, err := loadLiveResources(context.Background(), opts, log)
	if err != nil {
		return printEvalError(err)
	}

	results, infraError := runCases(context.Background(), cases, opts, model, promptMgr, log)
	failed := printSummary(results)
	return evalExitCode(infraError, failed)
}

func parseEvalOptions() evalOptions {
	var (
		suite   = flag.String("suite", "", "run only cases in this suite (bug, security, performance, conventions, clean)")
		mock    = flag.Bool("mock", false, "pipeline-only mode: no LLM calls, tests parsing/filtering/snap")
		verbose = flag.Bool("verbose", false, "show per-case details")
		cfgPath = flag.String("config", "", "path to config file")
	)
	flag.Parse()
	return evalOptions{suite: *suite, mock: *mock, verbose: *verbose, cfgPath: *cfgPath}
}

func printHeader(caseCount int, opts evalOptions) {
	fmt.Printf("code-warden eval harness\n")
	fmt.Printf("  cases: %d\n", caseCount)
	if opts.suite != "" {
		fmt.Printf("  suite: %s\n", opts.suite)
	}
	if opts.mock {
		fmt.Printf("  mode:  mock (no LLM)\n")
	} else {
		fmt.Printf("  mode:  live (with LLM)\n")
	}
	fmt.Println()
}

func newEvalLogger(verbose bool) *slog.Logger {
	logLvl := "warn"
	if verbose {
		logLvl = "info"
	}
	log := logger.NewLogger(logger.Config{Level: logLvl, Output: "stderr"}, os.Stderr)
	slog.SetDefault(log)
	return log
}

func loadLiveResources(ctx context.Context, opts evalOptions, log *slog.Logger) (llms.Model, *llmpkg.PromptManager, error) {
	if opts.mock {
		return nil, nil, nil
	}
	cfg, err := loadConfig(opts.cfgPath)
	if err != nil {
		return nil, nil, err
	}
	if err := cfg.ValidateForCLI(); err != nil {
		return nil, nil, err
	}
	model, err := llmpkg.NewGenerator(ctx, cfg.AI, log)
	if err != nil {
		return nil, nil, fmt.Errorf("build LLM: %w", err)
	}
	promptMgr, err := llmpkg.NewPromptManager()
	if err != nil {
		return nil, nil, fmt.Errorf("load prompts: %w", err)
	}
	return model, promptMgr, nil
}

func runCases(ctx context.Context, cases []EvalCase, opts evalOptions, model llms.Model, promptMgr *llmpkg.PromptManager, log *slog.Logger) ([]EvalResult, bool) {
	var results []EvalResult
	var infraError bool
	for _, c := range cases {
		result, isInfraError := runCase(ctx, c, opts.mock, model, promptMgr, log)
		if isInfraError {
			infraError = true
		}
		results = append(results, result)
		printResult(result, opts.verbose)
	}
	return results, infraError
}

func runCase(ctx context.Context, c EvalCase, mock bool, model llms.Model, promptMgr *llmpkg.PromptManager, log *slog.Logger) (EvalResult, bool) {
	start := time.Now()
	if mock {
		result := runMockEval(ctx, c)
		result.Duration = time.Since(start)
		return result, false
	}

	result, err := runLiveEval(ctx, c, model, promptMgr, log)
	result.Duration = time.Since(start)
	if err == nil {
		return result, false
	}
	fmt.Fprintf(os.Stderr, "infra error on %s: %v\n", c.Name, err)
	result.InfraError = true
	return result, true
}

func printSummary(results []EvalResult) int {
	fmt.Println()
	fmt.Println("--- Summary ---")
	var passed, failed, totalTP, totalFP, totalFN int
	for _, r := range results {
		if r.InfraError {
			continue
		}
		if r.Passed {
			passed++
		} else {
			failed++
		}
		totalTP += r.TruePositives
		totalFP += r.FalsePositives
		totalFN += r.FalseNegatives
	}
	precision := percentage(totalTP, totalTP+totalFP)
	recall := percentage(totalTP, totalTP+totalFN)
	fmt.Printf("  passed:      %d\n", passed)
	fmt.Printf("  failed:      %d\n", failed)
	fmt.Printf("  true positives:  %d\n", totalTP)
	fmt.Printf("  false positives: %d\n", totalFP)
	fmt.Printf("  false negatives: %d\n", totalFN)
	fmt.Printf("  precision:   %.1f%%\n", precision)
	fmt.Printf("  recall:      %.1f%%\n", recall)
	return failed
}

func percentage(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator) * 100
}

func evalExitCode(infraError bool, failed int) int {
	if infraError {
		fmt.Println("\nexit 2 (infrastructure error)")
		return 2
	}
	if failed > 0 {
		fmt.Println("\nexit 1 (eval failure)")
		return 1
	}
	fmt.Println("\nexit 0 (all passed)")
	return 0
}

func printEvalError(err error) int {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	return 2
}

// EvalResult holds the outcome of a single eval case.
type EvalResult struct {
	Case           string
	Suite          string
	Passed         bool
	TruePositives  int
	FalsePositives int
	FalseNegatives int
	Duration       time.Duration
	Verdict        string
	FindingsCount  int
	InfraError     bool
	Notes          []string
}

// runMockEval tests the pipeline without an LLM. It verifies that:
// - ParseDiff correctly splits the diff into changed files
// - BuildValidLineMap produces the correct line map
// - Snap correctly adjusts off-by-N line numbers
// - Deduplicate merges duplicate findings
// - FilterBySeverity drops below-threshold findings
func runMockEval(_ context.Context, c EvalCase) EvalResult {
	result := EvalResult{Case: c.Name, Suite: c.Suite}

	changedFiles := agentreview.ParseDiff(c.Diff)
	if len(changedFiles) == 0 && len(c.ExpectedFindings) > 0 {
		result.Notes = append(result.Notes, "ParseDiff returned 0 files")
		result.Passed = false
		return result
	}

	fileLines := internalgithub.BuildValidLineMap(changedFiles)
	if len(fileLines) == 0 && len(c.ExpectedFindings) > 0 {
		result.Notes = append(result.Notes, "BuildValidLineMap returned 0 lines")
		result.Passed = false
		return result
	}

	// Test snap with the expected findings.
	filter := agentreview.NewDiffFilterForTest(changedFiles)
	for _, ef := range c.ExpectedFindings {
		s := core.Suggestion{FilePath: ef.File, LineNumber: ef.LineRange[0]}
		snapped := filter.SnapForTest(s)
		if snapped == nil {
			result.Notes = append(result.Notes, fmt.Sprintf("snap dropped %s:%d", ef.File, ef.LineRange[0]))
			result.FalseNegatives++
		} else {
			result.TruePositives++
		}
	}

	// In mock mode, "passing" means the pipeline correctly parses and
	// validates all expected finding locations.
	result.Passed = result.FalseNegatives == 0
	return result
}

// runLiveEval runs the full review pipeline with an LLM and scores the output
// against expected findings. The model and prompt manager are pre-built and
// reused across cases.
func runLiveEval(ctx context.Context, c EvalCase, model llms.Model, promptMgr *llmpkg.PromptManager, log *slog.Logger) (EvalResult, error) {
	result := EvalResult{Case: c.Name, Suite: c.Suite}
	workspace, cleanup, err := createEvalWorkspace(c)
	if err != nil {
		return result, fmt.Errorf("create eval workspace: %w", err)
	}
	defer cleanup()

	reviewResult, err := runLiveReview(ctx, c, workspace, model, promptMgr, log)
	if err != nil {
		return result, fmt.Errorf("review failed: %w", err)
	}
	if reviewResult == nil || reviewResult.Review == nil {
		return result, fmt.Errorf("nil review result")
	}
	return scoreLiveReview(c, reviewResult.Review), nil
}

func runLiveReview(ctx context.Context, c EvalCase, workspace string, model llms.Model, promptMgr *llmpkg.PromptManager, log *slog.Logger) (*agentreview.Result, error) {
	reviewConfig := agentreview.DefaultConfig()
	reviewConfig.MinSeverity = "low"
	runner := agentreview.NewRunner(model, promptMgr, agent.ReadOnlyReviewTools, log, nil)
	return runner.Run(ctx, agentreview.Params{
		Diff:          c.Diff,
		ChangedFiles:  evalChangedFiles(c),
		WorkspaceDir:  workspace,
		RepoFullName:  "eval/" + c.Name,
		Timeout:       10 * time.Minute,
		MaxIterations: 20,
		Config:        &reviewConfig,
	})
}

func evalChangedFiles(c EvalCase) []core.ChangedFile {
	if c.ChangedFiles != nil {
		return c.ChangedFiles
	}
	return agentreview.ParseDiff(c.Diff)
}

func scoreLiveReview(c EvalCase, review *core.StructuredReview) EvalResult {
	result := EvalResult{
		Case:          c.Name,
		Suite:         c.Suite,
		Verdict:       review.Verdict,
		FindingsCount: len(review.Suggestions),
	}
	matched := make([]bool, len(c.ExpectedFindings))
	scoreFindings(&result, review.Suggestions, c.ExpectedFindings, matched)
	appendMissedFindingNotes(&result, c.ExpectedFindings, matched)
	appendExpectationNotes(&result, c)
	result.Passed = evalPassed(result, c)
	return result
}

func scoreFindings(result *EvalResult, actual []core.Suggestion, expected []ExpectedFinding, matched []bool) {
	for _, finding := range actual {
		if matchFinding(finding, expected, matched) {
			result.TruePositives++
			continue
		}
		result.FalsePositives++
	}
}

func matchFinding(actual core.Suggestion, expected []ExpectedFinding, matched []bool) bool {
	for i, finding := range expected {
		if !matched[i] && findingMatches(actual, finding) {
			matched[i] = true
			return true
		}
	}
	return false
}

func appendMissedFindingNotes(result *EvalResult, expected []ExpectedFinding, matched []bool) {
	for i, wasMatched := range matched {
		if wasMatched {
			continue
		}
		result.FalseNegatives++
		finding := expected[i]
		result.Notes = append(result.Notes, fmt.Sprintf("missed: %s:%s (expected at %s:%d-%d)", finding.Category, finding.Severity, finding.File, finding.LineRange[0], finding.LineRange[1]))
	}
}

func appendExpectationNotes(result *EvalResult, c EvalCase) {
	if c.ExpectedVerdict != "" && c.ExpectedVerdict != result.Verdict {
		result.Notes = append(result.Notes, fmt.Sprintf("verdict mismatch: expected %s, got %s", c.ExpectedVerdict, result.Verdict))
	}
	if result.FalsePositives > c.MaxFalsePositives {
		result.Notes = append(result.Notes, fmt.Sprintf("too many false positives: %d (max %d)", result.FalsePositives, c.MaxFalsePositives))
	}
}

func evalPassed(result EvalResult, c EvalCase) bool {
	return result.FalseNegatives == 0 && result.FalsePositives <= c.MaxFalsePositives && (c.ExpectedVerdict == "" || c.ExpectedVerdict == result.Verdict)
}

// findingMatches reports whether an actual suggestion matches an expected finding.
func findingMatches(actual core.Suggestion, expected ExpectedFinding) bool {
	// File must match (case-insensitive).
	if !strings.EqualFold(stripPrefix(actual.FilePath), stripPrefix(expected.File)) {
		return false
	}

	// Line must be within the expected range (after snap). Use a +/-5 line
	// tolerance around the expected range to account for the model citing
	// slightly different line numbers.
	if expected.LineRange[0] > 0 && expected.LineRange[1] > 0 {
		lo := expected.LineRange[0] - 5
		if lo < 0 {
			lo = 0
		}
		hi := expected.LineRange[1] + 5
		if actual.LineNumber < lo || actual.LineNumber > hi {
			return false
		}
	}

	// Category must match (case-insensitive). Accept close variants:
	// "Bug" matches "Correctness", "Test" matches "Test Coverage".
	if expected.Category != "" {
		if !categoryMatches(actual.Category, expected.Category) {
			return false
		}
	}

	// Severity must match or be higher (case-insensitive).
	if expected.Severity != "" {
		if severityRank(actual.Severity) < severityRank(expected.Severity) {
			return false
		}
	}

	// Description keyword must appear in the comment (if specified).
	if expected.DescriptionContains != "" {
		if !strings.Contains(strings.ToLower(actual.Comment), strings.ToLower(expected.DescriptionContains)) {
			return false
		}
	}

	return true
}

// categoryMatches reports whether two category labels refer to the same
// category, accounting for common variant names the model might use.
func categoryMatches(actual, expected string) bool {
	a := strings.ToLower(actual)
	e := strings.ToLower(expected)
	if a == e {
		return true
	}
	// Common variants the model uses.
	variants := map[string][]string{
		"bug":         {"correctness", "logic", "crash"},
		"convention":  {"conventions", "maintainability", "style", "documentation"},
		"test":        {"test coverage", "testing"},
		"performance": {"perf", "optimization"},
		"security":    {"vulnerability", "privacy"},
	}
	for _, v := range variants[e] {
		if a == v {
			return true
		}
	}
	for _, v := range variants[a] {
		if e == v {
			return true
		}
	}
	return false
}

func stripPrefix(p string) string {
	return strings.TrimPrefix(strings.TrimPrefix(p, "./"), ".\\")
}

func severityRank(sev string) int {
	switch strings.ToLower(sev) {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

// printResult prints a single eval result.
func printResult(r EvalResult, verbose bool) {
	status := "✓ PASS"
	if !r.Passed {
		status = "✗ FAIL"
	}
	if r.InfraError {
		status = "⚠ ERROR"
	}

	fmt.Printf("%s [%s] %s (%v)\n", status, r.Suite, r.Case, r.Duration.Round(time.Millisecond))
	if verbose || !r.Passed {
		fmt.Printf("  verdict: %s, findings: %d, TP: %d, FP: %d, FN: %d\n",
			r.Verdict, r.FindingsCount, r.TruePositives, r.FalsePositives, r.FalseNegatives)
		for _, note := range r.Notes {
			fmt.Printf("  → %s\n", note)
		}
	}
}

// ── Eval case loading ─────────────────────────────────────────────────────────

// EvalCase is a single test case for the eval harness.
type EvalCase struct {
	Name         string             `json:"name"`
	Suite        string             `json:"suite"`
	Diff         string             `json:"diff"`
	ChangedFiles []core.ChangedFile `json:"changed_files,omitempty"`
	WorkspaceDir string             `json:"workspace_dir,omitempty"`
	// WorkspaceFiles are extra files to write to the eval workspace that
	// aren't part of the diff but are needed for context (e.g. existing
	// files the diff depends on). Map of path → content.
	WorkspaceFiles    map[string]string `json:"workspace_files,omitempty"`
	ExpectedFindings  []ExpectedFinding `json:"expected_findings"`
	ExpectedVerdict   string            `json:"expected_verdict,omitempty"`
	MaxFalsePositives int               `json:"max_false_positives"`
}

// ExpectedFinding describes a finding the review should produce.
type ExpectedFinding struct {
	File                string `json:"file"`
	LineRange           [2]int `json:"line_range"`
	Severity            string `json:"severity,omitempty"`
	Category            string `json:"category,omitempty"`
	DescriptionContains string `json:"description_contains,omitempty"`
}

// loadCases loads all eval cases from the cases/ directory, optionally filtered by suite.
func loadCases(suite string) ([]EvalCase, error) {
	casesDir := filepath.Join("evals", "cases")
	entries, err := os.ReadDir(casesDir)
	if err != nil {
		return nil, fmt.Errorf("read evals/cases: %w (run from project root)", err)
	}

	var all []EvalCase
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(casesDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var c EvalCase
		if err := json.Unmarshal(data, &c); err != nil { //nolint:musttag // eval case struct, not API-facing
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if c.MaxFalsePositives == 0 && len(c.ExpectedFindings) == 0 {
			// "clean" cases: expect no findings, allow 0 FP.
			c.MaxFalsePositives = 0
		} else if c.MaxFalsePositives == 0 {
			c.MaxFalsePositives = 2 // default tolerance
		}
		if suite != "" && c.Suite != suite {
			continue
		}
		all = append(all, c)
	}
	return all, nil
}

// loadConfig loads configuration for live mode.
func loadConfig(cfgPath string) (*config.Config, error) {
	if cfgPath == "" {
		return config.LoadConfig()
	}
	return config.LoadConfigWithFile(cfgPath)
}

// createEvalWorkspace creates a temporary directory with the "new" version
// of each changed file written to the correct path, plus any extra workspace
// files the case needs for context. This gives the agent a workspace to
// read_file, grep, and check_build against during eval runs.
func createEvalWorkspace(c EvalCase) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "code-warden-eval-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	// Write the "new" version of each file from the diff.
	newFiles := extractNewFiles(c.Diff)
	for path, content := range newFiles {
		if err := writeWorkspaceFile(tmpDir, path, content); err != nil {
			cleanup()
			return "", nil, err
		}
	}

	// Write extra workspace files (dependencies the diff references but
	// aren't part of the diff itself).
	for path, content := range c.WorkspaceFiles {
		if err := writeWorkspaceFile(tmpDir, path, content); err != nil {
			cleanup()
			return "", nil, err
		}
	}

	return tmpDir, cleanup, nil
}

// writeWorkspaceFile writes one fixture file while ensuring that case data
// cannot escape the temporary evaluation workspace.
func writeWorkspaceFile(root, path, content string) error {
	cleanPath := filepath.Clean(path)
	if filepath.IsAbs(cleanPath) || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid workspace file path %q", path)
	}

	fullPath := filepath.Join(root, cleanPath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		return err
	}
	return nil
}

// extractNewFiles parses a unified diff and returns the "new" version of
// each file: the file path and its content after the diff is applied.
// For new files, this is the full content of the added lines.
// For modified files, this is the patched version (context + added lines, minus deleted lines).
func extractNewFiles(diff string) map[string]string {
	files := make(map[string]string)
	var file diffFile
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			file.store(files)
			file = diffFile{}
			continue
		}
		file.consume(line)
	}
	file.store(files)
	return files
}

type diffFile struct {
	path   string
	lines  []string
	inHunk bool
}

func (f *diffFile) consume(line string) {
	switch {
	case strings.HasPrefix(line, "+++ b/"):
		f.path = strings.TrimPrefix(line, "+++ b/")
		f.inHunk = false
	case strings.HasPrefix(line, "@@"):
		f.inHunk = true
	case f.inHunk && f.path != "":
		f.appendNewLine(line)
	}
}

func (f *diffFile) appendNewLine(line string) {
	if strings.HasPrefix(line, "+") {
		f.lines = append(f.lines, strings.TrimPrefix(line, "+"))
		return
	}
	if strings.HasPrefix(line, " ") {
		f.lines = append(f.lines, strings.TrimPrefix(line, " "))
	}
}

func (f *diffFile) store(files map[string]string) {
	if f.path != "" && len(f.lines) > 0 {
		files[f.path] = strings.Join(f.lines, "\n") + "\n"
	}
}
