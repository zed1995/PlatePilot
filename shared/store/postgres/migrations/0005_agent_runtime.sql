-- M4-07 agent run and tool call audit.
--
-- These tables are written by the audit side path: one row per agent run and
-- one row per tool invocation. They are operational evidence, never read on
-- the answer path, so their indexes target the operational queries (a
-- thread's recent runs, a run's tool calls) rather than analytics.

-- ---------------------------------------------------------------------------
-- agent_runs
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS agent_runs (
    run_id           text        PRIMARY KEY,
    trace_id         text        NOT NULL,
    thread_id        text        NOT NULL,
    status           text        NOT NULL,
    model_provider   text        NOT NULL DEFAULT '',
    model_name       text        NOT NULL DEFAULT '',
    started_at       timestamptz NOT NULL,
    finished_at      timestamptz,
    latency_ms       bigint      NOT NULL DEFAULT 0,
    token_input      integer     NOT NULL DEFAULT 0,
    token_output     integer     NOT NULL DEFAULT 0,
    retrieval_count  integer     NOT NULL DEFAULT 0,
    tool_call_count  integer     NOT NULL DEFAULT 0,
    error_code       text        NOT NULL DEFAULT '',

    CONSTRAINT agent_runs_trace_key UNIQUE (trace_id),
    CONSTRAINT agent_runs_status_check
        CHECK (status IN ('running', 'succeeded', 'failed', 'cancelled'))
);

-- "List this thread's runs, newest first" is the primary audit query.
CREATE INDEX IF NOT EXISTS agent_runs_thread_started
    ON agent_runs (thread_id, started_at DESC);

-- Operational time-window scans across threads.
CREATE INDEX IF NOT EXISTS agent_runs_started_at
    ON agent_runs (started_at DESC);

-- ---------------------------------------------------------------------------
-- tool_calls
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS tool_calls (
    call_id        text        PRIMARY KEY,
    run_id         text        NOT NULL REFERENCES agent_runs (run_id) ON DELETE CASCADE,
    tool_name      text        NOT NULL,
    -- Redacted argument digest only: whitelisted business fields, truncated.
    -- Contact details and free-form identifiers must never reach this column.
    arguments      jsonb       NOT NULL DEFAULT '{}'::jsonb,
    result_summary text        NOT NULL DEFAULT '',
    status         text        NOT NULL,
    latency_ms     bigint      NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL,

    CONSTRAINT tool_calls_status_check
        CHECK (status IN ('ok', 'error'))
);

-- "Show every tool call one run made" (FK cascade already narrows by run;
-- the index keeps the ordered fetch off a sequential scan).
CREATE INDEX IF NOT EXISTS tool_calls_run_created
    ON tool_calls (run_id, created_at);
