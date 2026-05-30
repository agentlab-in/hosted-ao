-- name: GetSessionMetadata :one
SELECT branch, workspace_path, runtime_handle_id, runtime_name, agent_session_id, prompt
FROM session_metadata
WHERE session_id = ?;

-- name: UpsertSessionMetadata :exec
-- Merge semantics: an empty incoming column is "leave unchanged", so a partial
-- patch (e.g. spawn writing only the runtime handle) never clobbers a value set
-- earlier (e.g. the branch set at creation). Mirrors the old per-key map merge.
INSERT INTO session_metadata (
    session_id, branch, workspace_path, runtime_handle_id, runtime_name, agent_session_id, prompt, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (session_id) DO UPDATE SET
    branch            = CASE WHEN excluded.branch            <> '' THEN excluded.branch            ELSE session_metadata.branch            END,
    workspace_path    = CASE WHEN excluded.workspace_path    <> '' THEN excluded.workspace_path    ELSE session_metadata.workspace_path    END,
    runtime_handle_id = CASE WHEN excluded.runtime_handle_id <> '' THEN excluded.runtime_handle_id ELSE session_metadata.runtime_handle_id END,
    runtime_name      = CASE WHEN excluded.runtime_name      <> '' THEN excluded.runtime_name      ELSE session_metadata.runtime_name      END,
    agent_session_id  = CASE WHEN excluded.agent_session_id  <> '' THEN excluded.agent_session_id  ELSE session_metadata.agent_session_id  END,
    prompt            = CASE WHEN excluded.prompt            <> '' THEN excluded.prompt            ELSE session_metadata.prompt            END,
    updated_at        = excluded.updated_at;
