-- name: InsertSession :execrows
-- CAS insert: only succeeds for a brand-new id. Incoming revision must be 0;
-- the row is persisted at revision 1.
INSERT INTO sessions (
    id, project_id, issue_id, kind, created_at, updated_at,
    revision,
    session_state, session_reason,
    pr_state, pr_reason, pr_number, pr_url,
    runtime_state, runtime_reason,
    activity_state, activity_last_at, activity_source,
    detecting_attempts, detecting_started_at, detecting_evidence_hash
) VALUES (
    ?, ?, ?, ?, ?, ?,
    1,
    ?, ?,
    ?, ?, ?, ?,
    ?, ?,
    ?, ?, ?,
    ?, ?, ?
)
ON CONFLICT (id) DO NOTHING;

-- name: UpdateSessionCAS :execrows
-- CAS update: succeeds only when the stored revision equals the caller's loaded
-- revision (@expected_revision). 0 rows affected => revision mismatch.
UPDATE sessions SET
    project_id = ?,
    issue_id = ?,
    kind = ?,
    updated_at = ?,
    revision = revision + 1,
    session_state = ?,
    session_reason = ?,
    pr_state = ?,
    pr_reason = ?,
    pr_number = ?,
    pr_url = ?,
    runtime_state = ?,
    runtime_reason = ?,
    activity_state = ?,
    activity_last_at = ?,
    activity_source = ?,
    detecting_attempts = ?,
    detecting_started_at = ?,
    detecting_evidence_hash = ?
WHERE id = ? AND revision = ?;

-- name: GetSessionRevision :one
SELECT revision FROM sessions WHERE id = ?;

-- name: GetSession :one
SELECT * FROM sessions WHERE id = ?;

-- name: ListSessionsByProject :many
SELECT * FROM sessions WHERE project_id = ?;

-- name: ListAllSessions :many
SELECT * FROM sessions;
