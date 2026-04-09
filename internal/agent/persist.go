package agent

// persist.go — thin persistence layer between the agent orchestrator and
// the agent_sessions PostgreSQL table.
//
// The Orchestrator.store field (storage.AgentSessionStore) is nil-safe:
// when the database is unavailable (e.g., in tests or dev without Postgres)
// all persist* calls log a warning and continue without failing the session.

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/sevigo/code-warden/internal/storage"
)

// persistSessionCreated inserts a new row into agent_sessions when a session
// transitions from pending → running.  Called from SpawnAgent.
func (o *Orchestrator) persistSessionCreated(ctx context.Context, session *Session) {
	if o.store == nil {
		return
	}
	row := &storage.AgentSession{
		ID:        session.ID,
		TaskType:  "implement",
		RepoOwner: session.Issue.RepoOwner,
		RepoName:  session.Issue.RepoName,
		Status:    string(StatusPending),
		Branch:    sql.NullString{Valid: false},
		IssueNumber: sql.NullInt64{
			Int64: int64(session.Issue.Number),
			Valid: session.Issue.Number != 0,
		},
		TaskInputs: marshalJSON(map[string]any{
			"issue_title": session.Issue.Title,
			"issue_body":  truncateString(session.Issue.Body, 500),
		}),
	}
	if err := o.store.CreateAgentSession(ctx, row); err != nil {
		o.logger.Warn("persist: failed to create agent session row",
			"session_id", session.ID, "error", err)
	}
}

// persistSessionRunning updates status to "running" and sets the branch.
// Called after the workspace branch is prepared.
func (o *Orchestrator) persistSessionRunning(ctx context.Context, session *Session, branch string) {
	if o.store == nil {
		return
	}
	row := &storage.AgentSession{
		ID:     session.ID,
		Status: string(StatusRunning),
		Branch: sql.NullString{String: branch, Valid: branch != ""},
	}
	if err := o.store.UpdateAgentSession(ctx, row); err != nil {
		o.logger.Warn("persist: failed to update session to running",
			"session_id", session.ID, "error", err)
	}
}

// persistSessionCompleted writes the final result to the database.
// Called from postSessionCompleted.
func (o *Orchestrator) persistSessionCompleted(ctx context.Context, session *Session, result *Result) {
	if o.store == nil {
		return
	}
	row := &storage.AgentSession{
		ID:     session.ID,
		Status: string(StatusCompleted),
		CompletedAt: sql.NullTime{
			Time:  time.Now(),
			Valid: true,
		},
		Result: marshalJSON(result),
		Iterations: result.Iterations,
		FinalVerdict: sql.NullString{
			String: result.Verdict,
			Valid:  result.Verdict != "",
		},
	}
	if err := o.store.UpdateAgentSession(ctx, row); err != nil {
		o.logger.Warn("persist: failed to update session to completed",
			"session_id", session.ID, "error", err)
	}
}

// persistSessionFailed writes the failure reason to the database.
// Called from failSession.
func (o *Orchestrator) persistSessionFailed(ctx context.Context, session *Session, errMsg string) {
	if o.store == nil {
		return
	}
	row := &storage.AgentSession{
		ID:     session.ID,
		Status: string(StatusFailed),
		CompletedAt: sql.NullTime{
			Time:  time.Now(),
			Valid: true,
		},
		Error: sql.NullString{String: errMsg, Valid: true},
	}
	if err := o.store.UpdateAgentSession(ctx, row); err != nil {
		o.logger.Warn("persist: failed to update session to failed",
			"session_id", session.ID, "error", err)
	}
}

// marshalJSON marshals v to JSON, returning nil on error (safe for db columns).
func marshalJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
