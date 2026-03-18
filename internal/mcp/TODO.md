# MCP Package TODO

## High Priority

### Fix `ListTools()` Silent Failure
`ListTools()` now parses the registry's internal `Definitions()` format via nested
`map[string]any` assertions. If goframe changes this format, tools silently disappear
from the list with no error or log — the `continue` on type assertion failure is invisible.
- [ ] Add a test: register a known tool, call `ListTools()`, assert name/description/schema match
- [ ] Log a warning when `fn, ok := def["function"].(map[string]any)` fails so breakage is visible
- [ ] Consider caching the tool metadata at `MustRegisterTool` time to avoid parsing `Definitions()`

### Fix Rate Limit Semantics (Global vs. Per-Session)
`GovernanceConfig.RateLimits` comment says "max calls per session" but the `Governance`
instance is shared across all SSE sessions on the `Server` singleton. Limits are global.
- [ ] Fix the comment in `config.go` to say "max calls globally across all active sessions"
- [ ] Or: implement per-session governance by creating a `Governance` instance per SSE session
  - Store in `sseSession` struct, create at `RegisterWorkspace`, destroy at `UnregisterWorkspace`
  - Per-session is more useful and matches user expectation from the comment

## Medium Priority

### Warn on Empty Governance
When `EnableGovernance: true` but no `AllowedTools`, `DeniedTools`, or `RateLimits` are set,
`s.governance` is non-nil with zero checks. Every `CallTool` runs through validation that
always passes — overhead with no benefit, and potentially confusing to debug.
- [ ] If `checks` is empty after processing the config, log a warning and skip setting `s.governance`

### Strategic: MCP as Workflow Enforcer, Not Code Explorer
The code discovery tools (`search_code`, `get_symbol`, `get_callers`, `get_callees`,
`find_usages`) duplicate capabilities that capable agents (Claude Code, OpenCode) already
have natively via Grep, Glob, direct file reading, and LSP — and the native tools are more
accurate (AST vs. regex, exact match vs. embedding noise).

The genuine MCP differentiators are:
- `review_code`: project-aware RAG review before PR — agents don't have this natively
- `push_branch` + `create_pull_request`: enforced workflow with diff-hash audit trail
- `get_issue` / `list_issues`: convenient but replaceable by `gh` CLI

**Implication:** When designing tools, prioritize workflow enforcement over code discovery.
Discovery tools are useful for less capable agents but add little value for Claude Code.
- [ ] Ensure `review_code` stays the core quality gate; resist weakening the enforcement
- [ ] Track which tools are actually called per session (see agent/TODO.md) to validate this hypothesis
- [ ] Consider: if `get_symbol` definition indexing lands, it becomes the one discovery tool
      with genuine unique value (semantic disambiguation when Grep returns 40 matches)

## Low Priority

### `MustRegisterTool` → `RegisterTool` with Error Propagation
Using `MustRegisterTool` in `registerTools()` means a duplicate registration panics the
server. Safe for now since `registerTools()` is called once, but fragile if re-initialization
is ever added.
- [ ] Switch to `RegisterTool` returning an error, propagate from `registerTools()` to `NewServer`

### `ToolInfo.InputSchema` Field Name Inconsistency
The `Tool` interface method is now `ParametersSchema()` but the `ToolInfo` struct field is
still `InputSchema`. The field is the MCP wire format name so it should stay, but the
asymmetry is confusing when reading the code.
- [ ] Add a comment on `ToolInfo.InputSchema` explaining it maps to the MCP protocol field name,
      not the `Tool.ParametersSchema()` method
