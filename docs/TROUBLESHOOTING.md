# Troubleshooting

Common issues and how to diagnose and fix them.

---

## Webhook not receiving events

**Symptom:** PR comments with `/review` do nothing. No log output on the server.

**Check:**
1. Go to your GitHub App settings → **Advanced** → **Recent Deliveries**. If deliveries are failing, you'll see the HTTP status and response body.
2. Verify the webhook URL is correct and publicly reachable (HTTPS required by GitHub).
3. Verify the webhook secret in `config.yaml` matches the secret set in the GitHub App settings.
4. If running locally, ensure your tunnel (ngrok/bore) is active and the URL in GitHub App settings is updated.

---

## `/review` command not triggering

**Symptom:** Webhook is received (200 OK in GitHub deliveries) but no review is posted.

**Check:**
1. The comment must be exactly `/review` (case-insensitive, no trailing text unless supported).
2. The GitHub App must be installed on the repository.
3. Check server logs for `processing review job` — if missing, the event was received but not enqueued. Look for `unrecognized event` or permission errors.
4. Check the job queue isn't full — `server.max_workers` controls concurrency.

---

## Review posted but context is empty / review is vague

**Symptom:** Review is generated but says something like "cannot provide specific feedback without repository context."

**Cause:** The RAG retrieval returned no documents.

**Check:**
1. Has the repository been indexed? Look for a Qdrant collection for the repo:
   ```sh
   curl http://localhost:6333/collections
   ```
2. If the collection is missing, run a prescan:
   ```sh
   ./bin/warden-cli prescan /path/to/repo
   ```
3. Check the embedder is reachable:
   ```sh
   curl http://localhost:11434/api/embeddings -d '{"model":"nomic-embed-text","prompt":"test"}'
   ```
4. Look for `HIGH HALLUCINATION RISK` in server logs — this confirms empty context was detected.

---

## Embedder model not found

**Symptom:** Log line: `failed to generate embeddings: model not found`

**Fix:**
```sh
ollama pull nomic-embed-text   # or whatever embedder_model you configured
```

If using Docker:
```sh
docker-compose exec ollama ollama pull nomic-embed-text
```

---

## LLM timeout during review

**Symptom:** Log line: `timeout awaiting response headers` or review never completes.

**Causes and fixes:**

| Cause | Fix |
|---|---|
| Model too slow for configured timeout | Increase `ai.http_response_header_timeout` (default: 15m) |
| Model not loaded in Ollama | Check `ollama list` and pull if missing |
| Ollama running out of memory | Use a smaller model or increase available RAM |
| Cloud proxy latency | Increase timeout, or switch to a faster model |

For cloud models routed via Ollama proxy, timeouts of 15–30 minutes are not unusual for large models.

---

## Qdrant connection refused

**Symptom:** Log line: `failed to connect to Qdrant` at startup.

**Check:**
```sh
docker-compose ps        # Is Qdrant running?
curl http://localhost:6333/healthz  # Is it healthy?
```

The gRPC port (6334) is what Code-Warden uses. Verify `storage.qdrant_host` in config points to the gRPC port, not the HTTP port (6333).

---

## Sparse vector generation fails

**Symptom:** Log line: `sparse vector generation failed, using dense only`

This is **non-fatal** — retrieval falls back to dense-only search. The review will still work but may miss exact identifier matches.

**Cause:** Usually an empty or whitespace-only text being tokenized. Check the surrounding log context for the specific input.

---

## Prescan stuck or very slow

**Symptom:** Prescan runs but makes no progress, or is extremely slow.

**Check:**
1. Large binary files may be causing the indexer to stall. Add their extensions to `exclude_exts` in `.code-warden.yml`.
2. Check the embedder throughput — prescan is bottlenecked by embedding generation. A faster embedder model or GPU acceleration will significantly speed it up.
3. Prescan is resumable — kill it and restart and it will continue from where it left off.

---

## Agent (`/implement`) not starting

**Symptom:** `/implement` comment on an issue does nothing or logs an error.

**Check:**
1. `agent.enabled: true` in `config.yaml`
2. MCP server is listening: look for `MCP server listening` in logs
3. OpenCode server is running and accessible at `agent.opencode_url`
4. `agent.max_concurrent_sessions` limit not reached — check active sessions in logs

---

## Agent workspace path mapping errors

**Symptom:** OpenCode agent errors with "no such file or directory" when accessing workspace files.

**Cause:** The host path and the path inside the OpenCode Docker container don't match.

**Fix:** See [AGENT_WORKSPACE_SETUP.md](./AGENT_WORKSPACE_SETUP.md) for the path mapping configuration. Ensure:
- `agent.working_dir` in `config.yaml` is the host path
- The same directory is mounted into the OpenCode container at `/agent-workspaces`
- No trailing slashes in either path

---

## Database connection errors

**Symptom:** Log line: `failed to connect to database` at startup.

**Check:**
```sh
docker-compose ps        # Is PostgreSQL running?
psql -h localhost -U warden -d codewarden  # Can you connect manually?
```

The password can be set via environment variable to avoid putting it in `config.yaml`:
```sh
export DATABASE_PASSWORD=secret
```

---

## MCP tools not discovered by OpenCode

**Symptom:** OpenCode connects to the MCP server but doesn't call any tools.

**Check:**
1. The MCP URL must end with `/sse` and use `"type": "remote"` in OpenCode config (not `"type": "sse"`):
   ```json
   "mcp": {
     "code-warden": {
       "type": "remote",
       "url": "http://127.0.0.1:8081/sse",
       "enabled": true
     }
   }
   ```
2. Check Code-Warden logs for `MCP tool call` entries — if tools are being called but failing, the error will be logged there.
3. Verify the MCP server bind address (`agent.mcp_addr`) is reachable from where OpenCode is running.

---

## Getting more diagnostic information

Set log level to `debug` in `config.yaml`:

```yaml
logging:
  level: "debug"
```

This will log every retrieval query, sparse vector generation, LLM call, and context assembly step. Be aware it is very verbose in production.
