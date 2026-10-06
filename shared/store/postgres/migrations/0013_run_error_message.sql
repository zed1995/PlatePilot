-- M4 follow-up: a failed run's reason, not just its category.
--
-- agent_runs recorded error_code and nothing else about a failure. A code is a
-- classification, and it is not enough to act on: a mistyped model id, a quota
-- refusal, and a region block can all surface as provider_unavailable, while
-- the one thing that says which it was — the provider's own message, naming the
-- model and the endpoint — was discarded the moment the turn failed.
--
-- The column is written only on the failure path and never read to build an
-- answer, so it is not indexed. It is NOT NULL with an empty default so every
-- read can scan it directly; an absent message is the empty string, not NULL.

ALTER TABLE agent_runs ADD COLUMN IF NOT EXISTS error_message text NOT NULL DEFAULT '';
