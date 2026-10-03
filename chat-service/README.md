# chat-service

The online service: HTTP + SSE chat API, two-stage retrieval, the agent runtime,
and the loopback-only admin API behind `/admin/v1/*`.

A standalone Go module. `shared/` is pulled in through `replace ... => ../shared`,
so this directory builds with nothing but the Go toolchain — but it does need a
reachable PostgreSQL (and, for the semantic channel, an embedding provider).

```bash
cp .env.example .env     # or share the repository-root .env
make run                 # HTTP on :8080
make run-admin           # same, with the admin API enabled
make build               # ./bin/chat-service
make test
make help                # every target
```

## Layout

| Path | Responsibility |
|---|---|
| `internal/app` | Wiring: config → stores → retrieval → HTTP server |
| `internal/httpapi` | Route handlers, SSE streaming, middleware |
| `internal/agent` | Plan/tool/answer graph, tool registry, model adapter, audit trail |
| `internal/retrieval` | Structured + keyword + vector channels and their fusion |
| `internal/admin` | Read-only admin service behind the admin routes |
| `internal/config` | This service's environment surface |

Everything shared with the pipeline — domain types, storage adapters, embedding
clients, config primitives — lives in `../shared`.

## Configuration

`make run` loads `./.env` if present, otherwise the repository-root `.env`. The
full annotated reference is the root `.env.example`; this file lists only what
this service reads.

Two switches matter when running without the full stack:

- `RETRIEVAL_ENABLE_VECTOR=false` — run search with no embedding provider; the
  trace records the channel as not run instead of pretending it contributed.
- `ADMIN_ENABLED=true` — expose `/admin/v1/*`, which is additionally restricted
  to loopback peers. Disabled by default, and the paths 404 when it is.

## Tests

`make test` runs offline: the write-side stores have an in-memory implementation
in `../shared/store/memory`. `make eval-retrieval` scores the retrieval fixtures
against a live corpus and needs `make -C .. pg-up`.
