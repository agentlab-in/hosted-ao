-- name: ListUnroutedNotifications :many
SELECT seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at, routed_at
FROM notifications
WHERE routed_at IS NULL
ORDER BY seq ASC
LIMIT ?;

-- name: MarkNotificationRouted :exec
UPDATE notifications
SET routed_at = COALESCE(routed_at, ?),
    updated_at = CASE WHEN routed_at IS NULL THEN ? ELSE updated_at END
WHERE id = ?;

-- name: InsertNotificationDelivery :one
INSERT INTO notification_deliveries (
    id, notification_id, notification_seq, project_id, session_id,
    route_name, sink, destination_key, request_json,
    status, attempts, max_attempts, next_attempt_at, lease_owner, lease_expires_at,
    last_error_code, last_error, external_id,
    created_at, updated_at, delivered_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(notification_id, route_name, destination_key) DO NOTHING
RETURNING id, notification_id, notification_seq, project_id, session_id,
    route_name, sink, destination_key, request_json,
    status, attempts, max_attempts, next_attempt_at, lease_owner, lease_expires_at,
    last_error_code, last_error, external_id,
    created_at, updated_at, delivered_at;

-- name: GetNotificationDelivery :one
SELECT id, notification_id, notification_seq, project_id, session_id,
    route_name, sink, destination_key, request_json,
    status, attempts, max_attempts, next_attempt_at, lease_owner, lease_expires_at,
    last_error_code, last_error, external_id,
    created_at, updated_at, delivered_at
FROM notification_deliveries
WHERE id = ?;

-- name: GetNotificationDeliveryByUnique :one
SELECT id, notification_id, notification_seq, project_id, session_id,
    route_name, sink, destination_key, request_json,
    status, attempts, max_attempts, next_attempt_at, lease_owner, lease_expires_at,
    last_error_code, last_error, external_id,
    created_at, updated_at, delivered_at
FROM notification_deliveries
WHERE notification_id = ? AND route_name = ? AND destination_key = ?;
