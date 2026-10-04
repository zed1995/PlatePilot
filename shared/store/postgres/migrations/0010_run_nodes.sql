-- M6-01 node-level trace: one row per graph node a run passed through.
--
-- agent_runs records the turn and tool_calls records each tool invocation.
-- Neither can answer "which step failed", because the steps that call no tool
-- — the planning round and the answer composition, which are the two that
-- call a model — leave no tool_calls row at all. A turn that failed inside
-- answer looked, in the audit trail, exactly like one that failed inside plan.
--
-- This table is the missing granularity: one row per node with its own start
-- and duration, so a failure is located by name.

CREATE TABLE IF NOT EXISTS run_nodes (
    node_id      text        PRIMARY KEY,
    run_id       text        NOT NULL REFERENCES agent_runs (run_id) ON DELETE CASCADE,
    trace_id     text        NOT NULL DEFAULT '',
    node         text        NOT NULL,
    seq          integer     NOT NULL,
    status       text        NOT NULL,
    started_at   timestamptz NOT NULL,
    latency_ms   bigint      NOT NULL DEFAULT 0,
    -- Node-specific facts written by the node itself: candidate counts, tool
    -- names, finish reason. Never read on the answer path, and never a place
    -- for anything the caller sent.
    detail       jsonb       NOT NULL DEFAULT '{}'::jsonb,
    error_code   text        NOT NULL DEFAULT '',

    CONSTRAINT run_nodes_status_check
        CHECK (status IN ('ok', 'error')),
    CONSTRAINT run_nodes_seq_positive CHECK (seq > 0)
);

-- "Show me this run's timeline" is the only read, and it is always by run in
-- execution order. seq is the ordering key rather than started_at because two
-- nodes of a fast run share a millisecond, and an order that depends on clock
-- resolution is an order that differs between runs.
CREATE UNIQUE INDEX IF NOT EXISTS run_nodes_run_seq
    ON run_nodes (run_id, seq);

-- Trace-scoped lookup: a trace_id names one user-visible request end to end,
-- and M6-01's acceptance is that any failure can be located from it.
CREATE INDEX IF NOT EXISTS run_nodes_trace
    ON run_nodes (trace_id, seq);
