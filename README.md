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

Milestone **M0 (工程基础)** — skeleton, configuration, domain DTOs, provider and
repository ports, Hertz HTTP skeleton, and the test baseline. The pipeline stages
(`import`, `build-documents`, `embed`) are declared seams and fail with a message
naming their milestone until M1/M2 implement them.

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

## Common commands

```bash
make test          # go test ./...
make test-race     # go test -race ./...
make vet           # go vet ./...
make build         # build both binaries
make cover         # cross-package coverage
make lint          # golangci-lint (if installed)
```

## Layout

```
chat-service/
  main.go                 # HTTP service entrypoint
  internal/app/           # dependency assembly
  internal/config/        # chat-service configuration
  internal/transport/     # Hertz routes, middleware, canonical errors
data-pipeline/
  main.go                 # batch CLI entrypoint (import / build-documents / embed)
  internal/config/        # pipeline configuration
  internal/pipeline/      # data production stages (M1/M2)
shared/
  config/                 # env loading, validation errors, redaction
  domain/                 # pure domain DTOs (no framework or vendor dependencies)
  port/                   # provider and repository interfaces
  adapter/                # vendor implementations (Mongo, Ollama, OpenAI-compatible)
  observability/logging/  # structured JSON logging
  testkit/                # mocks, fixtures, in-memory repositories
web/                      # future frontend
```

## Documents

- `plans/platepilot-technical-prd.md`
- `plans/platepilot-implementation-plan.md`
- `plans/platepilot-m0-task-document.md`
