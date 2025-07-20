-- Drop the index first to remove its dependency on the table
DROP INDEX IF EXISTS idx_reviews_repo_pr;

-- Drop the reviews table
DROP TABLE IF EXISTS reviews;