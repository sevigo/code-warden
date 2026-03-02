# Agent Package TODO

## High Priority

### Pre-flight Validation in `SpawnAgent`
- [ ] Check `opencode` binary is on PATH and executable
- [ ] Validate config (model exists, working dir is writable)
- [ ] Verify issue exists and is open via GitHub API before spawning
- [ ] Verify MCP server is listening (health check)
- [ ] Confirm VectorStore has indexed data for the target repo

### Streaming Agent Output (replaces `CombinedOutput`)
- [ ] Replace `cmd.CombinedOutput()` with streaming stdout/stderr to a log file per session
- [ ] Cap log file size (rotate or truncate at 10MB)
- [ ] Log raw output path on session completion for post-mortem debugging

### Workspace Setup Optimization
- [ ] Use `git clone --local --depth 1 --single-branch` for faster clones
- [ ] Investigate `git worktree add` as a near-zero-cost alternative
- [ ] Measure and log clone duration

### Session Persistence
- [ ] Add `AgentSession` model to PostgreSQL via `storage.Store`
- [ ] Persist session state transitions (created → running → completed/failed)
- [ ] Store path to agent output log per session
- [ ] On server startup, query for `pending`/`running` sessions and mark them `failed` with reason `"server restarted"`
- [ ] Add API endpoint to query session history

## Medium Priority

### Agent Progress Tracking
- [ ] Post progress comments on GitHub issue at key points ("Agent started", "Running tests...", "Creating PR...")
- [ ] Add `LastActivity` timestamp to `Session` for stale detection
- [ ] Periodically poll `git log --oneline` in workspace to detect commits
- [ ] Track MCP tool calls from server logs (which tools, how often, duration)

### Auto-Review Agent PRs
- [ ] After agent creates PR, automatically queue a `/review` job through the existing review pipeline
- [ ] Post review results as GitHub PR comment
- [ ] If review returns `REQUEST_CHANGES`, optionally re-spawn agent

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

### Multi-Repo Support
- [ ] Scope MCP tools per repository (not global project root)
- [ ] Support multiple VectorStore collections per repo
- [ ] Handle different GitHub App installations per org
