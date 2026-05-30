-- name: GetMetadata :many
SELECT key, value FROM session_metadata WHERE session_id = ?;

-- name: UpsertMetadata :exec
INSERT INTO session_metadata (session_id, key, value)
VALUES (?, ?, ?)
ON CONFLICT (session_id, key) DO UPDATE SET value = excluded.value;
