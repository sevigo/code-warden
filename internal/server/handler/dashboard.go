package handler

import (
	"context"
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
	"github.com/sevigo/code-warden/internal/core"
	"github.com/sevigo/code-warden/internal/storage"
)

const (
	severityCritical = "critical"
	severityHigh     = "high"
	severityMedium   = "medium"
	severityLow      = "low"
	statusError      = "error"
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

	dbStatus, dbLatency := h.pingDatabase(ctx)
	qdrantStatus, qdrantLatency := pingURL(h.cfg.Storage.QdrantHost, "/healthz", true)
	llmStatus, llmLatency := h.pingLLM()

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
			"llm":      map[string]any{"status": llmStatus, "latency_ms": llmLatency, "provider": h.cfg.AI.LLMProvider},
		},
		"ready": configured && dbStatus == "ok" && qdrantStatus == "ok" && llmStatus == "ok",
	})
}

// pingDatabase pings the store and returns (status, latency_ms).
func (h *DashboardHandler) pingDatabase(ctx context.Context) (string, int64) {
	start := time.Now()
	_, err := h.store.GetReviewStats(ctx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return statusError, latency
	}
	return "ok", latency
}

// pingURL performs a GET health check against a service URL.
// When grpcPort is true, ":6334" is converted to ":6333" (Qdrant gRPC→HTTP).
func pingURL(host, path string, grpcPort bool) (string, int64) {
	if grpcPort {
		if rest, ok := strings.CutSuffix(host, ":6334"); ok {
			host = rest + ":6333"
		}
	}
	if !strings.HasPrefix(host, "http") {
		host = "http://" + host
	}
	client := &http.Client{Timeout: 5 * time.Second}
	start := time.Now()
	resp, err := client.Get(host + path) //nolint:noctx // short health check
	latency := time.Since(start).Milliseconds()
	if err != nil {
		return statusError, latency
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 300 {
		return "ok", latency
	}
	return statusError, latency
}

// pingLLM checks reachability of the configured LLM provider.
func (h *DashboardHandler) pingLLM() (string, int64) {
	var url string
	switch h.cfg.AI.LLMProvider {
	case "gemini":
		url = "https://generativelanguage.googleapis.com/v1beta/models"
	default: // ollama
		host := h.cfg.AI.OllamaHost
		if host == "" {
			host = "http://localhost:11434"
		}
		url = host + "/api/tags"
	}
	return pingURL(url, "", false)
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
			"critical": 0,
			"high":     0,
			"medium":   0,
			"low":      0,
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
		Revision       int       `json:"revision"`
		IsReReview     bool      `json:"is_re_review"`
	}

	// Map PR number to list of reviews to compute revision numbers
	prReviews := make(map[int][]*core.Review)
	for i := len(reviews) - 1; i >= 0; i-- {
		rev := reviews[i]
		prReviews[rev.PRNumber] = append(prReviews[rev.PRNumber], rev)
	}

	out := make([]reviewDTO, 0, len(reviews))
	for _, rev := range reviews {
		counts := parseSeverityCounts(rev.ReviewContent)
		total := getTotalFromCounts(counts)

		// Find revision number (1-based index)
		revList := prReviews[rev.PRNumber]
		revision := 1
		for i, r := range revList {
			if r.ID == rev.ID {
				revision = i + 1
				break
			}
		}

		out = append(out, reviewDTO{
			ID:             rev.ID,
			PRNumber:       rev.PRNumber,
			PRTitle:        formatPRTitle(rev.PRNumber),
			HeadSHA:        rev.HeadSHA,
			Status:         "reviewed",
			SeverityCounts: counts,
			TotalFindings:  total,
			ReviewedAt:     rev.CreatedAt,
			CreatedAt:      rev.CreatedAt,
			Revision:       revision,
			IsReReview:     revision > 1,
		})
	}
	h.writeJSON(w, out)
}

func getTotalFromCounts(counts map[string]any) int {
	critical, _ := counts["critical"].(int)
	high, _ := counts["high"].(int)
	medium, _ := counts["medium"].(int)
	low, _ := counts["low"].(int)
	return critical + high + medium + low
}

func formatPRTitle(prNum int) string {
	return fmt.Sprintf("PR #%d", prNum)
}

func (h *DashboardHandler) GetReview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	repoID, err := strconv.ParseInt(chi.URLParam(r, "repoId"), 10, 64)
	if err != nil {
		http.Error(w, "invalid repo id", http.StatusBadRequest)
		return
	}
	prNum, err := strconv.Atoi(chi.URLParam(r, "prNumber"))
	if err != nil {
		http.Error(w, "invalid pr number", http.StatusBadRequest)
		return
	}

	repo, err := h.store.GetRepositoryByID(ctx, repoID)
	if err != nil {
		http.Error(w, "repository not found", http.StatusNotFound)
		return
	}

	// Fetch ALL reviews for this PR to show history
	allReviews, err := h.store.GetAllReviewsForPR(ctx, repo.FullName, prNum)
	if err != nil {
		http.Error(w, "review not found", http.StatusNotFound)
		return
	}

	// Determine which review to show
	var rev *core.Review
	specificID, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if specificID > 0 {
		for _, r := range allReviews {
			if r.ID == specificID {
				rev = r
				break
			}
		}
	}
	if rev == nil && len(allReviews) > 0 {
		// Default to latest
		rev = allReviews[len(allReviews)-1]
	}

	if rev == nil {
		http.Error(w, "review not found", http.StatusNotFound)
		return
	}

	counts := parseSeverityCounts(rev.ReviewContent)
	findings := parseFindings(rev.ReviewContent)
	total := getTotalFromCounts(counts)

	type historyDTO struct {
		ID        int64     `json:"id"`
		HeadSHA   string    `json:"head_sha"`
		CreatedAt time.Time `json:"created_at"`
		Revision  int       `json:"revision"`
		IsLatest  bool      `json:"is_latest"`
		TotalCrit int       `json:"total_critical"`
	}

	history := make([]historyDTO, 0, len(allReviews))
	for i, r := range allReviews {
		c := parseSeverityCounts(r.ReviewContent)
		crit, _ := c["critical"].(int)
		history = append(history, historyDTO{
			ID:        r.ID,
			HeadSHA:   r.HeadSHA,
			CreatedAt: r.CreatedAt,
			Revision:  i + 1,
			IsLatest:  i == len(allReviews)-1,
			TotalCrit: crit,
		})
	}

	h.writeJSON(w, map[string]any{
		"id":              rev.ID,
		"pr_number":       rev.PRNumber,
		"pr_title":        formatPRTitle(rev.PRNumber),
		"head_sha":        rev.HeadSHA,
		"status":          "reviewed",
		"severity_counts": counts,
		"total_findings":  total,
		"findings":        findings,
		"reviewed_at":     rev.CreatedAt,
		"created_at":      rev.CreatedAt,
		"history":         history,
		"revision":        len(history), // Actually we should find current revision index
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
	counts := map[string]int{"critical": 0, "high": 0, "medium": 0, "low": 0}

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
		case severityCritical:
			counts[severityCritical]++
		case severityHigh:
			counts[severityHigh]++
		case severityMedium:
			counts[severityMedium]++
		case severityLow, "suggestion":
			counts[severityLow]++
		}
		pos = end + len("</severity>")
	}

	return map[string]any{
		"critical": counts["critical"],
		"high":     counts["high"],
		"medium":   counts["medium"],
		"low":      counts["low"],
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
		closeTag := "</" + tag + ">"
		s := strings.Index(lblock, open)
		if s == -1 {
			return ""
		}
		s += len(open)
		e := strings.Index(lblock[s:], closeTag)
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
	uiSev := severityLow
	switch sev {
	case severityCritical:
		uiSev = severityCritical
	case severityHigh:
		uiSev = severityHigh
	case severityMedium:
		uiSev = severityMedium
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
	comment = strings.TrimSpace(comment)

	// Strip markdown bold markers if present at start: "**Observation:** info" -> "Observation: info"
	if strings.HasPrefix(comment, "**") {
		if end := strings.Index(comment[2:], "**"); end != -1 {
			// +4 to skip the two leading and two trailing asterisks
			content := comment[2 : end+2]
			rest := strings.TrimSpace(comment[end+4:])
			if rest != "" {
				comment = content + ": " + rest
			} else {
				comment = content
			}
		}
	}

	// Strip leading emoji if present: "🔍 **Observation:**" -> "**Observation:**"
	// (Simple check for common review emojis)
	emojis := []string{"🔍", "💡", "📖", "🔴", "🟠", "🟡", "🟢", "✅", "🚫", "💬"}
	for _, e := range emojis {
		if rest, ok := strings.CutPrefix(comment, e); ok {
			comment = strings.TrimSpace(rest)
			break
		}
	}

	// Final cleanup - if it still has markdown markers in the title (like logic above missed something)
	comment = strings.ReplaceAll(comment, "**", "")

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
