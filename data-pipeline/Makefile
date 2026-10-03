# PlatePilot data pipeline — a standalone Go module and CLI.
#
#   cp .env.example .env   # or share the repository-root .env
#   make check-config      # print the resolved configuration
#   make migrate           # apply SQL migrations (needs PostgreSQL)
#
# Depends on the shared library through `replace ... => ../shared`, so this
# directory builds on its own with nothing but the Go toolchain.

SERVICE := data-pipeline
BIN_DIR := bin
VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

# Loaded for `run*` targets: the pipeline's own .env first, then the repository
# root's as a fallback, so the two services can share one set of secrets.
ENV_FILE := $(shell [ -f .env ] && echo .env || echo ../.env)

.PHONY: help build check-config migrate import-sample report test test-race vet cover lint tidy clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build the CLI binary into ./bin
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(SERVICE) .

check-config: ## Print the resolved configuration
	@set -a; . ./$(ENV_FILE); set +a; go run . check-config

migrate: ## Apply SQL migrations (needs POSTGRES_DSN)
	@set -a; . ./$(ENV_FILE); set +a; go run . migrate

report: ## Print the last pipeline reports
	@set -a; . ./$(ENV_FILE); set +a; go run . report --last=5

import-sample: ## Import a bounded sample, then rebuild stats and scores
	@set -a; . ./$(ENV_FILE); set +a; \
	go run . import --stage=meta   --limit=20000 && \
	go run . import --stage=review --limit=200000 && \
	go run . import --stage=stats && \
	go run . import --stage=score && \
	go run . report --last=5

test: ## Run the unit tests
	go test ./...

test-race: ## Run the unit tests with the race detector
	go test -race ./...

vet: ## Run go vet
	go vet ./...

cover: ## Run tests with coverage (writes coverage.out)
	go test -coverpkg=./... -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | tail -1

lint: ## Run golangci-lint (must be installed separately)
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run

tidy: ## Sync go.mod/go.sum with the code
	go mod tidy

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out
