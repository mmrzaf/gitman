.PHONY: help build build-all test test-coverage verify clean install run-web run-worker fmt lint deps release-source

BINARY_NAME := gitman
BUILD_DIR := bin
GO := go
VERSION ?= dev
GOPROXY ?= https://proxy.golang.org,direct
LDFLAGS := -s -w -X main.version=$(VERSION)

.DEFAULT_GOAL := help

help: ## Show available commands
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-15s %s\n", $$1, $$2}'

build: ## Build the Gitman binary
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/gitman

build-all: ## Build supported Linux binaries
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/gitman
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64 ./cmd/gitman
	test -s $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64
	test -s $(BUILD_DIR)/$(BINARY_NAME)-linux-arm64
	test "$$($(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 version)" = "$(VERSION)"

test: ## Run the test suite with the race detector
	$(GO) test -race ./...

test-coverage: ## Run tests and write an HTML coverage report
	$(GO) test -race -coverprofile=coverage.out ./...
	$(GO) tool cover -html=coverage.out -o coverage.html

verify: ## Run the local release verification set
	$(GO) test -race ./...
	$(GO) vet ./...
	golangci-lint run
	@mkdir -p $(BUILD_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/gitman
	test "$$($(BUILD_DIR)/$(BINARY_NAME) version)" = "$(VERSION)"
	test "$$($(BUILD_DIR)/$(BINARY_NAME) --version)" = "$(VERSION)"

clean: ## Remove local build/test artifacts
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html

install: build ## Install the binary to /usr/local/bin
	install -m 0755 $(BUILD_DIR)/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)

run-web: ## Run the web server
	$(GO) run ./cmd/gitman web

run-worker: ## Run the CI worker
	$(GO) run ./cmd/gitman worker

fmt: ## Format tracked Go files
	gofmt -w $$(git ls-files '*.go')

lint: ## Run golangci-lint
	golangci-lint run

deps: ## Download and tidy Go modules
	GOPROXY=$(GOPROXY) $(GO) mod download
	GOPROXY=$(GOPROXY) $(GO) mod tidy

release-source: ## Create the tracked-files-only source archive
	scripts/release-source-archive.sh $${VERSION:?set VERSION}
