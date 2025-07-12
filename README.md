# Code-Warden

An AI-powered code review assistant that runs locally.

Code-Warden is a GitHub App that uses a local Large Language Model (LLM) to perform code reviews. It is triggered by a simple command in a pull request comment, providing contextual feedback by analyzing the entire repository.

## Features

-   **Flexible LLM Providers**: Supports local models via [Ollama](https://ollama.com/) and cloud-based models like Google's [Gemini](https://deepmind.google/technologies/gemini/).
-   **Context-Aware Reviews**: Uses Retrieval-Augmented Generation (RAG) to understand the entire codebase, leading to more relevant feedback.
-   **Simple Trigger**: Just comment `/review` on any pull request to start the process.
-   **GitHub Integration**: Posts reviews directly as PR comments and updates the check status.

## How It Works

1.  A user comments `/review` on a pull request.
2.  The Code-Warden server receives a webhook from GitHub.
3.  A background job is dispatched to handle the review.
4.  The job clones the repository and the PR branch.
5.  The entire codebase is parsed and converted into vector embeddings, which are stored in [Qdrant](https://qdrant.tech/).
6.  The job retrieves the code changes (diff) from the pull request.
7.  The diff is used to query the vector store for relevant context from the rest of the repository.
8.  A prompt, including the diff and the retrieved context, is sent to the configured LLM (e.g., a local `gemma3` via Ollama or a remote `gemini-1.5-flash` API).
9.  The LLM generates a code review in Markdown format.
10. The review is posted back to the pull request as a comment.

## Setup & Running

### 1. Prerequisites

-   Go 1.22+
-   Docker & Docker Compose
-   A configured GitHub App

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

### 3. Run Services

Start Ollama and Qdrant using Docker Compose.

```sh
docker-compose up -d
```

### 4. Pull LLM Models (for Ollama)

If you are using the `ollama` provider, pull the models specified in your `.env` file.

```sh
docker-compose -f docker-compose.setup.yml up --build --remove-orphans
```

### 5. Run the Application

Finally, start the Code-Warden server.

```sh
go run ./cmd/server/main.go
```

The server will start on the port specified in your `.env` file (default is `8080`).
