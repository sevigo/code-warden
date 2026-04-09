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
	default:
		h.logger.Debug("ignoring unhandled webhook event type", "type", github.WebHookType(r))
		_, _ = fmt.Fprint(w, "Event type not handled")
	}
}

func (h *WebhookHandler) handleIssueComment(ctx context.Context, w http.ResponseWriter, event *github.IssueCommentEvent) {
	// Ignore comment deletions - only process created and edited comments
	action := event.GetAction()
	if action != "created" && action != "edited" {
		h.logger.Debug("ignoring issue comment", "reason", "action is "+action, "repo", event.GetRepo().GetFullName())
		_, _ = fmt.Fprint(w, "Comment action ignored")
		return
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

		// Handle /dismiss command on PRs
		if strings.HasPrefix(event.GetBody(), "/dismiss") {
			dismissReason := strings.TrimSpace(event.GetBody()[len("/dismiss"):])
			
			// The target finding ID is provided by the in_reply_to_id context of the webhook payload.
			// We must assume the webhook event object or context provides this context identifier, 
			// which we will call 'targetFindingID' for this implementation.
			targetFindingID := event.GetContext("in_reply_to_id")
			
			if targetFindingID == "" {
				h.logger.Warn("dismiss command received, but no target finding ID found in context.")
				http.Error(w, "Could not determine the target finding. Please ensure this command is run as a reply to a Code-Warden finding.", http.StatusBadRequest)
				return
			}
			
			// Call a new/existing service method to handle persistence and acknowledgment
			dismissed, err := core.ProcessDismissal(ctx, targetFindingID, dismissReason, event.GetRepo().GetID())
			if err != nil {
				h.logger.Error("failed to process dismissal command", "error", err)
				http.Error(w, fmt.Sprintf("Error dismissing finding: %v", err), http.StatusInternalServerError)
				return
			}
			
			if !dismissed {
				h.logger.Warn("dismiss command processed, but finding was not actionable or already dismissed.")
				http.Error(w, "Dismissal action recorded, but no change was made or the finding was ineligible for dismissal.", http.StatusConflict)
				return
			}

			// Confirmation step: React and reply to the original comment
			if err := h.confirmDismissal(ctx, event.GetID(), dismissReason); err != nil {
				h.logger.Error("failed to confirm dismissal via GitHub API", "error", err)
				// We still succeed for the webhook because persistence is the goal, 
				// but we log the failure for follow-up.
			}

			h.logger.Info("dismissal processed successfully", "finding_id", targetFindingID, "reason", dismissReason)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "Finding dismissed successfully and recorded.")
			return
		}
		
		// Existing logic for /review and /rereview commands
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
}
