-- Versions 119-125 are allocated by upstream intake PR #150.
-- +goose Up
CREATE TABLE machine_handoffs (
    id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    source_machine TEXT NOT NULL,
    destination_machine TEXT NOT NULL CHECK (destination_machine <> source_machine),
    role TEXT NOT NULL CHECK (role IN ('source', 'destination')),
    state TEXT NOT NULL,
    checkpoint_hash TEXT NOT NULL,
    activation_hash TEXT NOT NULL,
    activation_secret TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (id, role),
    CHECK ((role = 'source' AND state IN ('freezing', 'checkpoint_ready', 'relinquished', 'cancelled'))
        OR (role = 'destination' AND state IN ('preparing', 'prepared', 'activating', 'active'))),
    CHECK (role = 'source' OR activation_secret = '')
);
-- No session FK: a destination must durably reserve ownership before creating
-- its local session, and source tombstones must survive session cleanup.
CREATE UNIQUE INDEX machine_handoff_session_fence ON machine_handoffs(session_id)
    WHERE state NOT IN ('cancelled', 'active');

-- +goose StatementBegin
CREATE TRIGGER machine_handoffs_cdc_insert AFTER INSERT ON machine_handoffs
WHEN EXISTS (SELECT 1 FROM sessions WHERE id = NEW.session_id)
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id,
        'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;
CREATE TRIGGER machine_handoffs_cdc_update AFTER UPDATE ON machine_handoffs
WHEN EXISTS (SELECT 1 FROM sessions WHERE id = NEW.session_id)
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES ((SELECT project_id FROM sessions WHERE id = NEW.session_id), NEW.session_id,
        'session_updated', json_object('id', NEW.session_id), NEW.updated_at);
END;
-- +goose StatementEnd

-- +goose Down
-- Ownership tombstones cannot be dropped safely while another machine may run.
SELECT 1;
