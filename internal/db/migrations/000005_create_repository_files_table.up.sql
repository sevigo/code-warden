CREATE TABLE IF NOT EXISTS repository_files (
    id SERIAL PRIMARY KEY,
    repository_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    file_path TEXT NOT NULL,
    file_hash TEXT NOT NULL,
    metadata JSONB, -- For future extensions: symbols, imports, summaries
    last_indexed_at TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    UNIQUE (repository_id, file_path)
);

CREATE INDEX idx_repository_files_repo_hash ON repository_files(repository_id, file_hash);
