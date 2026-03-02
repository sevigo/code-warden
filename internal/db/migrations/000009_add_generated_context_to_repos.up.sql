ALTER TABLE repositories ADD COLUMN generated_context TEXT NOT NULL DEFAULT '';
ALTER TABLE repositories ADD COLUMN context_updated_at TIMESTAMP WITH TIME ZONE;
