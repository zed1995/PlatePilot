-- OPT-06: a tool call's start, and a stable key for reading the round back.
--
-- A tool round may run several read-only calls at once, so a call that was
-- asked for first can finish last. Two consequences, one column each:
--
--   * The audit rows are written in completion order, because that is when
--     each write happens and holding them back to sort them would mean
--     buffering audit rows in memory to no end. created_at is therefore the
--     completion instant, and it no longer says anything about request order.
--   * The read path has to be able to reconstruct request order anyway, or the
--     tool chain reads back in whatever order the stores happened to answer
--     in. That key is seq.
--
-- seq rather than started_at, and this was measured rather than assumed: the
-- two calls of one round are launched a few hundred nanoseconds apart, while
-- started_at is a wall clock stored at microsecond resolution. A probe that
-- reproduced the launch loop found the two timestamps equal 87% of the time,
-- so an order derived from them would be a coin toss with a tiebreak, and it
-- would differ between runs of the same turn. run_nodes.seq records the same
-- decision for the same reason.
--
-- started_at is still added, because it answers the question seq cannot: when
-- did each call actually begin. That is what makes an overlap visible in the
-- trail, and what a latency without a start cannot tell you.

ALTER TABLE tool_calls ADD COLUMN IF NOT EXISTS started_at timestamptz;
ALTER TABLE tool_calls ADD COLUMN IF NOT EXISTS seq integer;

-- Backfill the rows written before either column existed. Their start is
-- recoverable from what they do carry: they finished at created_at, having
-- taken latency_ms. That is the same derivation the node spans use, and it is
-- the most these rows can honestly say.
UPDATE tool_calls
   SET started_at = created_at - (latency_ms * interval '1 millisecond')
 WHERE started_at IS NULL;

-- Their request order was never recorded and cannot be recovered — reads were
-- sequential when they were written, so created_at was a faithful proxy for it
-- and numbering within the run by that is the only reconstruction available.
WITH numbered AS (
    SELECT call_id,
           row_number() OVER (PARTITION BY run_id ORDER BY created_at, call_id) AS position
      FROM tool_calls
     WHERE seq IS NULL
)
UPDATE tool_calls AS target
   SET seq = numbered.position
  FROM numbered
 WHERE target.call_id = numbered.call_id;

-- Every later write supplies both. A nullable column would make the read
-- path's ORDER BY depend on a value that is only sometimes there.
ALTER TABLE tool_calls ALTER COLUMN started_at SET NOT NULL;
ALTER TABLE tool_calls ALTER COLUMN seq SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'tool_calls_seq_positive'
    ) THEN
        ALTER TABLE tool_calls ADD CONSTRAINT tool_calls_seq_positive CHECK (seq > 0);
    END IF;
END $$;

-- "Show every tool call one run made, in the order it asked for them" is the
-- only read. The index is unique because a run's tool calls have one position
-- each; a duplicate would be a bug in the caller rather than a row to keep.
CREATE UNIQUE INDEX IF NOT EXISTS tool_calls_run_seq
    ON tool_calls (run_id, seq);

-- The previous index led with the same column and ordered by the completion
-- instant, which is exactly the ordering this migration retires: keeping it
-- would leave an index that no query wants and a reader wondering which of the
-- two orders is the real one.
DROP INDEX IF EXISTS tool_calls_run_created;
