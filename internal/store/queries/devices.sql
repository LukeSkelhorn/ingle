-- name: CreateDevice :one
INSERT INTO devices (household_id, name, token_hash, created_by, created_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetDeviceByToken :one
SELECT * FROM devices WHERE token_hash = ? AND revoked_at IS NULL;

-- name: TouchDevice :exec
UPDATE devices SET last_seen_at = ? WHERE id = ?;

-- name: ListDevices :many
SELECT * FROM devices
WHERE household_id = ? AND revoked_at IS NULL
ORDER BY name, id;

-- name: RevokeDevice :execrows
UPDATE devices SET revoked_at = ?
WHERE household_id = ? AND id = ? AND revoked_at IS NULL;
