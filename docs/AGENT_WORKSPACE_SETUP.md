# Agent Workspace Configuration

## Overview

To enable the agent functionality in code-warden, you need to configure:

1. **Host filesystem** - Where agent workspaces are created
2. **Working directory** - An absolute path where sessions are isolated

## Configuration

### config.yaml

Set `agent.working_dir` to an absolute path on the host:

```yaml
agent:
  enabled: true
  mode: "warden"
  model: ollama/glm-5:cloud
  in_process_only: true
  working_dir: /path/to/agent-workspaces  # <-- Host path
  # ... other settings
```

When `in_process_only: true` is set, the MCP HTTP server is not started — tools
are registered directly in the goframe AgentLoop. This is the recommended mode
for warden/pi agents since no external process connects over HTTP.

### Pull Ollama Models

For local inference, pull a model:

```bash
# Pull Qwen 3.5 or qwen2.5-coder:3b
docker-compose exec ollama ollama pull qwen3.5:2b

# Pull cloud version of GLM-5
docker-compose exec ollama ollama pull glm-5:cloud

# List available models
docker-compose exec ollama ollama list
```

## How It Works

1. **code-warden** creates workspace directories on the host filesystem at `agent.working_dir`
2. The warden agent runs an in-process goframe AgentLoop with tools injected directly
3. **Changes** made by the agent are persisted to the host filesystem
4. The agent pushes changes to GitHub and opens a PR

## Troubleshooting

### "No such file or directory" errors

Check that:
- The host directory exists and is writable
- `agent.working_dir` in config.yaml is an absolute path

### Changes not persisting

- Ensure the agent workspace directory exists and is writable
- Check that `agent.working_dir` points to the correct location

### Agent not starting

- Verify `agent.enabled: true` in config.yaml
- Check that `agent.mode` is set to `warden` or `native`
- Ensure the configured LLM model is available (e.g., `ollama list`)

## Default Configuration

By default, the system uses:
- Working directory: `/tmp/code-warden-agents`
- Mode: `warden` (phased plan→implement→publish loop)
- `in_process_only: true` (no MCP HTTP server needed)

You can customize these by:
1. Changing `agent.working_dir` in config.yaml
2. Setting `agent.mode` to `warden` or `native`
3. Setting `agent.in_process_only` to `true` for in-process mode