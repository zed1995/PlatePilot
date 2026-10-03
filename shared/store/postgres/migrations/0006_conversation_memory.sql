-- M4-08 conversation threads, checkpoints, and messages; M4-09 user memories.
--
-- The conversation model is normalized rather than packed into one JSON blob:
-- messages are replayed independently of checkpoint recovery, and per-thread
-- seq gives history paging an index-backed order. Every query carries
-- thread_id (or user_id) so rows from one conversation can never surface in
-- another.

-- ---------------------------------------------------------------------------
-- conversations
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS conversations (
    thread_id       text        PRIMARY KEY,
    user_id         text        NOT NULL DEFAULT '',
    title           text        NOT NULL DEFAULT '',
    current_state   text        NOT NULL DEFAULT 'idle',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    last_message_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT conversations_state_check
        CHECK (current_state IN (
            'idle', 'awaiting_clarification', 'awaiting_confirmation',
            'completed', 'failed'
        ))
);

CREATE INDEX IF NOT EXISTS conversations_user_updated
    ON conversations (user_id, updated_at DESC);

-- ---------------------------------------------------------------------------
-- conversation_checkpoints
-- ---------------------------------------------------------------------------
-- One row per thread; recovery only recognises the newest version. Version
-- only moves forward: SaveCheckpoint's UPDATE carries
-- "WHERE version < $new", so a stale writer affects zero rows.
CREATE TABLE IF NOT EXISTS conversation_checkpoints (
    thread_id              text        PRIMARY KEY
        REFERENCES conversations (thread_id) ON DELETE CASCADE,
    version                bigint      NOT NULL,
    state                  text        NOT NULL DEFAULT 'idle',
    pending_action         text        NOT NULL DEFAULT '',
    missing_slots          text[]      NOT NULL DEFAULT '{}',
    evidence_ids           bigint[]    NOT NULL DEFAULT '{}',
    selected_restaurant_id bigint      NOT NULL DEFAULT 0,
    created_at             timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT conversation_checkpoints_state_check
        CHECK (state IN (
            'idle', 'awaiting_clarification', 'awaiting_confirmation',
            'completed', 'failed'
        ))
);

-- ---------------------------------------------------------------------------
-- conversation_messages
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS conversation_messages (
    message_id   text        PRIMARY KEY,
    thread_id    text        NOT NULL REFERENCES conversations (thread_id) ON DELETE CASCADE,
    role         text        NOT NULL,
    content      text        NOT NULL DEFAULT '',
    tool_calls   jsonb       NOT NULL DEFAULT '[]'::jsonb,
    evidence_ids bigint[]    NOT NULL DEFAULT '{}',
    -- Per-thread position, assigned by the repository as max(seq)+1. The
    -- unique constraint is the backstop against concurrent appends.
    seq          bigint      NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT conversation_messages_role_check
        CHECK (role IN ('user', 'assistant', 'tool')),
    CONSTRAINT conversation_messages_seq_positive CHECK (seq > 0),
    CONSTRAINT conversation_messages_thread_seq_key UNIQUE (thread_id, seq)
);

-- History replay: newest N for one thread, in seq order.
CREATE INDEX IF NOT EXISTS conversation_messages_thread_seq
    ON conversation_messages (thread_id, seq DESC);

-- ---------------------------------------------------------------------------
-- user_memories
-- ---------------------------------------------------------------------------
-- User-controlled long-term memories. Deletes are soft: the audit trail keeps
-- the row, List only sees rows whose deleted_at is null.
CREATE TABLE IF NOT EXISTS user_memories (
    memory_id   text        PRIMARY KEY,
    user_id     text        NOT NULL,
    memory_type text        NOT NULL,
    content     text        NOT NULL,
    source      text        NOT NULL DEFAULT '',
    confidence  double precision NOT NULL DEFAULT 0,
    embedding   vector(1024),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz,

    CONSTRAINT user_memories_type_check
        CHECK (memory_type IN ('preference', 'constraint', 'fact'))
);

-- The only read: one user's live memories.
CREATE INDEX IF NOT EXISTS user_memories_user_live
    ON user_memories (user_id, updated_at DESC)
    WHERE deleted_at IS NULL;
