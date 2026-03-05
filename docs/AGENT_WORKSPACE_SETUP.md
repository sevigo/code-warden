# Agent Workspace Configuration

## Overview

To enable the agent functionality in code-warden with Docker-based OpenCode, you need to configure:

1. **Host filesystem** - Where agent workspaces are created
2. **Docker container** - Mount that directory into the OpenCode container
3. **Path mapping** - Tell goframe/agent to translate host paths to container paths

## Configuration Steps

### 1. config.yaml

Set `agent.working_dir` to an absolute path on the host:

```yaml
agent:
  enabled: true
  provider: opencode-sdk
  opencode_url: http://localhost:3000
  model: ollama/glm-5:cloud
  working_dir: /path/to/agent-workspaces  # <-- Host path
  # ... other settings
```

### 2. docker-compose.yml

Mount the host workspace directory into the OpenCode container at `/agent-workspaces`:

```yaml
opencode:
  image: ghcr.io/anomalyco/opencode:latest
  volumes:
    - .:/app/workspace
    - ${AGENT_WORKSPACE_DIR:-/path/to/agent-workspaces}:/agent-workspaces
    - opencode_config:/root/.opencode
```

### Pull Ollama Models

For local inference, pull a model:

```bash
# Pull Qwen 3.5 or qwen2.5-coder:3b
docker-compose exec ollama ollama pull qwen3.5:2b
# docker-compose exec ollama ollama pull qwen2.5-coder:3b

# Pull cloud version of GLM-5
docker-compose exec ollama ollama pull glm-5:cloud

# List available models
docker-compose exec ollama ollama list
```

**Environment variable option:**
```bash
export AGENT_WORKSPACE_DIR=/path/to/agent-workspaces
export OPENCODE_BASE_URL=http://localhost:3000
export OPENCODE_MODEL=ollama/glm-5:cloud
#export OPENCODE_MODEL=ollama/qwen3.5:2b
docker-compose up -d
```

### 3. code-warden internal mapping

The path mapping is configured in `internal/agent/orchestrator.go`:

```go
pathMapping := map[string]string{
    o.config.WorkingDir: "/agent-workspaces",
}

ag, err := goframeagent.New(
    goframeagent.WithPathMapping(pathMapping),
    // ... other options
)
```

This tells the goframe agent SDK to translate:
- `/path/to/agent-workspaces` → `/agent-workspaces`
- `/path/to/agent-workspaces/session-123` → `/agent-workspaces/session-123`

## How It Works

1. **code-warden** creates workspace directories on the host filesystem at `agent.working_dir`
2. **Docker** mounts that directory into the OpenCode container at `/agent-workspaces`
3. **goframe/agent** SDK translates host paths to container paths when creating sessions
4. **OpenCode** runs in the container and can access the workspace files
5. **Changes** made by the agent are persisted to the host filesystem

## Troubleshooting

### "No such file or directory" errors

Check that:
- The host directory exists and is writable
- The Docker volume mount is configured correctly
- The path mapping in code-warden matches the Docker mount

### Changes not persisting

- Ensure the agent workspace directory is mounted as a volume, not a bind mount to a different location
- Check that `AGENT_WORKSPACE_DIR` environment variable is set if using the default

### Path mapping not working

- Verify paths are absolute (not relative)
- Check that the host path in config.yaml matches the path in the pathMapping
- Ensure no trailing slashes in paths (they can cause issues with matching)

## Default Configuration

By default, the system uses:
- Host path: `/path/to/agent-workspaces`
- Container path: `/agent-workspaces`
- Environment variable: `AGENT_WORKSPACE_DIR`

You can customize these by:
1. Changing `agent.working_dir` in config.yaml
2. Setting `AGENT_WORKSPACE_DIR` environment variable
3. Updating the path mapping in orchestrator.go if needed
