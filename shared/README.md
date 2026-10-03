# shared

The library both services are built on: domain types, configuration primitives,
ports and their adapters, embedding and rerank clients, logging, and test
fixtures.

A standalone Go module that imports neither service — the dependency only ever
points one way. `chat-service` and `data-pipeline` depend on it through
`replace github.com/zed1995/platepilot/shared => ../shared`, so a local edit is
picked up by both without publishing anything.

```bash
make test            # offline: the write-side stores have in-memory doubles
make test-postgres   # the Postgres adapter contract suite (needs `make -C .. pg-up`)
make vet
make help            # every target
```

## Layout

| Path | Responsibility |
|---|---|
| `domain/` | DTOs and invariants: restaurant, review, evidence, conversation, memory, tool, run |
| `config/` | Environment loading, validation, secret redaction |
| `store/` | Repository ports, the in-memory adapter, and the Postgres adapter + migrations |
| `embedding/`, `rerank/`, `chat/` | Provider ports and their clients |
| `idgen`, `requestctx`, `observability/` | Cross-cutting helpers |
| `testkit/` | Mocks and fixtures shared by both services' tests |

## Rules of thumb

- Anything a service needs to *run* separately belongs in that service, not here.
- Postgres-specific SQL lives in `store/postgres/migrations`; the migrations are
  applied by `data-pipeline migrate`, never by this library.

## Tests needing a database

The Postgres adapter suite is the only part that needs one. It **skips** when no
database is reachable, so `make test` stays runnable offline; `make test-postgres`
runs it verbosely, and `PLATEPILOT_TEST_POSTGRES_DSN` points it elsewhere.
