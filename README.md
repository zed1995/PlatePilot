# PlatePilot

Evidence-grounded restaurant discovery agent: Go + Eino + OpenAI-compatible chat
provider + local Qwen embedding + MongoDB Atlas.

## Services

The repository is a single Go module hosting two independently runnable services,
with room for a frontend alongside them.

| Directory | Service | Responsibility |
|---|---|---|
| `data-pipeline/` | 数据生产 | Batch CLI: import raw Google Local data into Atlas, build knowledge documents, generate embeddings (M1–M2) |
| `chat-service/` | 聊天服务 | HTTP/SSE API: agent runtime, tools, two-stage retrieval, answers and citations (M3–M5) |
| `shared/` | 共享库 | Domain DTOs, provider/repository ports, adapters, logging, config primitives |
| `web/` | 前端 | Reserved for the future frontend project |

Both binaries are built from the same module and can be run and deployed
separately.

## Status

Milestone **M1 (Atlas 数据底座)** — complete on the write path. The data
pipeline now connects to Atlas, materialises the four content collections and
their indexes, streams the raw Google Local Meta/Review gzip JSONL into curated
documents, materialises review statistics, and selects the demo restaurant set.
`build-documents` and `embed` remain declared seams for M2.

Milestone **M0 (工程基础)** — skeleton, configuration, domain DTOs, provider and
repository ports, Hertz HTTP skeleton, and the test baseline.

The read path (`chat-service` search/retrieval) is M3.

## Requirements

- Go 1.26+
- `golangci-lint` (optional, for `make lint`)

## Quick start

```bash
cp .env.example .env            # optional; defaults work for local boot
make run-chat                   # chat service on :8080
curl -s localhost:8080/healthz
# {"status":"ok","version":"dev","request_id":"..."}

make run-pipeline               # data pipeline config check
make build                      # bin/chat-service and bin/data-pipeline
```

## Data pipeline (M1)

Atlas access is configured through `MONGO_URI` / `MONGO_DATABASE`. Without
`MONGO_URI` only `--dry-run` works, which is enough to exercise the readers and
curation rules against the real files.

```bash
# create collections + indexes (idempotent; add --indexes-only to just add indexes)
make migrate

# parse and count without touching Atlas
bin/data-pipeline import --stage=meta --limit=500 --dry-run

# import a bounded sample, then the rest
bin/data-pipeline import --stage=meta   --limit=20000
bin/data-pipeline import --stage=review --limit=200000

# rebuild derived state
bin/data-pipeline import --stage=stats --batch=500
bin/data-pipeline import --stage=score --demo-target=3000

# audit
bin/data-pipeline report --last=5
bin/data-pipeline report --batch-id=<id>
```

`--stage=all` runs meta → review → stats → score in one pass. Every stage writes
a queryable batch report; rejections store a line number and reason only, never
review text.

## Common commands

```bash
make test          # go test ./...
make test-race     # go test -race ./...
make vet           # go vet ./...
make build         # build both binaries
make migrate       # ensure Atlas collections and indexes (needs MONGO_URI)
make cover         # cross-package coverage
make lint          # golangci-lint (if installed)

# Atlas integration tests are env-gated and skip without a cluster:
MONGO_URI=... go test ./shared/adapter/repository/mongo/... -run Atlas -v
```

## Layout

```
chat-service/
  main.go                 # HTTP service entrypoint
  internal/app/           # dependency assembly
  internal/config/        # chat-service configuration
  internal/transport/     # Hertz routes, middleware, canonical errors
data-pipeline/
  main.go                 # batch CLI (check-config / migrate / import / report / ...)
  internal/config/        # pipeline configuration
  internal/pipeline/      # stage orchestration (import, migrate), report collector
  internal/pipeline/raw/  # gzip JSONL streaming reader + raw source schemas
  internal/pipeline/curate/ # pure normalisation, dedup, scoring, stats
shared/
  config/                 # env loading, validation errors, redaction
  domain/                 # pure domain DTOs (no framework or vendor dependencies)
  port/                   # read-side repository + write-side store interfaces
  adapter/
    repository/memory/    # in-memory stores (offline contract tests)
    repository/mongo/     # Atlas adapter: schema, indexes, stores, search index
    repository/contract/  # behaviour suite shared by memory and Mongo
  observability/logging/  # structured JSON logging
  testkit/                # mocks, fixtures, in-memory repositories
scripts/atlas/            # Atlas Search index definition + setup notes
web/                      # future frontend
```

## Documents

- `plans/platepilot-technical-prd.md`
- `plans/platepilot-implementation-plan.md`
- `plans/platepilot-m0-task-document.md`
- `plans/platepilot-m1-task-document.md`
