-- OPT-04: keyword recall over a user's memories.
--
-- Injection used to be List-everything, truncated by confidence. That works
-- while a user has ten memories and stops working as soon as they have a
-- hundred: the ten highest-confidence rows are the ten the model sees, whatever
-- this turn is about. The read path now asks for the memories that match what
-- the user just said.
--
-- pg_trgm, not a vector index. user_memories.embedding exists and is never
-- written: nothing in the write path calls an embedding provider, so a vector
-- index would be an index over an empty column. Trigram matching needs no
-- provider and no new dependency (the extension is created in 0001), and it
-- accelerates ILIKE '%term%' — including for Chinese text, which has no word
-- boundaries to tokenise.
--
-- The predicate is partial on deleted_at IS NULL because every read filters
-- deleted rows: a memory the user removed should cost nothing to keep, and an
-- index that had to carry those rows would slow down the search that matters.
--
-- What it buys, measured rather than assumed. The search also filters user_id,
-- and 0006 already indexes (user_id, updated_at) over the same partial
-- predicate, so the planner narrows to one user's live rows and evaluates the
-- patterns as a filter. That plan survives far past this table's volumes —
-- with 50k live memories for one user it is still a sequential scan, because
-- PostgreSQL's LIKE selectivity estimate overprices the trigram bitmap. The
-- index is reachable (forcing the planner off sequential scans yields a
-- bitmap index scan on it), so it is insurance, not the hot path.
--
-- Volume note: memories are small per user (<1k), and scanning one user's live
-- rows is exactly the plan that should be used at that size. If a per-user
-- count ever grows past the point where scanning all of it per turn stops
-- being free, the thing to revisit is the injection policy (constraints
-- always, preferences by retrieval), not this index — the index only decides
-- who pays.

CREATE INDEX IF NOT EXISTS user_memories_content_trgm
    ON user_memories USING gin (content gin_trgm_ops)
    WHERE deleted_at IS NULL;
