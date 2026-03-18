# GoFrame Agent Integration Plan

## Overview

Integrate `github.com/sevigo/goframe/agent` package's `Registry` and `Governance` components into code-warden's MCP server. This provides standardized tool management and pre-execution validation.

## Goals

1. Use goframe's `Registry` for MCP tool registration and execution
2. Add `Governance` layer for security validation before tool execution
3. Maintain backward compatibility with existing MCP protocol

## Interface Compatibility

The existing `mcp.Tool` interface is **identical** to `goframe/agent.Tool`:

```go
// Code-warden: internal/mcp/server.go
type Tool interface {
    Name() string
    Description() string
    InputSchema() map[string]any
    Execute(ctx context.Context, args map[string]any) (any, error)
}

// GoFrame: agent/tool.go
type Tool interface {
    Name() string
    Description() string
    ParametersSchema() map[string]any  // Same purpose as InputSchema
    Execute(ctx context.Context, params map[string]any) (any, error)
}
```

**Minor difference**: `InputSchema()` vs `ParametersSchema()` - need adapter or rename.

## Implementation Steps

### Phase 1: Update Tool Interface (Small Breaking Change)

**Files to modify:**
- `internal/mcp/server.go` - Rename `InputSchema()` to `ParametersSchema()`
- `internal/mcp/tools/*.go` - Rename method in all tools

**Changes:**
```go
// Before
func (t *SearchCode) InputSchema() map[string]any { ... }

// After
func (t *SearchCode) ParametersSchema() map[string]any { ... }
```

**Update ToolInfo to adapt:**
```go
type ToolInfo struct {
    Name        string         `json:"name"`
    Description string         `json:"description"`
    InputSchema map[string]any `json:"inputSchema"` // Keep for JSON-RPC compatibility
}

// In ListTools, use ParametersSchema:
tools = append(tools, ToolInfo{
    Name:        name,
    Description: tool.Description(),
    InputSchema: tool.ParametersSchema(), // Adapter
})
```

### Phase 2: Integrate Registry

**Files to modify:**
- `internal/mcp/server.go`

**Changes:**
1. Import goframe agent package:
   ```go
   import "github.com/sevigo/goframe/agent"
   ```

2. Replace internal `map[string]Tool` with `agent.Registry`:
   ```go
   // Before
   type Server struct {
       tools map[string]Tool
       // ...
   }

   // After
   type Server struct {
       registry *agent.Registry
       // ...
   }
   ```

3. Update `registerTools()`:
   ```go
   func (s *Server) registerTools() {
       s.registry = agent.NewRegistry()
       
       // Register tools
       s.registry.MustRegisterTool(&tools.SearchCode{...})
       s.registry.MustRegisterTool(&tools.GetSymbol{...})
       // ...
   }
   ```

4. Update `CallTool`:
   ```go
   func (s *Server) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
       return s.registry.Execute(ctx, name, args)
   }
   ```

5. Update `ListTools`:
   ```go
   func (s *Server) ListTools() []ToolInfo {
       defs := s.registry.Definitions()
       tools := make([]ToolInfo, len(defs))
       for i, def := range defs {
           fn := def["function"].(map[string]any)
           tools[i] = ToolInfo{
               Name:        fn["name"].(string),
               Description: fn["description"].(string),
               InputSchema: fn["parameters"].(map[string]any),
           }
       }
       sort.Slice(tools, func(i, j int) bool {
           return tools[i].Name < tools[j].Name
       })
       return tools
   }
   ```

### Phase 3: Add Governance Layer

**Files to modify:**
- `internal/mcp/server.go`
- `internal/mcp/config.go` (new file)

**New configuration:**
```go
// internal/mcp/config.go
package mcp

type GovernanceConfig struct {
    // AllowedTools restricts which tools can be called.
    // If empty, all tools are allowed.
    AllowedTools []string `yaml:"allowed_tools"`
    
    // DeniedTools explicitly blocks certain tools.
    DeniedTools []string `yaml:"denied_tools"`
    
    // RateLimits maps tool names to max calls per session.
    RateLimits map[string]int `yaml:"rate_limits"`
    
    // EnableGovernance enables the governance layer.
    EnableGovernance bool `yaml:"enable_governance"`
}
```

**Governance setup:**
```go
func (s *Server) setupGovernance(cfg GovernanceConfig) *agent.Governance {
    checks := []agent.IntegrityCheck{}
    
    // Permission check
    if len(cfg.AllowedTools) > 0 || len(cfg.DeniedTools) > 0 {
        permCheck := agent.NewPermissionCheck()
        for _, tool := range cfg.AllowedTools {
            permCheck.Allow(tool)
        }
        for _, tool := range cfg.DeniedTools {
            permCheck.Deny(tool)
        }
        checks = append(checks, permCheck)
    }
    
    // Rate limits
    if len(cfg.RateLimits) > 0 {
        rateCheck := agent.NewRateLimitCheck()
        for tool, limit := range cfg.RateLimits {
            rateCheck.SetLimit(tool, limit)
        }
        checks = append(checks, rateCheck)
    }
    
    return agent.NewGovernance(checks...)
}
```

**Execution with governance:**
```go
func (s *Server) CallTool(ctx context.Context, name string, args map[string]any) (any, error) {
    // Validate with governance
    if s.governance != nil {
        if err := s.governance.Validate(ctx, name, args); err != nil {
            s.logger.Warn("tool execution blocked by governance",
                "tool", name,
                "error", err)
            return nil, fmt.Errorf("governance denied: %w", err)
        }
    }
    
    return s.registry.Execute(ctx, name, args)
}
```

### Phase 4: Update Wire Dependencies

**Files to modify:**
- `internal/wire/wire.go`

Add governance config to wire setup:
```go
// In ProviderSet
NewGovernanceConfig,
```

### Phase 5: Configuration File

**Files to modify:**
- `config.yaml` (document new options)

```yaml
mcp:
  governance:
    enable_governance: true
    # Optional: restrict tools for security
    allowed_tools:
      - search_code
      - get_symbol
      - get_structure
      - get_arch_context
    # Rate limiting
    rate_limits:
      review_code: 5  # max 5 reviews per session
```

## Testing Plan

### Unit Tests

1. **Registry Integration Tests**
   - Verify all tools register correctly
   - Verify tool execution through registry
   - Verify tool definitions format

2. **Governance Tests**
   - Test permission check (allow/deny)
   - Test rate limiting
   - Test composite checks

### Integration Tests

1. **MCP Protocol Compatibility**
   - SSE transport still works
   - JSON-RPC still works
   - Tool definitions match expected format

2. **Governance Integration**
   - Blocked tools return proper error
   - Rate limits reset between sessions

## Files Changed

### Modified Files
| File | Changes |
|------|---------|
| `internal/mcp/server.go` | Use Registry, add Governance |
| `internal/mcp/tools/*.go` | Rename InputSchema to ParametersSchema |
| `internal/wire/wire.go` | Add governance config |
| `config.yaml` | Document new options |

### New Files
| File | Purpose |
|------|---------|
| `internal/mcp/config.go` | Governance configuration |
| `internal/mcp/governance_test.go` | Governance tests |

## Rollback Plan

If issues arise:
1. Revert `internal/mcp/server.go` to use `map[string]Tool`
2. Keep `ParametersSchema()` rename (backward compatible adapter exists)
3. Remove governance config from wire

## Dependencies

- `github.com/sevigo/goframe/agent` >= v0.23.2

No new external dependencies required.

## Success Criteria

1. All existing MCP tests pass
2. All tools execute correctly through registry
3. Governance blocks unauthorized tool calls
4. Rate limits enforce per-session limits
5. No breaking changes to MCP protocol