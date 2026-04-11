// Package handler provides HTTP handlers for the Code-Warden application.
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/go-github/v73/github"

	"github.com/sevigo/code-warden/internal/config"
	"github.com/sevigo/code-warden/internal/core"
)

// WebhookHandler processes incoming webhooks from GitHub.
type WebhookHandler struct {
	cfg        *config.Config
	dispatcher core.JobDispatcher
	canceller  core.SessionCanceller // optional; nil when agent is disabled
	logger     *slog.Logger
}

// NewWebhookHandler creates a new webhook handler with the given configuration and dispatcher.
func NewWebhookHandler(cfg *config.Config, dispatcher core.JobDispatcher, canceller core.SessionCanceller, logger *slog.Logger) *WebhookHandler {
	return &WebhookHandler{
		cfg:        cfg,
		dispatcher: dispatcher,
		canceller:  canceller,
		logger:     logger,
	}
}

// Handle processes GitHub webhook requests.
func (h *WebhookHandler) Handle(w http.ResponseWriter, r *http.Request) {
	// Validate payload signature
	payload, err := github.ValidatePayload(r, []byte(h.cfg.GitHub.WebhookSecret))
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
	case *github.PushEvent:
		h.handlePushEvent(r.Context(), w, e)
	default:
		h.logger.Debug("ignoring unhandled webhook event type", "type", github.WebHookType(r))
		_, _ = fmt.Fprint(w, "Event type not handled")
	}
}

func (h *WebhookHandler) handlePushEvent(ctx context.Context, w http.ResponseWriter, event *github.PushEvent) {
	pushEvent, err := core.EventFromPushEvent(event)
	if err != nil {
		repoName := ""
		if event != nil && event.GetRepo() != nil {
			repoName = event.GetRepo().GetFullName()
		}
		h.logger.Debug("ignoring push event", "reason", err.Error(), "repo", repoName)
		_, _ = fmt.Fprint(w, "Push event ignored")
		return
	}

	if err := h.dispatcher.Dispatch(ctx, pushEvent); err != nil {
		h.logger.Error("failed to dispatch reindex job", "error", err, "repo", pushEvent.RepoFullName)
		http.Error(w, "Failed to start reindex job", http.StatusInternalServerError)
		return
	}

	h.logger.Info("reindex job dispatched successfully", "repo", pushEvent.RepoFullName, "sha", pushEvent.HeadSHA)
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprint(w, "Reindex job accepted")
}

func (h *WebhookHandler) handleIssueComment(ctx context.Context, w http.ResponseWriter, event *github.IssueCommentEvent) {
	// Ignore comment deletions - only process created and edited comments
	action := event.GetAction()
	if action != "created" && action != "edited" {
		h.logger.Debug("ignoring issue comment", "reason", "action is "+action, "repo", event.GetRepo().GetFullName())
		_, _ = fmt.Fprint(w, "Comment action ignored")
		return
	}

	// Handle /cancel <session-id> on any issue comment.
	if !event.GetIssue().IsPullRequest() {
		if handled := h.handleCancelCommand(w, event.GetComment().GetBody()); handled {
			return
		}
	}

	// Try to parse as /implement command on issue first
	if !event.GetIssue().IsPullRequest() {
		implementEvent, err := core.ImplementEventFromIssueComment(event)
		if err != nil {
			h.logger.Debug("ignoring issue comment", "reason", err.Error(), "repo", event.GetRepo().GetFullName())
			_, _ = fmt.Fprint(w, "Comment ignored")
			return
		}

		// Check if agent functionality is enabled
		if !h.cfg.Agent.Enabled {
			h.logger.Warn("agent functionality is disabled, ignoring /implement command",
				"repo", implementEvent.RepoFullName,
				"issue", implementEvent.IssueNumber)
			_, _ = fmt.Fprint(w, "Agent functionality is disabled. Enable it in config to use /implement.")
			return
		}

		if err := h.dispatcher.Dispatch(ctx, implementEvent); err != nil {
			h.logger.Error("failed to dispatch implement job", "error", err, "repo", implementEvent.RepoFullName)
			http.Error(w, "Failed to start implement job", http.StatusInternalServerError)
			return
		}

		h.logger.Info("implement job dispatched successfully", "repo", implementEvent.RepoFullName, "issue", implementEvent.IssueNumber)
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, "Implement job accepted")
		return
	}

	// Handle /review and /rereview commands on PRs
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

// handleCancelCommand checks if body is a /cancel command and cancels the session.
// Returns true if the command was handled (caller should return).
func (h *WebhookHandler) handleCancelCommand(w http.ResponseWriter, body string) bool {
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "/cancel ") {
		return false
	}
	sessionID := strings.TrimSpace(strings.TrimPrefix(trimmed, "/cancel "))
	if h.canceller == nil {
		h.logger.Warn("received /cancel but agent is not enabled")
		_, _ = fmt.Fprint(w, "Agent not enabled")
		return true
	}
	if err := h.canceller.CancelSession(sessionID); err != nil {
		h.logger.Warn("cancel session failed", "session_id", sessionID, "error", err)
		_, _ = fmt.Fprintf(w, "Cancel failed: %v", err)
		return true
	}
	h.logger.Info("session cancelled via webhook", "session_id", sessionID)
	_, _ = fmt.Fprint(w, "Session cancelled")
	return true
}
