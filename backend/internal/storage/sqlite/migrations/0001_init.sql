-- +goose Up
-- +goose StatementBegin

-- sessions holds identity + the canonical lifecycle as typed columns. The
-- display status is NEVER stored (it is derived on read). Metadata is NOT here —
-- it lives in session_metadata, written by a side-channel that bypasses CDC.
CREATE TABLE sessions (
    id                       TEXT PRIMARY KEY,
    project_id               TEXT NOT NULL,
    issue_id                 TEXT NOT NULL DEFAULT '',
    kind                     TEXT NOT NULL,
    created_at               TIMESTAMP NOT NULL,
    updated_at               TIMESTAMP NOT NULL,

    -- canonical lifecycle: revision is the optimistic-concurrency (CAS) counter,
    -- bumped only by the storage layer's Upsert.
    revision                 INTEGER NOT NULL,

    session_state            TEXT NOT NULL,
    session_reason           TEXT NOT NULL,

    pr_state                 TEXT NOT NULL,
    pr_reason                TEXT NOT NULL,
    pr_number                INTEGER NOT NULL DEFAULT 0,
    pr_url                   TEXT NOT NULL DEFAULT '',

    runtime_state            TEXT NOT NULL,
    runtime_reason           TEXT NOT NULL,

    activity_state           TEXT NOT NULL,
    activity_last_at         TIMESTAMP NOT NULL,
    activity_source          TEXT NOT NULL,

    -- detecting quarantine memory; NULL when the session is not in detecting.
    detecting_attempts       INTEGER,
    detecting_started_at     TIMESTAMP,
    detecting_evidence_hash  TEXT
);

CREATE INDEX idx_sessions_project ON sessions (project_id);

-- session_metadata is the 1:1 typed side-channel for a session's operational
-- handles and seed inputs — the fields the Session Manager and reaper need but
-- that are NOT part of the canonical lifecycle. One row per session, named
-- columns (not a free-form key/value bag), so the set of metadata a session can
-- carry is fixed by the schema. Written by PatchMetadata; never bumps revision
-- and never emits a CDC event.
CREATE TABLE session_metadata (
    session_id        TEXT PRIMARY KEY REFERENCES sessions (id) ON DELETE CASCADE,
    branch            TEXT NOT NULL DEFAULT '',
    workspace_path    TEXT NOT NULL DEFAULT '',
    runtime_handle_id TEXT NOT NULL DEFAULT '',
    runtime_name      TEXT NOT NULL DEFAULT '',
    agent_session_id  TEXT NOT NULL DEFAULT '',
    prompt            TEXT NOT NULL DEFAULT '',
    updated_at        TIMESTAMP NOT NULL
);

-- change_log is the durable, ordered record of every canonical write. seq is the
-- monotonic CDC ordering/idempotency key.
CREATE TABLE change_log (
    seq        INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    revision   INTEGER NOT NULL,
    payload    TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL
);

-- outbox is the transactional-outbox: one unsent row per canonical write, drained
-- by the publisher into JSONL. change_log_seq links it to its change_log row.
CREATE TABLE outbox (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    change_log_seq INTEGER NOT NULL REFERENCES change_log (seq),
    sent           INTEGER NOT NULL DEFAULT 0,
    sent_at        TIMESTAMP,
    attempts       INTEGER NOT NULL DEFAULT 0,
    last_error     TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMP NOT NULL
);

CREATE INDEX idx_outbox_unsent ON outbox (change_log_seq) WHERE sent = 0;

-- consumer_offsets is the durable per-consumer cursor (at-least-once delivery).
CREATE TABLE consumer_offsets (
    consumer  TEXT PRIMARY KEY,
    last_seq  INTEGER NOT NULL DEFAULT 0,
    updated_at TIMESTAMP NOT NULL
);

-- reaction_trackers is the durable escalation budget (persisted so a restart does
-- not re-fire human pages). Off the canonical CDC path. Mirrors the LCM's
-- in-memory reactionTracker: attempts (numeric budget), escalated (silences
-- further auto-dispatch), first_attempt_at (duration-escalation anchor),
-- project_id (captured at first attempt for the escalation event).
CREATE TABLE reaction_trackers (
    session_id       TEXT NOT NULL,
    reaction_key     TEXT NOT NULL,
    attempts         INTEGER NOT NULL DEFAULT 0,
    escalated        INTEGER NOT NULL DEFAULT 0,
    first_attempt_at TIMESTAMP,
    project_id       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (session_id, reaction_key)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE reaction_trackers;
DROP TABLE consumer_offsets;
DROP TABLE outbox;
DROP TABLE change_log;
DROP TABLE session_metadata;
DROP TABLE sessions;
-- +goose StatementEnd
