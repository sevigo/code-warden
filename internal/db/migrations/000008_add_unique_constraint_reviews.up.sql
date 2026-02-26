-- Add unique constraint to prevent duplicate reviews for the same PR/SHA combination
-- This ensures atomic duplicate prevention at the database level

-- First, remove any existing duplicates (keep the most recent one)
DELETE FROM reviews r1
WHERE EXISTS (
    SELECT 1 FROM reviews r2
    WHERE r2.repo_full_name = r1.repo_full_name
      AND r2.pr_number = r1.pr_number
      AND r2.head_sha = r1.head_sha
      AND r2.created_at > r1.created_at
);

-- Add the unique constraint
ALTER TABLE reviews
ADD CONSTRAINT reviews_repo_pr_sha_unique UNIQUE (repo_full_name, pr_number, head_sha);