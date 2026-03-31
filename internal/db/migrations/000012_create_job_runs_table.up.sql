CREATE TABLE IF NOT EXISTS job_runs (
    id           BIGSERIAL PRIMARY KEY,
    type         TEXT NOT NULL,
    repo_full_name TEXT NOT NULL,
    pr_number    INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL,
    triggered_by TEXT NOT NULL,
    triggered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    duration_ms  BIGINT
);

CREATE INDEX IF NOT EXISTS idx_job_runs_repo ON job_runs (repo_full_name);
CREATE INDEX IF NOT EXISTS idx_job_runs_triggered_at ON job_runs (triggered_at DESC);
