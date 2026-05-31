-- +goose Up
-- +goose StatementBegin
ALTER TABLE notifications ADD COLUMN routed_at TIMESTAMP;
-- Notifications that already exist when this migration runs predate the
-- delivery runtime. Treat them as already routed so an upgrade does not
-- synthesize new AO-app desktop toasts for historical notification rows; the
-- dashboard read model still sees those rows directly from notifications.
UPDATE notifications SET routed_at = updated_at WHERE routed_at IS NULL;
CREATE INDEX idx_notifications_unrouted
    ON notifications(seq)
    WHERE routed_at IS NULL;

CREATE TABLE notification_deliveries (
    id                  TEXT PRIMARY KEY,
    notification_id     TEXT NOT NULL REFERENCES notifications(id) ON DELETE CASCADE,
    notification_seq    INTEGER NOT NULL,
    project_id          TEXT NOT NULL REFERENCES projects(id),
    session_id          TEXT NOT NULL REFERENCES sessions(id),

    route_name          TEXT NOT NULL,
    sink                TEXT NOT NULL,
    destination_key     TEXT NOT NULL DEFAULT '',
    request_json        TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(request_json)),

    status              TEXT NOT NULL CHECK (status IN ('queued','leased','sent','retry_wait','failed','skipped','cancelled')),
    attempts            INTEGER NOT NULL DEFAULT 0,
    max_attempts        INTEGER NOT NULL DEFAULT 5,
    next_attempt_at     TIMESTAMP NOT NULL,
    lease_owner         TEXT NOT NULL DEFAULT '',
    lease_expires_at    TIMESTAMP,

    last_error_code     TEXT NOT NULL DEFAULT '',
    last_error          TEXT NOT NULL DEFAULT '',
    external_id         TEXT NOT NULL DEFAULT '',

    created_at          TIMESTAMP NOT NULL DEFAULT (datetime('now')),
    updated_at          TIMESTAMP NOT NULL DEFAULT (datetime('now')),
    delivered_at        TIMESTAMP,

    UNIQUE(notification_id, route_name, destination_key)
);

CREATE INDEX idx_notification_deliveries_due
    ON notification_deliveries(status, next_attempt_at, lease_expires_at, created_at);

CREATE INDEX idx_notification_deliveries_notification
    ON notification_deliveries(notification_id, status);

CREATE INDEX idx_notification_deliveries_project
    ON notification_deliveries(project_id, created_at DESC);

CREATE TABLE notification_delivery_attempts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    delivery_id TEXT NOT NULL REFERENCES notification_deliveries(id) ON DELETE CASCADE,
    attempt_no INTEGER NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('started','sent','retryable_failed','failed')),
    started_at TIMESTAMP NOT NULL,
    finished_at TIMESTAMP,
    error_code TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    response_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(response_json)),
    UNIQUE(delivery_id, attempt_no)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER notification_deliveries_cdc_insert
AFTER INSERT ON notification_deliveries
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        NEW.project_id,
        NEW.session_id,
        'notification_delivery_created',
        json_object(
            'id', NEW.id,
            'notificationId', NEW.notification_id,
            'routeName', NEW.route_name,
            'sink', NEW.sink,
            'status', NEW.status,
            'attempts', NEW.attempts,
            'lastErrorCode', NEW.last_error_code,
            'lastError', NEW.last_error
        ),
        NEW.created_at
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER notification_deliveries_cdc_update
AFTER UPDATE ON notification_deliveries
WHEN OLD.status <> NEW.status
  OR OLD.attempts <> NEW.attempts
  OR OLD.last_error_code <> NEW.last_error_code
  OR OLD.last_error <> NEW.last_error
  OR OLD.external_id <> NEW.external_id
  OR OLD.delivered_at IS NOT NEW.delivered_at
BEGIN
    INSERT INTO change_log (project_id, session_id, event_type, payload, created_at)
    VALUES (
        NEW.project_id,
        NEW.session_id,
        'notification_delivery_updated',
        json_object(
            'id', NEW.id,
            'notificationId', NEW.notification_id,
            'routeName', NEW.route_name,
            'sink', NEW.sink,
            'status', NEW.status,
            'attempts', NEW.attempts,
            'lastErrorCode', NEW.last_error_code,
            'lastError', NEW.last_error
        ),
        NEW.updated_at
    );
END;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TRIGGER IF EXISTS notification_deliveries_cdc_update;
DROP TRIGGER IF EXISTS notification_deliveries_cdc_insert;
DROP TABLE IF EXISTS notification_delivery_attempts;
DROP TABLE IF EXISTS notification_deliveries;
DROP INDEX IF EXISTS idx_notifications_unrouted;
ALTER TABLE notifications DROP COLUMN routed_at;
-- +goose StatementEnd
