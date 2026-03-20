package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/github"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/rag"
	indexpkg "github.com/sevigo/code-warden/internal/rag/index"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/storage"
)

type WebUIHandler struct {
	store      storage.Store
	ragService rag.Service
	repoMgr    repomanager.RepoManager
	gitClient  *gitutil.Client
	cfg        *config.Config
	logger     *slog.Logger
}

func NewWebUIHandler(store storage.Store, ragService rag.Service, repoMgr repomanager.RepoManager, gitClient *gitutil.Client, cfg *config.Config, logger *slog.Logger) *WebUIHandler {
	return &WebUIHandler{
		store:      store,
		ragService: ragService,
		repoMgr:    repoMgr,
		gitClient:  gitClient,
		cfg:        cfg,
		logger:     logger,
	}
}

type RepositoryResponse struct {
	ID                   int64  `json:"id"`
	FullName             string `json:"full_name"`
	ClonePath            string `json:"clone_path"`
	QdrantCollectionName string `json:"qdrant_collection_name"`
	LastIndexedSHA       string `json:"last_indexed_sha"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

type ScanStateResponse struct {
	ID           int64          `json:"id"`
	RepositoryID int64          `json:"repository_id"`
	Status       string         `json:"status"`
	Progress     *ProgressInfo  `json:"progress,omitempty"`
	Artifacts    *ArtifactsInfo `json:"artifacts,omitempty"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
}

type ProgressInfo struct {
	FilesTotal  int    `json:"files_total"`
	FilesDone   int    `json:"files_done"`
	Stage       string `json:"stage"`
	CurrentFile string `json:"current_file,omitempty"`
}

type ArtifactsInfo struct {
	ChunksCount int    `json:"chunks_count"`
	IndexedAt   string `json:"indexed_at"`
}

type RegisterRepoRequest struct {
	FullName string `json:"full_name"`
}

type ChatRequest struct {
	Question string   `json:"question"`
	History  []string `json:"history"`
}

type ChatResponse struct {
	Answer string `json:"answer"`
}

type ExplainRequest struct {
	Path string `json:"path"`
}

type ExplainResponse struct {
	Content string `json:"content"`
}

func parseRepoID(r *http.Request) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(chi.URLParam(r, "repoId"), "%d", &id)
	return id, err
}

type RepoStatsResponse struct {
	ChunksCount    int    `json:"chunks_count"`
	FilesCount     int    `json:"files_count"`
	LastIndexedSHA string `json:"last_indexed_sha"`
	LastScanDate   string `json:"last_scan_date"`
}

func (h *WebUIHandler) GetRepoStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repoID, err := parseRepoID(r)
	if err != nil {
		http.Error(w, "invalid repo id", http.StatusBadRequest)
		return
	}

	repo, err := h.store.GetRepositoryByID(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to get repository", http.StatusInternalServerError)
		return
	}

	stats := RepoStatsResponse{
		LastIndexedSHA: repo.LastIndexedSHA,
	}

	// File count
	files, err := h.store.GetFilesForRepo(ctx, repo.ID)
	if err == nil {
		stats.FilesCount = len(files)
	}

	// Chunk count and last scan date from scan state
	scanState, err := h.store.GetScanState(ctx, repo.ID)
	if err == nil && scanState != nil {
		stats.LastScanDate = scanState.UpdatedAt.Format(time.RFC3339)
		if scanState.Artifacts != nil {
			var artifacts ArtifactsInfo
			if json.Unmarshal(*scanState.Artifacts, &artifacts) == nil {
				stats.ChunksCount = artifacts.ChunksCount
			}
		}
	}

	h.json(w, stats)
}

func (h *WebUIHandler) ListRepos(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repos, err := h.store.GetAllRepositories(ctx)
	if err != nil {
		h.logger.Error("failed to list repositories", "error", err)
		http.Error(w, "failed to list repositories", http.StatusInternalServerError)
		return
	}

	response := make([]RepositoryResponse, len(repos))
	for i, repo := range repos {
		response[i] = toRepositoryResponse(repo)
	}

	h.json(w, response)
}

func (h *WebUIHandler) GetRepo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repoIDStr := chi.URLParam(r, "repoId")
	var repoID int64
	if _, err := fmt.Sscanf(repoIDStr, "%d", &repoID); err != nil {
		http.Error(w, "invalid repo id", http.StatusBadRequest)
		return
	}

	repo, err := h.store.GetRepositoryByID(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get repository", "error", err)
		http.Error(w, "failed to get repository", http.StatusInternalServerError)
		return
	}

	h.json(w, toRepositoryResponse(repo))
}

func (h *WebUIHandler) RegisterRepo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req RegisterRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.FullName == "" {
		http.Error(w, "full_name is required", http.StatusBadRequest)
		return
	}

	clonePath := filepath.Join(h.cfg.Storage.RepoPath, req.FullName)
	collectionName := repomanager.GenerateCollectionName(req.FullName)
	repo := &storage.Repository{
		FullName:             req.FullName,
		ClonePath:            clonePath,
		QdrantCollectionName: collectionName,
	}

	if err := h.store.CreateRepository(ctx, repo); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			http.Error(w, fmt.Sprintf("repository %q already exists", req.FullName), http.StatusConflict)
			return
		}
		h.logger.Error("failed to create repository", "error", err)
		http.Error(w, "failed to create repository", http.StatusInternalServerError)
		return
	}

	h.json(w, toRepositoryResponse(repo))
}

func (h *WebUIHandler) GetScanStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repoIDStr := chi.URLParam(r, "repoId")
	var repoID int64
	if _, err := fmt.Sscanf(repoIDStr, "%d", &repoID); err != nil {
		http.Error(w, "invalid repo id", http.StatusBadRequest)
		return
	}

	state, err := h.store.GetScanState(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			h.json(w, nil)
			return
		}
		h.logger.Error("failed to get scan state", "error", err)
		http.Error(w, "failed to get scan state", http.StatusInternalServerError)
		return
	}

	h.json(w, toScanStateResponse(state))
}

func isPlaceholderToken(token string) bool {
	return token == "" || strings.HasPrefix(token, "ghp_your_")
}

func (h *WebUIHandler) TriggerScan(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repoID, err := parseRepoID(r)
	if err != nil {
		http.Error(w, "invalid repo id", http.StatusBadRequest)
		return
	}

	repo, err := h.store.GetRepositoryByID(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get repository", "error", err)
		http.Error(w, "failed to get repository", http.StatusInternalServerError)
		return
	}

	initialProgress, _ := json.Marshal(ProgressInfo{Stage: "scanning"})
	if err := h.store.UpsertScanState(ctx, &storage.ScanState{
		RepositoryID: repoID,
		Status:       "scanning",
		Progress:     initialProgress,
	}); err != nil {
		h.logger.Error("failed to create scan state", "error", err)
		http.Error(w, "failed to trigger scan", http.StatusInternalServerError)
		return
	}

	// The goroutine must outlive the HTTP request, so we use a fresh context
	// rather than r.Context() which is cancelled once the response is sent.
	go h.runScan(repoID, repo) //nolint:gosec // intentional: scan outlives the request

	w.WriteHeader(http.StatusAccepted)
	h.json(w, map[string]string{"status": "scanning"})
}

// runScan performs the full RAG indexing pipeline in the background.
// It owns its own context with a 30-minute timeout.
func (h *WebUIHandler) runScan(repoID int64, repo *storage.Repository) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if err := h.doScan(ctx, repoID, repo); err != nil {
		h.logger.Error("scan failed", "repo", repo.FullName, "error", err)
		h.setScanFailed(ctx, repoID)
	}
}

func (h *WebUIHandler) doScan(ctx context.Context, repoID int64, repo *storage.Repository) error {
	// Check if repo exists locally, clone if needed
	if _, err := os.Stat(repo.ClonePath); os.IsNotExist(err) {
		h.logger.Info("Repository not found locally, cloning", "repo", repo.FullName, "path", repo.ClonePath)

		// 1. Determine which token to use
		token := h.cfg.GitHub.Token
		if isPlaceholderToken(token) {
			token = ""
		}
		if repo.InstallationID > 0 {
			_, instToken, err := github.CreateInstallationClient(ctx, h.cfg, repo.InstallationID, h.logger)
			if err != nil {
				h.logger.Warn("failed to create installation client for manual scan, falling back to config token",
					"repo", repo.FullName,
					"installation_id", repo.InstallationID,
					"error", err)
			} else {
				token = instToken
			}
		}

		cloneURL := fmt.Sprintf("https://github.com/%s.git", repo.FullName)
		if err := os.MkdirAll(filepath.Dir(repo.ClonePath), 0750); err != nil {
			return fmt.Errorf("create parent dir: %w", err)
		}
		if _, err := h.gitClient.Clone(ctx, cloneURL, repo.ClonePath, token); err != nil {
			return fmt.Errorf("clone repository: %w", err)
		}
	}

	scanResult, err := h.repoMgr.ScanLocalRepo(ctx, repo.ClonePath, repo.FullName, true)
	if err != nil {
		return fmt.Errorf("scan local repo: %w", err)
	}

	repoConfig, err := config.LoadRepoConfig(scanResult.RepoPath)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			h.logger.Info("no .code-warden.yml found, using defaults", "repo", scanResult.RepoFullName)
		} else {
			h.logger.Warn("failed to parse .code-warden.yml, using defaults", "error", err, "repo", scanResult.RepoFullName)
		}
		repoConfig = core.DefaultRepoConfig()
	}

	repoRecord, err := h.store.GetRepositoryByID(ctx, repoID)
	if err != nil {
		return fmt.Errorf("reload repo record: %w", err)
	}

	progressFn := h.makeScanProgressFn(ctx, repoID)
	if err := h.ragService.SetupRepoContext(ctx, repoConfig, repoRecord, scanResult.RepoPath, progressFn); err != nil {
		return fmt.Errorf("RAG setup: %w", err)
	}

	artifactsJSON, _ := json.Marshal(ArtifactsInfo{ChunksCount: 0, IndexedAt: time.Now().Format(time.RFC3339)})
	artifactsRaw := json.RawMessage(artifactsJSON)
	doneProgress, _ := json.Marshal(ProgressInfo{Stage: "completed"})
	if err := h.store.UpsertScanState(ctx, &storage.ScanState{
		RepositoryID: repoID,
		Status:       "completed",
		Progress:     doneProgress,
		Artifacts:    &artifactsRaw,
	}); err != nil {
		h.logger.Error("failed to update scan state to completed", "error", err)
	}

	if err := h.repoMgr.UpdateRepoSHA(ctx, repo.FullName, scanResult.HeadSHA); err != nil {
		h.logger.Warn("failed to update repo SHA after scan", "error", err)
	}
	return nil
}

func (h *WebUIHandler) setScanFailed(ctx context.Context, repoID int64) {
	failProgress, _ := json.Marshal(ProgressInfo{Stage: "failed"})
	if err := h.store.UpsertScanState(ctx, &storage.ScanState{
		RepositoryID: repoID,
		Status:       "failed",
		Progress:     failProgress,
	}); err != nil {
		h.logger.Error("failed to update scan state to failed", "error", err)
	}
}

// makeScanProgressFn returns a ProgressFunc that writes indexing progress to
// scan_state every time it is called (the indexer calls it every ~10 files).
// Uses a detached background context so the write survives after the request
// context is cancelled (scan runs in a goroutine that owns its own context).
func (h *WebUIHandler) makeScanProgressFn(ctx context.Context, repoID int64) indexpkg.ProgressFunc {
	return func(done, total int) {
		p, _ := json.Marshal(ProgressInfo{
			Stage:      "indexing",
			FilesDone:  done,
			FilesTotal: total,
		})
		if err := h.store.UpsertScanState(ctx, &storage.ScanState{
			RepositoryID: repoID,
			Status:       "scanning",
			Progress:     p,
		}); err != nil {
			h.logger.Warn("failed to write scan progress", "error", err)
		}
	}
}

func (h *WebUIHandler) Chat(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repoIDStr := chi.URLParam(r, "repoId")
	var repoID int64
	if _, err := fmt.Sscanf(repoIDStr, "%d", &repoID); err != nil {
		http.Error(w, "invalid repo id", http.StatusBadRequest)
		return
	}

	repo, err := h.store.GetRepositoryByID(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get repository", "error", err)
		http.Error(w, "failed to get repository", http.StatusInternalServerError)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Question == "" {
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}

	answer, err := h.ragService.AnswerQuestion(ctx, repo.QdrantCollectionName, h.cfg.AI.EmbedderModel, req.Question, req.History)
	if err != nil {
		h.logger.Error("failed to answer question", "error", err)
		http.Error(w, "failed to answer question", http.StatusInternalServerError)
		return
	}

	h.json(w, ChatResponse{Answer: answer})
}

func (h *WebUIHandler) Explain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repoIDStr := chi.URLParam(r, "repoId")
	var repoID int64
	if _, err := fmt.Sscanf(repoIDStr, "%d", &repoID); err != nil {
		http.Error(w, "invalid repo id", http.StatusBadRequest)
		return
	}

	repo, err := h.store.GetRepositoryByID(ctx, repoID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get repository", "error", err)
		http.Error(w, "failed to get repository", http.StatusInternalServerError)
		return
	}

	var req ExplainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	content, err := h.ragService.ExplainPath(ctx, repo.QdrantCollectionName, h.cfg.AI.EmbedderModel, req.Path)
	if err != nil {
		h.logger.Error("failed to explain path", "error", err)
		http.Error(w, "failed to explain path", http.StatusInternalServerError)
		return
	}

	h.json(w, ExplainResponse{Content: content})
}

func (h *WebUIHandler) SSEEvents(w http.ResponseWriter, r *http.Request) {
	repoIDStr := r.URL.Query().Get("repo_id")
	var repoID int64
	if _, err := fmt.Sscanf(repoIDStr, "%d", &repoID); err != nil {
		http.Error(w, "invalid repo_id", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state, err := h.store.GetScanState(ctx, repoID)
			if err != nil {
				continue
			}

			data, _ := json.Marshal(toScanStateResponse(state))
			fmt.Fprintf(w, "event: scan\ndata: %s\n\n", data)
			flusher.Flush()

			if state.Status == "completed" || state.Status == "failed" {
				return
			}
		}
	}
}

func (h *WebUIHandler) json(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("failed to encode JSON response", "error", err)
	}
}

func toRepositoryResponse(repo *storage.Repository) RepositoryResponse {
	return RepositoryResponse{
		ID:                   repo.ID,
		FullName:             repo.FullName,
		ClonePath:            repo.ClonePath,
		QdrantCollectionName: repo.QdrantCollectionName,
		LastIndexedSHA:       repo.LastIndexedSHA,
		CreatedAt:            repo.CreatedAt.Format(time.RFC3339),
		UpdatedAt:            repo.UpdatedAt.Format(time.RFC3339),
	}
}

func toScanStateResponse(state *storage.ScanState) *ScanStateResponse {
	if state == nil {
		return nil
	}

	resp := &ScanStateResponse{
		ID:           state.ID,
		RepositoryID: state.RepositoryID,
		Status:       state.Status,
		CreatedAt:    state.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    state.UpdatedAt.Format(time.RFC3339),
	}

	if len(state.Progress) > 0 {
		var progress ProgressInfo
		if json.Unmarshal(state.Progress, &progress) == nil {
			resp.Progress = &progress
		}
	}

	if state.Artifacts != nil {
		var artifacts ArtifactsInfo
		if json.Unmarshal(*state.Artifacts, &artifacts) == nil {
			resp.Artifacts = &artifacts
		}
	}

	return resp
}
