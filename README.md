# Code-Warden

An AI-powered code review assistant that runs locally.

Code-Warden is a GitHub App that uses a local Large Language Model (LLM) to perform code reviews. It is triggered by a simple command in a pull request comment, providing contextual feedback by analyzing the entire repository.

## Features

-   **Flexible LLM Providers**: Supports local models via [Ollama](https://ollama.com/) and cloud-based models like Google's [Gemini](https://deepmind.google/technologies/gemini/).
-   **Context-Aware Reviews**: Uses Retrieval-Augmented Generation (RAG) to understand the entire codebase, leading to more relevant feedback.
-   **Efficient Indexing**: Intelligently updates its context by performing incremental indexing based on file changes, avoiding full re-indexing of the entire repository on subsequent reviews.
-   **Repository-Specific Configuration**: Customize review behavior, excluded files/directories, and custom instructions via a `.code-warden.yml` file in your repository root.
-   **Simple Trigger**: Just comment `/review` on any pull request to start the process, or `/rereview` for a follow-up analysis.
-   **GitHub Integration**: Posts reviews directly as PR comments and updates the check status.

## How It Works

1.  A user comments `/review` or `/rereview` on a pull request.
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

## RunPod

```bash
apt update && apt install -y git python3-venv vim
cd /workspace
git clone https://github.com/sevigo/code-warden.git
cd code-warden/embeddings
python3 -m venv venv
source venv/bin/activate
export HF_HOME="/workspace/huggingface"
echo 'export HF_HOME="/workspace/huggingface"' >> ~/.bashrc
pip install -r requirements.txt
uvicorn main:app --host 0.0.0.0 --port 18000
```