BIN_DIR       := bin
CHAT_SERVICE  := chat-service
DATA_PIPELINE := data-pipeline
WEB_DIR       := web
PKG           := ./...
VERSION       ?= dev
LDFLAGS       := -X main.version=$(VERSION)

.PHONY: help build build-chat build-pipeline run-chat run-chat-admin run-pipeline \
        web-install web-dev web-build dev \
        migrate import-sample test test-offline test-race test-postgres \
        eval-retrieval vet cover lint clean pg-up pg-down pg-logs

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: build-chat build-pipeline ## Build both service binaries into bin/

build-chat: ## Build only the chat service
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(CHAT_SERVICE) ./$(CHAT_SERVICE)

build-pipeline: ## Build only the data pipeline
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(DATA_PIPELINE) ./$(DATA_PIPELINE)

run-chat: ## Run the chat service (HTTP, default :8080)
	go run ./$(CHAT_SERVICE)

run-chat-admin: ## Run the chat service with the admin API enabled (loopback only)
	ADMIN_ENABLED=true go run ./$(CHAT_SERVICE)

run-pipeline: ## Run the data pipeline (prints the resolved config)
	go run ./$(DATA_PIPELINE) check-config

# --- Admin console (web) ----------------------------------------------------

web-install: ## Install the frontend dependencies
	cd $(WEB_DIR) && npm install

web-dev: ## Run the admin console dev server (Vite, proxies /admin to :8080)
	cd $(WEB_DIR) && npm run dev

web-build: ## Build the admin console for production into web/dist
	cd $(WEB_DIR) && npm run build

# Start both processes in one shell; `kill 0` takes down the whole group
# (including the binary spawned by `go run`) on Ctrl-C.
dev: web-install ## Start the chat service (admin enabled) and the Vite dev server together
	@echo "starting chat-service (ADMIN_ENABLED=true) and $(WEB_DIR) dev server; Ctrl-C stops both"
	@trap 'kill 0' INT TERM EXIT; \
	ADMIN_ENABLED=true go run ./$(CHAT_SERVICE) & \
	(cd $(WEB_DIR) && npm run dev) & \
	wait

migrate: ## Apply SQL migrations (requires POSTGRES_DSN)
	go run ./$(DATA_PIPELINE) migrate

pg-up: ## Start the local PostgreSQL (pgvector + PostGIS) on :55432
	docker compose -f deploy/docker-compose.yml up -d --build
	@echo "waiting for postgres..."
	@until docker exec platepilot-postgres pg_isready -U platepilot -d platepilot >/dev/null 2>&1; \
		do sleep 1; done
	@echo "ready: $$(grep -m1 POSTGRES_DSN .env 2>/dev/null || echo 'set POSTGRES_DSN in .env')"

pg-down: ## Stop the local PostgreSQL and delete its volume
	docker compose -f deploy/docker-compose.yml down -v

pg-logs: ## Tail the local PostgreSQL logs
	docker compose -f deploy/docker-compose.yml logs -f postgres

import-sample: ## Import a bounded sample, then rebuild stats and scores
	go run ./$(DATA_PIPELINE) import --stage=meta   --limit=20000
	go run ./$(DATA_PIPELINE) import --stage=review --limit=200000
	go run ./$(DATA_PIPELINE) import --stage=stats
	go run ./$(DATA_PIPELINE) import --stage=score
	go run ./$(DATA_PIPELINE) report --last=5

test: ## Run unit tests for both services
	go test $(PKG)

test-race: ## Run tests with the race detector
	go test -race $(PKG)

test-postgres: ## Run the Postgres adapter contract suite (needs `make pg-up`)
	go test ./shared/adapter/repository/postgres/... -count=1 -v

# The retrieval fixtures assert against a live corpus, so they skip unless the
# database is actually there: a green suite that quietly asserted nothing is
# worse than a red one. REQUIRE_DB turns the skip into a failure instead of a
# pass, which is what makes this usable as a gate rather than as a smoke test.
eval-retrieval: ## Score the retrieval fixtures and print the gate metrics (needs `make pg-up`)
	PLATEPILOT_REQUIRE_DB=1 go test ./chat-service/internal/retrieval/ -run TestRetrievalFixtures -count=1 -v

vet: ## Run go vet
	go vet $(PKG)

cover: ## Run tests with cross-package coverage (writes coverage.out)
	go test -coverpkg=$(PKG) -coverprofile=coverage.out $(PKG)
	@go tool cover -func=coverage.out | tail -1

# The Postgres contract suite needs a live database; without one its tests skip
# rather than fail, so `make test` stays runnable offline.
test-offline: ## Run tests excluding those that need PostgreSQL
	go test $(filter-out ./shared/adapter/repository/postgres/...,$(PKG))

lint: ## Run golangci-lint (must be installed separately)
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/welcome/install/"; exit 1; }
	golangci-lint run

clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out
