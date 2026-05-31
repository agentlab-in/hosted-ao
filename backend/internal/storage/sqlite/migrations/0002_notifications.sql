-- +goose Up
-- +goose StatementBegin
CREATE TABLE notifications (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    id            TEXT NOT NULL UNIQUE DEFAULT ('ntf_' || lower(hex(randomblob(16)))),
    project_id    TEXT NOT NULL REFERENCES projects(id),
    session_id    TEXT NOT NULL REFERENCES sessions(id),
    source        TEXT NOT NULL DEFAULT 'lifecycle' CHECK (source IN ('lifecycle')),
    event_type    TEXT NOT NULL,
    semantic_type TEXT NOT NULL,
    priority      TEXT NOT NULL CHECK (priority IN ('urgent','action','warning','info')),
    message       TEXT NOT NULL,
    payload_json  TEXT NOT NULL CHECK (json_valid(payload_json)),
    actions_json  TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(actions_json)),
    dedupe_key    TEXT NOT NULL UNIQUE,
    cause_key     TEXT NOT NULL DEFAULT '',
    read_at       TIMESTAMP,
    archived_at   TIMESTAMP,
    created_at    TIMESTAMP NOT NULL DEFAULT (datetime('now')),
    updated_at    TIMESTAMP NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_notifications_project_seq ON notifications(project_id, seq DESC);
CREATE INDEX idx_notifications_session_seq ON notifications(session_id, seq DESC);
CREATE INDEX idx_notifications_unread ON notifications(seq DESC)
    WHERE read_at IS NULL AND archived_at IS NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER notifications_cdc_insert
AFTER INSERT ON notifications
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        NEW.project_id,
        NEW.session_id,
        'notification_created',
        json_object(
            'seq', NEW.seq,
            'id', NEW.id,
            'type', NEW.semantic_type,
            'priority', NEW.priority,
            'message', NEW.message,
            'data', json(NEW.payload_json),
            'actions', json(NEW.actions_json),
            'readAt', NEW.read_at,
            'archivedAt', NEW.archived_at
        ),
        NEW.created_at
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER notifications_cdc_update
AFTER UPDATE ON notifications
WHEN OLD.read_at IS NOT NEW.read_at
  OR OLD.archived_at IS NOT NEW.archived_at
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        NEW.project_id,
        NEW.session_id,
        'notification_updated',
        json_object(
            'seq', NEW.seq,
            'id', NEW.id,
            'readAt', NEW.read_at,
            'archivedAt', NEW.archived_at
        ),
        NEW.updated_at
    );
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS notifications_cdc_update;
DROP TRIGGER IF EXISTS notifications_cdc_insert;
DROP TABLE IF EXISTS notifications;
-- +goose StatementEnd
