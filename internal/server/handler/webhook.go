// Package handler provides HTTP handlers for the Code-Warden application.
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/go-github/v73/github"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
)

// WebhookHandler processes incoming webhooks from GitHub.
type WebhookHandler struct {
	cfg        *config.Config
	dispatcher core.JobDispatcher
	logger     *slog.Logger
}

// NewWebhookHandler creates a new webhook handler with the given configuration and dispatcher.
func NewWebhookHandler(cfg *config.Config, dispatcher core.JobDispatcher, logger *slog.Logger) *WebhookHandler {
	return &WebhookHandler{
		cfg:        cfg,
		dispatcher: dispatcher,
		logger:     logger,
	}
}

// Handle processes GitHub webhook requests.
func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	payload, err := github.ValidatePayload(r, []byte(h.cfg.GitHubWebhookSecret))
	if err != nil {
		h.logger.Error("invalid webhook payload signature", "error", err)
		http.Error(w, "Invalid signature", http.StatusUnauthorized)
		return
	}

	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		h.logger.Error("could not parse webhook", "error", err)
		http.Error(w, "Could not parse webhook", http.StatusBadRequest)
		return
	}

	switch e := event.(type) {
	case *github.IssueCommentEvent:
		h.handleIssueComment(r.Context(), w, e)
	default:
		h.logger.Debug("ignoring unhandled webhook event type", "type", github.WebHookType(r))
		_, _ = fmt.Fprint(w, "Event type not handled")
	}
}

// handleIssueComment processes issue comment events from GitHub.
func (h *WebhookHandler) handleIssueComment(ctx context.Context, w http.ResponseWriter, event *github.IssueCommentEvent) {
	reviewEvent, err := core.EventFromIssueComment(event)
	if err != nil {
		h.logger.Debug("ignoring issue comment", "reason", err.Error(), "repo", event.GetRepo().GetFullName())
		_, _ = fmt.Fprint(w, "Comment ignored")
		return
	}

	if err := h.dispatcher.Dispatch(ctx, reviewEvent); err != nil {
		h.logger.Error("failed to dispatch review job", "error", err, "repo", reviewEvent.RepoFullName)
		http.Error(w, "Failed to start review job", http.StatusInternalServerError)
		return
	}

	h.logger.Info("review job dispatched successfully", "repo", reviewEvent.RepoFullName, "pr", reviewEvent.PRNumber)
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprint(w, "Review job accepted")
}
