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

## Review posted but review is vague

**Symptom:** Review says something like "cannot provide specific feedback without repository context."

The agent was unable to investigate the diff against the repository. Common causes:

1. The repo checkout could not be cloned (check network/auth and `repo_path` permissions).
2. The configured LLM model is unreachable or overloaded.

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

This logs every agent tool call and LLM call. It's very verbose — don't run in production unless you're actively debugging.
