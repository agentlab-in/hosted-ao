-- name: ListReactionTrackers :many
SELECT session_id, reaction_key, attempts, escalated, first_attempt_at, project_id
FROM reaction_trackers;

-- name: UpsertReactionTracker :exec
INSERT INTO reaction_trackers (session_id, reaction_key, attempts, escalated, first_attempt_at, project_id)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id, reaction_key) DO UPDATE SET
    attempts = excluded.attempts,
    escalated = excluded.escalated,
    first_attempt_at = excluded.first_attempt_at,
    project_id = excluded.project_id;

-- name: DeleteReactionTracker :exec
DELETE FROM reaction_trackers WHERE session_id = ? AND reaction_key = ?;

-- name: DeleteSessionReactionTrackers :exec
DELETE FROM reaction_trackers WHERE session_id = ?;
