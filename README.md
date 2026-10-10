# PlatePilot

Evidence-grounded restaurant discovery agent: Go + Eino + OpenAI-compatible chat
provider + local Qwen embedding + PostgreSQL (pgvector + PostGIS).

## Services

Each directory is a self-contained project with its own module and Makefile — a
multi-module repository, not one module with several `main` packages. There is no
`go.mod` at the repository root.

| Directory | Project | Kind | Responsibility |
|---|---|---|---|
| `data-pipeline/` | 数据生产 | Go module, CLI | Batch CLI: import raw Google Local data into PostgreSQL, build knowledge documents, generate embeddings (M1–M2) |
| `chat-service/` | 聊天服务 | Go module, service | HTTP/SSE API: conversational agent runtime (planning, tools, RAG answers, HITL confirmation, memory), two-stage retrieval, run trace, eval harnesses (M3–M6) |
| `shared/` | 共享库 | Go module, library | Domain DTOs, provider/repository ports, adapters, logging, config primitives |
| `web/` | 前端 | npm project | Admin console + agent verification console (Vite + React) |

The two services depend on `shared` through a `replace` directive pointing at
`../shared`, so each one builds, tests, and deploys on its own:

```bash
make -C chat-service  run     # or: cd chat-service  && go run .
make -C data-pipeline migrate # or: cd data-pipeline && go run . migrate
```

`make work` writes a `go.work` at the root for editors and for cross-module
commands; it is a local convenience (git-ignored), never a build requirement.

The root `Makefile` only forwards to the projects and owns what is not a
service: `deploy/` (local PostgreSQL), `docs/`, `data/`, and `.env.example`.

## Status

Milestone **M6（可观测、评测与验证台）** — complete. Every agent turn records
its graph-node spans (planning, tools, clarify, answer, finalize) with per-node
latency, queryable through `GET /v1/runs/:run_id/nodes` and
`GET /v1/traces/:trace_id`. The RAG fixture suite grew from 25 to 86 cases with
coverage ratchets per query family, and a fully offline agent fixture suite
scores tool selection, confirmation-gate safety, duplicate-booking safety and
end-to-end success (`make eval-agent`). Fixed requests replay identically and
two model configurations diff case by case. `make perf` measures live search
latency (p50 129 ms / p95 158 ms, of which the embedding round trip is ~53%)
and the agent runtime's own overhead (p50 0.34 ms per turn, offline, with
time-to-first-answer-text reported under both answer paths). The web
console ships the agent verification surface (`/agent`) with trace and
candidate panels, and the mock inventory is viewable and resettable
(`/inventory`, `GET/POST /admin/v1/restaurants/:id/inventory[/reset]`).

Milestone **M5（对话 Agent 与预约）** — complete. `POST /v1/conversations/:id/messages`
streams a full agent turn over SSE: slot interpretation, tool use over six
deterministic tools, evidence-grounded answers with citations, and — for the
reservation flow — a hard confirmation gate before any write, with
idempotency keys so a repeated confirmation is the same booking rather than a
second one. Long-term preferences live under `/v1/memories` with
view/update/delete. Every turn persists a run row with tool-call records.

Milestone **M4（模型接入）** — complete. An OpenAI-compatible chat client
satisfies all three model ports (planning, answering, interpretation); the
agent degrades to rules when the model is absent instead of failing the turn.

Milestone **M3（两级检索）** — complete. `chat-service` answers
`POST /v1/restaurants/search` with four fused channels: hard filters run in the
database, names and addresses match through the `pg_trgm` indexes, and one
shared query embedding drives two semantic channels — the restaurant's own
profile and an offline review digest built per restaurant from the review
corpus — fused as one family with shared normalization bounds, optionally
rescored with the exact cosine over the whole candidate pool
(`RETRIEVAL_ENABLE_POOL_RESCORE`, default on) and optionally ranked by weighted
reciprocal rank (`RETRIEVAL_FUSION_METHOD=rrf`). The digest is ranking fuel,
never a citation. It also answers `POST /v1/restaurants/{id}/evidence` and
`POST /v1/restaurants/evidence`, which recall and assemble citable evidence for a
named set of restaurants — the scope is a precondition, never a filter applied
after the fact.

Measured on the 3,000-restaurant corpus over the 86-case fixture suite: hard
filter accuracy 1.000, recall@5 0.843, citation precision 1.000, semantic
answer relevance 0.960, zero cross-restaurant leaks. `make eval-retrieval`
re-runs those numbers against a live database (and fails when the vector
channel silently degrades, via `PLATEPILOT_REQUIRE_VECTOR=1`).

Milestone **M2 (Embedding 与知识文档)** — complete. `build-documents` and `embed`
produce 11,775 active knowledge documents over 3,000 restaurants. The optional
`build-digests` stage adds one review digest per restaurant — off by default
because it calls a paid LLM API (`DIGEST_ENABLED=true`; `digest:rules:v1` is the
zero-model baseline); digests enter the same embed queue and are never citable.

Milestone **M1 (数据底座)** — complete on the write path. The data pipeline
connects to PostgreSQL, applies its SQL migrations, streams the raw Google Local
Meta/Review gzip JSONL into curated rows, materialises review statistics, and
selects the demo restaurant set.

Milestone **M0 (工程基础)** — skeleton, configuration, domain DTOs, provider and
repository ports, Hertz HTTP skeleton, and the test baseline.

Measured on the full corpus (272k meta rows, 4.24M reviews) against a local
container:

| Stage | Rows | Time |
|---|---|---|
| `import --stage=meta` | 272,189 read → 36,133 written | 8.6 s |
| `import --stage=review` | 4,243,445 written | 281 s |
| `import --stage=stats` | 35,979 restaurants rolled up | 32 s |
| `import --stage=score` | 36,133 scored, 3,000 active for demo | 5.3 s |

The resulting database is 2.9 GB, of which `reviews` is 2.7 GB. That is down
from 4.2 GB before the primary keys moved to `bigint` (see [Primary
keys](#primary-keys)).

## Requirements

- Go 1.26+
- Docker (for the local PostgreSQL)
- `golangci-lint` (optional, for `make lint`)

## Quick start

```bash
cp .env.example .env            # optional; defaults work for local boot
make pg-up                      # PostgreSQL with pgvector + PostGIS on :55432
make migrate                    # apply SQL migrations

make run-chat                   # chat service on :8080
curl -s localhost:8080/healthz
# {"status":"ok","version":"dev","request_id":"..."}

make run-pipeline               # data pipeline config check
make build                      # chat-service/bin/… and data-pipeline/bin/…
make -C <project> help          # the project-local targets
```

## Storage: PostgreSQL

The system of record is a self-hosted PostgreSQL with `pgvector`, `PostGIS`, and
`pg_trgm`.

The image is built rather than pulled because no published image carries both
pgvector and PostGIS for arm64 (see `deploy/postgres/Dockerfile`).

```bash
make pg-up        # start on :55432
make pg-logs      # tail logs
make pg-down      # stop and delete the volume
```

Connection is configured through `POSTGRES_DSN`. Without it only `--dry-run`
works, which is enough to exercise the readers and curation rules against the
real files.

### The service refuses a stale schema

`chat-service` compares the database against the migrations embedded in its own
binary at startup and exits when one has not been applied:

```
verify postgres schema: invalid_argument: postgres: database "platepilot" is behind
this binary: 2 migration(s) not applied (0011_memory_search.sql, 0012_tool_call_order.sql);
run `make migrate`
```

A startup failure rather than a warning, because a mismatched schema is quiet from
both sides: the agent's audit path swallows write errors by design — a lost tool-call
row does not fail a turn — and the read path reports the same mistake as a generic
upstream error. Before this check, a database two migrations behind answered
`GET /v1/runs/{id}` with `502 provider_unavailable`, which describes an outage for a
database that was reachable and answering every other query.

The check only reads. Applying migrations stays the pipeline's job, and no service
repairs the schema itself. It is also one-directional: it compares the migration
bookkeeping rather than the schema, so a database *ahead* of the binary (a rollback
without a schema rollback) passes and is reported at the first write instead.

### Primary keys

Every internal key is `bigint GENERATED BY DEFAULT AS IDENTITY` and is assigned
by the database, not the application. Three reasons, in order of weight:

- **Size.** A UUID string costs 36 bytes plus varlena overhead against 8. Across
  the 4.15M-row `reviews` table and its foreign key that is roughly 300 MB of the
  old schema's 4.2 GB.
- **Index locality.** Identity values ascend with insertion order, so the primary
  key btree is appended to instead of being randomly split. This matters most on
  `reviews`.
- **One source of truth.** The pipeline no longer mints ids, so a partial import
  that retries cannot leave a row with an id nothing else references.

`source_record_id` stays `text` on purpose: it holds Google's `gmap_id`, an
external identifier whose shape this system does not get to choose.

Two consequences are load-bearing rather than incidental:

- **A restaurant re-import keeps its id.** The upsert conflicts on
  `source_record_id` and the identity column is left out of the column list, so
  reviews keep pointing at a stable parent.
- **Review idempotency moved off the primary key.** It used to be
  `sha256(gmap_id + user_id + time + text_hash)` with `ON CONFLICT (id)`. With a
  database-assigned id that is no longer available, so idempotency is the unique
  index on `(restaurant_id, text_hash, rating, reviewed_at)`. Measured over the
  full corpus the two keys select exactly the same rows — `rating` and `user_id`
  are both determined by `text_hash`, so zero groups differ. A repeated import
  updates in place instead of writing a second copy.

### Why one database

Three of the product's needs are not interchangeable, and a document store makes
the combination awkward:

| Need | Implementation |
|---|---|
| Hard filters (cuisine, price, rating, borough) | indexed columns, `=` and `ANY` |
| Geography | `geography(Point,4326)`, GiST, `ST_DWithin` |
| Vector recall | `vector(1024)`, HNSW |
| Fuzzy text | `pg_trgm` |

Retrieval runs hard filters *before* vector recall. That ordering is not a
stylistic choice: pgvector's HNSW is an `ORDER BY` structure, so a `WHERE` clause
alone degrades it to a sequential scan. Measured on 50k rows:

```sql
ORDER BY embedding <=> q              -- Index Scan using embedding_hnsw
WHERE borough = 'manhattan' ORDER BY .. -- Seq Scan, 49759 rows filtered by hand
```

The schema answers this with one partial HNSW index per borough, so a filtered
query still resolves to an index scan:

```sql
WHERE is_active AND borough = 'manhattan' ORDER BY embedding <=> q
-- Index Scan using knowledge_documents_hnsw_manhattan
```

A query that stacks a borough filter, a radius, and a cuisine filter still
falls back to filter-then-sort. That is acceptable at this corpus size and is
recorded in the schema as the case to revisit if evidence grows by an order of
magnitude.

### Known limitations

These are accepted trade-offs, not open bugs. Each names what would trigger a
revisit.

- **`boundaries` is empty.** The table and its indexes exist, but nothing loads
  it yet. Borough labels are derived at import time by the in-process
  point-in-polygon resolver in `curate`, which is checksum-pinned and correct —
  but it cannot answer a query that starts from a place *name*. Loading the table
  is the remaining step for name-to-area search.
- **Stacked filters fall back to filter-then-sort.** borough + radius + cuisine
  together do not resolve to an index scan (see above).
- **Chinese text search is trigram-based.** `pg_trgm` handles Chinese *fuzzy*
  matching but is not a Chinese tokenizer. Adequate for name/address matching;
  revisit if the corpus grows Chinese-language descriptive content.
- **Memory search scans one user's live rows.** `user_memories_content_trgm`
  exists and is usable, but the search also filters `user_id`, which 0006 already
  indexes, so PostgreSQL narrows by user first and evaluates the patterns as a
  filter — still a sequential scan at 50k memories for a single user. Correct at
  the documented volume (<1k memories/user); the trigger to revisit is a
  per-user count where scanning all of it per turn stops being free, and the
  thing to change then is the injection policy, not the index.
- **Identity is a request header, not authentication.** `/v1` trusts whatever
  `X-User-ID` the caller sends, and `/admin/v1` is gated only by a loopback
  check — there are no credentials anywhere. This is deliberate for a local,
  single-user demo (the admin rationale is in `docs/platepilot-admin-prd.md`
  §5.3–5.4), and it is the first thing to replace before any multi-user or
  hosted deployment.
- **Deleting a conversation leaves its reservations behind.** `DELETE
  /v1/conversations/:id` removes the thread, its transcript, its checkpoint, its
  candidate snapshot and its run audit, but not the `reservations` rows it
  produced: those are holds and bookings against real seat counts, and removing
  them would leave `reservation_slots.booked` describing seats nobody holds.
  The residue is reachable from the inventory console, which is where a booking
  belongs. The trigger to revisit is a deployment where a conversation is the
  owner of a booking — that needs a cancel path, not a cascade.
- **One Postgres instance is a single point of failure.** Acceptable for a
  local-first MVP; it is the explicit trade for zero per-month cost.
- **The full corpus lives on local disk** (2.9 GB, of which `reviews` is 2.7 GB).

## Data pipeline (M1)

```bash
# create tables + indexes (idempotent; already-applied versions are skipped)
make migrate

# parse and count without touching the database
bin/data-pipeline import --stage=meta --limit=500 --dry-run

# import a bounded sample, then the rest
bin/data-pipeline import --stage=meta   --limit=20000
bin/data-pipeline import --stage=review --limit=200000

# optional but strongly recommended: prefilter the review corpus locally first
# (see below). Without this the review stage reads the 2.5 GB raw file and
# writes several times as many rows.
bin/data-pipeline prefilter
bin/data-pipeline import --stage=review \
  --data-dir=data/processed --review-file=review-filtered.json.gz

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

## Borough boundaries

`borough_guess` is how a place is attributed to one of the five boroughs, which
is what an area filter ("restaurants in Brooklyn") runs on. It is derived from
the NYC Department of City Planning borough boundaries, water areas included.

The geometry is git-ignored because it is external fact data, not code. Fetch it
once before the first meta import:

```bash
# see data/boundaries/README.md for the source URL, licence, and pinned checksum
curl -sS -L -o data/boundaries/nyc-borough-boundaries-water.geojson \
  "https://data.cityofnewyork.us/resource/wh2p-dxnf.json?\$limit=100"
```

The checksum is pinned in `curate.DefaultBoundarySHA256` and verified on load, so
a truncated or edited download fails loudly instead of quietly relabelling the
corpus. A missing file degrades to approximate bounding boxes with a warning;
pass `--require-boundaries` (or `PIPELINE_REQUIRE_BOUNDARIES=true`) in scheduled
runs that must never produce approximate labels.

This matters more than it looks. The two figures below differ only in their
denominator, and both are measured against the shipped corpus:

- over all 272,189 raw meta rows, the bounding boxes disagree with the real
  boundaries on **10.2%** of records;
- over the 36,133 restaurants that survive ingestion into the database, the
  disagreement is **24.1%** (8,694 rows). The surviving rows are concentrated in
  the contested parts of the city, so the error rate of what you actually query
  is more than twice the raw-corpus rate.

The stored labels match the real boundaries on **100%** of those 36,133 rows
(34,531 labelled, 1,602 outside every borough). Representative disagreements:

```
exact=""          box="queens"      550   outside all boroughs, inside the Queens box
exact="brooklyn"  box="manhattan"  3700
exact="queens"    box="manhattan"  1852
exact="manhattan" box="bronx"       644
```

An audit of which geometry produced a given corpus is in
`ingestion_batches.boundary_version`; it is empty only when a run deliberately
fell back to the boxes.

Rebuild the labels after changing the geometry:

```bash
bin/data-pipeline import --stage=meta --require-boundaries
```

## Prefiltering the review corpus (recommended before a full import)

The shipped review file is 2.5 GB / 33.5 M rows, of which only about 12.7% is
importable: ~74% of rows reference places outside the ingested service area and
can never join to a restaurant, and about half of the rest carry no text at all.
Pushing those rows through the database join and write path costs hours.

`prefilter` resolves the whole joinable `gmap_id` set in one projection-only
query, streams the source locally, and writes a much smaller corpus:

```bash
bin/data-pipeline prefilter                    # -> data/processed/review-filtered.json.gz
bin/data-pipeline prefilter --limit=200000     # sample run
bin/data-pipeline prefilter --keep-short-text  # also keep sub-threshold text
```

The output is a valid gzip JSONL file in the raw schema, so the importer reads it
unchanged — pass `--data-dir` and `--review-file` to select it. Measured on the
shipped data: 33.5 M rows in, 4.24 M kept (12.7%), 308 MB out, 3m15s, covering
35,979 of 36,133 restaurants. PII is scrubbed and the `user_id` / `name` / `pics`
identity fields are stripped before the corpus is written.

Prefilter reuses the importer's own `curate` rules, so a row that survives
filtering is exactly a row the importer would keep. Output is written to a
`.partial` file and renamed on success, so an interrupted run never leaves a
half-written corpus behind. `data/processed/` is git-ignored.

`data/processed/` is a local artifact: re-run `prefilter` after re-importing
restaurants, since the joinable set is read from the current `restaurants`
table.

## Observing a long import

A full-corpus `review` run reads a 2.5 GB gzipped file line by line, so the
streaming stages (`meta`, `review`) print a live status line to **stderr**:

```bash
bin/data-pipeline import --stage=all
```

```
[review]  47.31%  1.1GiB/2.5GiB  @12.8MiB/s  eta 1m 7s  rows=15,842,301  accepted=412,884 written=412,884 dedup=8,201 filtered=0 rejected=2,214 unmatched=1,430,015
```

The line is rewritten in place and carries the counters that explain where rows
are going: percent complete, throughput, ETA, and the accept / filter / reject /
unmatched breakdown.

Progress is measured in **compressed bytes read**, not rows. The review file's
total line count is not known in advance, but its size on disk is, so
`bytes-read / file-size` is a true completion ratio. Updates are throttled to
one line per 32 MiB (and never faster than every 2 s), so a fast stage cannot
flood the terminal.

```bash
bin/data-pipeline import --stage=review --quiet   # suppress the live line
bin/data-pipeline import --stage=review 2>/dev/null  # stdout-only reports
```

`--skip-file-hash` skips the SHA-256 of the source file, which otherwise reads
the whole 2.5 GB once before ingestion starts.

> **Note on `--limit`**: it bounds **rows read from the source file**, not
> documents written. The source is US-wide while the pipeline keeps only the
> five boroughs, so a `--limit=20000` meta run writes far fewer than 20,000
> restaurants (about 6%). Reviews are additionally joined to `restaurants` by
> `gmap_id` and dropped when unmatched, so review acceptance is lower still.

## Common commands

```bash
make test          # go test ./... in every Go project
make test-race     # go test -race ./... in every Go project
make vet           # go vet ./... in every Go project
make build         # build both binaries into their own ./bin
make migrate       # apply SQL migrations (needs POSTGRES_DSN)
make test-postgres # Postgres adapter contract suite (needs `make pg-up`)
make cover         # coverage per project (<project>/coverage.out)
make lint          # golangci-lint (if installed)
```

### Testing without a database

The write-side stores have an in-memory implementation
(`shared/store/memory`) that both services and the tests use, so
`make test` is fast and runs offline.

The Postgres adapter is covered separately because the thing that makes it
valuable — pgvector, PostGIS, and `pg_trgm` doing the work — cannot be faked by
a double. Its tests run against a real database and **skip** when none is
reachable, so the default suite never fails for want of one:

```bash
make pg-up           # start PostgreSQL with pgvector + PostGIS
make test-postgres   # contract suite + extension and index assertions
make test-offline    # everything except the Postgres adapter
```

What the Postgres suite asserts beyond the shared behaviour contract:

- `vector`, `postgis`, and `pg_trgm` are installed.
- Every declared index exists, including the six HNSW indexes (one unfiltered
  plus one per borough) whose whole purpose is to survive a `WHERE` clause.
- `Migrate` is idempotent: a second run applies nothing and reports an
  `applied_at` for every version.

Point it at another instance with `PLATEPILOT_TEST_POSTGRES_DSN`.

## Restaurant search and evidence

`chat-service` serves three read-only endpoints. Search needs PostgreSQL, and
additionally an embedding provider for the semantic channel; evidence needs both.
Set `RETRIEVAL_ENABLE_VECTOR=false` to run the search path without a provider at
all — the trace then records the channel as not run rather than pretending it
contributed.

```bash
make pg-up && make migrate
make -C chat-service run        # or: cd chat-service && go run .
```

```bash
# Hard conditions plus free text. Conditions run in the database; the text is
# matched against name and address through the pg_trgm indexes.
curl -s localhost:8080/v1/restaurants/search \
  -H 'content-type: application/json' \
  -d '{"filter":{"cuisines":["italian"],"borough":"manhattan"},"text":"pizza"}' | jq
```

Every candidate explains itself. `reasons` names the channel that surfaced it and
how much that channel contributed, and `trace.channels` records every channel
that ran:

```json
{
  "candidates": [{
    "restaurant_id": 7715,
    "name": "Vezzo",
    "score": 0.682,
    "rating": 4.53,
    "rating_count": 1161,
    "reasons": [
      "满足全部硬条件（菜系=italian、行政区=manhattan）（权重 1.00，贡献 0.500）",
      "评分先验（按评论样本量收缩）（权重 0.20，贡献 0.172）"
    ]
  }],
  "trace": {
    "candidate_pool": 48,
    "returned": 12,
    "channels": [
      {"channel": "structured", "ran": true, "results": 240, "weight": 1},
      {"channel": "keyword",    "ran": true, "results": 24,  "weight": 0.5},
      {"channel": "vector",     "ran": true, "results": 30,  "weight": 0.4}
    ]
  }
}
```

Behaviour worth knowing:

- A request with neither text nor filters is refused. A filterless search would
  return the corpus in score order, which looks like an answer.
- An unknown borough is a `400`, not an empty list. "No restaurants there" and
  "that is not a borough" are different messages and must not look the same.
- Casing is accepted (`Manhattan` works); a misspelling is not.
- No matches is `200` with `"candidates": []`. It is a search that found
  nothing, not a failure.
- Text shorter than three characters is refused: a trigram index cannot match
  it, so answering would mean scanning the corpus.
- Wildcards in the text are escaped, so `50%` searches for that literal string.
- Ranking is reproducible — the same query returns the same list, bit for bit.

### Evidence

Evidence is what a citation points at. Both endpoints refuse a request that
names no restaurant: a citation is a claim about a specific restaurant, so a
request without one has no correct answer to give.

```bash
# One restaurant, straight from the path.
curl -s localhost:8080/v1/restaurants/7715/evidence \
  -H 'content-type: application/json' \
  -d '{"query":"等位久吗","top_k":5}' | jq

# Several at once. restaurant_ids is required here — the path carries no scope.
curl -s localhost:8080/v1/restaurants/evidence \
  -H 'content-type: application/json' \
  -d '{"restaurant_ids":[7715,640],"topic":"wait"}' | jq
```

```json
{
  "evidence": [{
    "document_id": "a1b2…",
    "restaurant_id": 7715,
    "doc_type": "restaurant_review_summary",
    "content": "周末晚市排队约 40 分钟…",
    "source": "google_reviews",
    "score": 0.71
  }],
  "trace": {"scope_size": 1, "recalled": 12, "kept": 5, "dropped": 7,
            "tokens": 612, "token_budget": 2000}
}
```

Behaviour worth knowing:

- Assembly never truncates. A chunk that would exceed the token budget is dropped
  whole, because half a quote is not a quote — a truncated citation renders as
  text that reads correctly and cites something the source never said.
- At most three chunks per restaurant per document type, applied *before* the
  budget. Letting the budget decide diversity would mean a restaurant with long
  chunks silently monopolises the answer.
- `dropped_by_reason` separates "the budget was full" from "a duplicate" from
  "too many of this kind", so a short answer reads as a decision rather than as a
  thin corpus.
- A chunk with no recorded source is returned as `"unknown"` and flagged in
  `warnings`. An unsourced citation is worse than an absent one.
- If every candidate was unusable the request fails rather than returning an
  empty bundle, because an empty bundle reads as "this restaurant has no
  evidence" rather than as a data problem.

### Running the retrieval evaluation

```bash
make pg-up && make migrate
make eval-retrieval      # in chat-service; needs `ollama serve` for the vector channel
```

The fixture suite scores recall@5, citation precision, hard-filter accuracy,
semantic answer relevance and cross-restaurant leaks over 86 cases covering
every query family (hard filters, soft conditions, evidence topics, prompt
injection, scope discipline), and prints the metric table with its gates.
Coverage ratchets fail the run if a family loses cases. Without
`PLATEPILOT_REQUIRE_DB=1` it skips loudly rather than passing quietly — a green
run that asserted nothing is worse than a red one, and
`PLATEPILOT_REQUIRE_VECTOR=1` turns a silent embedding-provider outage into a
failure instead of a keyword-only pass.

Ranking knobs live in the environment so they can be tuned against an evaluation
set without a recompile:

```bash
RETRIEVAL_TOP_K=5                  # page size
RETRIEVAL_OVERSAMPLE=2             # how much deeper each channel reads
RETRIEVAL_WEIGHT_STRUCTURED=1.0    # satisfied hard conditions
RETRIEVAL_WEIGHT_KEYWORD=0.5       # name / address match
RETRIEVAL_WEIGHT_VECTOR=1.0        # semantic channel
RETRIEVAL_WEIGHT_QUALITY=0.2       # rating prior, shrunk by sample size
```

### Running the agent evaluation and performance harness

```bash
make -C chat-service eval-agent   # offline: no database, no providers
make -C chat-service perf         # live: needs pg-up + ollama serve
```

`eval-agent` scores the agent fixture suite over four gates — tool-selection
accuracy, confirmation-gate safety, duplicate-booking safety, end-to-end
success — all required at 1.0. Every case is graded **twice**, once per answer
path, because a streamed turn publishes text before the citation check has run
and the gates have to hold in the configuration where a violation could reach
the user. The suite then replays every fixed request twice to assert
bit-identical behaviour, and diffs two model configurations case by case. The
fixtures are plain YAML (scripted provider calls, runtime-property assertions),
so adding a case is a data edit.

`perf` prints search p50/p95 with the embedding share called out, plus the agent
runtime's own per-turn overhead split into **time to first answer text** and
**whole-turn latency**, under both answer paths:

| Grounded turn (offline, `PLATEPILOT_PERF=1`) | first text | whole turn |
|---|---|---|
| one-shot answer | p50 122.4 ms | p50 123.0 ms |
| streamed answer | p50 65.2 ms | p50 130.9 ms |

Those numbers are the runtime's own, measured against a scripted provider paced
at a fixed 20 ms per chunk: they say what the agent spends, not what a model
would take. The comparison is the point — streaming does the same work and
publishes it about one line earlier, and the whole-turn cost rises slightly
because the answer is validated against a stream rather than in one piece.

## Conversational agent (M4–M5)

The search and evidence endpoints are raw building blocks. The conversational
surface composes them into an agent: one endpoint drives a whole turn.

```bash
POST /v1/conversations                     # open a thread
POST /v1/conversations/:id/messages        # one agent turn, streamed over SSE
POST /v1/conversations/:id/confirm         # answer a pending confirmation
GET  /v1/conversations/:id/candidates      # the turn's search candidates (position, score, reasons, snapshot_at)
GET  /v1/conversations/:id/runs            # per-turn run records
GET  /v1/runs/:run_id/nodes                # graph-node spans with latency
GET  /v1/traces/:trace_id                  # trace-id lookup for support
GET  /v1/memories                          # long-term preferences
PATCH/DELETE /v1/memories/:memory_id
```

The turn is a compiled graph (Eino): ingress → interpretation → planning →
tool round → answer → finalize, each node emitting a trace span. Tools are
six deterministic store-backed operations (search, resolve, evidence,
availability, reserve, memory) — the model plans and phrases; the database
computes.

Behaviour worth knowing:

- **The confirmation gate is not a prompt.** A reservation write happens only
  through `POST /v1/conversations/:id/confirm` after the assistant has shown a
  final summary; a model that talks its way past the gate still produces no
  write, because the write path is not reachable from the tool round.
- **Idempotency.** The idempotency key is derived from the thread, the action
  and the request id the user approved — a retried confirmation yields the same
  booking, not a second one.
- **Soft degradation.** A missing chat provider fails startup (the model is
  primary from M4); a missing embedding provider degrades retrieval to the
  structured and keyword channels and says so in the trace.
- **Long-term memories are retrieved, not replayed.** Constraints go in whole —
  they are hard requirements, and a search that happened not to surface one
  would otherwise silently drop it. Preferences and facts are chosen by matching
  the user's current message against their content, so the injection window is
  spent on the memories this turn is about. A search that fails or matches
  nothing falls back to most-confident-first, which means retrieval can only
  narrow the window, never empty it.
- **One round's read-only tool calls run concurrently; a write stops the round.**
  A model that asks for several independent lookups in one message is saying it
  does not need them in order, so up to three of them run at once. Nothing above
  the tool loop can tell: the transcript, the turn state and the next round's
  context are exactly what sequential execution would have produced. A call that
  changes something outside the conversation never runs alongside anything and
  ends the round — it is parked on the thread for approval, and the calls after
  it are dropped rather than run against a state the write has not established.
  On the audit trail this is visible as: `tool.finish` frames arriving out of
  call order, rows written as each call returns, and `seq` on each row — the
  request order a reader gets, since neither timestamp can carry it once the
  calls start together.
- **Mock inventory.** Reservation slots are demo state. The console can view
  and reset it (`GET/POST /admin/v1/restaurants/:id/inventory[/reset]?date=`)
  so a demo can be replayed from the top; the reset is the one write behind
  the admin loopback guard.
- **Review digests with provenance.** Each restaurant's
  `restaurant_review_digest` (one per restaurant, scope `restaurant`) records
  the reviews the model actually grounded its conclusions in, not merely the
  bundle it was offered. The ops console lists digests under
  `/digests`, and a digest's detail page links every recorded source review
  via `GET /admin/v1/documents/:id/source-reviews`; the rules baseline names
  the representative input set. Digest content is English retrieval fuel,
  while the advisor answers in the language of the user's question.

The web console (`web/`, `npm run dev`) exposes this as two surfaces: the
ops pages read the database through `/admin/v1`, and the verification pages —
`/agent` for scripted conversation runs with trace and candidate panels,
`/inventory` for the mock inventory — are the ones that act.

### The turn's event stream

`POST /v1/conversations/:id/messages` answers with Server-Sent Events. The
event set is closed: a client that meets an unknown name may ignore it, but the
server never emits one the contract does not define.

| Event | Payload | Meaning |
|---|---|---|
| `message.start` | `run_id`, `thread_id` | A turn opened. Everything before it belongs to the previous one. |
| `message.delta` | `delta` | Answer text, **provisional for the whole run**. |
| `message.replace` | `text` | Discard this run's text and keep this body instead. |
| `tool.start` | `call_id`, `tool` | A tool invocation began. |
| `tool.finish` | `call_id`, `status`, `latency_ms` | The invocation with that `call_id` ended. |
| `citation` | `evidence_ids` | The validated evidence IDs of the answer. |
| `state.awaiting_input` | `state`, `pending_action`, `missing_slots` | The thread parked a question; the next request is the user's. |
| `confirmation.required` | `state`, `pending_action`, `summary` | The thread parked a write; answer it on the confirm route. |
| `memory.saved` | `memory_id`, `memory_type`, `content`, `refreshed` | The turn wrote one long-term memory. |
| `message.end` | `finish_reason`, `usage`, `warnings` | The turn finished. Only now is the text final. |
| `error` | `code`, `message` | The turn failed; `code` is an `errs.Code`. |

**Text is provisional until `message.end`.** The answer is composed with
citation closure enforced in code: a first generation that cites an evidence ID
this turn does not have is regenerated, but under streaming it has already
reached the client by then. So `message.delta` frames accumulate into a
*rendering*, `message.replace` supersedes everything accumulated in the run, and
nothing should be written to a local store before `message.end`. A client that
renders only the last complete text it holds is always correct.

**Two credential kinds, one citation closure.** A turn distinguishes
objective merchant-record facts from review opinions. Rating (with its review
sample size), price level, cuisines, borough and address come from the search
candidates — system-of-record facts from the Google Local 2021 snapshot — and
may be stated directly without a `[^id]` marker; the first such statement
attributes the snapshot, and a rating always travels with its sample count.
Fields the candidate does not carry (opening hours, amenities) are answered as
"not in the record", never guessed. Opinions (taste, quietness, atmosphere,
service) remain under the full citation closure: they are answerable only from
recalled review evidence, every claim keeps its `[^id]`, and an opinion
question with no reviews is flagged in the answer as unconfirmed rather than
silently replaced by record facts. A turn that gathered neither candidates nor
evidence is the fixed refusal; a turn with candidates but no evidence still
runs the grounded composer.

Streaming is on by default and can be switched off with
`PLATEPILOT_ANSWER_STREAMING=false`: the answer then arrives as a single
`message.delta` and `message.replace` is never used. That is the fallback for a
client that does not understand the replacement event, not a different answer —
the same text is validated either way. A provider that cannot open a stream at
all degrades to a one-shot completion automatically and records a warning on
`message.end`.

Frames are forwarded **as the graph produces them**, not collected and replayed
at the end. That is a property of the two runner entry points rather than of the
handler: `Runner.Run` executes a turn to completion and hands back the whole
transcript, which is what the offline harnesses want, while `Runner.RunLive`
drives the graph on its own goroutine and calls back per frame, which is what
the SSE handler uses. Only the second can deliver a first token, and it is also
what keeps the emitter's 128-frame buffer from becoming a ceiling on how long an
answer may be.


## Layout

```
chat-service/
  main.go                 # HTTP service entrypoint
  internal/app/           # dependency assembly (the only place wiring concrete implementations)
  internal/config/        # chat-service configuration
  internal/admin/         # admin console application layer (read-only + mock inventory reset)
  internal/retrieval/     # read path: channel orchestration, fusion, rerank, eval + perf harnesses
  internal/agent/         # conversational agent runtime: compiled graph, tools, eval + replay harnesses
  internal/agent/tools/   # the six deterministic store-backed tools
  internal/agent/audit/   # run / tool-call / node trace hooks
  internal/agent/einomodel/ # Eino graph model bindings
  internal/hitl/          # human-in-the-loop confirmation gate
  internal/memorywrite/   # long-term preference extraction and storage
  internal/reservation/   # reservation application service (holds, idempotency)
  internal/httpapi/       # Hertz routes, handlers, middleware
  internal/httperr/       # canonical HTTP error envelope
data-pipeline/
  main.go                 # batch CLI (check-config / migrate / import / report / ...)
  internal/config/        # pipeline configuration
  internal/pipeline/      # stage orchestration (import, migrate), report collector
  internal/pipeline/raw/  # gzip JSONL streaming reader + raw source schemas
  internal/pipeline/curate/ # pure normalisation, dedup, scoring, stats
shared/
  config/                 # env loading, validation errors, redaction
  domain/                 # pure domain DTOs (no framework or vendor dependencies)
  store/                  # repository + store interfaces (read-side and write-side)
    postgres/             # Postgres: client, stores, migrations, geo
    memory/               # in-memory stores (offline contract tests)
    contract/             # behaviour suite shared by memory and Postgres
  chat/                   # ChatProvider interfaces (+ openai adapter, M4)
  embedding/              # EmbeddingProvider interface + ollama / fake clients
  rerank/                 # RerankProvider interface
  requestctx/             # per-request context values (request id, log fields)
  observability/logging/  # structured JSON logging
  testkit/                # mocks, fixtures, in-memory repositories
deploy/                   # docker-compose + PostgreSQL image (pgvector + PostGIS)
web/                      # admin console frontend
```

## Documents

- `docs/platepilot-technical-prd.md`
- `docs/platepilot-implementation-plan.md`
- `docs/platepilot-m0-task-document.md`
- `docs/platepilot-m1-task-document.md`
- `docs/platepilot-m2-task-document.md`
- `docs/platepilot-m3-task-document.md`
- `docs/platepilot-m4-task-document.md`
- `docs/platepilot-m5-task-document.md`
- `docs/platepilot-admin-prd.md`
- `docs/platepilot-admin_plan.md`
- `docs/platepilot-web-agent-console-task-document.md`
