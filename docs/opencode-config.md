# OpenCode Agent Configuration

This document describes how to configure OpenCode to work with code-warden's MCP server.

## Prerequisites

1. **OpenCode installed** - Follow installation instructions at [opencode.ai](https://opencode.ai)
2. **code-warden MCP server running** - The MCP server should be accessible at `http://127.0.0.1:8081`
3. **Ollama running** (or other configured LLM provider) - Required for the agent to function

## Configuration

### OpenCode Config File

Create or edit `~/.config/opencode/opencode.json`:

```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "ollama": {
      "models": {
        "qwen2.5-coder:latest": {
          "name": "qwen2.5-coder:latest"
        }
      },
      "name": "Ollama (local)",
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "baseURL": "http://127.0.0.1:11434/v1"
      }
    }
  },
  "mcp": {
    "code-warden": {
      "type": "remote",
      "url": "http://127.0.0.1:8081/sse",
      "enabled": true
    }
  }
}
```

### Configuration Fields

| Field | Description | Required |
|-------|-------------|----------|
| `type` | Must be `"remote"` for SSE transport | Yes |
| `url` | The MCP server endpoint (must end with `/sse`) | Yes |
| `enabled` | Whether the MCP server is active | Yes |

### Important Notes

1. **URL format**: The URL must end with `/sse` for SSE transport. Using `"type": "sse"` is incorrect; use `"type": "remote"` instead.

2. **Provider configuration**: You can use any supported LLM provider. The example shows Ollama with qwen2.5-coder.

3. **MCP server URL**: The default MCP server address is `http://127.0.0.1:8081`. This can be changed in code-warden's `config.yaml`:

```yaml
agent:
  enabled: true
  mcp_addr: "127.0.0.1:8081"
  opencode_addr: "http://127.0.0.1:4096"
  model: "qwen2.5-coder"
  timeout: "30m"
  max_iterations: 3
  working_dir: "/tmp/code-warden-agents"
```

## Starting the Services

### Option 1: Using Makefile

```bash
# Start code-warden server
make run

# In another terminal, start OpenCode server (optional)
make opencode-start
```

### Option 2: Manual Start

```bash
# Start code-warden
./bin/code-warden

# OpenCode will connect automatically when needed
# Or start OpenCode server manually:
opencode serve &>/tmp/opencode.log &
```

## MCP Tools Available

When connected, the agent has access to these MCP tools:

### Code Understanding Tools

| Tool | Description |
|------|-------------|
| `search_code` | Search for code using semantic similarity |
| `get_arch_context` | Get architectural summary for a directory |
| `get_symbol` | Get type/function definition by name |
| `get_structure` | Get project structure overview |
| `review_code` | Request internal code review |

### GitHub Integration Tools

| Tool | Description |
|------|-------------|
| `create_pull_request` | Create a pull request with title, body, and branch |
| `list_issues` | List repository issues with filtering options |
| `get_issue` | Get detailed information about a specific issue |

These tools allow agents to:
1. Understand what needs to be implemented (`get_issue`, `list_issues`)
2. Explore the codebase (`search_code`, `get_arch_context`, `get_symbol`)
3. Validate changes (`review_code`)
4. Submit work (`create_pull_request`)

## Agent Modes

### CLI Mode (Recommended)

When `opencode_addr` is empty or not set, the agent uses CLI mode:

```yaml
agent:
  enabled: true
  opencode_addr: ""  # Empty = CLI mode
```

CLI mode is recommended because:
- ✅ Actually executes commands (git, make, etc.)
- ✅ Better integration with MCP tools
- ✅ Simpler architecture

### HTTP API Mode

When `opencode_addr` is set, the agent uses HTTP API mode:

```yaml
agent:
  enabled: true
  opencode_addr: "http://127.0.0.1:4096"
```

⚠️ **Note**: HTTP API mode may not execute commands automatically. The agent responds to prompts but may not run shell commands or use MCP tools without additional configuration.

## Troubleshooting

### MCP Tools Not Discovered

If OpenCode connects but doesn't use MCP tools:

1. Verify config format uses `"type": "remote"` not `"type": "sse"`
2. Verify URL ends with `/sse`
3. Check code-warden logs for "MCP tool call" messages

### Connection Refused

1. Ensure code-warden is running
2. Verify `agent.mcp_addr` matches in config
3. Check firewall settings

### Session Timeout

Default timeout is 30 minutes. Adjust in `config.yaml`:

```yaml
agent:
  timeout: "60m"  # Increase timeout for complex issues
```

## Security Considerations

1. **Local binding only**: MCP server binds to `127.0.0.1` by default
2. **No authentication**: MCP server has no authentication - only use on trusted networks
3. **Branch sanitization**: Agent branch names are sanitized to prevent injection
4. **Shell command safety**: Git commands use proper escaping

## Example Session Flow

1. Agent receives issue via `/implement` comment on GitHub
2. Agent connects to MCP server
3. Agent explores codebase using MCP tools
4. Agent implements changes
5. Agent runs `make lint` and `make test` (mandatory)
6. Agent calls `review_code` tool
7. Agent creates pull request