# Code Warden Cleanup Script
# This script wipes the database tables, Qdrant collections, and local cached data.

param(
    [string]$DbUser = "warden",
    [string]$DbName = "codewarden",
    [string]$DbPass = "secret",
    [string]$QdrantUrl = "http://localhost:6333"
)

Write-Host "--- Code Warden Cleanup ---" -ForegroundColor Cyan

# 1. Database Cleanup
Write-Host "[1/3] Truncating database tables..." -ForegroundColor Yellow
try {
    # We use docker exec to run psql inside the container if available
    docker exec -e PGPASSWORD=$DbPass postgres-db psql -U $DbUser -d $DbName -c "TRUNCATE TABLE reviews, repositories, repository_files, scan_state RESTART IDENTITY CASCADE;"
} catch {
    Write-Warning "Failed to clean database via docker exec. Ensure the 'postgres-db' container is running."
}

# 2. Qdrant Cleanup
Write-Host "[2/3] Deleting Qdrant collections..." -ForegroundColor Yellow
try {
    $response = Invoke-RestMethod -Uri "$QdrantUrl/collections"
    if ($response.result.collections.Count -eq 0) {
        Write-Host "  - No collections found."
    } else {
        foreach ($col in $response.result.collections) {
            Write-Host "  - Deleting collection: $($col.name)"
            Invoke-RestMethod -Method Delete -Uri "$QdrantUrl/collections/$($col.name)" | Out-Null
        }
    }
} catch {
    Write-Warning "Failed to clean Qdrant via REST API ($QdrantUrl). Ensure Qdrant is running."
}

# 3. Local Files Cleanup
Write-Host "[3/3] Cleaning local storage directories..." -ForegroundColor Yellow
$PathsToClean = @("data/repos", "reviews")
foreach ($path in $PathsToClean) {
    if (Test-Path $path) {
        Write-Host "  - Clearing content of $path"
        Get-ChildItem -Path $path -Exclude ".gitkeep" -ErrorAction SilentlyContinue | Remove-Item -Recurse -Force -ErrorAction SilentlyContinue
    } else {
        Write-Host "  - Path $path does not exist, skipping."
    }
}

Write-Host "`nCleanup completed successfully!" -ForegroundColor Green
