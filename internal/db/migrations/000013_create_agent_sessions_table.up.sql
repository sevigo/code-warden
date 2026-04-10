CREATE TABLE IF NOT EXISTS agent_sessions (
    id              TEXT PRIMARY KEY,
    task_type       VARCHAR(50) NOT NULL,
    repo_owner      VARCHAR(255) NOT NULL,
    repo_name       VARCHAR(255) NOT NULL,
    branch          VARCHAR(255),
    issue_number    INTEGER,

    status          VARCHAR(50) NOT NULL DEFAULT 'pending',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at    TIMESTAMPTZ,

    task_inputs     JSONB,
    result          JSONB,
    error           TEXT,
    iterations      INTEGER DEFAULT 0,
    final_verdict   VARCHAR(50),

    tokens_input    BIGINT DEFAULT 0,
    tokens_output   BIGINT DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_agent_sessions_repo    ON agent_sessions (repo_owner, repo_name);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_status  ON agent_sessions (status);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_created ON agent_sessions (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_sessions_type    ON agent_sessions (task_type);

CREATE OR REPLACE FUNCTION update_agent_session_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER agent_sessions_updated_at
    BEFORE UPDATE ON agent_sessions
    FOR EACH ROW
    EXECUTE FUNCTION update_agent_session_updated_at();
