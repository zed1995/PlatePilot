-- Digest source-review provenance.
--
-- A restaurant_review_digest is the offline model-backed comprehension of one
-- restaurant. Its conclusions must be traceable to the reviews the model
-- actually used: the prompt now asks the model to report the review ids its
-- digest is grounded in, and those ids are stored here as a dedicated column
-- rather than buried in metadata so the console can join them straight to the
-- reviews table.
--
-- Only digest rows carry values; every other document kind keeps the empty
-- default. The rules generator records its full representative set, since it
-- uses all of it.

ALTER TABLE knowledge_documents
    ADD COLUMN IF NOT EXISTS source_review_ids bigint[] NOT NULL DEFAULT '{}';
