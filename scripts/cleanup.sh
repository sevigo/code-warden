#!/bin/bash
# Code Warden Cleanup Script
# This script wipes the database tables, Qdrant collections, and local cached data.

# Default values
DB_USER="${DB_USER:-warden}"
DB_NAME="${DB_NAME:-codewarden}"
DB_PASS="${DB_PASS:-secret}"
QDRANT_URL="${QDRANT_URL:-http://localhost:6333}"

echo -e "\033[36m--- Code Warden Cleanup ---\033[0m"

# 1. Database Cleanup
echo -e "\033[33m[1/3] Truncating database tables...\033[0m"
if docker ps --format '{{.Names}}' | grep -q 'postgres-db'; then
    docker exec -e PGPASSWORD="$DB_PASS" postgres-db psql -U "$DB_USER" -d "$DB_NAME" -c "TRUNCATE TABLE reviews, repositories, repository_files, scan_state RESTART IDENTITY CASCADE;" 2>/dev/null
    if [ $? -eq 0 ]; then
        echo "  - Database tables truncated."
    else
        echo -e "\033[33m  - Warning: Failed to clean database. Ensure the 'postgres-db' container is running.\033[0m"
    fi
else
    echo -e "\033[33m  - Warning: postgres-db container not found. Skipping database cleanup.\033[0m"
fi

# 2. Qdrant Cleanup
echo -e "\033[33m[2/3] Deleting Qdrant collections...\033[0m"
response=$(curl -s "$QDRANT_URL/collections" 2>/dev/null)
if [ $? -eq 0 ] && [ -n "$response" ]; then
    collections=$(echo "$response" | grep -o '"name":"[^"]*"' | sed 's/"name":"//;s/"//g')
    if [ -z "$collections" ]; then
        echo "  - No collections found."
    else
        echo "$collections" | while read -r col; do
            if [ -n "$col" ]; then
                echo "  - Deleting collection: $col"
                curl -s -X DELETE "$QDRANT_URL/collections/$col" > /dev/null 2>&1
            fi
        done
    fi
else
    echo -e "\033[33m  - Warning: Failed to connect to Qdrant ($QDRANT_URL). Ensure Qdrant is running.\033[0m"
fi

# 3. Local Files Cleanup
echo -e "\033[33m[3/3] Cleaning local storage directories...\033[0m"
PATHS_TO_CLEAN=("data/repos" "reviews")
for path in "${PATHS_TO_CLEAN[@]}"; do
    if [ -d "$path" ]; then
        echo "  - Clearing content of $path"
        find "$path" -mindepth 1 ! -name ".gitkeep" -delete 2>/dev/null
    else
        echo "  - Path $path does not exist, skipping."
    fi
done

echo -e "\n\033[32mCleanup completed successfully!\033[0m"