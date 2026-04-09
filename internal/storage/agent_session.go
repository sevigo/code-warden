package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// AgentSession is the PostgreSQL row for an agent session.
// It mirrors the agent_sessions table defined in agent_schema.sql.
type AgentSession struct {
	ID          string         `db:"id"`
	TaskType    string         `db:"task_type"`
	RepoOwner   string         `db:"repo_owner"`
	RepoName    string         `db:"repo_name"`
	Branch      sql.NullString `db:"branch"`
	IssueNumber sql.NullInt64  `db:"issue_number"`

	Status      string       `db:"status"`
	CreatedAt   time.Time    `db:"created_at"`
	UpdatedAt   time.Time    `db:"updated_at"`
	CompletedAt sql.NullTime `db:"completed_at"`

	// JSON columns – stored as raw bytes, unmarshalled by callers.
	TaskInputs json.RawMessage `db:"task_inputs"`
	Result     json.RawMessage `db:"result"`

	Error        sql.NullString `db:"error"`
	Iterations   int            `db:"iterations"`
	FinalVerdict sql.NullString `db:"final_verdict"`

	// Token usage summed across all phases (plan + implement + publish).
	TokensInput  int64 `db:"tokens_input"`
	TokensOutput int64 `db:"tokens_output"`
}

// AgentSessionStore defines persistence operations for agent sessions.
// It is a sub-interface implemented by postgresStore, allowing callers
// to depend only on what they need.
type AgentSessionStore interface {
	// CreateAgentSession inserts a new row and returns the generated ID.
	CreateAgentSession(ctx context.Context, s *AgentSession) error
	// UpdateAgentSession overwrites status, result, error, iterations,
	// final_verdict, and completed_at for the given session ID.
	UpdateAgentSession(ctx context.Context, s *AgentSession) error
	// GetAgentSession retrieves a session by its UUID primary key.
	GetAgentSession(ctx context.Context, id string) (*AgentSession, error)
	// ListAgentSessions returns sessions for a repository, newest first.
	ListAgentSessions(ctx context.Context, repoOwner, repoName string, limit int) ([]*AgentSession, error)
}

// CreateAgentSession inserts a new agent_sessions row.
// s.ID must be set by the caller (use generateSessionID() in the agent package).
func (p *postgresStore) CreateAgentSession(ctx context.Context, s *AgentSession) error {
	const q = `
INSERT INTO agent_sessions
  (id, task_type, repo_owner, repo_name, branch, issue_number,
   status, task_inputs)
VALUES
  (:id, :task_type, :repo_owner, :repo_name, :branch, :issue_number,
   :status, :task_inputs)`

	if _, err := p.db.NamedExecContext(ctx, q, s); err != nil {
		return fmt.Errorf("CreateAgentSession: %w", err)
	}
	return nil
}

// UpdateAgentSession writes the mutable columns back to the database.
// It is called on every significant state transition (running, completed, failed).
func (p *postgresStore) UpdateAgentSession(ctx context.Context, s *AgentSession) error {
	const q = `
UPDATE agent_sessions SET
  status         = :status,
  branch         = COALESCE(NULLIF(:branch, ''), branch),
  completed_at   = :completed_at,
  result         = :result,
  error          = :error,
  iterations     = :iterations,
  final_verdict  = :final_verdict,
  tokens_input   = :tokens_input,
  tokens_output  = :tokens_output
WHERE id = :id`

	res, err := p.db.NamedExecContext(ctx, q, s)
	if err != nil {
		return fmt.Errorf("UpdateAgentSession: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("UpdateAgentSession: %w (id=%s)", ErrNotFound, s.ID)
	}
	return nil
}

// GetAgentSession fetches one row by primary key.
func (p *postgresStore) GetAgentSession(ctx context.Context, id string) (*AgentSession, error) {
	const q = `SELECT * FROM agent_sessions WHERE id = $1`
	var s AgentSession
	if err := p.db.GetContext(ctx, &s, q, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("GetAgentSession: %w", err)
	}
	return &s, nil
}

// ListAgentSessions returns up to limit sessions for a repo, ordered newest first.
func (p *postgresStore) ListAgentSessions(ctx context.Context, repoOwner, repoName string, limit int) ([]*AgentSession, error) {
	const q = `
SELECT * FROM agent_sessions
WHERE  repo_owner = $1 AND repo_name = $2
ORDER  BY created_at DESC
LIMIT  $3`

	rows := make([]*AgentSession, 0, limit)
	if err := p.db.SelectContext(ctx, &rows, q, repoOwner, repoName, limit); err != nil {
		return nil, fmt.Errorf("ListAgentSessions: %w", err)
	}
	return rows, nil
}
