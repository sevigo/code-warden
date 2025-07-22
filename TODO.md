# TODO

This document outlines planned improvements and future work for Code-Warden.

### 1. **Implement Intelligent RAG Context Caching and Invalidation** (DONE)

This was the most critical step to make the tool practical and efficient.

*   **Resolution:** The system now tracks repository state in the PostgreSQL database, storing `last_indexed_sha` and `qdrant_collection_name`. For new reviews, `git diff` is performed between the current `HEAD` SHA and `last_indexed_sha`. The vector store is updated incrementally: added/modified files are upserted, and deleted files' vectors are removed from the Qdrant collection.
*   **Benefit:** Reduces the time for subsequent reviews on the same repository from minutes to seconds, making the tool feel instantaneous after the initial indexing.

### 2. **Refine the Review Prompt and Add Structured Output** (Partially Done)

Improve the quality and consistency of the AI's feedback.

*   **Problem:** The current prompt is good, but the LLM's output is free-form Markdown. It can be inconsistent and hard to parse for metrics or UI enhancements.
*   **TODO:**
    1.  **Chain-of-Thought Prompting:** Modify the prompt to ask the LLM to "think step-by-step" before writing the final review. Ask it to first identify potential issues, categorize them (e.g., "Bug," "Style," "Performance"), and then formulate its response.
    2.  **Structured JSON Output:** Change the prompt to request the final output as a **JSON object** that your application can parse. This JSON could have a structure like:
        ```json
        {
          "summary": "Overall, the changes look good but I have a few suggestions regarding error handling.",
          "suggestions": [
            {
              "file_path": "internal/jobs/review.go",
              "line_number": 85,
              "severity": "Medium", // "Low", "Medium", "High"
              "category": "Best Practice", // "Bug", "Style", "Performance"
              "comment": "The error from `statusUpdater.Completed` is not handled. While the job is ending, it's good practice to log this failure.",
              "code_suggestion": "if err != nil { j.logger.Error(\"failed to update final status\", \"error\", err) }"
            }
          ]
        }
        ```
    3.  **Render the JSON:** Your `review.go` job would then parse this JSON and format it into a beautiful Markdown comment with tables, code blocks, and clear sections.
*   **Benefit:** Produces higher-quality, more consistent, and more actionable reviews. Opens the door for future features like reporting metrics on review categories.

### 3. **Enhance GitHub Integration: Line-Specific Comments**

Post comments directly on the lines of code being changed, just like a human reviewer.

*   **Problem:** The current implementation posts a single, large comment on the PR's general discussion thread. This can be hard to map back to the specific lines of code in the "Files Changed" tab.
*   **TODO:**
    1.  **Parse the Diff:** In your `review.go` job, you already have the `.diff` file content. You need to parse this diff to map the file paths and line numbers of the changes.
    2.  **Use the GitHub "Review Comments" API:** Instead of the Issues/Comments API, use the [Pull Request Review Comments API](https://docs.github.com/en/rest/pulls/comments?apiVersion=2022-11-28#create-a-review-comment-for-a-pull-request).
    3.  **Post Line Comments:** If you implemented structured JSON output (from TODO #2), you can now loop through the `suggestions` array. For each suggestion that has a `file_path` and `line_number`, make an API call to post that specific comment on that exact line in the PR.
*   **Benefit:** Massively improves the user experience. The AI's feedback appears exactly where it's relevant, making it feel much more like a real team member's review.

### 4. **Add a Configuration File to the User's Repository** (DONE)

Allow users to customize the behavior of `Code-Warden` for their specific repository.

*   **Resolution:** Users can now define a `.code-warden.yml` file in their repository root. This file allows for `custom_instructions`, `exclude_dirs`, and `exclude_exts`. The `review.go` job loads and applies this configuration to dynamically modify the LLM prompt and file parsing/indexing logic.
*   **Benefit:** Makes the tool far more powerful and adaptable, allowing teams to tailor it to their specific needs.

### 5. **Create a Simple Web UI for Onboarding and Status**

Provide a user-friendly way to see what the app is doing.

*   **Problem:** Currently, the only way to interact with the app is through GitHub comments. There's no way to see which repositories are enabled or the status of past jobs.
*   **TODO:**
    1.  **Add Frontend Routes:** In `internal/server/router.go`, add routes to serve a simple static HTML/JS frontend.
    2.  **Build a Status Page:** Create a simple page that lists the repositories the app is installed on (requires a DB query).
    3.  **Show Job History:** Create a page that lists recent review jobs from your database, showing their status (success/failure) and linking to the PR.
    4.  **Real-time Updates:** Use Server-Sent Events (SSE) or WebSockets to update the UI in real-time when a new job starts or finishes.
*   **Benefit:** Improves transparency and makes the tool feel more like a complete product rather than just a backend service.

### 6. **Implement Resource Lifecycle Management (Garbage Collection)**

To ensure long-term stability and manage resource consumption, a garbage collection mechanism is needed to clean up old, unused, or abandoned resources.

*   **Problem:** The application currently persists Git repository clones and Qdrant vector collections indefinitely. This will lead to unbounded disk and memory usage over time, especially if repositories are abandoned or the app is uninstalled.
*   **TODO:**
    1.  **Create a "Janitor" Service:** A new background service responsible for cleanup tasks. This service will run periodically (e.g., every 24 hours), controlled by a configuration setting.
    2.  **Implement TTL-based Cleanup:**
        *   The janitor will query the `repositories` table for entries where the `updated_at` timestamp is older than a configurable Time-To-Live (TTL), e.g., `90 days`.
        *   For each expired repository, the janitor will perform a three-step cleanup:
            1.  Delete the corresponding Qdrant collection using the `qdrant_collection_name`.
            2.  Delete the local Git repository clone from the disk using the `clone_path`.
            3.  Delete the record from the `repositories` table in the database.
    3.  **Handle External Deletion Events:**
        *   Implement a new webhook handler for the `installation_repositories` event with `action: "removed"` and the `installation` event with `action: "deleted"`.
        *   When a repository is removed from the app installation or the app is uninstalled, trigger an immediate, explicit deletion of all associated resources (DB record, Qdrant collection, disk files) for the affected repositories.
*   **Benefit:** Prevents resource leaks, controls operational costs, and ensures the application remains performant and stable over its lifetime.