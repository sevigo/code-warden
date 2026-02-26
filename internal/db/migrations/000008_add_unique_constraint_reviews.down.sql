-- Remove unique constraint from reviews table
ALTER TABLE reviews
DROP CONSTRAINT IF EXISTS reviews_repo_pr_sha_unique;