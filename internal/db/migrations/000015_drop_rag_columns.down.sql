ALTER TABLE repositories ADD COLUMN qdrant_collection_name TEXT;
ALTER TABLE repositories ADD COLUMN generated_context TEXT;
ALTER TABLE repositories ADD COLUMN context_updated_at TIMESTAMPTZ;
