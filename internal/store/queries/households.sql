-- name: CreateHousehold :one
INSERT INTO households (name, created_at) VALUES (?, ?)
RETURNING *;

-- name: GetHousehold :one
SELECT * FROM households WHERE id = ?;

-- name: GetFirstHousehold :one
SELECT * FROM households ORDER BY id LIMIT 1;

-- name: UpdateHouseholdName :exec
UPDATE households SET name = ? WHERE id = ?;
-- name: ListHouseholdIDs :many
SELECT id FROM households ORDER BY id;

-- name: DeleteHousehold :exec
-- Its rows in every other table go with it (ON DELETE CASCADE).
DELETE FROM households WHERE id = ?;
