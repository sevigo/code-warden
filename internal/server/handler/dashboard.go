package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/storage"
)

// DashboardHandler serves dashboard, stats, reviews, jobs, and config endpoints.
type DashboardHandler struct {
	cfg    *config.Config
	store  storage.Store
	logger *slog.Logger
}

func NewDashboardHandler(cfg *config.Config, store storage.Store, logger *slog.Logger) *DashboardHandler {
	return &DashboardHandler{cfg: cfg, store: store, logger: logger}
}

func (h *DashboardHandler) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("failed to encode JSON response", "error", err)
	}
}

// ── Setup & Config ──────────────────────────────────────────────────────────

func (h *DashboardHandler) SetupStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	configured := h.cfg.GitHub.AppID != 0
	appName := "Code Warden"
	if !configured {
		appName = ""
	}

	// Ping DB via a lightweight store call
	dbStatus, dbLatency := "error", int64(0)
	{
		start := time.Now()
		_, err := h.store.GetReviewStats(ctx)
		dbLatency = time.Since(start).Milliseconds()
		if err == nil {
			dbStatus = "ok"
		}
	}

	// Ping Qdrant HTTP health endpoint
	qdrantStatus, qdrantLatency := "error", int64(0)
	{
		host := h.cfg.Storage.QdrantHost
		if !strings.HasPrefix(host, "http") {
			host = "http://" + host
		}
		start := time.Now()
		resp, err := http.Get(host + "/healthz") //nolint:noctx
		qdrantLatency = time.Since(start).Milliseconds()
		if err == nil {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
			if resp.StatusCode < 300 {
				qdrantStatus = "ok"
			}
		}
	}

	installURL := ""
	if configured && appName != "" {
		installURL = fmt.Sprintf("https://github.com/apps/%s/installations/new",
			strings.ToLower(strings.ReplaceAll(appName, " ", "-")))
	}

	h.writeJSON(w, map[string]any{
		"github_app": map[string]any{
			"configured":  configured,
			"app_id":      h.cfg.GitHub.AppID,
			"app_name":    appName,
			"install_url": installURL,
		},
		"services": map[string]any{
			"database": map[string]any{"status": dbStatus, "latency_ms": dbLatency},
			"qdrant":   map[string]any{"status": qdrantStatus, "latency_ms": qdrantLatency},
		},
		"ready": configured && dbStatus == "ok" && qdrantStatus == "ok",
	})
}

func (h *DashboardHandler) GetConfig(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, map[string]any{
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

func (h *DashboardHandler) GlobalStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	repos, err := h.store.GetAllRepositories(ctx)
	if err != nil {
		h.logger.Error("failed to get repositories for stats", "error", err)
	}

	totalRepos := len(repos)
	indexedRepos := 0
	for _, repo := range repos {
		if repo.LastIndexedSHA != "" {
			indexedRepos++
		}
	}

	reviewStats, err := h.store.GetReviewStats(ctx)
	if err != nil {
		h.logger.Error("failed to get review stats", "error", err)
		reviewStats = &storage.ReviewStats{}
	}

	h.writeJSON(w, map[string]any{
		"total_repos":       totalRepos,
		"indexed_repos":     indexedRepos,
		"total_reviews":     reviewStats.TotalReviews,
		"reviews_this_week": reviewStats.ReviewsThisWeek,
		"total_findings":    0,
		"findings_by_severity": map[string]int{
			"critical":   0,
			"warning":    0,
			"suggestion": 0,
		},
		"avg_findings_per_review": 0.0,
		"jobs_running":            0,
		"jobs_queued":             0,
	})
}

// ── Jobs ────────────────────────────────────────────────────────────────────

func (h *DashboardHandler) ListJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limit := 50
	offset := 0
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("offset")); err == nil && v >= 0 {
		offset = v
	}

	jobs, err := h.store.ListJobRuns(ctx, limit, offset)
	if err != nil {
		h.logger.Error("failed to list job runs", "error", err)
		h.writeJSON(w, []any{})
		return
	}

	type jobDTO struct {
		ID           int64      `json:"id"`
		Type         string     `json:"type"`
		RepoFullName string     `json:"repo_full_name"`
		PRNumber     int        `json:"pr_number"`
		Status       string     `json:"status"`
		TriggeredBy  string     `json:"triggered_by"`
		TriggeredAt  time.Time  `json:"triggered_at"`
		CompletedAt  *time.Time `json:"completed_at"`
		DurationMs   *int64     `json:"duration_ms"`
	}

	out := make([]jobDTO, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobDTO{
			ID:           j.ID,
			Type:         j.Type,
			RepoFullName: j.RepoFullName,
			PRNumber:     j.PRNumber,
			Status:       j.Status,
			TriggeredBy:  j.TriggeredBy,
			TriggeredAt:  j.TriggeredAt,
			CompletedAt:  j.CompletedAt,
			DurationMs:   j.DurationMs,
		})
	}
	h.writeJSON(w, out)
}

// ── Reviews ─────────────────────────────────────────────────────────────────

func (h *DashboardHandler) ListReviews(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	repoID, err := strconv.ParseInt(chi.URLParam(r, "repoId"), 10, 64)
	if err != nil {
		http.Error(w, "invalid repo id", http.StatusBadRequest)
		return
	}

	repo, err := h.store.GetRepositoryByID(ctx, repoID)
	if err != nil {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}

	reviews, err := h.store.GetReviewsForRepo(ctx, repo.FullName)
	if err != nil {
		h.logger.Error("failed to get reviews for repo", "repo", repo.FullName, "error", err)
		h.writeJSON(w, []any{})
		return
	}

	type reviewDTO struct {
		ID             int64     `json:"id"`
		PRNumber       int       `json:"pr_number"`
		PRTitle        string    `json:"pr_title"`
		HeadSHA        string    `json:"head_sha"`
		Status         string    `json:"status"`
		SeverityCounts any       `json:"severity_counts"`
		TotalFindings  int       `json:"total_findings"`
		ReviewedAt     time.Time `json:"reviewed_at"`
		CreatedAt      time.Time `json:"created_at"`
	}

	out := make([]reviewDTO, 0, len(reviews))
	for _, rev := range reviews {
		counts := parseSeverityCounts(rev.ReviewContent)
		total := counts["critical"].(int) + counts["warning"].(int) + counts["suggestion"].(int)
		out = append(out, reviewDTO{
			ID:             rev.ID,
			PRNumber:       rev.PRNumber,
			PRTitle:        fmt.Sprintf("PR #%d", rev.PRNumber),
			HeadSHA:        rev.HeadSHA,
			Status:         "reviewed",
			SeverityCounts: counts,
			TotalFindings:  total,
			ReviewedAt:     rev.CreatedAt,
			CreatedAt:      rev.CreatedAt,
		})
	}
	h.writeJSON(w, out)
}

func (h *DashboardHandler) GetReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	repoID, err := strconv.ParseInt(chi.URLParam(r, "repoId"), 10, 64)
	if err != nil {
		http.Error(w, "invalid repo id", http.StatusBadRequest)
		return
	}
	prNum, err := strconv.Atoi(chi.URLParam(r, "prNum"))
	if err != nil {
		http.Error(w, "invalid pr number", http.StatusBadRequest)
		return
	}

	repo, err := h.store.GetRepositoryByID(ctx, repoID)
	if err != nil {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}

	rev, err := h.store.GetLatestReviewForPR(ctx, repo.FullName, prNum)
	if err != nil {
		http.Error(w, "review not found", http.StatusNotFound)
		return
	}

	counts := parseSeverityCounts(rev.ReviewContent)
	findings := parseFindings(rev.ReviewContent)
	total := counts["critical"].(int) + counts["warning"].(int) + counts["suggestion"].(int)

	h.writeJSON(w, map[string]any{
		"id":              rev.ID,
		"pr_number":       rev.PRNumber,
		"pr_title":        fmt.Sprintf("PR #%d", rev.PRNumber),
		"head_sha":        rev.HeadSHA,
		"status":          "reviewed",
		"severity_counts": counts,
		"total_findings":  total,
		"findings":        findings,
		"reviewed_at":     rev.CreatedAt,
		"created_at":      rev.CreatedAt,
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
	h.writeJSON(w, map[string]bool{"ok": true})
}

// ── Content Parsers ──────────────────────────────────────────────────────────

// parseSeverityCounts scans review_content XML for <severity> tags.
// Severity values from prompts: Critical, High, Medium, Low.
// Maps to UI categories: critical, warning, suggestion.
func parseSeverityCounts(content string) map[string]any {
	counts := map[string]int{"critical": 0, "warning": 0, "suggestion": 0}

	lower := strings.ToLower(content)
	pos := 0
	for {
		start := strings.Index(lower[pos:], "<severity>")
		if start == -1 {
			break
		}
		start += pos
		end := strings.Index(lower[start:], "</severity>")
		if end == -1 {
			break
		}
		end += start
		sev := strings.TrimSpace(lower[start+len("<severity>") : end])
		switch sev {
		case "critical", "high":
			counts["critical"]++
		case "medium":
			counts["warning"]++
		case "low", "suggestion":
			counts["suggestion"]++
		}
		pos = end + len("</severity>")
	}

	return map[string]any{
		"critical":   counts["critical"],
		"warning":    counts["warning"],
		"suggestion": counts["suggestion"],
	}
}

// parseFindings extracts structured findings from review XML content.
func parseFindings(content string) []map[string]any {
	var findings []map[string]any
	pos := 0
	idx := 0

	for {
		start := strings.Index(content[pos:], "<suggestion>")
		if start == -1 {
			break
		}
		start += pos
		endTag := strings.Index(content[start:], "</suggestion>")
		if endTag == -1 {
			break
		}
		endTag += start + len("</suggestion>")
		block := content[start:endTag]
		if f := parseSuggestionBlock(block, idx); f != nil {
			findings = append(findings, f)
			idx++
		}
		pos = endTag
	}
	return findings
}

func parseSuggestionBlock(block string, idx int) map[string]any {
	lblock := strings.ToLower(block)

	getTag := func(tag string) string {
		open := "<" + tag + ">"
		close := "</" + tag + ">"
		s := strings.Index(lblock, open)
		if s == -1 {
			return ""
		}
		s += len(open)
		e := strings.Index(lblock[s:], close)
		if e == -1 {
			return ""
		}
		// Return from original (non-lowercased) block to preserve content
		return strings.TrimSpace(block[s : s+e])
	}

	file := getTag("file")
	if file == "" {
		return nil
	}

	sev := strings.ToLower(getTag("severity"))
	uiSev := "suggestion"
	switch sev {
	case "critical", "high":
		uiSev = "critical"
	case "medium":
		uiSev = "warning"
	}

	lineStr := getTag("line")
	startLineStr := getTag("start_line")
	lineEnd, _ := strconv.Atoi(lineStr)
	lineStart, _ := strconv.Atoi(startLineStr)
	if lineStart == 0 {
		lineStart = lineEnd
	}

	comment := getTag("comment")
	title := buildTitle(comment, idx)

	codeSuggestion := getTag("code_suggestion")

	return map[string]any{
		"id":          fmt.Sprintf("f%d", idx+1),
		"severity":    uiSev,
		"category":    getTag("category"),
		"file":        file,
		"line_start":  lineStart,
		"line_end":    lineEnd,
		"title":       title,
		"description": comment,
		"suggestion":  codeSuggestion,
	}
}

func buildTitle(comment string, idx int) string {
	// Use first sentence as title, capped at 80 chars
	if i := strings.IndexAny(comment, ".!?\n"); i > 0 && i < 80 {
		return strings.TrimSpace(comment[:i])
	}
	if len(comment) > 80 {
		return comment[:77] + "..."
	}
	if comment != "" {
		return comment
	}
	return fmt.Sprintf("Finding %d", idx+1)
}
