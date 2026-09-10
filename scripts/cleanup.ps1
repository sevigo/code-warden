# Code Warden Cleanup Script
# This script wipes the database tables and local cached data.

param(
    [string]$DbUser = "warden",
    [string]$DbName = "codewarden",
    [string]$DbPass = "secret"
)

Write-Host "--- Code Warden Cleanup ---" -ForegroundColor Cyan

# 1. Database Cleanup
Write-Host "[1/2] Truncating database tables..." -ForegroundColor Yellow
try {
    # We use docker exec to run psql inside the container if available
    docker exec -e PGPASSWORD=$DbPass postgres-db psql -U $DbUser -d $DbName -c "TRUNCATE TABLE reviews, repositories, repository_files, scan_state RESTART IDENTITY CASCADE;"
} catch {
    Write-Warning "Failed to clean database via docker exec. Ensure the 'postgres-db' container is running."
}

# 2. Local Files Cleanup
Write-Host "[2/2] Cleaning local storage directories..." -ForegroundColor Yellow
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
