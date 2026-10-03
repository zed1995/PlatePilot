-- M5-06 mock reservation inventory, bookings, and the parked-action columns.
--
-- Two things live together here because they are one feature. A reservation can
-- only be written after the user confirms an action the model merely requested,
-- so the schema has to hold both the inventory the request is checked against
-- and the pending action the confirmation replays. Splitting them would let a
-- deployment apply half the feature.

-- ---------------------------------------------------------------------------
-- reservation_slots
-- ---------------------------------------------------------------------------
-- The mock inventory: a fixed number of seats per restaurant per time. It is
-- generated from a template by the application rather than imported, which is
-- why capacity is a plain counter — there is no real table plan to model.
CREATE TABLE IF NOT EXISTS reservation_slots (
    slot_id        text        PRIMARY KEY,
    restaurant_id  bigint      NOT NULL,
    slot_date      date        NOT NULL,
    slot_time      time        NOT NULL,
    capacity       integer     NOT NULL,
    booked         integer     NOT NULL DEFAULT 0,
    policy_version text        NOT NULL DEFAULT '',
    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT reservation_slots_capacity_positive CHECK (capacity > 0),
    -- The check that makes overbooking impossible even under a buggy caller:
    -- a conditional update that would push booked past capacity is rejected by
    -- the row itself, not only by the statement that phrased the condition.
    CONSTRAINT reservation_slots_booked_within_capacity CHECK (booked >= 0 AND booked <= capacity)
);

CREATE INDEX IF NOT EXISTS reservation_slots_restaurant_date
    ON reservation_slots (restaurant_id, slot_date, slot_time);

-- ---------------------------------------------------------------------------
-- reservations
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS reservations (
    reservation_id  text        PRIMARY KEY,
    thread_id       text        NOT NULL,
    user_id         text        NOT NULL DEFAULT '',
    restaurant_id   bigint      NOT NULL,
    slot_id         text        NOT NULL REFERENCES reservation_slots (slot_id),
    party_size      integer     NOT NULL,
    status          text        NOT NULL,
    hold_expires_at timestamptz,
    -- Unique by construction: this is what turns a retried confirmation into
    -- the same booking instead of a second one. The key is derived server-side
    -- from the thread, the action, the argument digest, and the checkpoint
    -- version, so two unrelated requests cannot collide on it.
    idempotency_key text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT reservations_status_check
        CHECK (status IN ('held', 'confirmed', 'cancelled', 'expired')),
    CONSTRAINT reservations_party_size_positive CHECK (party_size > 0),
    CONSTRAINT reservations_idempotency_key_key UNIQUE (idempotency_key)
);

CREATE INDEX IF NOT EXISTS reservations_thread_created
    ON reservations (thread_id, created_at DESC);

-- A held reservation that passes its TTL is expired by a sweeper; the partial
-- index keeps that sweep off a sequential scan as the table grows.
CREATE INDEX IF NOT EXISTS reservations_held_expiring
    ON reservations (hold_expires_at)
    WHERE status = 'held';

-- ---------------------------------------------------------------------------
-- conversation_checkpoints: parked actions
-- ---------------------------------------------------------------------------
-- A write the model requested but the user has not authorised yet. It is stored
-- beside the checkpoint rather than in process memory because the confirmation
-- arrives as a later request and may arrive after a restart.
ALTER TABLE conversation_checkpoints
    ADD COLUMN IF NOT EXISTS pending_tool_call_id text NOT NULL DEFAULT '';

ALTER TABLE conversation_checkpoints
    ADD COLUMN IF NOT EXISTS pending_arguments jsonb NOT NULL DEFAULT '{}'::jsonb;
