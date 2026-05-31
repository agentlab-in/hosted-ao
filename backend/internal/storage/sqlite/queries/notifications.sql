-- name: InsertNotification :one
INSERT INTO notifications (
    project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (dedupe_key) DO NOTHING
RETURNING seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at;

-- name: GetNotification :one
SELECT seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at
FROM notifications WHERE id = ?;

-- name: GetNotificationByDedupeKey :one
SELECT seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at
FROM notifications WHERE dedupe_key = ?;

-- name: ListNotifications :many
SELECT seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at
FROM notifications
ORDER BY seq DESC
LIMIT ?;

-- name: ListNotificationsByProject :many
SELECT seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at
FROM notifications
WHERE project_id = ?
ORDER BY seq DESC
LIMIT ?;

-- name: ListNotificationsBySession :many
SELECT seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at
FROM notifications
WHERE session_id = ?
ORDER BY seq DESC
LIMIT ?;

-- name: ListUnreadNotifications :many
SELECT seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at
FROM notifications
WHERE read_at IS NULL AND archived_at IS NULL
ORDER BY seq DESC
LIMIT ?;

-- name: MarkNotificationRead :one
UPDATE notifications
SET read_at = ?, updated_at = ?
WHERE id = ? AND read_at IS NULL
RETURNING seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at;

-- name: MarkNotificationUnread :one
UPDATE notifications
SET read_at = NULL, updated_at = ?
WHERE id = ? AND read_at IS NOT NULL
RETURNING seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at;

-- name: ArchiveNotification :one
UPDATE notifications
SET archived_at = ?, updated_at = ?
WHERE id = ? AND archived_at IS NULL
RETURNING seq, id, project_id, session_id, source, event_type, semantic_type, priority,
    message, payload_json, actions_json, dedupe_key, cause_key, read_at, archived_at, created_at, updated_at;
