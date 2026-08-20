ALTER TABLE reviews
ADD COLUMN publication_status TEXT NOT NULL DEFAULT 'published';

ALTER TABLE reviews
ADD CONSTRAINT reviews_publication_status_valid
CHECK (publication_status IN ('pending', 'published'));
