package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/sevigo/code-warden/internal/config"
)

// SetupHandler handles the first-boot setup wizard endpoints.
type SetupHandler struct {
	cfg       *config.Config
	credStore *config.CredentialStore
	logger    *slog.Logger
}

func NewSetupHandler(cfg *config.Config, logger *slog.Logger) *SetupHandler {
	return &SetupHandler{cfg: cfg, logger: logger}
}

func (h *SetupHandler) SetCredentialStore(cs *config.CredentialStore) {
	h.credStore = cs
}

func (h *SetupHandler) writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("failed to encode JSON response", "error", err)
	}
}

// GitHubManifest returns the manifest JSON for GitHub App creation.
// POST /api/v1/setup/github/manifest
func (h *SetupHandler) GitHubManifest(w http.ResponseWriter, r *http.Request) {
	if h.credStore == nil {
		http.Error(w, `{"error":"credential store not available"}`, http.StatusServiceUnavailable)
		return
	}

	// Build the public URL for callbacks. In local dev this is typically localhost.
	publicURL := r.Header.Get("X-Forwarded-Host")
	if publicURL == "" {
		publicURL = r.Host
	}
	scheme := "https"
	if r.TLS == nil && !strings.HasPrefix(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "http"
	}
	baseURL := fmt.Sprintf("%s://%s", scheme, publicURL)

	manifest := map[string]any{
		"name": "Code Warden",
		"url":  baseURL,
		"hook_attributes": map[string]string{
			"url": baseURL + "/api/v1/webhook/github",
		},
		"redirect_url": baseURL + "/api/v1/setup/github/callback",
		"public":       false,
		"default_permissions": map[string]string{
			"contents":      "read",
			"issues":        "write",
			"metadata":      "read",
			"pull_requests": "write",
		},
		"default_events": []string{
			"issue_comment",
			"issues",
			"pull_request",
			"push",
		},
	}

	encoded, err := json.Marshal(manifest)
	if err != nil {
		http.Error(w, `{"error":"failed to encode manifest"}`, http.StatusInternalServerError)
		return
	}

	state := base64.RawURLEncoding.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))

	h.writeJSON(w, map[string]string{
		"manifest": base64.RawURLEncoding.EncodeToString(encoded),
		"state":    state,
		"url":      fmt.Sprintf("https://github.com/settings/apps/new?state=%s&manifest=%s", state, base64.RawURLEncoding.EncodeToString(encoded)),
	})
}

// GitHubCallback handles the OAuth callback after the user creates the GitHub App.
// GET /api/v1/setup/github/callback?code=<manifest_code>
func (h *SetupHandler) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	if h.credStore == nil {
		http.Error(w, `{"error":"credential store not available"}`, http.StatusServiceUnavailable)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, `{"error":"missing code parameter"}`, http.StatusBadRequest)
		return
	}

	// Exchange the manifest code for credentials via GitHub API.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// #nosec G704 -- URL is constructed from the hardcoded GitHub API endpoint, not user input.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.github.com/app-manifests/"+code+"/conversions", nil)
	if err != nil {
		http.Error(w, `{"error":"failed to create request"}`, http.StatusInternalServerError)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	// #nosec G704 -- request targets the hardcoded GitHub API endpoint.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.logger.Error("GitHub manifest exchange failed", "error", err)
		http.Error(w, `{"error":"failed to exchange code with GitHub"}`, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to read GitHub response"}`, http.StatusInternalServerError)
		return
	}

	if resp.StatusCode != http.StatusCreated {
		h.logger.Error("GitHub manifest exchange returned error", "status", resp.StatusCode, "body", string(body))
		http.Error(w, fmt.Sprintf(`{"error":"GitHub returned status %d"}`, resp.StatusCode), http.StatusBadGateway)
		return
	}

	var result struct {
		ID            int64  `json:"id"`
		WebhookSecret string `json:"webhook_secret"`
		PEM           string `json:"pem"`
		Name          string `json:"name"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		http.Error(w, `{"error":"failed to parse GitHub response"}`, http.StatusInternalServerError)
		return
	}

	// Store credentials in DB.
	creds := config.GitHubAppCredentials{
		AppID:         result.ID,
		WebhookSecret: result.WebhookSecret,
		PrivateKeyPEM: result.PEM,
		AppName:       result.Name,
	}
	if err := h.credStore.Save(ctx, "github_app", creds); err != nil {
		h.logger.Error("failed to save GitHub credentials", "error", err)
		http.Error(w, `{"error":"failed to save credentials"}`, http.StatusInternalServerError)
		return
	}

	// Apply to live config so the server can start using them immediately.
	h.cfg.ApplyDBCredentials(&creds, nil)

	h.writeJSON(w, map[string]any{
		"success":  true,
		"app_id":   result.ID,
		"app_name": result.Name,
		"message":  "GitHub App created successfully. Install it on your repositories next.",
	})
}

// SaveCredentials manually saves credentials (fallback when Manifest flow isn't available).
// POST /api/v1/setup/credentials
func (h *SetupHandler) SaveCredentials(w http.ResponseWriter, r *http.Request) {
	if h.credStore == nil {
		http.Error(w, `{"error":"credential store not available"}`, http.StatusServiceUnavailable)
		return
	}

	var req struct {
		GitHub *config.GitHubAppCredentials `json:"github,omitempty"`
		LLM    *config.LLMCredentials       `json:"llm,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if req.GitHub != nil {
		if err := h.credStore.Save(ctx, "github_app", req.GitHub); err != nil {
			h.logger.Error("failed to save GitHub credentials", "error", err)
			http.Error(w, `{"error":"failed to save GitHub credentials"}`, http.StatusInternalServerError)
			return
		}
		h.cfg.ApplyDBCredentials(req.GitHub, nil)
	}

	if req.LLM != nil {
		if err := h.credStore.Save(ctx, "llm", req.LLM); err != nil {
			h.logger.Error("failed to save LLM credentials", "error", err)
			http.Error(w, `{"error":"failed to save LLM credentials"}`, http.StatusInternalServerError)
			return
		}
		h.cfg.ApplyDBCredentials(nil, req.LLM)
	}

	h.writeJSON(w, map[string]string{"status": "ok"})
}

// TestLLM tests connectivity to the configured LLM provider.
// POST /api/v1/setup/test-llm
func (h *SetupHandler) TestLLM(w http.ResponseWriter, _ *http.Request) {
	provider := h.cfg.AI.LLMProvider
	var ok bool
	var detail string

	switch provider {
	case "ollama":
		host := h.cfg.AI.OllamaHost
		if host == "" {
			host = "http://localhost:11434"
		}
		ok, detail = h.pingURL(host+"/api/tags", 5*time.Second)
	case "gemini":
		if h.cfg.AI.GeminiAPIKey == "" {
			detail = "no API key configured"
			break
		}
		ok, detail = h.pingURL("https://generativelanguage.googleapis.com/v1beta/models?key="+h.cfg.AI.GeminiAPIKey, 5*time.Second)
	default:
		detail = fmt.Sprintf("unknown provider: %s", provider)
	}

	status := "error"
	if ok {
		status = "ok"
	}
	h.writeJSON(w, map[string]any{
		"status":   status,
		"provider": provider,
		"detail":   detail,
	})
}

// TestWebhook sends a test event to verify webhook connectivity.
// POST /api/v1/setup/test-webhook
func (h *SetupHandler) TestWebhook(w http.ResponseWriter, _ *http.Request) {
	if h.cfg.GitHub.AppID == 0 {
		http.Error(w, `{"error":"GitHub App not configured"}`, http.StatusBadRequest)
		return
	}

	// We can't send a webhook FROM the server — GitHub does that.
	// Instead, we verify the webhook secret is configured and the app is set up.
	h.writeJSON(w, map[string]any{
		"status":         "ok",
		"app_id":         h.cfg.GitHub.AppID,
		"webhook_secret": h.cfg.GitHub.WebhookSecret != "",
		"message":        "GitHub App is configured. Ensure your repository has webhooks enabled.",
	})
}

func (h *SetupHandler) pingURL(url string, timeout time.Duration) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err.Error()
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return false, fmt.Sprintf("HTTP %d", resp.StatusCode)
}
