ALTER TABLE repositories DROP COLUMN IF EXISTS qdrant_collection_name;
ALTER TABLE repositories DROP COLUMN IF EXISTS generated_context;
ALTER TABLE repositories DROP COLUMN IF EXISTS context_updated_at;
