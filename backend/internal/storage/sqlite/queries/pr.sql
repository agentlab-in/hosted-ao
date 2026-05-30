-- name: UpsertPR :exec
INSERT INTO pr (
    session_id, review_decision, mergeability, ci_state, ci_passed, ci_failed, ci_pending, ci_log_tail, last_fetched_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id) DO UPDATE SET
    review_decision = excluded.review_decision,
    mergeability    = excluded.mergeability,
    ci_state        = excluded.ci_state,
    ci_passed       = excluded.ci_passed,
    ci_failed       = excluded.ci_failed,
    ci_pending      = excluded.ci_pending,
    ci_log_tail     = excluded.ci_log_tail,
    last_fetched_at = excluded.last_fetched_at;

-- name: GetPR :one
SELECT session_id, review_decision, mergeability, ci_state, ci_passed, ci_failed, ci_pending, ci_log_tail, last_fetched_at
FROM pr
WHERE session_id = ?;

-- name: DeletePR :exec
DELETE FROM pr WHERE session_id = ?;

-- name: DeletePRChecks :exec
DELETE FROM pr_check WHERE session_id = ?;

-- name: InsertPRCheck :exec
INSERT INTO pr_check (session_id, name, status, url) VALUES (?, ?, ?, ?);

-- name: ListPRChecks :many
SELECT name, status, url FROM pr_check WHERE session_id = ? ORDER BY name;

-- name: DeletePRComments :exec
DELETE FROM pr_comment WHERE session_id = ?;

-- name: InsertPRComment :exec
INSERT INTO pr_comment (session_id, comment_id, author, file, line, body, resolved, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListPRComments :many
SELECT comment_id, author, file, line, body, resolved, created_at
FROM pr_comment
WHERE session_id = ?
ORDER BY created_at, comment_id;
