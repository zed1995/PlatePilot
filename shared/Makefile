# PlatePilot shared library
#
# A standalone Go module: everything here is imported by the services, and
# nothing here imports them. Build and test it on its own:
#
#   make test
#
# Services depend on it through a `replace` directive pointing at ../shared,
# so a local edit is picked up without publishing anything.

.PHONY: help test test-postgres test-race vet cover lint tidy clean

help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

test: ## Run the unit tests
	go test ./...

test-race: ## Run the unit tests with the race detector
	go test -race ./...

# The Postgres adapter contract suite needs live PostgreSQL (make -C ../ pg-up);
# without it the tests skip rather than fail, so `make test` stays runnable offline.
test-postgres: ## Run the Postgres adapter contract suite (needs `make -C .. pg-up`)
	go test ./store/postgres/... -count=1 -v

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

clean: ## Remove local artifacts
	rm -f coverage.out
