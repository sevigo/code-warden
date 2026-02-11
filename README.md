# Code-Warden

An AI-powered code review assistant that runs locally.

Code-Warden is a GitHub App that uses a local Large Language Model (LLM) to perform code reviews. It is triggered by a simple command in a pull request comment, providing contextual feedback by analyzing the entire repository.

## Features

-   **Flexible LLM Providers**: Supports local models via [Ollama](https://ollama.com/) and cloud-based models like Google's [Gemini](https://deepmind.google/technologies/gemini/).
-   **Context-Aware Reviews**: Uses Retrieval-Augmented Generation (RAG) to understand the entire codebase, leading to more relevant feedback.
-   **Efficient Indexing**: Intelligently updates its context by performing incremental indexing based on file changes, avoiding full re-indexing of the entire repository on subsequent reviews.
-   **Repository-Specific Configuration**: Customize review behavior, excluded files/directories, and custom instructions via a `.code-warden.yml` file in your repository root.
-   **Simple Trigger**: Comment `/review` for a full review, or `/rereview` for a context-aware follow-up. Supports custom instructions like `/rereview check security`.
-   **GitHub Integration**: Posts reviews directly as PR comments and updates the check status.
-   **Enhanced Reporting**: Inline comments feature severity badges (🔴, 🟠, 🟡) and categories, while the review summary provides a clear statistical breakdown of issues.

## How It Works

1.  A user comments `/review` or `/rereview <instructions>` on a pull request.
2.  The Code-Warden server receives a webhook from GitHub.
3.  A background job is dispatched to handle the review.
4.  The job securely clones or updates the repository to the latest PR branch.
5.  The codebase is parsed and converted into vector embeddings, which are stored in [Qdrant](https://qdrant.tech/). For existing repositories, it performs incremental indexing based on file changes, only processing new or modified files.
6.  The job retrieves the code changes (diff) from the pull request.
7.  The diff and other PR details are used to query the vector store for relevant context from the rest of the repository.
8.  A prompt, including the diff, the retrieved context, and any custom instructions from `.code-warden.yml`, is sent to the configured LLM (e.g., a local `gemma3` via Ollama or a remote `gemini-1.5-flash` API).
9.  The LLM generates a code review in Markdown format.
10. The review is posted back to the pull request as a comment, and the GitHub check status is updated.

### Requested review

<img width="613" height="794" alt="image" src="https://github.com/user-attachments/assets/9cc0a478-f942-4b7e-b2f6-0d913d1d83f9" />

---
### Requested re-review

<img width="620" height="394" alt="image" src="https://github.com/user-attachments/assets/d3094a33-84e1-4ffa-bc05-e912dbc01bc0" />

---
## Setup & Running

### 1. Prerequisites

-   Go 1.22+
-   Docker & Docker Compose
-   A configured GitHub App (detailed steps for creating one are usually provided in a separate `CONTRIBUTING.md` or `DEVELOPER_GUIDE.md`).

### 2. Configure Environment

Copy the example environment file and update it with your credentials.

```sh
cp .env.example .env
```

Update the `.env` file with the following:

-   `GITHUB_APP_ID`: Your GitHub App's ID.
-   `GITHUB_WEBHOOK_SECRET`: The secret you configured for your webhook.
-   `GITHUB_PRIVATE_KEY_PATH`: Path to your app's private key (default is `keys/code-warden-app.private-key.pem`).
-   `LLM_PROVIDER`: The LLM provider to use. Supported values are `ollama` (default) and `gemini`.
-   `GENERATOR_MODEL_NAME`: The model to use for generating reviews (e.g., `gemma3:latest` for Ollama, `gemini-1.5-flash` for Gemini).
-   `EMBEDDER_MODEL_NAME`: The model used for creating embeddings (e.g., `nomic-embed-text`).
-   `GEMINI_API_KEY`: Your Google AI Studio API key (only required if `LLM_PROVIDER` is `gemini`).
-   `OLLAMA_HOST`: The URL for your Ollama instance (default: `http://localhost:11434`).
-   `QDRANT_HOST`: The URL for your Qdrant instance (default: `localhost:6334`).
-   `REPO_PATH`: The local directory where repositories will be cloned and managed (default: `./data/repos`).
-   `MAX_WORKERS`: The maximum number of concurrent review jobs (default: `5`).
-   `DB_DRIVER`: Database driver to use (default: `postgres`).
-   `DB_HOST`, `DB_PORT`, `DB_NAME`, `DB_USERNAME`, `DB_PASSWORD`, `DB_SSL_MODE`: PostgreSQL connection details.

### 3. Run Services

Start Ollama, Qdrant, and PostgreSQL using Docker Compose.

```sh
docker-compose up -d
```

### 4. Pull LLM Models (for Ollama)

If you are using the `ollama` provider, pull the models specified in your `.env` file. This step uses a separate `docker-compose.setup.yml` to run a temporary container that pulls the models, ensuring they are available for the main application.

```sh
docker-compose -f docker-compose.setup.yml up --build --remove-orphans
```

### 5. Run the Application

Finally, start the Code-Warden server.

```sh
go run ./cmd/server/main.go
```

The server will start on the port specified in your `.env` file (default is `8080`).

### 6. Building from Source

Instead of running the application directly with `go run`, you can build the binaries for the server and the CLI using the provided `Makefile`.

```sh
# Build both the 'code-warden' server and 'warden-cli' tool
make build
# Run the server from the built binary
./bin/code-warden
# Run the CLI tool
./bin/warden-cli --help
```

* Run the preload for large repo:
```sh
export CW_GITHUB_TOKEN="ghp_YourPersonalAccessTokenGoesHere"
./bin/warden-cli preload --repo-url https://github.com/owner/repo.git
```

* Run a scan for a local repository:
```sh
./bin/warden-cli scan /path/to/your/local/repo
```

### 7. CLI Review Command

Run a code review for any GitHub pull request directly from the command line:

```sh
# Set your GitHub token (required for API access)
export CW_GITHUB_TOKEN="ghp_YourPersonalAccessTokenGoesHere"

# Basic usage
./bin/warden-cli review https://github.com/owner/repo/pull/123

# With verbose output (shows timing and debug info)
./bin/warden-cli review --verbose https://github.com/owner/repo/pull/123
```

**Verbose mode output includes:**
- Step-by-step progress with timing for each phase
- PR metadata (title, SHA, language)
- Index update statistics
- Suggestion count and severity breakdown
- **Consensus Review**: If `comparison_models` are configured in `config.yaml`, the CLI will automatically trigger a **Multi-Model Consensus Review**.
    1.  It queries all configured models + the generator model in parallel.
    2.  It synthesizes their findings into a single, high-quality "Remix" review.
    3.  Benefits: Drastically reduced hallucinations, higher confidence in critical issues, and "Safety in Numbers".

**Troubleshooting:**
| Issue | Solution |
|-------|----------|
| `GITHUB_TOKEN is not set` | Set `CW_GITHUB_TOKEN` or `GITHUB_TOKEN` environment variable |
| `failed to fetch PR` | Check PR URL format and token permissions |
| `failed to sync repo` | Verify network connectivity and disk space |
| `failed to generate review` | Ensure LLM service (Ollama/Gemini) is running |



### 8. CLI Prescan Command

The `prescan` command allows you to pre-process a repository (local or remote) to populate the vector store and generate documentation. It supports resumable scans, meaning if the process is interrupted, it can pick up where it left off.

```sh
# Scan a remote repository (will be cloned to your configured repo_path)
./bin/warden-cli prescan https://github.com/owner/repo

# Scan a local repository
./bin/warden-cli prescan /path/to/local/repo

# Force a fresh scan (ignoring previous progress)
./bin/warden-cli prescan --force https://github.com/owner/repo
```

**Key Features:**
- **Auto-Resume**: Automatically tracks progress. Re-running the command resumes from the last processed file.
- **Centralized Storage**: Remote URLs are cloned to the directory specified in `repo_path`.
- **Documentation**: Generates project structure summaries.
- **Architectural Comparison**: If `comparison_models` are configured, `prescan` will also generate `arch_comparison_<model>.md` files.
    - These files contain high-level architectural summaries of the repository from the perspective of each model.
    - Useful for evaluating which model understands your codebase best before setting it as the main reviewer.
    - Configure specific directories to analyze via `comparison_paths` in `config.yaml`.

## RunPod

```bash
apt update && apt install -y git python3-venv vim
cd /workspace
git clone https://github.com/sevigo/code-warden.git
cd code-warden/embeddings
python3 -m venv venv
source venv/bin/activate

export HF_HOME="/workspace/huggingface"
export EMBEDDING_API_SECRET="YOUR_SECRET_KEY_HERE"
export PYTORCH_CUDA_ALLOC_CONF="expandable_segments:True"

pip install -r requirements.txt
uvicorn main:app --host 0.0.0.0 --port 18000
```