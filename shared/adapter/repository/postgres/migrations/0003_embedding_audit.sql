-- ---------------------------------------------------------------------------
-- M2: embedding and document-build audit counters
-- ---------------------------------------------------------------------------

-- The M2 stages need counters that the M1 columns do not express. documents
-- built, documents embedded, and the per-reason rejection counts are all real
-- outcomes of a run, and the reviewer-facing `report` command has to be able to
-- show them.
--
-- They are added as their own nullable columns rather than being folded into
-- missing_fields: that column means "a required source field was absent", and a
-- query counting embeddings is not that. Overloading it would make the audit
-- table lie about its own contents.
--
-- All four are nullable so a row written by the M1 stages reads as "this stage
-- did not apply" instead of a misleading zero.
ALTER TABLE ingestion_batches
    ADD COLUMN IF NOT EXISTS documents_built   bigint,
    ADD COLUMN IF NOT EXISTS documents_embedded bigint,
    ADD COLUMN IF NOT EXISTS documents_rejected bigint,
    ADD COLUMN IF NOT EXISTS embedding_model   text,
    ADD COLUMN IF NOT EXISTS embedding_dimensions integer;

-- Rejection counts by reason. This is a jsonb map of reason -> count, which is
-- the right shape for an open-ended set of quality codes: a column per reason
-- would mean a migration every time the quality gate grows a rule.
ALTER TABLE ingestion_batches
    ADD COLUMN IF NOT EXISTS reject_reasons jsonb;
