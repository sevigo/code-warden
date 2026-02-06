package prescan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/llm"
	"github.com/sevigo/code-warden/internal/storage"
)

// Scanner handles the resumable scanning process.
type Scanner struct {
	Manager    *Manager
	RAGService llm.RAGService
}

func NewScanner(m *Manager, rag llm.RAGService) *Scanner {
	return &Scanner{
		Manager:    m,
		RAGService: rag,
	}
}

func (s *Scanner) generateAndSaveDocumentation(localPath string) (map[string]interface{}, error) {
	docGen := NewDocGenerator(localPath)
	structure, err := docGen.GenerateProjectStructure(localPath)

	if err != nil {
		s.Manager.logger.Warn("Failed to generate project structure", "error", err)
		return nil, err
	}

	// Save locally (0600 per gosec)
	docPath := filepath.Join(localPath, "project_structure.md")
	if err := os.WriteFile(docPath, []byte(structure), 0600); err != nil {
		s.Manager.logger.Error("Failed to save project structure", "error", err)
		// We return the error but maybe we should allow partial success?
		// For now, let's return error as it's cleaner.
		return nil, err
	} else {
		s.Manager.logger.Info("Generated project documentation", "path", docPath)
	}

	// Prepare for DB
	return map[string]interface{}{
		"project_structure": structure,
	}, nil
}

func (s *Scanner) Scan(ctx context.Context, input string, force bool) error {
	// 1. Prepare Repo (Clone if needed)
	localPath, owner, repo, err := s.Manager.PrepareRepo(ctx, input)
	if err != nil {
		return err
	}
	repoFullName := fmt.Sprintf("%s/%s", owner, repo)
	s.Manager.logger.Info("Starting scan", "repo", repoFullName, "path", localPath)

	// 2. Ensure Repo Record in DB
	repoRecord, err := s.ensureRepoRecord(ctx, repoFullName, localPath)
	if err != nil {
		return err
	}

	// 3. Load State
	stateMgr := NewStateManager(s.Manager.store, repoRecord.ID)
	scanState, progress, err := stateMgr.LoadState(ctx)
	if err != nil {
		return err
	}

	// Auto-resume logic
	if force || scanState == nil || scanState.Status == string(StatusCompleted) || scanState.Status == string(StatusFailed) {
		s.Manager.logger.Info("Starting fresh scan")
		progress = &Progress{
			Files:       make(map[string]bool),
			LastUpdated: time.Now(),
		}
		if err := stateMgr.SaveState(ctx, StatusPending, progress, nil); err != nil {
			return err
		}
	} else {
		s.Manager.logger.Info("Resuming scan", "processed", progress.ProcessedFiles, "total_known", progress.TotalFiles)
	}

	// 4. Discover Files
	files, err := s.listFiles(localPath)
	if err != nil {
		return fmt.Errorf("failed to list files: %w", err)
	}
	progress.TotalFiles = len(files)

	// 5. Update State to In Progress
	if err := stateMgr.SaveState(ctx, StatusInProgress, progress, nil); err != nil {
		return err
	}

	// 6. Iterate and Process
	if err := s.runScanLoop(ctx, stateMgr, repoRecord, localPath, files, progress); err != nil {
		return err
	}

	// 7. Generate Documentation
	// 7. Generate Documentation
	artifacts, err := s.generateAndSaveDocumentation(localPath)
	if err != nil {
		s.Manager.logger.Warn("documentation generation failed", "error", err)
	}

	// 8. Multi-Model Comparison (if configured)
	if len(s.Manager.cfg.AI.ComparisonModels) > 0 {
		s.Manager.logger.Info("Starting multi-model architectural comparison", "models", s.Manager.cfg.AI.ComparisonModels)
		characteristicPaths := s.Manager.cfg.AI.ComparisonPaths
		if len(characteristicPaths) == 0 {
			characteristicPaths = []string{"."}
		}
		results, err := s.RAGService.GenerateComparisonSummaries(ctx, s.Manager.cfg.AI.ComparisonModels, localPath, characteristicPaths)
		if err != nil {
			s.Manager.logger.Warn("Multi-model comparison failed", "error", err)
		} else {
			for modelName, summaries := range results {
				sanitizedModel := strings.ReplaceAll(modelName, ":", "_")
				fileName := fmt.Sprintf("arch_comparison_%s.md", sanitizedModel)
				filePath := filepath.Join(localPath, fileName)

				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("# Architectural Comparison: %s\n\n", modelName))
				for path, summary := range summaries {
					sb.WriteString(fmt.Sprintf("## Directory: %s\n\n%s\n\n", path, summary))
				}

				if err := os.WriteFile(filePath, []byte(sb.String()), 0600); err != nil {
					s.Manager.logger.Warn("Failed to save comparison file", "file", fileName, "error", err)
				}
			}
		}
	}

	// 9. Complete & Save Artifacts
	if err := stateMgr.SaveState(ctx, StatusCompleted, progress, artifacts); err != nil {
		return err
	}
	s.Manager.logger.Info("Scan completed successfully")

	return s.updateRepoIndexVersion(ctx, localPath, repoRecord)
}

func (s *Scanner) runScanLoop(ctx context.Context, stateMgr *StateManager, repoRecord *storage.Repository, localPath string, files []string, progress *Progress) error {
	batchSize := 100
	var batch []string

	for i, file := range files {
		if progress.Files[file] {
			continue
		}

		batch = append(batch, file)

		if len(batch) >= batchSize {
			// Process Batch
			s.Manager.logger.Info("Processing batch", "size", len(batch), "current", i+1, "total", len(files))

			err := s.processBatch(ctx, stateMgr, repoRecord, localPath, &batch, progress)
			if err != nil {
				return err
			}
		}
	}

	// Flush remaining batch
	if len(batch) > 0 {
		s.Manager.logger.Info("Processing final batch", "size", len(batch))
		if err := s.processBatch(ctx, stateMgr, repoRecord, localPath, &batch, progress); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scanner) processBatch(ctx context.Context, stateMgr *StateManager, repoRecord *storage.Repository, localPath string, batch *[]string, progress *Progress) error {
	repoConfig, configErr := config.LoadRepoConfig(localPath)
	if configErr != nil && !errors.Is(configErr, config.ErrConfigNotFound) {
		s.Manager.logger.Warn("Failed to load .code-warden.yml", "error", configErr)
	}

	err := s.RAGService.UpdateRepoContext(ctx, repoConfig, repoRecord, localPath, *batch, nil)
	if err != nil {
		s.Manager.logger.Error("Failed to process batch", "error", err)
		return err
	}

	// Update Progress
	for _, f := range *batch {
		progress.Files[f] = true
		progress.ProcessedFiles++
	}
	progress.LastUpdated = time.Now()
	if err := stateMgr.SaveState(ctx, StatusInProgress, progress, nil); err != nil {
		s.Manager.logger.Warn("Failed to save state", "error", err)
	}

	*batch = nil // Reset batch
	return nil
}

func (s *Scanner) updateRepoIndexVersion(ctx context.Context, localPath string, repoRecord *storage.Repository) error {
	// 9. Update Repository LastIndexedSHA
	s.Manager.logger.Info("Updating repository index version")

	sha, err := s.Manager.GetRepoSHA(ctx, localPath)
	if err == nil {
		repoRecord.LastIndexedSHA = sha
		if err := s.Manager.store.UpdateRepository(ctx, repoRecord); err != nil {
			s.Manager.logger.Warn("Failed to update repository LastIndexedSHA", "error", err)
		} else {
			s.Manager.logger.Info("Updated synced SHA", "sha", sha)
		}
	} else {
		s.Manager.logger.Warn("Failed to determine HEAD SHA", "error", err)
	}
	return nil
}

func (s *Scanner) ensureRepoRecord(ctx context.Context, fullName, path string) (*storage.Repository, error) {
	rec, err := s.Manager.store.GetRepositoryByFullName(ctx, fullName)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		newRec := &storage.Repository{
			FullName:             fullName,
			ClonePath:            path,
			EmbedderModelName:    s.Manager.cfg.AI.EmbedderModel,
			QdrantCollectionName: strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(fullName, "/", "_"), "-", "_")+"_"+s.Manager.cfg.AI.EmbedderModel, ":", "_"),
		}
		if err := s.Manager.store.CreateRepository(ctx, newRec); err != nil {
			return nil, err
		}
		return newRec, nil
	}

	// Check for model mismatch OR collection name mismatch
	// Note: We deliberately include the full model name (sanitized) in the collection name to prevent collisions.
	sanitizedModel := strings.ReplaceAll(s.Manager.cfg.AI.EmbedderModel, ":", "_")
	expectedCollectionName := strings.ReplaceAll(strings.ReplaceAll(fullName, "/", "_"), "-", "_") + "_" + sanitizedModel

	if rec.EmbedderModelName != s.Manager.cfg.AI.EmbedderModel || rec.QdrantCollectionName != expectedCollectionName {
		s.Manager.logger.Warn("Repo configuration mismatch",
			"old_model", rec.EmbedderModelName, "new_model", s.Manager.cfg.AI.EmbedderModel,
			"old_collection", rec.QdrantCollectionName, "new_collection", expectedCollectionName)

		// Update record
		rec.EmbedderModelName = s.Manager.cfg.AI.EmbedderModel
		rec.QdrantCollectionName = expectedCollectionName

		if err := s.Manager.store.UpdateRepository(ctx, rec); err != nil {
			return nil, fmt.Errorf("failed to update repo record: %w", err)
		}

		// Reset scan state
		stateMgr := NewStateManager(s.Manager.store, rec.ID)
		emptyProgress := &Progress{
			Files:       make(map[string]bool),
			LastUpdated: time.Now(),
		}
		if err := stateMgr.SaveState(ctx, StatusPending, emptyProgress, nil); err != nil {
			s.Manager.logger.Warn("Failed to reset scan state", "error", err)
		}
	}

	return rec, nil
}

func (s *Scanner) listFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if strings.HasPrefix(name, ".") && name != "." {
				return filepath.SkipDir
			}
			return nil
		}
		// Basic extension filter
		ext := strings.ToLower(filepath.Ext(path))
		if !validExt(ext) {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, strings.ReplaceAll(rel, "\\", "/"))
		return nil
	})
	return files, err
}

func validExt(ext string) bool {
	switch ext {
	case ".go", ".js", ".ts", ".py", ".java", ".c", ".cpp", ".h", ".rs", ".md", ".json", ".yaml", ".yml":
		return true
	}
	return false
}
