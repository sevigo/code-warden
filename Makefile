# Binary and Command Path Configuration
SERVER_BINARY_NAME=code-warden
SERVER_CMD_PATH=./cmd/server

CLI_BINARY_NAME=warden-cli
CLI_CMD_PATH=./cmd/cli

TERMINAL_BINARY_NAME=warden-term
TERMINAL_CMD_PATH=./cmd/terminal

# Output directory for all binaries and tools
BIN_DIR=$(CURDIR)/bin

GOLINT_BIN_DIR=$(CURDIR)/bin
GOLINT_CMD=$(GOLINT_BIN_DIR)/golangci-lint
GOLINT_VERSION=v2.11.3

.DEFAULT_GOAL := all
.PHONY: all build run clean test lint dev ui-deps build-ui dev-ui run/server run/ui \
	demo quickstart pull-models demo-up demo-down demo-logs

all: build

build: build/server build/cli build/terminal
	@echo "All binaries built successfully in $(BIN_DIR)/"

build/server:
	@echo "Building server ($(SERVER_BINARY_NAME))..."
	@mkdir -p $(BIN_DIR)
	@go build -v -o $(BIN_DIR)/$(SERVER_BINARY_NAME) $(SERVER_CMD_PATH)

build/cli:
	@echo "Building CLI ($(CLI_BINARY_NAME))..."
	@mkdir -p $(BIN_DIR)
	@go build -v -o $(BIN_DIR)/$(CLI_BINARY_NAME) $(CLI_CMD_PATH)

build/terminal:
	@echo "Building terminal UI ($(TERMINAL_BINARY_NAME))..."
	@mkdir -p $(BIN_DIR)
	@go build -v -o $(BIN_DIR)/$(TERMINAL_BINARY_NAME) $(TERMINAL_CMD_PATH)

run: build/server
	@echo "Starting server ($(SERVER_BINARY_NAME))..."
	@$(BIN_DIR)/$(SERVER_BINARY_NAME)

# Convenience aliases
run/server: run

run/ui: dev-ui

run/cli:
	@echo "Starting CLI ($(CLI_BINARY_NAME))..."
	@go run $(CLI_CMD_PATH)

run/terminal:
	@echo "Starting terminal UI ($(TERMINAL_BINARY_NAME))..."
	@go run $(TERMINAL_CMD_PATH)
	
test:
	@echo "Running tests..."
	@go test -v ./...

lint:
	@echo "Linting Go code..."
	@if ! command -v $(GOLINT_CMD) &> /dev/null; then \
		echo "golangci-lint $(GOLINT_VERSION) not found or wrong version, installing to $(GOLINT_BIN_DIR)..."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(GOLINT_BIN_DIR) $(GOLINT_VERSION); \
	fi
	$(GOLINT_CMD) run ./...

# Clean up the built binary and tools
clean:
	@echo "Cleaning up binaries..."
	@rm -rf ./bin

# Clean up database, qdrant and local data
clean-data:
	@echo "Cleaning up data..."
	@powershell -ExecutionPolicy Bypass -File ./scripts/cleanup.ps1

# Web UI targets
ui-deps:
	@echo "Installing UI dependencies..."
	@cd ui && npm install

build-ui:
	@echo "Building web UI..."
	@cd ui && npm run build

dev-ui:
	@echo "Starting web UI development server..."
	@cd ui && npm run dev

# Full build including UI
build-all: build build-ui
	@echo "All binaries and UI built successfully"

# ── Demo & Quickstart ─────────────────────────────────────────────────────────

## CLI review — no server, no GitHub App needed. Just a GitHub PAT.
## Usage: make demo PR=https://github.com/owner/repo/pull/123
demo:
	@[ -f .env ] || cp .env.example .env
	@[ -n "$(PR)" ] || (echo "Usage: make demo PR=<pr-url>" && exit 1)
	@go run ./cmd/cli review $(PR)

## Full server quickstart — starts all services in Docker, opens web UI.
quickstart:
	@[ -f .env ] || cp .env.example .env
	@bash scripts/quickstart.sh

## Pull local Ollama models for demo (run on host Ollama, not Docker)
## Generator (kimi-k2.5) is a cloud model — no local download needed for it.
pull-models:
	ollama pull qwen3-embedding:0.6b
	ollama pull qwen2.5-coder:1.5b

## Start all demo services (after initial quickstart)
demo-up:
	docker compose -f docker-compose.demo.yml up -d

## Stop all demo services
demo-down:
	docker compose -f docker-compose.demo.yml down

## Stream server logs from the demo stack
demo-logs:
	docker compose -f docker-compose.demo.yml logs -f server
