-- name: CreateReward :one
INSERT INTO rewards (household_id, title, description, cost_points, cost_pence, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetReward :one
SELECT * FROM rewards WHERE household_id = ? AND id = ?;

-- name: ListRewards :many
SELECT * FROM rewards
WHERE household_id = ? AND archived_at IS NULL
ORDER BY cost_points, cost_pence, title;

-- name: ArchiveReward :exec
UPDATE rewards SET archived_at = ? WHERE household_id = ? AND id = ?;

-- name: CreateRedemption :one
INSERT INTO redemptions (household_id, reward_id, person_id, points, pence, status, requested_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListPendingRedemptions :many
SELECT * FROM redemptions
WHERE household_id = ? AND status = 'pending'
ORDER BY requested_at;
