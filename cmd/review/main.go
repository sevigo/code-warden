// Command review runs the agent-based code review engine standalone, without
// requiring the GitHub App integration. It can review a local git checkout or a
// public GitHub pull request, and renders the structured review to the terminal
// with colorized formatting.
//
// Usage:
//
//	review --local <path> [--base <ref>]          review a local checkout
//	review --pr owner/repo <number> [--token X]   review a public (or authed) PR
//
// Flags:
//
//	--json         print the raw structured review as JSON (no color)
//	--prompt-only  print compact text for AI agent consumption (no color)
//	--no-color     disable colorized output
//	--config PATH  path to a config file (default: ./config.yaml, $HOME/.code-warden)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentreview "github.com/sevigo/code-warden/internal/agent/review"
	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/logger"
	"github.com/sevigo/code-warden/internal/reviewcli"
	"github.com/sevigo/code-warden/internal/reviewcli/render"
)

func main() {
	fs := flag.NewFlagSet("review", flag.ExitOnError)

	var (
		local   = fs.String("local", "", "path to a local git checkout to review")
		pr      = fs.String("pr", "", "PR to review as owner/repo")
		prNum   = fs.Int("number", 0, "pull request number (used with --pr)")
		token   = fs.String("token", "", "GitHub token (optional; private repos / higher limits)")
		base    = fs.String("base", "", "git ref to diff against (default: HEAD = uncommitted changes)")
		asJSON  = fs.Bool("json", false, "print raw structured review as JSON (no color)")
		prompt  = fs.Bool("prompt-only", false, "print compact structured text for AI agent consumption (no color)")
		noColor = fs.Bool("no-color", false, "disable colorized output")
		cfgPath = fs.String("config", "", "path to a config file (default: ./config.yaml, $HOME/.code-warden)")
		timeout = fs.Duration("timeout", 0, "per-angle timeout (default: 3m; raise for slow local models)")
		maxIter = fs.Int("max-iterations", 0, "per-angle agent-loop iteration cap (default: 8)")
		logLvl  = fs.String("log-level", "info", "log level: debug, info, warn, error")
		ctxWin  = fs.Int("context-window", 0, "model context window in tokens (default: 128000; compaction triggers at 60%)")
		sev     = fs.String("severity", "", "minimum severity to report: low, medium, high, critical (default: medium)")
		ignore  = fs.String("ignore", "", "comma-separated glob patterns to ignore (e.g. \"vendor/**,*.lock\")")
		cats    = fs.String("categories", "", "comma-separated categories to enable (e.g. \"bug,security\"); default: all")
		maxF    = fs.Int("max-files", 0, "skip review when more than N files changed (default: 100)")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "review — run the agent-based code review standalone")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Usage:")
		fmt.Fprintf(os.Stderr, "  %s --local <path> [--base <ref>]\n", fs.Name())
		fmt.Fprintf(os.Stderr, "  %s --pr owner/repo --number <n> [--token X]\n", fs.Name())
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	_ = fs.Parse(os.Args[1:])

	if *noColor || *asJSON || *prompt {
		render.SetEnabled(false)
	}

	if (*local == "") == (*pr == "") {
		fmt.Fprintln(os.Stderr, "error: specify exactly one of --local or --pr")
		fs.Usage()
		os.Exit(2)
	}
	if *pr != "" && *prNum == 0 {
		fmt.Fprintln(os.Stderr, "error: --number is required with --pr")
		os.Exit(2)
	}

	ctx := context.Background()
	logger := logger.NewLogger(logger.Config{Level: *logLvl, Output: "stderr"}, os.Stderr)

	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.ValidateForCLI(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	rc := buildReviewConfig(*sev, *ignore, *cats, *maxF)

	opts, err := buildOptions(ctx, *local, *pr, *prNum, *token, *base, *timeout, *maxIter, *ctxWin, rc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	result, err := reviewcli.Run(ctx, cfg, logger, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: review failed: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(result.Review); err != nil {
			fmt.Fprintf(os.Stderr, "error: encode review: %v\n", err)
			os.Exit(1)
		}
		os.Exit(exitCodeForReview(result.Review))
	}

	if *prompt {
		renderPromptOnly(os.Stdout, result.Review)
		os.Exit(exitCodeForReview(result.Review))
	}

	render.Render(os.Stdout, result.Review, render.Options{})
	os.Exit(exitCodeForReview(result.Review))
}

// exitCodeForReview returns the process exit code based on the review verdict.
// 0 = APPROVE or no findings, 1 = REQUEST_CHANGES (caller controls with --severity).
func exitCodeForReview(review *core.StructuredReview) int {
	if review == nil {
		return 0
	}
	switch review.Verdict {
	case "REQUEST_CHANGES":
		return 1
	default:
		return 0
	}
}

// buildReviewConfig constructs a ReviewConfig from CLI flags, falling back to
// defaults when flags are empty.
func buildReviewConfig(severity, ignore, categories string, maxFiles int) *agentreview.Config {
	rc := agentreview.DefaultConfig()
	if severity != "" {
		rc.MinSeverity = strings.ToLower(severity)
	}
	if ignore != "" {
		rc.IgnorePaths = strings.Split(ignore, ",")
		for i, p := range rc.IgnorePaths {
			rc.IgnorePaths[i] = strings.TrimSpace(p)
		}
	}
	if categories != "" {
		rc.EnabledCategories = make(map[string]bool)
		for _, c := range strings.Split(categories, ",") {
			c = strings.TrimSpace(strings.ToLower(c))
			if c != "" {
				rc.EnabledCategories[c] = true
			}
		}
	}
	if maxFiles > 0 {
		rc.MaxFiles = maxFiles
	}
	return &rc
}

// renderPromptOnly writes a compact, structured text format optimized for AI
// coding agents (Claude Code, Cursor, etc.) to parse and act on.
func renderPromptOnly(w *os.File, review *core.StructuredReview) {
	if review == nil {
		return
	}
	fmt.Fprintf(w, "VERDICT: %s\n", review.Verdict)
	if review.Confidence > 0 {
		fmt.Fprintf(w, "CONFIDENCE: %d\n", review.Confidence)
	}
	fmt.Fprintf(w, "SUMMARY: %s\n", review.Summary)
	if len(review.Suggestions) == 0 {
		fmt.Fprintln(w, "SUGGESTIONS: none")
		return
	}
	fmt.Fprintf(w, "SUGGESTIONS: %d\n", len(review.Suggestions))
	for i, s := range review.Suggestions {
		fmt.Fprintf(w, "\n--- Finding %d ---\n", i+1)
		fmt.Fprintf(w, "File: %s:%d\n", s.FilePath, s.LineNumber)
		fmt.Fprintf(w, "Severity: %s\n", s.Severity)
		fmt.Fprintf(w, "Category: %s\n", s.Category)
		if s.Source != "" {
			fmt.Fprintf(w, "Source: %s\n", s.Source)
		}
		fmt.Fprintf(w, "Comment:\n%s\n", s.Comment)
		if s.CodeSuggestion != "" {
			fmt.Fprintf(w, "Fix:\n%s\n", s.CodeSuggestion)
		}
	}
}

// buildOptions resolves the review inputs (local diff or public PR).
func buildOptions(ctx context.Context, local, pr string, prNum int, token, base string, timeout time.Duration, maxIter, ctxWin int, rc *agentreview.Config) (reviewcli.Options, error) {
	if local != "" {
		abs, err := filepath.Abs(local)
		if err != nil {
			return reviewcli.Options{}, err
		}
		if _, err := os.Stat(abs); err != nil {
			return reviewcli.Options{}, fmt.Errorf("not a directory: %s", abs)
		}
		diff, err := reviewcli.GitDiff(ctx, abs, base)
		if err != nil {
			return reviewcli.Options{}, err
		}
		commits, _ := reviewcli.GitLog(ctx, abs, base)
		return reviewcli.Options{
			Diff:           diff,
			ChangedFiles:   reviewcli.ChangedFilesFromDiff(diff),
			WorkspaceDir:   abs,
			RepoFullName:   filepath.Base(abs),
			CommitMessages: commits,
			Timeout:        timeout,
			MaxIterations:  maxIter,
			ContextWindow:  ctxWin,
			Config:         rc,
		}, nil
	}

	owner, repo, err := splitOwnerRepo(pr)
	if err != nil {
		return reviewcli.Options{}, err
	}
	data, err := reviewcli.FetchPR(ctx, reviewcli.PRInput{Owner: owner, Repo: repo, Number: prNum, Token: token})
	if err != nil {
		return reviewcli.Options{}, err
	}
	return reviewcli.Options{
		Diff:           data.Diff,
		ChangedFiles:   data.ChangedFiles,
		RepoURL:        data.CloneURL,
		RepoFullName:   owner + "/" + repo,
		CommitMessages: data.CommitMessages,
		Timeout:        timeout,
		MaxIterations:  maxIter,
		ContextWindow:  ctxWin,
		Config:         rc,
	}, nil
}

// loadConfig loads configuration, optionally from an explicit path.
func loadConfig(cfgPath string) (*config.Config, error) {
	if cfgPath == "" {
		return config.LoadConfig()
	}
	return config.LoadConfigWithFile(cfgPath)
}

// splitOwnerRepo parses "owner/repo".
func splitOwnerRepo(s string) (owner, repo string, err error) {
	for i := range len(s) {
		if s[i] == '/' {
			return s[:i], s[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("expected owner/repo, got %q", s)
}
