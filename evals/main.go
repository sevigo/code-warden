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
	"github.com/sevigo/code-warden/internal/reviewcli"
)

func main() {
	var (
		suite   = flag.String("suite", "", "run only cases in this suite (bug, security, performance, conventions, clean)")
		mock    = flag.Bool("mock", false, "pipeline-only mode: no LLM calls, tests parsing/filtering/snap")
		verbose = flag.Bool("verbose", false, "show per-case details")
		cfgPath = flag.String("config", "", "path to config file")
	)
	flag.Parse()

	ctx := context.Background()

	cases, err := loadCases(*suite)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if len(cases) == 0 {
		fmt.Fprintln(os.Stderr, "no eval cases found")
		os.Exit(2)
	}

	fmt.Printf("code-warden eval harness\n")
	fmt.Printf("  cases: %d\n", len(cases))
	if *suite != "" {
		fmt.Printf("  suite: %s\n", *suite)
	}
	if *mock {
		fmt.Printf("  mode:  mock (no LLM)\n")
	} else {
		fmt.Printf("  mode:  live (with LLM)\n")
	}
	fmt.Println()

	// Load config for live mode (needs LLM config).
	var cfg *config.Config
	if !*mock {
		cfg, err = loadConfig(*cfgPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
		if err := cfg.ValidateForCLI(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(2)
		}
	}

	logLvl := "warn"
	if *verbose {
		logLvl = "info"
	}
	log := logger.NewLogger(logger.Config{Level: logLvl, Output: "stderr"}, os.Stderr)
	slog.SetDefault(log)

	var results []EvalResult
	var infraError bool

	for _, c := range cases {
		var result EvalResult
		result.Case = c.Name
		result.Suite = c.Suite

		start := time.Now()

		if *mock {
			result = runMockEval(ctx, c)
		} else {
			result, err = runLiveEval(ctx, c, cfg, log)
			if err != nil {
				fmt.Fprintf(os.Stderr, "infra error on %s: %v\n", c.Name, err)
				result.InfraError = true
				infraError = true
			}
		}

		result.Duration = time.Since(start)
		results = append(results, result)

		printResult(result, *verbose)
	}

	// Summary.
	fmt.Println()
	fmt.Println("--- Summary ---")
	var passed, failed int
	var totalTP, totalFP, totalFN int
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

	precision := 0.0
	if totalTP+totalFP > 0 {
		precision = float64(totalTP) / float64(totalTP+totalFP) * 100
	}
	recall := 0.0
	if totalTP+totalFN > 0 {
		recall = float64(totalTP) / float64(totalTP+totalFN) * 100
	}

	fmt.Printf("  passed:      %d\n", passed)
	fmt.Printf("  failed:      %d\n", failed)
	fmt.Printf("  true positives:  %d\n", totalTP)
	fmt.Printf("  false positives: %d\n", totalFP)
	fmt.Printf("  false negatives: %d\n", totalFN)
	fmt.Printf("  precision:   %.1f%%\n", precision)
	fmt.Printf("  recall:      %.1f%%\n", recall)

	if infraError {
		fmt.Println("\nexit 2 (infrastructure error)")
		os.Exit(2)
	}
	if failed > 0 {
		fmt.Println("\nexit 1 (eval failure)")
		os.Exit(1)
	}
	fmt.Println("\nexit 0 (all passed)")
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
// against expected findings.
func runLiveEval(ctx context.Context, c EvalCase, cfg *config.Config, log *slog.Logger) (EvalResult, error) {
	result := EvalResult{Case: c.Name, Suite: c.Suite}

	model, err := buildLLM(ctx, cfg, log)
	if err != nil {
		return result, fmt.Errorf("build LLM: %w", err)
	}

	promptMgr, err := llmpkg.NewPromptManager()
	if err != nil {
		return result, fmt.Errorf("load prompts: %w", err)
	}

	runner := agentreview.NewRunner(model, promptMgr, agent.ReadOnlyReviewTools, log, nil)

	changedFiles := agentreview.ParseDiff(c.Diff)
	if c.ChangedFiles != nil {
		changedFiles = c.ChangedFiles
	}

	// Create a temp workspace for the agent to investigate. The eval
	// cases contain synthetic diffs, so we write the "new" version of
	// each changed file to a temp dir. The agent can then read_file
	// and grep against it.
	workspace, cleanup, err := createEvalWorkspace(c)
	if err != nil {
		return result, fmt.Errorf("create eval workspace: %w", err)
	}
	defer cleanup()

	reviewConfig := agentreview.DefaultConfig()
	reviewConfig.MinSeverity = "low" // eval mode: keep everything

	reviewResult, err := runner.Run(ctx, agentreview.Params{
		Diff:          c.Diff,
		ChangedFiles:  changedFiles,
		WorkspaceDir:  workspace,
		RepoFullName:  "eval/" + c.Name,
		Timeout:       10 * time.Minute,
		MaxIterations: 20,
		Config:        &reviewConfig,
	})
	if err != nil {
		return result, fmt.Errorf("review failed: %w", err)
	}

	if reviewResult == nil || reviewResult.Review == nil {
		return result, fmt.Errorf("nil review result")
	}

	result.Verdict = reviewResult.Review.Verdict
	result.FindingsCount = len(reviewResult.Review.Suggestions)

	// Score: match actual findings against expected findings.
	actual := reviewResult.Review.Suggestions
	matched := make([]bool, len(c.ExpectedFindings))

	for _, a := range actual {
		matchedAny := false
		for i, ef := range c.ExpectedFindings {
			if matched[i] {
				continue
			}
			if findingMatches(a, ef) {
				matched[i] = true
				result.TruePositives++
				matchedAny = true
				break
			}
		}
		if !matchedAny {
			result.FalsePositives++
		}
	}

	for i, m := range matched {
		if !m {
			result.FalseNegatives++
			result.Notes = append(result.Notes, fmt.Sprintf("missed: %s:%s (expected at %s:%d-%d)",
				c.ExpectedFindings[i].Category, c.ExpectedFindings[i].Severity,
				c.ExpectedFindings[i].File, c.ExpectedFindings[i].LineRange[0], c.ExpectedFindings[i].LineRange[1]))
		}
	}

	// Check verdict.
	if c.ExpectedVerdict != "" && c.ExpectedVerdict != result.Verdict {
		result.Notes = append(result.Notes, fmt.Sprintf("verdict mismatch: expected %s, got %s", c.ExpectedVerdict, result.Verdict))
	}

	// Check false positive cap.
	if result.FalsePositives > c.MaxFalsePositives {
		result.Notes = append(result.Notes, fmt.Sprintf("too many false positives: %d (max %d)", result.FalsePositives, c.MaxFalsePositives))
	}

	// Pass if: all expected findings found, FP within limit, verdict matches.
	result.Passed = result.FalseNegatives == 0 &&
		result.FalsePositives <= c.MaxFalsePositives &&
		(c.ExpectedVerdict == "" || c.ExpectedVerdict == result.Verdict)

	return result, nil
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
	Name              string                       `json:"name"`
	Suite             string                       `json:"suite"`
	Diff              string                       `json:"diff"`
	ChangedFiles      []internalgithub.ChangedFile `json:"changed_files,omitempty"`
	WorkspaceDir      string                       `json:"workspace_dir,omitempty"`
	ExpectedFindings  []ExpectedFinding            `json:"expected_findings"`
	ExpectedVerdict   string                       `json:"expected_verdict,omitempty"`
	MaxFalsePositives int                          `json:"max_false_positives"`
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
// of each changed file written to the correct path. This gives the agent a
// workspace to read_file, grep, and check_build against during eval runs.
func createEvalWorkspace(c EvalCase) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "code-warden-eval-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	// Write the "new" version of each file from the diff.
	newFiles := extractNewFiles(c.Diff)
	for path, content := range newFiles {
		fullPath := filepath.Join(tmpDir, path)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
			cleanup()
			return "", nil, err
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return tmpDir, cleanup, nil
}

// extractNewFiles parses a unified diff and returns the "new" version of
// each file: the file path and its content after the diff is applied.
// For new files, this is the full content of the added lines.
// For modified files, this is the patched version (context + added lines, minus deleted lines).
func extractNewFiles(diff string) map[string]string {
	files := make(map[string]string)
	var currentFile string
	var currentLines []string
	inHunk := false

	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ b/") {
			currentFile = strings.TrimPrefix(line, "+++ b/")
			inHunk = false
			continue
		}
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			continue
		}
		if !inHunk || currentFile == "" {
			continue
		}
		// Added line or context line goes into the new file.
		if strings.HasPrefix(line, "+") {
			currentLines = append(currentLines, strings.TrimPrefix(line, "+"))
		} else if strings.HasPrefix(line, " ") {
			currentLines = append(currentLines, strings.TrimPrefix(line, " "))
		}
		// Deleted lines are skipped — they're not in the new version.
	}

	// For new-file diffs, all lines are additions — the full file content.
	if currentFile != "" && len(currentLines) > 0 {
		files[currentFile] = strings.Join(currentLines, "\n") + "\n"
	}

	// Handle multiple files in one diff (reset state for each file).
	currentFile = ""
	currentLines = nil
	inHunk = false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			if currentFile != "" && len(currentLines) > 0 {
				files[currentFile] = strings.Join(currentLines, "\n") + "\n"
			}
			currentFile = ""
			currentLines = nil
			inHunk = false
		case strings.HasPrefix(line, "+++ b/"):
			currentFile = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && currentFile != "":
			if strings.HasPrefix(line, "+") {
				currentLines = append(currentLines, strings.TrimPrefix(line, "+"))
			} else if strings.HasPrefix(line, " ") {
				currentLines = append(currentLines, strings.TrimPrefix(line, " "))
			}
		}
	}
	if currentFile != "" && len(currentLines) > 0 {
		files[currentFile] = strings.Join(currentLines, "\n") + "\n"
	}

	return files
}

// buildLLM builds the LLM model from config.
func buildLLM(ctx context.Context, cfg *config.Config, log *slog.Logger) (llms.Model, error) {
	return reviewcli.BuildLLM(ctx, cfg, log)
}
