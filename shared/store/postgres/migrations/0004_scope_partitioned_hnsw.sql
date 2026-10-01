-- ---------------------------------------------------------------------------
-- M3-03: scope-partitioned HNSW indexes
-- ---------------------------------------------------------------------------
-- The borough-partitioned indexes from 0001 are wrong for a scoped recall, and
-- the failure they produce is silent.
--
-- Measured on this corpus at 30,198 active documents, manhattan partition:
--
--   retrieval_scope | rows
--   ---------------+------
--   restaurant     |  2,010
--   evidence       | 18,287
--
-- 90% of the manhattan partition is evidence. An HNSW scan is a bounded beam:
-- it visits roughly hnsw.ef_search candidates (40 by default), ranks those, and
-- stops. It does not skip rows for a filter -- the filter is applied to whatever
-- the beam surfaced. So a recall scoped to `restaurant` descends into a
-- partition that is 90% evidence, gets 40 neighbours that are nearly all
-- evidence, the scope filter rejects every one of them, and the query returns
-- zero rows.
--
-- Reproduced before this migration, on a live query vector with 8 documents
-- known to be within distance 0.5:
--
--   Index Scan using knowledge_documents_hnsw_manhattan
--     rows=0 ... Rows Removed by Filter: 40
--
-- and the same query with the index disabled returns 5 rows in 42ms. A recall
-- that returns nothing while an exact search finds something is a wrong answer,
-- not a slow one, which is why the fix is a schema change rather than a tuning
-- knob. Raising hnsw.ef_search reduces the frequency without removing it: the
-- beam still competes with 18,287 evidence rows for its slots.
--
-- Partitioning the index by (borough, retrieval_scope) puts the recall's own
-- predicate into the index, so the graph only ever contains rows that can
-- satisfy it. Measured after this migration: 5 rows in 0.6ms, with no rows
-- discarded by a filter.
--
-- The old indexes are kept rather than dropped. Dropping them would take a
-- full rebuild under an ACCESS EXCLUSIVE lock on a 30k-row table, and the
-- planner already prefers the narrower indexes. They remain correct for an
-- unscoped recall, which is the one query shape they suit.
DO $$
DECLARE
    boro text;
    scop text;
BEGIN
    FOREACH boro IN ARRAY ARRAY[
        'manhattan', 'brooklyn', 'queens', 'bronx', 'staten_island'
    ] LOOP
        FOREACH scop IN ARRAY ARRAY['restaurant', 'evidence'] LOOP
            EXECUTE format(
                'CREATE INDEX IF NOT EXISTS knowledge_documents_hnsw_%s_%s
                   ON knowledge_documents USING hnsw (embedding vector_cosine_ops)
                   WHERE is_active AND borough = %L AND retrieval_scope = %L',
                boro, scop, boro, scop
            );
        END LOOP;
    END LOOP;
END $$;
