-- ---------------------------------------------------------------------------
-- M2: knowledge document idempotency
-- ---------------------------------------------------------------------------

-- The document identity that makes a rebuild idempotent.
--
-- UpsertDocuments keys on (restaurant_id, retrieval_scope, doc_type,
-- content_hash): an unchanged document is skipped, and changed content becomes
-- a new version beside the old one instead of overwriting it. Without this
-- index the ON CONFLICT clause has nothing to bind to and a re-run would
-- insert a duplicate row for every document on every run.
--
-- The key deliberately excludes is_active and version. Those change over a
-- document's life, so keying on them would defeat the idempotency a rebuild
-- depends on and would block a genuine new version of the same text.
CREATE UNIQUE INDEX IF NOT EXISTS knowledge_documents_identity
    ON knowledge_documents (restaurant_id, retrieval_scope, doc_type, content_hash);
