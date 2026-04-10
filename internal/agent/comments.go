package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/google/go-github/v73/github"
)

// maxErrorDisplayLength is the maximum number of characters shown in GitHub
// failure comments to keep them readable without truncating useful context.
const maxErrorDisplayLength = 800

// postIssueComment posts a comment to the GitHub issue linked to this session.
// Errors are logged but not returned — comment failures must never block agent execution.
func (o *Orchestrator) postIssueComment(ctx context.Context, issue Issue, body string) {
	if o.ghClient == nil {
		return
	}
	if err := o.ghClient.CreateComment(ctx, issue.RepoOwner, issue.RepoName, issue.Number, body); err != nil {
		o.logger.Warn("failed to post issue comment",
			"issue", issue.Number,
			"repo", issue.RepoOwner+"/"+issue.RepoName,
			"error", err)
	}
}

// createIssueComment posts a new comment and returns its ID (0 on failure).
// Used by progressTracker to create the single rolling status comment.
func (o *Orchestrator) createIssueComment(ctx context.Context, issue Issue, body string) int64 {
	if o.ghClient == nil {
		return 0
	}
	id, err := o.ghClient.CreateCommentID(ctx, issue.RepoOwner, issue.RepoName, issue.Number, body)
	if err != nil {
		o.logger.Warn("failed to create issue comment",
			"issue", issue.Number,
			"repo", issue.RepoOwner+"/"+issue.RepoName,
			"error", err)
		return 0
	}
	return id
}

// updateIssueComment edits an existing comment by ID.
// Used by progressTracker to update the rolling status comment in-place.
func (o *Orchestrator) updateIssueComment(ctx context.Context, issue Issue, commentID int64, body string) {
	if o.ghClient == nil || commentID == 0 {
		return
	}
	if err := o.ghClient.UpdateComment(ctx, issue.RepoOwner, issue.RepoName, commentID, body); err != nil {
		o.logger.Warn("failed to update issue comment",
			"issue", issue.Number,
			"comment_id", commentID,
			"repo", issue.RepoOwner+"/"+issue.RepoName,
			"error", err)
	}
}

func (o *Orchestrator) postSessionStarted(ctx context.Context, session *Session) {
	body := fmt.Sprintf(
		"🤖 **Implementation started** — session `%s`\n\n"+
			"Working on issue #%d. I'll post updates here as I progress.\n\n"+
			"> You can cancel by commenting `/cancel %s`",
		session.ID, session.Issue.Number, session.ID,
	)
	o.postIssueComment(ctx, session.Issue, body)
}

func (o *Orchestrator) postSessionCompleted(ctx context.Context, session *Session, result *Result) {
	o.persistSessionCompleted(ctx, session, result)
	var body string
	if result.PRURL != "" {
		filesNote := ""
		if len(result.FilesChanged) > 0 {
			filesNote = fmt.Sprintf("\n- **Files changed:** %d", len(result.FilesChanged))
		}
		body = fmt.Sprintf(
			"✅ **Implementation complete** — session `%s`\n\n"+
				"**Pull request:** %s\n"+
				"- **Branch:** `%s`%s\n"+
				"- **Review verdict:** `%s`\n"+
				"- **Review iterations:** %d",
			session.ID, result.PRURL, result.Branch, filesNote,
			result.Verdict, result.Iterations,
		)
	} else {
		body = fmt.Sprintf(
			"✅ **Implementation complete** — session `%s`\n\n"+
				"Branch `%s` is ready (%d files changed). No PR was created — push the branch and open one manually if needed.",
			session.ID, result.Branch, len(result.FilesChanged),
		)
	}
	o.postIssueComment(ctx, session.Issue, body)
}

func (o *Orchestrator) postSessionFailed(ctx context.Context, session *Session, errMsg string) {
	body := fmt.Sprintf(
		"❌ **Implementation failed** — session `%s`\n\n"+
			"The agent could not complete the implementation.\n\n"+
			"<details><summary>Error detail</summary>\n\n```\n%s\n```\n</details>\n\n"+
			"You can retry by commenting `/implement` again.",
		session.ID, truncateString(errMsg, maxErrorDisplayLength),
	)
	o.postIssueComment(ctx, session.Issue, body)
}

// getBaseSHA fetches the HEAD SHA for the repository's default base branch ("main").
// Returns "" on error — callers treat empty SHA as "Check Run unavailable".
func (o *Orchestrator) getBaseSHA(ctx context.Context, owner, repo string) string {
	if o.ghClient == nil {
		return ""
	}
	b, err := o.ghClient.GetBranch(ctx, owner, repo, "main")
	if err != nil {
		o.logger.Warn("getBaseSHA: failed to fetch main branch SHA", "owner", owner, "repo", repo, "error", err)
		return ""
	}
	return b.GetCommit().GetSHA()
}

// createCheckRun creates a GitHub Check Run and returns its ID (0 on failure).
// The Check Run is associated with headSHA so it appears on the commit in GitHub UI.
func (o *Orchestrator) createCheckRun(ctx context.Context, issue Issue, headSHA, summary string) int64 {
	if o.ghClient == nil || headSHA == "" {
		return 0
	}
	status := "in_progress"
	startedAt := github.Timestamp{Time: time.Now()}
	cr, err := o.ghClient.CreateCheckRun(ctx, issue.RepoOwner, issue.RepoName, github.CreateCheckRunOptions{
		Name:      "code-warden: implementation",
		HeadSHA:   headSHA,
		Status:    &status,
		StartedAt: &startedAt,
		Output: &github.CheckRunOutput{
			Title:   github.Ptr("Implementation in progress"),
			Summary: github.Ptr(summary),
		},
	})
	if err != nil {
		o.logger.Warn("createCheckRun: failed", "issue", issue.Number, "error", err)
		return 0
	}
	return cr.GetID()
}

// updateCheckRun edits the Check Run summary for in-progress updates.
func (o *Orchestrator) updateCheckRun(ctx context.Context, issue Issue, checkRunID int64, summary string) {
	if o.ghClient == nil || checkRunID == 0 {
		return
	}
	status := "in_progress"
	if _, err := o.ghClient.UpdateCheckRun(ctx, issue.RepoOwner, issue.RepoName, checkRunID, github.UpdateCheckRunOptions{
		Name:   "code-warden: implementation",
		Status: &status,
		Output: &github.CheckRunOutput{
			Title:   github.Ptr("Implementation in progress"),
			Summary: github.Ptr(summary),
		},
	}); err != nil {
		o.logger.Warn("updateCheckRun: failed", "check_run_id", checkRunID, "error", err)
	}
}

// completeCheckRun marks the Check Run as completed with the given conclusion
// ("success", "failure", "action_required").
func (o *Orchestrator) completeCheckRun(ctx context.Context, issue Issue, checkRunID int64, conclusion, summary string) {
	if o.ghClient == nil || checkRunID == 0 {
		return
	}
	status := "completed"
	completedAt := github.Timestamp{Time: time.Now()}
	if _, err := o.ghClient.UpdateCheckRun(ctx, issue.RepoOwner, issue.RepoName, checkRunID, github.UpdateCheckRunOptions{
		Name:        "code-warden: implementation",
		Status:      &status,
		Conclusion:  &conclusion,
		CompletedAt: &completedAt,
		Output: &github.CheckRunOutput{
			Title:   github.Ptr("Implementation " + conclusion),
			Summary: github.Ptr(summary),
		},
	}); err != nil {
		o.logger.Warn("completeCheckRun: failed", "check_run_id", checkRunID, "conclusion", conclusion, "error", err)
	}
}

// deferCheckRunCompletion returns a function suitable for deferring that
// completes a Check Run with a conclusion based on the session's final status.
func (o *Orchestrator) deferCheckRunCompletion(ctx context.Context, session *Session, checkRunID int64) {
	conclusion := "success"
	summary := fmt.Sprintf("Session `%s` completed.", session.ID)
	if s := session.GetStatus(); s == StatusFailed {
		conclusion = "failure"
		summary = fmt.Sprintf("Session `%s` failed: %s", session.ID, session.GetError())
	} else if s == StatusDraft {
		conclusion = "action_required"
		summary = fmt.Sprintf("Session `%s` yielded a draft PR for human review.", session.ID)
	}
	o.completeCheckRun(ctx, session.Issue, checkRunID, conclusion, summary)
}
