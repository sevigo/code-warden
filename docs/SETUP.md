# Setup Guide

## Quick Demo (no GitHub App)

**Full server with web UI (15 minutes):**

```sh
make quickstart         # guided setup, starts all Docker services
# open http://localhost:8080
```

See the [README](../README.md#quick-start) for GPU support and useful commands.

---

## Full Production Setup

Prerequisites: Go 1.22+, Docker & Docker Compose, a GitHub account with permission to create GitHub Apps, and Ollama running locally or accessible via network.

---

### Step 1: Create the GitHub App

Go to **GitHub → Settings → Developer settings → GitHub Apps → New GitHub App** (or for an org: **Org Settings → Developer settings → GitHub Apps**).

**Basic settings:**

| Field | Value |
|---|---|
| GitHub App name | `code-warden` (or whatever you prefer) |
| Homepage URL | Your server URL, e.g. `https://code-warden.example.com` |
| Webhook URL | `https://your-host/webhook` |
| Webhook secret | Generate a random string — you'll need it in config |

**Permissions** (under Repository permissions):

| Permission | Access |
|---|---|
| Contents | Read |
| Issues | Read & Write |
| Metadata | Read |
| Pull requests | Read & Write |

**Subscribe to events:** Issue comment, Issues, Pull request, Push

**After creating:**

1. Note the **App ID** at the top of the app settings page
2. Scroll to **Private keys** → **Generate a private key**
3. Download the `.pem` file → save as `keys/code-warden-app.private-key.pem`
4. Go to **Install App** → install it on the repositories you want reviewed

---

### Step 2: Clone and configure

```sh
git clone https://github.com/sevigo/code-warden
cd code-warden
cp config.yaml.example config.yaml
mkdir -p keys
mv ~/Downloads/your-app.private-key.pem keys/code-warden-app.private-key.pem
```

Edit `config.yaml`:

```yaml
github:
  app_id: 12345
  webhook_secret: "your-webhook-secret"
  private_key_path: "keys/code-warden-app.private-key.pem"

ai:
  llm_provider: "ollama"
  ollama_host: "http://localhost:11434"
  generator_model: "qwen2.5-coder:7b"

storage:
  repo_path: "/path/to/data/repos"

database:
  host: "localhost"
  port: 5432
  database: "codewarden"
  username: "warden"
  password: "secret"
```

---

### Step 3: Start infrastructure

```sh
docker-compose up -d
docker-compose ps   # verify PostgreSQL is running
```

Pull the Ollama models:

```sh
ollama pull qwen2.5-coder:7b
```

Or if using Docker Compose Ollama:

```sh
docker-compose -f docker-compose.setup.yml up --build
```

---

### Step 4: Build and run

```sh
make build
./bin/code-warden
```

You should see logs confirming that the database connected and the HTTP server is listening on port 8080.

For development:

```sh
go run ./cmd/server/main.go
```

---

### Step 5: Expose the webhook (local development)

GitHub needs to reach your webhook URL. For local development, use a tunnel:

```sh
ngrok http 8080
# or:
bore local 8080 --to bore.pub
```

Update the **Webhook URL** in your GitHub App settings to the tunnel URL + `/webhook`.

---

### Step 6: Index the repository

Before reviews work, Code-Warden needs to index the repository. This happens automatically on the first `/review`, but for large repos you should run a full scan first. For incremental updates after a scan, `/review` re-indexes changed files automatically.

---

### Step 7: Trigger a review

1. Open a pull request in a repository where the GitHub App is installed
2. Comment `/review` on the PR
3. Code-Warden will post a status check, then review findings as inline comments

---

## Verifying the setup

**Qdrant collections created?**

```sh
curl http://localhost:6333/collections
```

After the first scan you should see a collection named after the repository.

**Webhook receiving events?**

Check your GitHub App → **Advanced** → **Recent Deliveries** to see incoming webhook calls and their response codes.

**Reviews posting?**

Check the server logs — look for `generating review` and `posted review comment` lines.

---

## Production considerations

- Run behind a reverse proxy (nginx/caddy) with TLS — GitHub webhooks require HTTPS
- Set `DATABASE_PASSWORD` via environment variable rather than in `config.yaml`
- Use a process manager (systemd, supervisor) or container orchestration to keep the server running
- Set `logging.format: "json"` for structured log aggregation
- Configure `server.max_workers` based on available CPU/memory and expected review volume
