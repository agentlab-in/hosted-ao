-- name: InsertMachineHandoff :execrows
INSERT INTO machine_handoffs (
    id, session_id, source_machine, destination_machine, role, state,
    checkpoint_hash, activation_hash, activation_secret, revision, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT DO NOTHING;

-- name: GetMachineHandoff :one
SELECT * FROM machine_handoffs WHERE id = ? AND role = ?;

-- name: AdvanceMachineHandoff :execrows
UPDATE machine_handoffs SET state = sqlc.arg(next_state),
    checkpoint_hash = sqlc.arg(checkpoint_hash), revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND role = sqlc.arg(role)
    AND state = sqlc.arg(expected_state) AND revision = sqlc.arg(expected_revision);

-- name: MachineHandoffFencesSession :one
SELECT EXISTS (SELECT 1 FROM machine_handoffs WHERE session_id = ?
    AND state NOT IN ('cancelled', 'active'));
