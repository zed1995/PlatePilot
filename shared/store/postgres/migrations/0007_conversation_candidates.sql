-- M5-05 conversation candidate snapshot.
--
-- One row per (thread, position). The snapshot is replaced wholesale on a turn
-- that produced candidates and left alone on a turn that did not, so the
-- composite primary key is the whole identity: two threads never share a
-- position, and one thread can never hold two restaurants at position 2 —
-- which is what makes "第二家" a deterministic reference.

CREATE TABLE IF NOT EXISTS conversation_candidates (
    thread_id     text        NOT NULL REFERENCES conversations (thread_id) ON DELETE CASCADE,
    position      integer     NOT NULL,
    restaurant_id bigint      NOT NULL,
    name          text        NOT NULL DEFAULT '',
    score         double precision NOT NULL DEFAULT 0,
    reasons       text[]      NOT NULL DEFAULT '{}',
    -- The observation time of the underlying data, carried so a follow-up can
    -- still say when the recommendation was based on data from.
    snapshot_at   timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (thread_id, position),
    CONSTRAINT conversation_candidates_position_positive CHECK (position > 0),
    CONSTRAINT conversation_candidates_restaurant_positive CHECK (restaurant_id > 0)
);

-- The only read is "this thread's candidates in position order", which the
-- primary key already serves; the redundant index is dropped on purpose so the
-- write path stays cheap.
