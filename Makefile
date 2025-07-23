# Binary and Command Path Configuration
SERVER_BINARY_NAME=code-warden
SERVER_CMD_PATH=./cmd/server

CLI_BINARY_NAME=warden-cli
CLI_CMD_PATH=./cmd/cli

# Output directory for all binaries and tools
BIN_DIR=$(CURDIR)/bin

GOLINT_BIN_DIR=$(CURDIR)/bin
GOLINT_CMD=$(GOLINT_BIN_DIR)/golangci-lint
GOLINT_VERSION=v2.1.6

.DEFAULT_GOAL := all
.PHONY: all build run clean test lint

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

run:
	@echo "Starting $(BINARY_NAME)..."
	@go run $(CMD_PATH)

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
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@rm -rf ./bin
