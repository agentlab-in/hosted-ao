-- name: UpsertPREnrichment :exec
INSERT INTO pr_enrichment (session_id, ci_summary, review_decision, mergeability, pending_comments, ci_log_tail, last_fetched_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id) DO UPDATE SET
    ci_summary = excluded.ci_summary,
    review_decision = excluded.review_decision,
    mergeability = excluded.mergeability,
    pending_comments = excluded.pending_comments,
    ci_log_tail = excluded.ci_log_tail,
    last_fetched_at = excluded.last_fetched_at;

-- name: GetPREnrichment :one
SELECT session_id, ci_summary, review_decision, mergeability, pending_comments, ci_log_tail, last_fetched_at
FROM pr_enrichment
WHERE session_id = ?;

-- name: DeletePREnrichment :exec
DELETE FROM pr_enrichment WHERE session_id = ?;
