-- name: UpsertProject :exec
INSERT INTO projects (id, path, repo_owner, repo_name, repo_platform, repo_origin_url, default_branch, display_name, session_prefix, source, registered_at, archived_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
    path = excluded.path,
    repo_owner = excluded.repo_owner,
    repo_name = excluded.repo_name,
    repo_platform = excluded.repo_platform,
    repo_origin_url = excluded.repo_origin_url,
    default_branch = excluded.default_branch,
    display_name = excluded.display_name,
    session_prefix = excluded.session_prefix,
    source = excluded.source,
    registered_at = excluded.registered_at,
    archived_at = excluded.archived_at;

-- name: GetProject :one
SELECT id, path, repo_owner, repo_name, repo_platform, repo_origin_url, default_branch, display_name, session_prefix, source, registered_at, archived_at
FROM projects
WHERE id = ?;

-- name: ListProjects :many
SELECT id, path, repo_owner, repo_name, repo_platform, repo_origin_url, default_branch, display_name, session_prefix, source, registered_at, archived_at
FROM projects
WHERE archived_at IS NULL
ORDER BY id;

-- name: ArchiveProject :exec
UPDATE projects SET archived_at = ? WHERE id = ?;

-- name: DeleteProject :exec
DELETE FROM projects WHERE id = ?;
