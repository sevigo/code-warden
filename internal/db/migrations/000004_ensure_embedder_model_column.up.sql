ALTER TABLE repositories ADD COLUMN IF NOT EXISTS embedder_model_name TEXT NOT NULL DEFAULT '';
