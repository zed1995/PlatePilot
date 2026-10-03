# PlatePilot chat service — a standalone Go module and deployable unit.
#
#   cp .env.example .env   # or share the repository-root .env
#   make run               # HTTP API on :8080
#   make run-admin         # same, with the loopback-only /admin/v1 API
#
# Depends on the shared library through `replace ... => ../shared`, so this
# directory builds on its own with nothing but the Go toolchain.

SERVICE := chat-service
BIN_DIR := bin
VERSION ?= dev
LDFLAGS := -X main.version=$(VERSION)

# Loaded for `run*` only: the service's own .env first, then the repository
# root's as a fallback, so the two services can share one set of secrets.
ENV_FILE := $(shell [ -f .env ] && echo .env || echo ../.env)

.PHONY: help build run run-admin check-config test test-race vet cover lint tidy clean eval-retrieval

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build the service binary into ./bin
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(SERVICE) .

run: ## Run the service (HTTP, default :8080)
	@set -a; . ./$(ENV_FILE); set +a; go run .

run-admin: ## Run the service with the admin API enabled (loopback only)
	@set -a; . ./$(ENV_FILE); set +a; ADMIN_ENABLED=true go run .

check-config: ## Print the resolved configuration and exit
	@set -a; . ./$(ENV_FILE); set +a; go run . check-config

# The retrieval fixtures score against a live corpus, so they skip unless the
# database is actually there: a green suite that quietly asserted nothing is
# worse than a red one. REQUIRE_DB turns the skip into a failure instead of a
# pass, which is what makes this usable as a gate rather than as a smoke test.
eval-retrieval: ## Score the retrieval fixtures and print the gate metrics (needs `make -C .. pg-up`)
	PLATEPILOT_REQUIRE_DB=1 go test ./internal/retrieval/ -run TestRetrievalFixtures -count=1 -v

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
