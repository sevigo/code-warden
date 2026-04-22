# Troubleshooting

Common issues and how to fix them.

---

## Webhook not receiving events

**Symptom:** PR comments with `/review` do nothing. No log output on the server.

1. Go to your GitHub App settings → **Advanced** → **Recent Deliveries**. If deliveries are failing, you'll see the HTTP status and response body.
2. Verify the webhook URL is correct and publicly reachable (HTTPS required by GitHub).
3. Verify the webhook secret in `config.yaml` matches the one in your GitHub App settings.
4. If running locally, ensure your tunnel (ngrok/bore) is active and the URL in GitHub App settings is updated.

---

## `/review` command not triggering

**Symptom:** Webhook is received (200 OK) but no review is posted.

1. The comment must be exactly `/review` (case-insensitive, no trailing text unless supported).
2. The GitHub App must be installed on the repository.
3. Check server logs for `processing review job` — if missing, the event was received but not enqueued.
4. Check the job queue isn't full — `server.max_workers` controls concurrency.

---

## Review posted but context is empty / review is vague

**Symptom:** Review says something like "cannot provide specific feedback without repository context."

The RAG retrieval returned no documents.

1. Is the repository indexed?
   ```sh
   curl http://localhost:6333/collections
   ```
   If the collection is missing, run a prescan:
   ```sh
   ./bin/warden-cli prescan /path/to/repo
   ```
2. Is the embedder reachable?
   ```sh
   curl http://localhost:11434/api/embeddings -d '{"model":"nomic-embed-text","prompt":"test"}'
   ```
3. Look for `HIGH HALLUCINATION RISK` in server logs — this confirms empty context was detected.

---

## Embedder model not found

**Symptom:** Log line: `failed to generate embeddings: model not found`

```sh
ollama pull nomic-embed-text   # or whatever embedder_model you configured
```

With Docker:
```sh
docker-compose exec ollama ollama pull nomic-embed-text
```

---

## LLM timeout during review

**Symptom:** Log line: `timeout awaiting response headers` or review never completes.

| Cause | Fix |
|---|---|
| Model too slow | Increase `ai.http_response_header_timeout` (default: 15m) |
| Model not loaded in Ollama | Check `ollama list` and pull if missing |
| Ollama out of memory | Use a smaller model or increase RAM |
| Cloud proxy latency | Increase timeout, or switch to a faster model |

---

## Qdrant connection refused

**Symptom:** Log line: `failed to connect to Qdrant` at startup.

```sh
docker-compose ps                       # Is Qdrant running?
curl http://localhost:6333/healthz       # Is it healthy?
```

Code-Warden uses the gRPC port (6334), not the HTTP port (6333). Verify `storage.qdrant_host` in config.

---

## Sparse vector generation fails

**Symptom:** Log line: `sparse vector generation failed, using dense only`

This is **non-fatal** — retrieval falls back to dense-only search. Reviews still work but may miss exact identifier matches. Usually caused by an empty or whitespace-only text being tokenized. Check the surrounding log context.

---

## Prescan stuck or very slow

1. Large binary files may be causing the indexer to stall. Add their extensions to `exclude_exts` in `.code-warden.yml`.
2. Prescan is bottlenecked by embedding generation. A faster embedder model or GPU acceleration helps significantly.
3. Prescan is resumable — kill it and restart, it picks up where it left off.

---

## Agent (`/implement`) not starting

1. `agent.enabled: true` in `config.yaml`
2. `agent.mode` is set to `warden` or `native`
3. `agent.max_concurrent_sessions` limit not reached — check active sessions in logs

---

## Agent workspace errors

**Symptom:** "no such file or directory" when the agent tries to access workspace files.

See [AGENT_WORKSPACE_SETUP.md](./AGENT_WORKSPACE_SETUP.md). Make sure `agent.working_dir` in `config.yaml` is an absolute path and the directory exists and is writable.

---

## Database connection errors

**Symptom:** Log line: `failed to connect to database` at startup.

```sh
docker-compose ps                       # Is PostgreSQL running?
psql -h localhost -U warden -d codewarden  # Can you connect manually?
```

You can set the password via environment variable to avoid putting it in `config.yaml`:
```sh
export DATABASE_PASSWORD=secret
```

---

## Getting more diagnostic information

Set log level to `debug` in `config.yaml`:

```yaml
logging:
  level: "debug"
```

This logs every retrieval query, sparse vector generation, LLM call, and context assembly step. It's very verbose — don't run in production unless you're actively debugging.