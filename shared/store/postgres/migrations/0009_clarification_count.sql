-- M5-03 clarification counter.
--
-- "How many times has this thread already asked the user to disambiguate a
-- restaurant name" is thread state, not turn state: each clarification happens
-- in a different request, so a runtime variable cannot see the second one. It
-- lives on the checkpoint because that is the row that already survives between
-- turns, and the alternative — counting consecutive awaiting_clarification
-- checkpoints — can only ever count to one, since only the newest state is
-- stored.
--
-- 0 is the correct value for every existing thread: a thread that has never
-- clarified has clarified zero times, which is also what an absent value would
-- have to mean.

ALTER TABLE conversation_checkpoints
    ADD COLUMN IF NOT EXISTS clarification_count integer NOT NULL DEFAULT 0;

-- The counter is a count of rounds, so a negative value is a bug in the writer
-- rather than a state the reader should try to interpret.
ALTER TABLE conversation_checkpoints
    DROP CONSTRAINT IF EXISTS conversation_checkpoints_clarification_count_check;
ALTER TABLE conversation_checkpoints
    ADD CONSTRAINT conversation_checkpoints_clarification_count_check
        CHECK (clarification_count >= 0);
