# Agent Workspace Configuration

How to set up the `/implement` agent — workspace directory and model setup.

## Configuration

Set `agent.working_dir` to an absolute path on the host:

```yaml
agent:
  enabled: true
  mode: "warden"
  model: ollama/glm-5:cloud
  in_process_only: true
  working_dir: /path/to/agent-workspaces
```

When `in_process_only: true` is set, the MCP HTTP server is not started — tools are registered directly in the goframe AgentLoop. This is the recommended mode since no external process connects over HTTP.

### Pull Ollama Models

```bash
docker-compose exec ollama ollama pull qwen3.5:2b
docker-compose exec ollama ollama pull glm-5:cloud
docker-compose exec ollama ollama list
```

## How It Works

1. Code-Warden creates workspace directories at `agent.working_dir`
2. The warden agent runs an in-process goframe AgentLoop with tools injected directly
3. Changes are persisted to the host filesystem
4. The agent pushes changes to GitHub and opens a PR

## Troubleshooting

**"No such file or directory" errors:** Check that the host directory exists and is writable, and `agent.working_dir` is an absolute path.

**Changes not persisting:** Ensure the workspace directory is writable and `agent.working_dir` points to the right place.

**Agent not starting:** Verify `agent.enabled: true`, `agent.mode` is `warden` or `native`, and the configured LLM model is available (`ollama list`).

## Defaults

- Working directory: `/tmp/code-warden-agents`
- Mode: `warden` (phased plan→implement→publish loop)
- `in_process_only: true` (no MCP HTTP server)