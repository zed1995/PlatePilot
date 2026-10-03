# data-pipeline

The batch CLI: stream raw Google Local data into PostgreSQL, curate it, build
knowledge documents, embed them, score the demo set, and apply migrations.

A standalone Go module. `shared/` is pulled in through `replace ... => ../shared`,
so this directory builds with nothing but the Go toolchain.

```bash
cp .env.example .env      # or share the repository-root .env
make check-config         # print the resolved configuration
make migrate              # apply SQL migrations (needs PostgreSQL)
make import-sample        # bounded import + stats + scores + report
make build                # ./bin/data-pipeline
make test
make help                 # every target
```

## Stages

| Command | What it does |
|---|---|
| `go run . check-config` | Print the resolved configuration and exit |
| `go run . migrate` | Apply the SQL migrations in `../shared/store/postgres/migrations` |
| `go run . import --stage=meta` | Stream raw Meta JSONL into `restaurants` |
| `go run . import --stage=review` | Stream raw Review JSONL into `reviews` |
| `go run . import --stage=stats` | Materialise per-restaurant review statistics |
| `go run . import --stage=score` | Score and select the demo restaurant set |
| `go run . build-documents` | Build knowledge documents from curated rows |
| `go run . embed` | Embed the active documents through the configured provider |
| `go run . report --last=N` | Print the last N run reports |

## Layout

| Path | Responsibility |
|---|---|
| `main.go` | Command line surface |
| `internal/config` | This pipeline's environment surface |
| `internal/pipeline/raw` | Raw JSONL reading and streaming |
| `internal/pipeline/curate` | Curation, statistics, scoring |
| `internal/pipeline/knowledge` | Knowledge documents, facts, quality, hashing |
| `internal/pipeline/report` | Run reports |

Domain types, storage adapters, and the embedding clients live in `../shared`.

## Configuration

`make migrate` and the `import*` targets load `./.env` if present, otherwise the
repository-root `.env`. The full annotated reference is the root `.env.example`;
this file lists only what this pipeline reads.

Input data is shared at the repository root: `PIPELINE_DATA_DIR=data/raw/...` is
resolved relative to the repository root, so run the CLI from here or set the
path explicitly.
