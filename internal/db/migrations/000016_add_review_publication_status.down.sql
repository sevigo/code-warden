ALTER TABLE reviews
DROP CONSTRAINT IF EXISTS reviews_publication_status_valid;

ALTER TABLE reviews
DROP COLUMN IF EXISTS publication_status;
