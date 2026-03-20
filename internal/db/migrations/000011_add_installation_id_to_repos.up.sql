-- migration to add installation_id to repositories table
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS installation_id BIGINT NOT NULL DEFAULT 0;
