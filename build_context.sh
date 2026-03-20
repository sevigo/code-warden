#!/bin/bash

# A script to concatenate Markdown, Go, and frontend source files into a single
# context file for an LLM.
#
# It processes files in three stages:
# 1. All relevant Markdown (.md) files are added first for high-level context.
# 2. All Go source files (.go), excluding tests, are appended after.
# 3. All frontend files (TypeScript, TSX, CSS, HTML, configs) from ui/.

# Exit immediately if a command exits with a non-zero status.
set -e

# --- Configuration ---
OUTPUT_DIR="data"
OUTPUT_FILE="$OUTPUT_DIR/llm_context.txt"
# Exclude directories to speed up search and avoid irrelevant files.
EXCLUDE_DIRS=("./vendor" "./.git" "./node_modules" "./data" "./ui/dist" "./ui/node_modules")
# --- End Configuration ---


# 1. Prepare the output directory and file
echo "Preparing output file at $OUTPUT_FILE..." >&2
mkdir -p "$OUTPUT_DIR"
# Clear the output file to start fresh
> "$OUTPUT_FILE"


# 2. Build the 'find' command arguments for excluding directories
find_args=()
for dir in "${EXCLUDE_DIRS[@]}"; do
  # -prune is an efficient way to tell find not to descend into a directory
  find_args+=(-path "$dir" -prune -o)
done

# 3. Define a reusable function to process files
# Arguments to this function are passed directly as predicates to the `find` command.
process_files() {
    find . "${find_args[@]}" "$@" -print0 | \
    while IFS= read -r -d $'\0' filepath; do
        # This loop reads null-terminated paths for safety with special characters.
        echo "  -> Adding $filepath" >&2

        # Append the file header and content to the output file
        {
            echo "--- File: $filepath ---"
            cat "$filepath"
            echo "" # Add a blank line for better separation
        } >> "$OUTPUT_FILE"
    done
}


# --- Stage 1: Process Markdown files ---
echo "Searching for Markdown context files (.md)..." >&2
process_files -type f -name "*.md"


# --- Stage 2: Process Go source files ---
echo "Searching for Go source files (.go)..." >&2
process_files -type f -name "*.go" -not -name "*_test.go"


# --- Stage 3: Process frontend source files ---
echo "Searching for frontend source files (.ts, .tsx, .css, .html)..." >&2
process_files -type f \( -name "*.ts" -o -name "*.tsx" -o -name "*.css" \) -path "*/ui/*"
# Include key config files from ui/ root
for cfg in ui/index.html ui/vite.config.ts ui/tailwind.config.js ui/package.json ui/tsconfig.json; do
    if [ -f "$cfg" ]; then
        echo "  -> Adding ./$cfg" >&2
        {
            echo "--- File: ./$cfg ---"
            cat "$cfg"
            echo ""
        } >> "$OUTPUT_FILE"
    fi
done


# 5. Final summary message
file_count=$(grep -c -e "--- File:" "$OUTPUT_FILE" || true)

if [ "$file_count" -eq 0 ]; then
    echo "Warning: No source files found to build context." >&2
else
    echo "Successfully concatenated $file_count files into $OUTPUT_FILE." >&2
fi