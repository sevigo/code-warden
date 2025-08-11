Of course. Given the significant progress made, here is a refreshed `TODO.md` that reflects the completed work and sets a clear path for the next set of enhancements.

---

# TODO

This document outlines the development roadmap for Code-Warden. It tracks completed features, immediate priorities, and future ideas.

## ✅ Recently Completed

-   **1. Structured, Line-Specific Reviews:** The review process has been fundamentally upgraded.
    -   **Resolution:** The system now prompts the LLM for a structured JSON output. This JSON is parsed into `Suggestions` which are then posted as line-specific comments on the pull request using the GitHub Review API. This provides a vastly superior and more intuitive user experience.
    -   **Benefit:** Feedback is now contextual, actionable, and appears exactly where it's relevant in the "Files Changed" tab.

-   **2. Intelligent RAG Context Caching:** This was the most critical step to make the tool practical and efficient.
    -   **Resolution:** The system now tracks repository state in the PostgreSQL database, storing `last_indexed_sha`. Subsequent reviews perform a `git diff` to incrementally update the vector store, avoiding costly full re-indexing.
    -   **Benefit:** Reduces the time for subsequent reviews on the same repository from minutes to seconds.

-   **3. Repository Configuration (`.code-warden.yml`):** Users can now customize behavior.
    -   **Resolution:** A `.code-warden.yml` file in the repository root allows for `custom_instructions`, `exclude_dirs`, and `exclude_exts`. This configuration is loaded dynamically for each job.
    -   **Benefit:** Makes the tool far more powerful and adaptable, allowing teams to tailor it to their specific needs.

## 🚀 Next Up: Immediate Priorities

### 1. **Re-implement the `/rereview` Command**

This is the highest priority task, as the feature was temporarily disabled to support the new structured review flow.

-   **Problem:** The `/rereview` command is needed to check if a developer has addressed the AI's initial feedback without performing a full, new review. The old implementation is incompatible with the new JSON-based review content.
-   **TODO:**
    1.  **Create a New Prompt:** Design a new `rereview_default.prompt`. It should accept the `NewDiff` and the `OriginalReview` (which is now a JSON string of `core.StructuredReview`). The prompt will instruct the LLM to evaluate the new diff against the original suggestions and determine which have been addressed.
    2.  **Define Structured Output:** The LLM's output for a re-review should also be structured JSON. For example:
        ```json
        {
          "summary": "Looks like you've addressed most of the feedback. One suggestion regarding error handling still seems to be open.",
          "resolved_suggestions": [ /* list of original suggestions that are now fixed */ ],
          "unresolved_suggestions": [ /* list of original suggestions that are still pending */ ]
        }
        ```
    3.  **Update Services:**
        -   Modify `RAGService.GenerateReReview` to use the new prompt and parse the resulting JSON.
        -   Update the `ReviewJob` to call the service and post the `summary` back to the PR. For an enhanced experience, it could quote the `unresolved_suggestions` in the comment.
-   **Benefit:** Restores a core feature of the application and completes the transition to a fully structured review lifecycle.

### 2. **Create a Simple Web UI for Status & Onboarding**

Provide a user-friendly way to see what the app is doing and what repositories are managed.

-   **Problem:** Currently, the only way to interact with the app is through GitHub comments. There's no central place to view the status of managed repositories or past jobs.
-   **TODO:**
    1.  **Add Frontend Routes:** In `internal/server/router.go`, add routes to serve a simple static HTML/JS frontend.
    2.  **Build a Status Page:** Create a page that lists all repositories from the database (`GetAllRepositories`), showing their name, last indexed SHA, and last update time.
    3.  **Show Job History:** Create a page that lists recent review jobs, showing their status (success/failure) and linking to the relevant PR.
-   **Benefit:** Improves transparency and makes the tool feel more like a complete product rather than just a backend service.

## 💡 Future Enhancements & Ideas

### 3. **Implement GitHub "Suggested Changes"**

Take the AI's role one step further by allowing it to suggest concrete code changes that a developer can accept with a single click.

-   **Problem:** The AI currently only comments on what should be changed. Developers must still manually implement the fix.
-   **TODO:**
    1.  **Enhance the Prompt & Struct:** Add a `code_suggestion` field to the `core.Suggestion` struct and update the main review prompt to ask the LLM to populate it.
    2.  **Use the "Suggested Changes" API:** The GitHub Review API supports creating suggestions. The `github.Client` would need to be updated to format comments within the special ````suggestion` code fence.
    3.  **Update the Review Job:** The job would pass the `code_suggestion` content to the `StatusUpdater` to be posted.
-   **Benefit:** Drastically reduces the friction for developers to accept the AI's feedback, speeding up the development cycle.

### 4. **Implement Resource Lifecycle Management (Garbage Collection)**

Ensure long-term stability and manage resource consumption.

-   **Problem:** Git clones and Qdrant collections persist indefinitely, leading to unbounded disk and memory usage over time.
-   **TODO:**
    1.  **Create a "Janitor" Service:** A background service that runs periodically (e.g., every 24 hours).
    2.  **TTL-based Cleanup:** The janitor will find repositories with an `updated_at` timestamp older than a configured TTL (e.g., 90 days) and delete their associated Qdrant collection, disk files, and database record.
    3.  **Handle Uninstallation Events:** Implement a webhook handler for the `installation` event with `action: "deleted"`. When the app is uninstalled, trigger an immediate cleanup of all resources for that installation.
-   **Benefit:** Prevents resource leaks, controls operational costs, and ensures the application remains performant and stable.