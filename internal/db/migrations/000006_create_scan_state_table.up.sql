CREATE TABLE IF NOT EXISTS scan_state (
    id SERIAL PRIMARY KEY,
    repository_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    status TEXT NOT NULL, -- 'pending', 'in_progress', 'completed', 'failed'
    progress JSONB, -- Stores the list of processed files or current cursor
    created_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE (repository_id)
);

CREATE TRIGGER update_scan_state_updated_at
BEFORE UPDATE ON scan_state
FOR EACH ROW
EXECUTE FUNCTION update_updated_at_column();
