BIN_DIR       := bin
CHAT_SERVICE  := chat-service
DATA_PIPELINE := data-pipeline
PKG           := ./...
VERSION       ?= dev
LDFLAGS       := -X main.version=$(VERSION)

.PHONY: help build build-chat build-pipeline run-chat run-pipeline \
        test test-race vet cover lint clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: build-chat build-pipeline ## Build both service binaries into bin/

build-chat: ## Build only the chat service
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(CHAT_SERVICE) ./$(CHAT_SERVICE)

build-pipeline: ## Build only the data pipeline
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(DATA_PIPELINE) ./$(DATA_PIPELINE)

run-chat: ## Run the chat service (HTTP, default :8080)
	go run ./$(CHAT_SERVICE)

run-pipeline: ## Run the data pipeline (prints the resolved config)
	go run ./$(DATA_PIPELINE) check-config

test: ## Run unit tests for both services
	go test $(PKG)

test-race: ## Run tests with the race detector
	go test -race $(PKG)

vet: ## Run go vet
	go vet $(PKG)

cover: ## Run tests with cross-package coverage (writes coverage.out)
	go test -coverpkg=$(PKG) -coverprofile=coverage.out $(PKG)
	@go tool cover -func=coverage.out | tail -1

lint: ## Run golangci-lint (must be installed separately)
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out
