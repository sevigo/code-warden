package review

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sevigo/code-warden/internal/core"
)

// ComparisonResult represents the outcome of an LLM code review generation.
type ComparisonResult struct {
	Model    string
	Review   string
	Duration time.Duration
	Error    error
}

// EnsureReviewsDir creates the reviews output directory if it doesn't exist.
func EnsureReviewsDir(logger *slog.Logger, reviewsDir string) error {
	if err := os.MkdirAll(reviewsDir, 0750); err != nil {
		logger.Error("failed to create reviews output directory", "error", err, "dir", reviewsDir)
		return fmt.Errorf("failed to create reviews output directory: %w", err)
	}
	return nil
}

func SaveReviewArtifact(logger *slog.Logger, dir string, res ComparisonResult, event *core.GitHubEvent, ts string) {
	if res.Error != nil {
		return
	}

	safeName := SanitizeModelForFilename(res.Model)
	filename := filepath.Join(dir, fmt.Sprintf("review_%s_pr%d_%s_%s.md",
		event.RepoName, event.PRNumber, safeName, ts))

	header := fmt.Sprintf("<!-- Model: %s | Duration: %s -->\n\n", res.Model, res.Duration)

	if err := os.WriteFile(filename, []byte(header+res.Review), 0600); err != nil {
		logger.Warn("failed to save review artifact", "model", res.Model, "error", err)
	}
}

func SaveConsensusArtifact(logger *slog.Logger, dir, raw, ts string, event *core.GitHubEvent, duration time.Duration, models []string, contextDuration time.Duration) {
	filename := filepath.Join(dir, fmt.Sprintf("consensus_%s_pr%d_%s.md",
		event.RepoName, event.PRNumber, ts))

	header := fmt.Sprintf("<!-- Consensus Review | Duration: %s | Context Build: %s | Models: %s -->\n\n",
		duration, contextDuration, strings.Join(models, ", "))

	if err := os.WriteFile(filename, []byte(header+raw), 0600); err != nil {
		logger.Warn("failed to save consensus artifact", "error", err)
	}
}
