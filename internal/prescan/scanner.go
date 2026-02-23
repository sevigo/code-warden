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
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/rag"
	"github.com/sevigo/code-warden/internal/storage"
)

// Scanner handles the resumable scanning process.
type Scanner struct {
	Manager    *Manager
	RAGService rag.Service
	Verbose    bool
	startTime  time.Time
}

func NewScanner(m *Manager, rag rag.Service) *Scanner {
	return &Scanner{
		Manager:    m,
		RAGService: rag,
	}
}

func (s *Scanner) generateAndSaveDocumentation(localPath string) (map[string]any, error) {
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
	}
	s.Manager.logger.Info("Generated project documentation", "path", docPath)

	// Prepare for DB
	return map[string]any{
		"project_structure": structure,
	}, nil
}

func (s *Scanner) Scan(ctx context.Context, input string, force bool, verbose bool) error {
	s.Verbose = verbose
	s.startTime = time.Now()

	// 1. Prepare Repo (Clone if needed)
	localPath, owner, repo, err := s.Manager.PrepareRepo(ctx, input)
	if err != nil {
		return err
	}
	repoFullName := fmt.Sprintf("%s/%s", owner, repo)

	s.printMetadata(repoFullName, localPath)
	s.Manager.logger.Info("Starting scan", "repo", repoFullName, "path", localPath)

	// 2. Ensure Repo Record in DB
	repoRecord, err := s.ensureRepoRecord(ctx, repoFullName, localPath)
	if err != nil {
		return err
	}
	s.printCollection(repoRecord.QdrantCollectionName)

	// 3. Load State & Initialize Progress
	stateMgr := NewStateManager(s.Manager.store, repoRecord.ID)
	scanState, progress, err := stateMgr.LoadState(ctx)
	if err != nil {
		return err
	}

	if force || scanState == nil || scanState.Status == string(StatusCompleted) || scanState.Status == string(StatusFailed) {
		s.Manager.logger.Info("Starting fresh scan")
		progress = &Progress{Files: make(map[string]bool), LastUpdated: time.Now()}
		if err := stateMgr.SaveState(ctx, StatusPending, progress, nil); err != nil {
			return err
		}
	} else {
		s.Manager.logger.Info("Resuming scan", "processed", progress.ProcessedFiles, "total_known", progress.TotalFiles)
	}

	// 4. Discover Files (filtered by .code-warden.yml if present)
	repoConfig, _ := config.LoadRepoConfig(localPath)
	files, err := s.listFiles(localPath, repoConfig)
	if err != nil {
		return fmt.Errorf("failed to list files: %w", err)
	}
	progress.TotalFiles = len(files)

	if err := stateMgr.SaveState(ctx, StatusInProgress, progress, nil); err != nil {
		return err
	}

	// 5. Run main processing loop
	if err := s.runScanLoop(ctx, stateMgr, repoRecord, localPath, files, progress, repoConfig); err != nil {
		return err
	}

	// 6. Post-processing: Documentation & Comparisons
	docMap, _ := s.generateAndSaveDocumentation(localPath)
	s.generateArchitecturalComparisons(ctx, localPath)

	if err := stateMgr.SaveState(ctx, StatusCompleted, progress, docMap); err != nil {
		return err
	}

	s.printSummary(s.startTime, progress.ProcessedFiles)
	s.Manager.logger.Info("Scan completed successfully")

	return s.updateRepoIndexVersion(ctx, localPath, repoRecord)
}

func (s *Scanner) generateArchitecturalComparisons(ctx context.Context, localPath string) {
	if len(s.Manager.cfg.AI.ComparisonModels) == 0 {
		return
	}

	s.Manager.logger.Info("Generating architectural comparisons", "models", s.Manager.cfg.AI.ComparisonModels)

	validatedPath, err := s.validateRepoPath(s.Manager.cfg.Storage.RepoPath, localPath)
	if err != nil {
		s.Manager.logger.Warn("Skipping comparisons: invalid path", "error", err)
		return
	}

	characteristicPaths := s.Manager.cfg.AI.ComparisonPaths
	if len(characteristicPaths) == 0 {
		characteristicPaths = []string{"."}
	}

	results, err := s.RAGService.GenerateComparisonSummaries(ctx, s.Manager.cfg.AI.ComparisonModels, validatedPath, characteristicPaths)
	if err != nil {
		s.Manager.logger.Warn("Multi-model comparison failed", "error", err)
		return
	}

	for modelName, summaries := range results {
		sanitizedModel := rag.SanitizeModelForFilename(modelName)
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

func (s *Scanner) printMetadata(repoFullName, localPath string) {
	if s.Verbose {
		s.Manager.logger.Info("Starting Pre-scan",
			"repo", repoFullName,
			"path", localPath,
			"embedder", s.Manager.cfg.AI.EmbedderModel,
			"generator", s.Manager.cfg.AI.GeneratorModel,
			"features", map[string]bool{
				"Hybrid": s.Manager.cfg.AI.EnableHybrid,
				"Rerank": s.Manager.cfg.AI.EnableReranking,
				"HyDE":   s.Manager.cfg.AI.EnableHyDE,
			},
		)
	}
}

func (s *Scanner) printCollection(collectionName string) {
	if s.Verbose {
		fmt.Printf("   📊 Collection: %s\n\n", collectionName)
	}
}

func (s *Scanner) printSummary(startTime time.Time, processedFiles int) {
	if s.Verbose {
		duration := time.Since(startTime)
		filesPerMin := float64(processedFiles) / duration.Minutes()
		fmt.Printf("\n✨ Scan complete in %s\n", duration.Round(time.Second))
		fmt.Printf("   Total Files: %d\n", processedFiles)
		fmt.Printf("   Performance: %.1f files/min\n", filesPerMin)
	}
}

func (s *Scanner) runScanLoop(ctx context.Context, stateMgr *StateManager, repoRecord *storage.Repository, localPath string, files []string, progress *Progress, repoConfig *core.RepoConfig) error {
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

			err := s.processBatch(ctx, stateMgr, repoRecord, localPath, &batch, progress, repoConfig)
			if err != nil {
				return err
			}
		}
	}

	// Flush remaining batch
	if len(batch) > 0 {
		s.Manager.logger.Info("Processing final batch", "size", len(batch))
		if err := s.processBatch(ctx, stateMgr, repoRecord, localPath, &batch, progress, repoConfig); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scanner) processBatch(ctx context.Context, stateMgr *StateManager, repoRecord *storage.Repository, localPath string, batch *[]string, progress *Progress, repoConfig *core.RepoConfig) error {
	batchStartTime := time.Now()
	err := s.RAGService.UpdateRepoContext(ctx, repoConfig, repoRecord, localPath, *batch, nil)
	if err != nil {
		s.Manager.logger.Error("Failed to process batch", "error", err)
		return err
	}
	batchDuration := time.Since(batchStartTime)

	// Update Progress
	for _, f := range *batch {
		if s.Verbose {
			fmt.Printf("   [%d/%d] Indexing %s\n", progress.ProcessedFiles+1, progress.TotalFiles, f)
		}
		progress.Files[f] = true
		progress.ProcessedFiles++
	}

	if s.Verbose {
		fmt.Printf("   ⚡ Batch finish: %s", batchDuration.Round(time.Millisecond))
		if progress.ProcessedFiles > 0 {
			elapsed := time.Since(s.startTime)
			avgPerFileOverall := elapsed / time.Duration(progress.ProcessedFiles)
			remaining := progress.TotalFiles - progress.ProcessedFiles
			totalRemainingTime := avgPerFileOverall * time.Duration(remaining)
			fmt.Printf(" (ETA: %s)", totalRemainingTime.Round(time.Second))
		}
		fmt.Println()
	}
	progress.LastUpdated = time.Now()
	if err := stateMgr.SaveState(ctx, StatusInProgress, progress, nil); err != nil {
		s.Manager.logger.Warn("Failed to save state", "error", err)
	}

	*batch = nil // Reset batch
	return nil
}

func (s *Scanner) updateRepoIndexVersion(ctx context.Context, localPath string, repoRecord *storage.Repository) error {
	// Update Repository LastIndexedSHA
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
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	if errors.Is(err, storage.ErrNotFound) {
		rec = nil
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

func (s *Scanner) listFiles(root string, repoConfig *core.RepoConfig) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = strings.ReplaceAll(rel, "\\", "/")

		if info.IsDir() {
			if s.shouldExcludeDir(info.Name(), repoConfig) {
				return filepath.SkipDir
			}
			return nil
		}

		if s.shouldExcludeFile(rel, path, repoConfig) {
			return nil
		}

		files = append(files, rel)
		return nil
	})
	return files, err
}

func (s *Scanner) shouldExcludeDir(name string, repoConfig *core.RepoConfig) bool {
	if strings.HasPrefix(name, ".") && name != "." {
		return true
	}
	for _, excludeDir := range repoConfig.ExcludeDirs {
		if name == excludeDir {
			return true
		}
	}
	return false
}

func (s *Scanner) shouldExcludeFile(rel, absPath string, repoConfig *core.RepoConfig) bool {
	ext := strings.ToLower(filepath.Ext(absPath))
	if !validExt(ext) {
		return true
	}

	// Filter by RepoConfig extensions
	for _, excludeExt := range repoConfig.ExcludeExts {
		normalizedExt := strings.ToLower(strings.TrimPrefix(excludeExt, "."))
		if strings.TrimPrefix(ext, ".") == normalizedExt {
			return true
		}
	}

	// Filter by RepoConfig specific files
	for _, excludeFile := range repoConfig.ExcludeFiles {
		if rel == strings.ReplaceAll(excludeFile, "\\", "/") {
			return true
		}
	}

	return false
}

func validExt(ext string) bool {
	switch ext {
	case ".go", ".js", ".ts", ".py", ".java", ".c", ".cpp", ".h", ".rs", ".md", ".json", ".yaml", ".yml":
		return true
	}
	return false
}

// validateRepoPath ensures that a provided path stays within a base directory, resolving symlinks for security.
func (s *Scanner) validateRepoPath(basePath, providedPath string) (string, error) {
	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute base path: %w", err)
	}
	// Resolve symlinks for base path too (e.g. handles macOS /var -> /private/var)
	if resolvedBase, err := filepath.EvalSymlinks(absBase); err == nil {
		absBase = resolvedBase
	}

	absPath, err := s.resolveAbsolutePath(absBase, providedPath)
	if err != nil {
		return "", err
	}

	// Resolve symlinks - fail on ANY error that indicates security issue (Priority 2)
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("symlink resolution failed (possible traversal): %w", err)
		}
		// Path doesn't exist yet - validate the closest existing parent (Priority 2)
		absPath, err = s.resolveMissingPathTarget(absPath)
		if err != nil {
			return "", err
		}
	} else {
		absPath = resolvedPath
	}

	// Sec: Robust containment check using Rel (Deepseek review suggestion)
	rel, err := filepath.Rel(absBase, absPath)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("provided path %q is outside of the repository base path", providedPath)
	}

	return absPath, nil
}

func (s *Scanner) resolveAbsolutePath(absBase, providedPath string) (string, error) {
	absPath := providedPath
	if !filepath.IsAbs(providedPath) {
		absPath = filepath.Join(absBase, providedPath)
	}
	res, err := filepath.Abs(absPath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}
	return res, nil
}

func (s *Scanner) resolveMissingPathTarget(absPath string) (string, error) {
	curr := absPath
	var missing []string
	for {
		parent := filepath.Dir(curr)
		if parent == curr || parent == "." || parent == "/" || parent == filepath.VolumeName(parent) {
			break
		}
		if resolved, pErr := filepath.EvalSymlinks(parent); pErr == nil {
			// Found the first existing parent! Reconstruct the path from it downwards.
			res := filepath.Join(resolved, filepath.Base(curr))
			for i := len(missing) - 1; i >= 0; i-- {
				res = filepath.Join(res, missing[i])
			}
			return res, nil
		} else if !os.IsNotExist(pErr) {
			return "", fmt.Errorf("parent path validation failed: %w", pErr)
		}
		missing = append(missing, filepath.Base(curr))
		curr = parent
	}
	return absPath, nil
}
