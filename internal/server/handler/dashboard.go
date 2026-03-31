package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/storage"
)

// DashboardHandler serves dashboard, stats, reviews, jobs, and config endpoints.
// All data is currently mocked — wire to real services in a follow-up.
type DashboardHandler struct {
	cfg    *config.Config
	store  storage.Store
	logger *slog.Logger
}

func NewDashboardHandler(cfg *config.Config, store storage.Store, logger *slog.Logger) *DashboardHandler {
	return &DashboardHandler{cfg: cfg, store: store, logger: logger}
}

func (h *DashboardHandler) json(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("failed to encode JSON response", "error", err)
	}
}

// ── Setup & Config ──────────────────────────────────────────────────────────

func (h *DashboardHandler) SetupStatus(w http.ResponseWriter, _ *http.Request) {
	configured := h.cfg.GitHub.AppID != 0
	appName := "Code Warden"
	if !configured {
		appName = ""
	}

	h.json(w, map[string]any{
		"github_app": map[string]any{
			"configured":  configured,
			"app_id":      h.cfg.GitHub.AppID,
			"app_name":    appName,
			"install_url": "https://github.com/apps/code-warden/installations/new",
		},
		"services": map[string]any{
			"database": map[string]any{"status": "ok", "latency_ms": 2},
			"qdrant":   map[string]any{"status": "ok", "latency_ms": 5},
		},
		"ready": configured,
	})
}

func (h *DashboardHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	h.json(w, map[string]any{
		"ai": map[string]any{
			"llm_provider":    h.cfg.AI.LLMProvider,
			"generator_model": h.cfg.AI.GeneratorModel,
			"embedder_model":  h.cfg.AI.EmbedderModel,
		},
		"github": map[string]any{
			"app_id":             h.cfg.GitHub.AppID,
			"webhook_configured": h.cfg.GitHub.WebhookSecret != "",
		},
		"storage": map[string]any{
			"qdrant_host": h.cfg.Storage.QdrantHost,
		},
	})
}

// ── Global Stats ────────────────────────────────────────────────────────────

func (h *DashboardHandler) GlobalStats(w http.ResponseWriter, _ *http.Request) {
	h.json(w, map[string]any{
		"total_repos":       3,
		"indexed_repos":     2,
		"total_reviews":     12,
		"reviews_this_week": 3,
		"total_findings":    47,
		"findings_by_severity": map[string]int{
			"critical":   2,
			"warning":    18,
			"suggestion": 27,
		},
		"avg_findings_per_review": 3.9,
		"jobs_running":            0,
		"jobs_queued":             0,
	})
}

// ── Jobs ────────────────────────────────────────────────────────────────────

func (h *DashboardHandler) ListJobs(w http.ResponseWriter, _ *http.Request) {
	h.json(w, []map[string]any{
		{
			"id":             "job_001",
			"type":           "review",
			"repo_full_name": "sevigo/code-warden",
			"pr_number":      42,
			"status":         "completed",
			"triggered_by":   "webhook:/review",
			"triggered_at":   "2026-03-29T14:20:00Z",
			"completed_at":   "2026-03-29T14:23:00Z",
			"duration_ms":    180000,
		},
		{
			"id":             "job_002",
			"type":           "scan",
			"repo_full_name": "sevigo/code-warden",
			"pr_number":      0,
			"status":         "completed",
			"triggered_by":   "ui:manual",
			"triggered_at":   "2026-03-29T10:05:00Z",
			"completed_at":   "2026-03-29T10:18:00Z",
			"duration_ms":    780000,
		},
		{
			"id":             "job_003",
			"type":           "review",
			"repo_full_name": "sevigo/code-warden",
			"pr_number":      41,
			"status":         "completed",
			"triggered_by":   "webhook:/review",
			"triggered_at":   "2026-03-28T09:08:00Z",
			"completed_at":   "2026-03-28T09:11:00Z",
			"duration_ms":    162000,
		},
		{
			"id":             "job_004",
			"type":           "review",
			"repo_full_name": "sevigo/code-warden",
			"pr_number":      39,
			"status":         "completed",
			"triggered_by":   "webhook:/review",
			"triggered_at":   "2026-03-25T16:43:00Z",
			"completed_at":   "2026-03-25T16:45:00Z",
			"duration_ms":    130000,
		},
		{
			"id":             "job_005",
			"type":           "implement",
			"repo_full_name": "sevigo/code-warden",
			"pr_number":      0,
			"status":         "failed",
			"triggered_by":   "webhook:/implement",
			"triggered_at":   "2026-03-24T11:30:00Z",
			"completed_at":   "2026-03-24T11:35:00Z",
			"duration_ms":    300000,
		},
		{
			"id":             "job_006",
			"type":           "scan",
			"repo_full_name": "sevigo/karakuri-os",
			"pr_number":      0,
			"status":         "completed",
			"triggered_by":   "ui:manual",
			"triggered_at":   "2026-03-22T08:00:00Z",
			"completed_at":   "2026-03-22T08:22:00Z",
			"duration_ms":    1320000,
		},
		{
			"id":             "job_007",
			"type":           "rereview",
			"repo_full_name": "sevigo/karakuri-os",
			"pr_number":      17,
			"status":         "completed",
			"triggered_by":   "webhook:/rereview",
			"triggered_at":   "2026-03-21T13:10:00Z",
			"completed_at":   "2026-03-21T13:13:00Z",
			"duration_ms":    190000,
		},
		{
			"id":             "job_008",
			"type":           "review",
			"repo_full_name": "sevigo/karakuri-os",
			"pr_number":      16,
			"status":         "completed",
			"triggered_by":   "webhook:/review",
			"triggered_at":   "2026-03-20T09:55:00Z",
			"completed_at":   "2026-03-20T09:58:00Z",
			"duration_ms":    145000,
		},
	})
}

// ── Reviews ─────────────────────────────────────────────────────────────────

func (h *DashboardHandler) ListReviews(w http.ResponseWriter, _ *http.Request) {
	h.json(w, []map[string]any{
		{
			"id":        1,
			"pr_number": 42,
			"pr_title":  "feat: add multi-level summary enhancements for vector indexing",
			"head_sha":  "a3f1c9d",
			"status":    "reviewed",
			"severity_counts": map[string]int{
				"critical":   0,
				"warning":    2,
				"suggestion": 5,
			},
			"total_findings": 7,
			"reviewed_at":    "2026-03-29T14:23:00Z",
			"created_at":     "2026-03-29T14:20:00Z",
		},
		{
			"id":        2,
			"pr_number": 41,
			"pr_title":  "fix: regenerate package summaries on incremental updates",
			"head_sha":  "b8e2f4a",
			"status":    "reviewed",
			"severity_counts": map[string]int{
				"critical":   1,
				"warning":    3,
				"suggestion": 2,
			},
			"total_findings": 6,
			"reviewed_at":    "2026-03-28T09:11:00Z",
			"created_at":     "2026-03-28T09:08:00Z",
		},
		{
			"id":        3,
			"pr_number": 39,
			"pr_title":  "chore: update goframe to v0.36.5 for new metadata indexes",
			"head_sha":  "c7d3a1b",
			"status":    "reviewed",
			"severity_counts": map[string]int{
				"critical":   0,
				"warning":    0,
				"suggestion": 3,
			},
			"total_findings": 3,
			"reviewed_at":    "2026-03-25T16:45:00Z",
			"created_at":     "2026-03-25T16:43:00Z",
		},
	})
}

func (h *DashboardHandler) GetReview(w http.ResponseWriter, _ *http.Request) {
	h.json(w, map[string]any{
		"id":        1,
		"pr_number": 42,
		"pr_title":  "feat: add multi-level summary enhancements for vector indexing",
		"head_sha":  "a3f1c9d8e5b2",
		"status":    "reviewed",
		"severity_counts": map[string]int{
			"critical":   0,
			"warning":    2,
			"suggestion": 5,
		},
		"findings": []map[string]any{
			{
				"id":          "f1",
				"severity":    "warning",
				"category":    "Logic",
				"file":        "internal/rag/index/summarizer.go",
				"line_start":  142,
				"line_end":    158,
				"title":       "Missing nil check before dereferencing pointer",
				"description": "The `parent` pointer is dereferenced without checking if it's nil first. If the parent node doesn't exist in the tree, this will cause a nil pointer dereference panic at runtime.",
				"suggestion":  "Add `if parent == nil { return }` before the dereference.",
			},
			{
				"id":          "f2",
				"severity":    "warning",
				"category":    "Performance",
				"file":        "internal/rag/contextpkg/builder.go",
				"line_start":  87,
				"line_end":    92,
				"title":       "Redundant DB query inside loop",
				"description": "GetRepositoryByID is called inside a loop over chunks, causing N+1 queries. The repository data is constant within the loop and should be fetched once before it.",
				"suggestion":  "Move the GetRepositoryByID call outside the loop.",
			},
			{
				"id":          "f3",
				"severity":    "suggestion",
				"category":    "Style",
				"file":        "internal/rag/index/toc.go",
				"line_start":  34,
				"line_end":    34,
				"title":       "Consider using a named constant",
				"description": "The magic number 1024 appears without explanation. A named constant improves readability and makes future changes safer.",
				"suggestion":  "Define `const maxTOCEntries = 1024` near the top of the file.",
			},
			{
				"id":          "f4",
				"severity":    "suggestion",
				"category":    "Documentation",
				"file":        "internal/rag/service.go",
				"line_start":  201,
				"line_end":    210,
				"title":       "Exported function missing doc comment",
				"description": "UpdateRepoContext is exported but has no doc comment. Adding one improves discoverability with `go doc`.",
				"suggestion":  "Add: `// UpdateRepoContext incrementally updates the vector index for the given repository.`",
			},
			{
				"id":          "f5",
				"severity":    "suggestion",
				"category":    "Testing",
				"file":        "internal/rag/index/summarizer.go",
				"line_start":  0,
				"line_end":    0,
				"title":       "No unit tests for new summarizer logic",
				"description": "The multi-level summary logic added in this PR has no corresponding unit tests. Edge cases like empty trees and single-child nodes are unverified.",
				"suggestion":  "Add tests in internal/rag/index/summarizer_test.go covering at least empty input, single file, and deep nesting.",
			},
			{
				"id":          "f6",
				"severity":    "suggestion",
				"category":    "Style",
				"file":        "internal/rag/contextpkg/builder.go",
				"line_start":  115,
				"line_end":    118,
				"title":       "Unnecessary type assertion",
				"description": "The type assertion `.(string)` on line 116 is redundant — the variable is already typed as string by the surrounding code.",
				"suggestion":  "Remove the type assertion.",
			},
			{
				"id":          "f7",
				"severity":    "suggestion",
				"category":    "Style",
				"file":        "internal/rag/index/toc.go",
				"line_start":  67,
				"line_end":    71,
				"title":       "Extract repeated transformation into helper",
				"description": "Lines 67-71 perform the same string transformation in 3 separate places. Extracting this into a helper reduces duplication and makes future changes easier.",
				"suggestion":  "Extract a `formatEntry(e Entry) string` helper function.",
			},
		},
		"reviewed_at": "2026-03-29T14:23:00Z",
		"created_at":  "2026-03-29T14:20:00Z",
	})
}

// ── Feedback ─────────────────────────────────────────────────────────────────

func (h *DashboardHandler) SubmitFeedback(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	h.logger.Info("feedback received",
		"finding_id", body["finding_id"],
		"verdict", body["verdict"],
		"note", body["note"],
	)
	h.json(w, map[string]bool{"ok": true})
}
