# PlatePilot — a monorepo of independently buildable projects.
#
# There is no Go module (or npm package) at this level. Each directory below is
# a complete project with its own module and its own Makefile:
#
#   shared/         Go library: domain, config, storage adapters, embeddings
#   chat-service/   Go service: HTTP + SSE chat API and the admin API
#   data-pipeline/  Go CLI: ingestion, curation, embedding, scoring, migrations
#   web/            Node project: the admin console (Vite + React)
#
# The targets here are thin forwarders; run `make -C <project> help` for the
# project-local ones. Shared infrastructure that is not a service — the local
# PostgreSQL compose file, docs, and the sample data — stays at this level.

GO_PROJECTS := shared chat-service data-pipeline
WEB_DIR     := web

# Cross-module development convenience. Generated rather than hand-maintained:
# every project also builds standalone, so a missing go.work is never fatal.
GO_WORK := go.work

.PHONY: help build build-chat build-pipeline run-chat run-chat-admin run-pipeline \
        web-install web-dev web-build dev \
        migrate import-sample test test-race test-postgres test-offline eval-retrieval \
        vet cover lint tidy clean work pg-up pg-down pg-logs \
        ollama-up ollama-down ollama-logs ollama-pull

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
	@echo ""
	@echo "  Per-project targets: make -C {shared|chat-service|data-pipeline|web} help"

# --- Go workspace -----------------------------------------------------------

work: ## Regenerate go.work so ./... covers every Go module
	@go work init $(GO_PROJECTS) 2>/dev/null || go work use $(GO_PROJECTS)
	@echo "wrote $(GO_WORK): $(GO_PROJECTS)"

# --- Build and run ----------------------------------------------------------

build: build-chat build-pipeline ## Build both service binaries

build-chat: ## Build only the chat service (into chat-service/bin)
	$(MAKE) -C chat-service build

build-pipeline: ## Build only the data pipeline (into data-pipeline/bin)
	$(MAKE) -C data-pipeline build

run-chat: ## Run the chat service (HTTP, default :8080)
	$(MAKE) -C chat-service run

run-chat-admin: ## Run the chat service with the admin API enabled (loopback only)
	$(MAKE) -C chat-service run-admin

run-pipeline: ## Print the data pipeline's resolved config
	$(MAKE) -C data-pipeline check-config

# --- Admin console (web) ----------------------------------------------------

web-install: ## Install the frontend dependencies
	cd $(WEB_DIR) && npm install

web-dev: ## Run the admin console dev server (Vite, proxies /admin to :8080)
	cd $(WEB_DIR) && npm run dev

web-build: ## Build the admin console for production into web/dist
	cd $(WEB_DIR) && npm run build

# --- Data pipeline operations -----------------------------------------------

migrate: ## Apply SQL migrations (requires POSTGRES_DSN)
	$(MAKE) -C data-pipeline migrate

import-sample: ## Import a bounded sample, then rebuild stats and scores
	$(MAKE) -C data-pipeline import-sample

# --- Tests and checks -------------------------------------------------------

test: ## Run unit tests across every Go project
	@for p in $(GO_PROJECTS); do $(MAKE) -C $$p test || exit 1; done

test-race: ## Run tests with the race detector across every Go project
	@for p in $(GO_PROJECTS); do $(MAKE) -C $$p test-race || exit 1; done

test-postgres: ## Run the Postgres adapter contract suite (needs `make pg-up`)
	$(MAKE) -C shared test-postgres

# The Postgres contract suite needs a live database; without one its tests skip
# rather than fail, so `make test` stays runnable offline.
test-offline: ## Run tests excluding those that need PostgreSQL
	@for p in shared chat-service data-pipeline; do $(MAKE) -C $$p test || exit 1; done

eval-retrieval: ## Score the retrieval fixtures and print the gate metrics (needs `make pg-up`)
	$(MAKE) -C chat-service eval-retrieval

vet: ## Run go vet across every Go project
	@for p in $(GO_PROJECTS); do $(MAKE) -C $$p vet || exit 1; done

cover: ## Run tests with coverage across every Go project (writes <project>/coverage.out)
	@for p in $(GO_PROJECTS); do $(MAKE) -C $$p cover || exit 1; done

lint: ## Run golangci-lint in every Go project (must be installed separately)
	@command -v golangci-lint >/dev/null 2>&1 || { echo "golangci-lint not installed: https://golangci-lint.run/welcome/install/"; exit 1; }
	@for p in $(GO_PROJECTS); do $(MAKE) -C $$p lint || exit 1; done

tidy: ## Sync go.mod/go.sum with the code in every Go project
	@for p in $(GO_PROJECTS); do $(MAKE) -C $$p tidy || exit 1; done

clean: ## Remove build artifacts from every project
	@for p in $(GO_PROJECTS); do $(MAKE) -C $$p clean || exit 1; done
	rm -rf $(WEB_DIR)/dist

# --- Local PostgreSQL -------------------------------------------------------

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

# --- Local Ollama -----------------------------------------------------------

# Ollama is a host-native process, not a container, so there is no compose file
# to bring up the way pg-up does. It sits at this level for the same reason the
# PostgreSQL compose file does: both services fall back to a degraded retrieval
# path when the embedding provider is down, and a machine that rebooted turns
# that silent degradation into something that reads like a ranking regression.
#
# The model name is taken from EMBEDDING_MODEL in .env so the pull and the
# service configuration cannot drift apart; the fallback matches .env.example.

OLLAMA_HOST  ?= http://localhost:11434
OLLAMA_LOG   ?= /tmp/platepilot-ollama.log
OLLAMA_MODEL ?= $(shell grep -m1 '^EMBEDDING_MODEL=' .env 2>/dev/null | cut -d= -f2-)
OLLAMA_MODEL := $(if $(OLLAMA_MODEL),$(OLLAMA_MODEL),qwen3-embedding:0.6b)

ollama-up: ## Start the local Ollama server and pull the embedding model if it is missing
	@command -v ollama >/dev/null 2>&1 || { echo "ollama not installed: https://ollama.com/download"; exit 1; }
	@if curl -sf $(OLLAMA_HOST)/api/tags >/dev/null 2>&1; then \
		echo "ollama already serving at $(OLLAMA_HOST)"; \
	else \
		echo "starting ollama serve (log: $(OLLAMA_LOG))"; \
		nohup ollama serve >$(OLLAMA_LOG) 2>&1 & \
	fi
	@echo "waiting for ollama..."
	@n=0; until curl -sf $(OLLAMA_HOST)/api/tags >/dev/null 2>&1; do \
		n=$$((n+1)); \
		[ $$n -ge 30 ] && { echo "ollama did not become ready in 30s; see $(OLLAMA_LOG)"; exit 1; }; \
		sleep 1; \
	done
	@echo "pulling $(OLLAMA_MODEL) (a no-op when it is already present)"
	@ollama pull $(OLLAMA_MODEL)
	@echo "ready: $(OLLAMA_MODEL) at $(OLLAMA_HOST)"

ollama-down: ## Stop the local Ollama server (also stops the macOS app's server when that is the one running)
	@if pgrep -x ollama >/dev/null 2>&1; then pkill -x ollama && echo "stopped ollama"; else echo "ollama is not running"; fi

ollama-logs: ## Tail the log written by `make ollama-up`
	@test -f $(OLLAMA_LOG) || { echo "no log at $(OLLAMA_LOG); start the server with 'make ollama-up'"; exit 1; }
	@tail -f $(OLLAMA_LOG)

ollama-pull: ## Pull (or refresh) the embedding model named by EMBEDDING_MODEL
	@command -v ollama >/dev/null 2>&1 || { echo "ollama not installed: https://ollama.com/download"; exit 1; }
	@ollama pull $(OLLAMA_MODEL)

# --- Combined development loop ----------------------------------------------

# Start both processes in one shell; `kill 0` takes down the whole group
# (including the binary spawned by `go run`) on Ctrl-C.
dev: web-install ## Start the chat service (admin enabled) and the Vite dev server together
	@echo "starting chat-service (ADMIN_ENABLED=true) and $(WEB_DIR) dev server; Ctrl-C stops both"
	@trap 'kill 0' INT TERM EXIT; \
	$(MAKE) -C chat-service run-admin & \
	(cd $(WEB_DIR) && npm run dev) & \
	wait
