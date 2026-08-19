package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/gitutil"
	"github.com/sevigo/code-warden/internal/repomanager"
	"github.com/sevigo/code-warden/internal/storage"
)

type WebUIHandler struct {
	store     storage.Store
	repoMgr   repomanager.RepoManager
	gitClient *gitutil.Client
	cfg       *config.Config
	logger    *slog.Logger
}

func NewWebUIHandler(store storage.Store, repoMgr repomanager.RepoManager, gitClient *gitutil.Client, cfg *config.Config, logger *slog.Logger) *WebUIHandler {
	return &WebUIHandler{
		store:     store,
		repoMgr:   repoMgr,
		gitClient: gitClient,
		cfg:       cfg,
		logger:    logger,
	}
}

type RepositoryResponse struct {
	ID             int64  `json:"id"`
	FullName       string `json:"full_name"`
	ClonePath      string `json:"clone_path"`
	LastIndexedSHA string `json:"last_indexed_sha"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type RegisterRepoRequest struct {
	FullName string `json:"full_name"`
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
	repo := &storage.Repository{
		FullName:  req.FullName,
		ClonePath: clonePath,
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

func (h *WebUIHandler) json(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("failed to encode JSON response", "error", err)
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
	}
}

func (h *WebUIHandler) SSEEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func toRepositoryResponse(repo *storage.Repository) RepositoryResponse {
	return RepositoryResponse{
		ID:             repo.ID,
		FullName:       repo.FullName,
		ClonePath:      repo.ClonePath,
		LastIndexedSHA: repo.LastIndexedSHA,
		CreatedAt:      repo.CreatedAt.Format(time.RFC3339),
		UpdatedAt:      repo.UpdatedAt.Format(time.RFC3339),
	}
}
