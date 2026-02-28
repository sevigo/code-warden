# Binary and Command Path Configuration
SERVER_BINARY_NAME=code-warden
SERVER_CMD_PATH=./cmd/server

CLI_BINARY_NAME=warden-cli
CLI_CMD_PATH=./cmd/cli

# Output directory for all binaries and tools
BIN_DIR=$(CURDIR)/bin

GOLINT_BIN_DIR=$(CURDIR)/bin
GOLINT_CMD=$(GOLINT_BIN_DIR)/golangci-lint
GOLINT_VERSION=v2.5.0

# OpenCode configuration
OPENCODE_PORT=4096
OPENCODE_MCP_URL=http://127.0.0.1:8081/sse

.DEFAULT_GOAL := all
.PHONY: all build run clean test lint opencode-start opencode-stop opencode-config dev

all: build

build: build-server build-cli
	@echo "All binaries built successfully in $(BIN_DIR)/"

build-server:
	@echo "Building server ($(SERVER_BINARY_NAME))..."
	@mkdir -p $(BIN_DIR)
	@go build -v -o $(BIN_DIR)/$(SERVER_BINARY_NAME) $(SERVER_CMD_PATH)

build-cli:
	@echo "Building CLI ($(CLI_BINARY_NAME))..."
	@mkdir -p $(BIN_DIR)
	@go build -v -o $(BIN_DIR)/$(CLI_BINARY_NAME) $(CLI_CMD_PATH)

run: build-server
	@echo "Starting server ($(SERVER_BINARY_NAME))..."
	@$(BIN_DIR)/$(SERVER_BINARY_NAME)

run-cli:
	@echo "Starting CLI ($(CLI_BINARY_NAME))..."
	@go run $(CLI_CMD_PATH)

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

# OpenCode server management
opencode-start:
	@echo "Starting OpenCode server on port $(OPENCODE_PORT)..."
	@OPENCODE_PORT=$(OPENCODE_PORT) opencode serve &>/tmp/opencode.log &
	@sleep 2
	@echo "OpenCode server started. Logs: /tmp/opencode.log"

opencode-stop:
	@echo "Stopping OpenCode server..."
	@pkill -f "opencode serve" || echo "OpenCode server not running"
	@echo "OpenCode server stopped"

opencode-config:
	@echo "Configuring OpenCode MCP server..."
	@mkdir -p ~/.config/opencode
	@echo '{\n\
  "$$schema": "https://opencode.ai/config.json",\n\
  "mcp": {\n\
    "code-warden": {\n\
      "type": "sse",\n\
      "url": "$(OPENCODE_MCP_URL)",\n\
      "enabled": true\n\
    }\n\
  }\n\
}' > ~/.config/opencode/opencode.json
	@echo "OpenCode MCP configured to connect to $(OPENCODE_MCP_URL)"

# Development mode: start both code-warden and OpenCode
dev: build-server opencode-config
	@echo "Starting development environment..."
	@$(MAKE) opencode-start
	@echo "Starting code-warden server..."
	@$(BIN_DIR)/$(SERVER_BINARY_NAME)

# Clean up the built binary and tools
clean:
	@echo "Cleaning up binaries..."
	@rm -rf ./bin

# Clean up database, qdrant and local data
clean-data:
	@echo "Cleaning up data..."
	@powershell -ExecutionPolicy Bypass -File ./scripts/cleanup.ps1
