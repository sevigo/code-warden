CREATE TABLE IF NOT EXISTS repositories (
    id                      BIGSERIAL PRIMARY KEY,
    full_name               TEXT NOT NULL UNIQUE,
    clone_path              TEXT NOT NULL,
    qdrant_collection_name  TEXT NOT NULL,
    last_indexed_sha        TEXT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
