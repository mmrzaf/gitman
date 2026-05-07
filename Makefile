.PHONY: build test clean install run-web run-worker run-admin fmt lint deps dev help

BINARY_NAME=gitman
BUILD_DIR=bin
GO=go

# Default target
.DEFAULT_GOAL := help

# Colors for output
GREEN := \033[0;32m
RED := \033[0;31m
NC := \033[0m # No Color

help: ## Show this help message
	@echo "$(GREEN)Available commands:$(NC)"
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2}'

build: ## Build the GitMan binary
	@echo "Building GitMan..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/gitman

build-all: ## Build for multiple platforms
	@echo "Building for multiple platforms..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/gitman
	GOOS=linux GOARCH=arm64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/gitman
	GOOS=darwin GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/gitman
	GOOS=darwin GOARCH=arm64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/gitman
	GOOS=windows GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-windows-amd64.exe ./cmd/gitman

test: ## Run tests
	$(GO) test -v -race -coverprofile=coverage.out ./...

test-coverage: ## Run tests with coverage report
	$(GO) test -v -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "$(GREEN)Coverage report generated: coverage.html$(NC)"

clean: ## Clean build artifacts
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html

install: build ## Install binary to /usr/local/bin
	@echo "Installing..."
	@cp $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/
	@echo "$(GREEN)Installed to /usr/local/bin/$(BINARY_NAME)$(NC)"

run-web: ## Run web server
	$(GO) run ./cmd/gitman web

run-worker: ## Run worker
	$(GO) run ./cmd/gitman worker

run-admin: ## Run admin commands
	$(GO) run ./cmd/gitman admin

run-serve: ## Run serve command
	$(GO) run ./cmd/gitman serve

dev: ## Run in development mode (web server)
	$(GO) run ./cmd/gitman web

fmt: ## Format code
	$(GO) fmt ./...
	@gofmt -w .

lint: ## Run linter
	golangci-lint run

deps: ## Download and tidy dependencies
	$(GO) mod download
	$(GO) mod tidy

mod-update: ## Update dependencies
	$(GO) get -u ./...
	$(GO) mod tidy

watch: ## Run with hot reload (requires air)
	@command -v air >/dev/null 2>&1 || { echo "$(RED)air is not installed. Run: go install github.com/cosmtrek/air@latest$(NC)"; exit 1; }
	air

# Development helper targets
web-dev: ## Run web server with debug logging
	$(GO) run ./cmd/gitman web --verbose

worker-dev: ## Run worker with debug logging
	$(GO) run ./cmd/gitman worker --verbose

# Quick test targets
test-short: ## Run short tests (no race detection)
	$(GO) test -short ./...

test-verbose: ## Run tests with verbose output
	$(GO) test -v ./...
