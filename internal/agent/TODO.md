# Agent Package TODO

## High Priority

### Implement `parseAgentOutput`
- [ ] Define structured output format for OpenCode (JSON envelope with PR info, files changed, verdict)
- [ ] Parse stdout for PR URL/number, branch name, files changed, review verdict
- [ ] Handle partial output (agent crashed mid-run) gracefully
- [ ] Extract iteration count from review cycles
- [ ] Log raw output to file for post-mortem debugging

### Session Persistence
- [ ] Add `AgentSession` model to PostgreSQL via `storage.Store`
- [ ] Persist session state transitions (created → running → completed/failed)
- [ ] Store agent output logs per session
- [ ] Recover in-progress sessions on server restart (mark as failed with "server restarted")
- [ ] Add API endpoint to query session history

### Pre-flight Validation in `SpawnAgent`
- [ ] Verify issue exists and is open via GitHub API before spawning
- [ ] Check `opencode` binary is on PATH and executable
- [ ] Verify MCP server is listening (health check)
- [ ] Confirm VectorStore has indexed data for the target repo
- [ ] Validate config (model exists, working dir is writable)

## Medium Priority

### Agent Progress Tracking
- [ ] Periodically poll `git log --oneline` in workspace to detect commits
- [ ] Track MCP tool calls from server logs (which tools, how often, duration)
- [ ] Post progress comments on GitHub issue ("Agent is exploring codebase...", "Running tests...")
- [ ] Add `LastActivity` timestamp to `Session` for stale detection

### Streaming Agent Output
- [ ] Replace `cmd.CombinedOutput()` with streaming to a log file
- [ ] Tail the log file for real-time progress updates
- [ ] Cap log file size (rotate or truncate at 10MB)
- [ ] Expose log stream via SSE endpoint for dashboard

### Workspace Setup Optimization
- [ ] Use `git clone --depth 1 --single-branch` for faster clones
- [ ] Investigate `git worktree add` as alternative to full clone
- [ ] Cache base clone and copy for each session (copy-on-write if supported)
- [ ] Measure and log clone duration

### Failed Session Cleanup
- [ ] Delete orphaned remote branches (`git push origin --delete <branch>`) on session failure
- [ ] Add configurable retention policy for workspace directories
- [ ] Log cleanup actions for audit trail

## Low Priority / Future

### Workflow Enforcement
- [ ] Track which MCP tools were called per session
- [ ] Gate `create_pull_request` behind prior `review_code` call
- [ ] Gate `create_pull_request` behind prior `push_branch` call
- [ ] Add `run_command` MCP tool for verified lint/test execution
- [ ] Enforce minimum review confidence before allowing PR creation

### Agent Metrics & Observability
- [ ] Track: success rate, avg duration, tool usage frequency, iteration count
- [ ] Track: token usage per session (if LLM provider exposes it)
- [ ] Export metrics via Prometheus endpoint or structured logs
- [ ] Dashboard for active/completed/failed sessions

### Auto-Review Agent PRs
- [ ] After agent creates PR, automatically queue a `/review` job
- [ ] Post review results as GitHub PR comment for visibility
- [ ] If review returns `REQUEST_CHANGES`, optionally re-spawn agent

### Multi-Repo Support
- [ ] Scope MCP tools per repository (not global project root)
- [ ] Support multiple VectorStore collections per repo
- [ ] Handle different GitHub App installations per org

### OpenCode Client Improvements
- [ ] Add retry logic with exponential backoff to `doRequest`
- [ ] Add request/response logging at debug level
- [ ] Consider replacing `fmt.Sprintf("gh pr create --title %q ...")` with `exec.Command` args
- [ ] Add connection pooling configuration
