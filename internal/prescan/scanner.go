package prescan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

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
	batchSize := 10
	var batch []string

	for i, file := range files {
		if progress.Files[file] {
			continue
		}

		batch = append(batch, file)

		if len(batch) >= batchSize || i == len(files)-1 {
			// Process Batch
			s.Manager.logger.Info("Processing batch", "size", len(batch), "current", i+1, "total", len(files))

			err := s.RAGService.UpdateRepoContext(ctx, nil, repoRecord, localPath, batch, nil)
			if err != nil {
				s.Manager.logger.Error("Failed to process batch", "error", err)
				return err
			}

			// Update Progress
			for _, f := range batch {
				progress.Files[f] = true
				progress.ProcessedFiles++
			}
			progress.LastUpdated = time.Now()
			if err := stateMgr.SaveState(ctx, StatusInProgress, progress, nil); err != nil {
				s.Manager.logger.Warn("Failed to save state", "error", err)
			}

			batch = nil // Reset batch
		}
	}

	// 7. Generate Documentation
	docGen := NewDocGenerator(localPath)
	structure, err := docGen.GenerateProjectStructure(localPath)
	var artifacts map[string]interface{}

	if err != nil {
		s.Manager.logger.Warn("Failed to generate project structure", "error", err)
	} else {
		// Save locally
		docPath := filepath.Join(localPath, "project_structure.md")
		if err := os.WriteFile(docPath, []byte(structure), 0644); err != nil {
			s.Manager.logger.Error("Failed to save project structure", "error", err)
		} else {
			s.Manager.logger.Info("Generated project documentation", "path", docPath)
		}

		// Prepare for DB
		artifacts = map[string]interface{}{
			"project_structure": structure,
		}
	}

	// 8. Complete & Save Artifacts
	if err := stateMgr.SaveState(ctx, StatusCompleted, progress, artifacts); err != nil {
		return err
	}
	s.Manager.logger.Info("Scan completed successfully")

	// 9. Update Repository LastIndexedSHA
	// We need the current HEAD SHA.
	// Since we cloned/fetched to localPath, we can get it from there.
	// Assuming git is installed and redundant check with internal/gitutil or just direct exec.
	// We can reuse gitutil.
	// But Scanner struct doesn't have direct access to gitutil helper methods easily for SHA.
	// We can use a simple exec command or add helper to Manager.
	// For now, let's try to read HEAD from gitutil if possible, or use exec.

	// We can use the gitutil.Cloner which we don't hold a reference to?
	// s.Manager has Cloner? No, `manager.go` has `prepareRepo`.
	// We can just run a quick git command.
	s.Manager.logger.Info("Updating repository index version")

	// Simple execution to get SHA
	cm := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cm.Dir = localPath
	shaBytes, err := cm.Output()
	if err == nil {
		sha := strings.TrimSpace(string(shaBytes))
		repoRecord.LastIndexedSHA = sha
		// Also update updated_at? Store should handle it.
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
