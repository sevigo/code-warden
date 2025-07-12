BINARY_NAME=code-warden
CMD_PATH=./cmd/server

GOLINT_BIN_DIR=$(CURDIR)/bin
GOLINT_CMD=$(GOLINT_BIN_DIR)/golangci-lint
GOLINT_VERSION=v2.1.6

.DEFAULT_GOAL := all
.PHONY: all build run clean test lint

all: build

build:
	@echo "Building $(BINARY_NAME)..."
	@go build -v -o $(BINARY_NAME) $(CMD_PATH)

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
