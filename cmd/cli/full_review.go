package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/wire"
)

var (
	fullReviewCmdSkipRAG bool
	fullReviewCmdTimeout int
)

var fullReviewCmd = &cobra.Command{
	Use:   "full-review [path]",
	Short: "Perform a full code review of all files in the project.",
	Long:  `Scans the local repository and performs a file-by-file code review, saving results to markdown files.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		repoPath := args[0]
		slog.Info("Starting full project review", "path", repoPath)

		timeout := time.Duration(fullReviewCmdTimeout) * time.Minute
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		app, cleanup, err := wire.InitializeApp(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize application: %w", err)
		}
		defer cleanup()

		// 1. Scan and index repo first to ensure we have context
		// Note: repoFullName is defined in scan.go (package main)
		updateResult, err := app.RepoMgr.ScanLocalRepo(ctx, repoPath, repoFullName, false)
		if err != nil {
			return fmt.Errorf("failed to scan local repository: %w", err)
		}

		repoConfig, err := config.LoadRepoConfig(updateResult.RepoPath)
		if err != nil {
			slog.Warn("failed to load repo config, using defaults", "error", err, "repo", updateResult.RepoFullName)
			repoConfig = core.DefaultRepoConfig()
		}

		repoRecord, err := app.RepoMgr.GetRepoRecord(ctx, updateResult.RepoFullName)
		if err != nil {
			return fmt.Errorf("failed to retrieve repository record for %s: %w", updateResult.RepoFullName, err)
		}
		if repoRecord == nil {
			return fmt.Errorf("repository record not found for %s", updateResult.RepoFullName)
		}

		// 2. Identify files to review
		files, err := listFiles(updateResult.RepoPath, repoConfig.ExcludeDirs, repoConfig.ExcludeExts)
		if err != nil {
			return fmt.Errorf("failed to list files for review: %w", err)
		}

		slog.Info("Identified files for review", "count", len(files))

		// 3. Create output directory
		outputDir := filepath.Join(repoPath, "full_reviews")
		if err := os.MkdirAll(outputDir, 0750); err != nil {
			return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
		}

		// 4. Review each file
		for _, relPath := range files {
			if !isCodeFile(relPath) {
				continue
			}

			slog.Info("Reviewing file", "file", relPath, "skip_rag", fullReviewCmdSkipRAG)
			review, err := app.RAGService.GenerateFileReview(ctx, repoConfig, repoRecord, relPath, updateResult.RepoPath, fullReviewCmdSkipRAG)
			if err != nil {
				slog.Error("Failed to review file", "file", relPath, "error", err)
				return fmt.Errorf("interrupted: failed to review %s: %w", relPath, err)
			}

			if err := saveReviewToMarkdown(outputDir, relPath, review); err != nil {
				slog.Error("Failed to save review", "file", relPath, "error", err)
				return fmt.Errorf("interrupted: failed to save review for %s: %w", relPath, err)
			}
		}

		slog.Info("✅ Full project review complete. Results stored in", "path", outputDir)
		return nil
	},
}

func isCodeFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".java", ".rs", ".c", ".cpp", ".h", ".hpp":
		return true
	}
	return false
}

func saveReviewToMarkdown(outputDir, relPath string, review *core.StructuredReview) error {
	if review == nil {
		return fmt.Errorf("cannot save nil review for %s", relPath)
	}

	// Security: Sanitize and validate path to prevent directory traversal
	cleanRel := filepath.Clean(relPath)
	if strings.HasPrefix(cleanRel, "..") || strings.Contains(cleanRel, string(filepath.Separator)+"..") {
		return fmt.Errorf("invalid path traversal detected in: %s", relPath)
	}

	targetPath := filepath.Join(outputDir, cleanRel+".md")

	// Ensure the final target path is actually within the outputDir
	absOutput, _ := filepath.Abs(outputDir)
	absTarget, _ := filepath.Abs(targetPath)
	if !strings.HasPrefix(absTarget, absOutput+string(filepath.Separator)) {
		return fmt.Errorf("path escapes output directory: %s", relPath)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0750); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# Code Review: %s\n\n", relPath))
	sb.WriteString("## Summary\n")
	sb.WriteString(review.Summary)
	sb.WriteString("\n\n")

	if len(review.Suggestions) > 0 {
		sb.WriteString("## Suggestions\n\n")
		for _, s := range review.Suggestions {
			sb.WriteString(fmt.Sprintf("### [%s] %s (Line %d)\n", s.Severity, s.Category, s.LineNumber))
			sb.WriteString(s.Comment)
			sb.WriteString("\n\n---\n\n")
		}
	} else {
		sb.WriteString("✅ No issues found.")
	}

	return os.WriteFile(targetPath, []byte(sb.String()), 0600)
}

// listFiles uses WalkDir for better performance (available since Go 1.16)
func listFiles(root string, excludeDirs, excludeExts []string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, err)
		}
		if rel == "." {
			return nil
		}

		if d.IsDir() {
			if shouldSkipDir(d.Name(), rel, excludeDirs) {
				return filepath.SkipDir
			}
			return nil
		}

		if !isExcludedExt(filepath.Ext(path), excludeExts) {
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

func shouldSkipDir(name, rel string, excludeDirs []string) bool {
	if strings.HasPrefix(name, ".") && name != "." {
		return true
	}
	// Check if any part of the path is in excludeDirs
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts {
		for _, d := range excludeDirs {
			if part == d {
				return true
			}
		}
	}
	return false
}

func isExcludedExt(ext string, excludeExts []string) bool {
	ext = strings.ToLower(ext)
	for _, e := range excludeExts {
		if ext == e {
			return true
		}
	}
	return false
}

func init() { //nolint:gochecknoinits
	fullReviewCmd.Flags().StringVar(&repoFullName, "repo-full-name", "local/project", "The full name of the repository")
	fullReviewCmd.Flags().BoolVar(&fullReviewCmdSkipRAG, "skip-rag", false, "Skip building RAG context for each file (much faster)")
	fullReviewCmd.Flags().IntVar(&fullReviewCmdTimeout, "timeout", 60, "Maximum time in minutes for the full review")
	rootCmd.AddCommand(fullReviewCmd)
}
