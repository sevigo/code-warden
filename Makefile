BINARY_NAME=code-warden
CMD_PATH=./cmd/server
GOLANGCI_LINT := ./bin/golangci-lint
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

lint: $(GOLANGCI_LINT)
	@echo "Running linter..."
	@$(GOLANGCI_LINT) run ./...

# Clean up the built binary and tools
clean:
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@rm -rf ./bin

# Installs golangci-lint binary into the local ./bin directory
$(GOLANGCI_LINT):
	@echo "Installing golangci-lint..."
	@curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(shell dirname $(GOLANGCI_LINT)) v1.59.1