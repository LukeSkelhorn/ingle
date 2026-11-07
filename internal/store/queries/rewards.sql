-- name: CreateReward :one
INSERT INTO rewards (household_id, title, emoji, description, cost_points, cost_pence, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
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

-- name: GetRedemption :one
SELECT * FROM redemptions WHERE household_id = ? AND id = ?;

-- name: ReviewRedemption :execrows
-- Settle a waiting claim. Only a pending claim changes, so two adults
-- tapping at once can't both act on it.
UPDATE redemptions
SET status = sqlc.arg(status), reviewed_by = sqlc.arg(reviewed_by), reviewed_at = sqlc.arg(reviewed_at)
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id) AND status = 'pending';

-- name: ListPendingRedemptionDetails :many
-- Waiting claims with who and what, oldest first.
SELECT
    d.id, d.reward_id, d.person_id, d.points, d.pence, d.status, d.requested_at,
    p.name AS person_name, p.colour AS person_colour, p.avatar AS person_avatar,
    r.title, r.emoji
FROM redemptions d
JOIN people p ON p.id = d.person_id
JOIN rewards r ON r.id = d.reward_id
WHERE d.household_id = sqlc.arg(household_id) AND d.status = 'pending'
ORDER BY d.requested_at, d.id;

-- name: GetRedemptionDetail :one
SELECT
    d.id, d.reward_id, d.person_id, d.points, d.pence, d.status, d.requested_at,
    p.name AS person_name, p.colour AS person_colour, p.avatar AS person_avatar,
    r.title, r.emoji
FROM redemptions d
JOIN people p ON p.id = d.person_id
JOIN rewards r ON r.id = d.reward_id
WHERE d.household_id = sqlc.arg(household_id) AND d.id = sqlc.arg(id);

-- name: HeldForPerson :one
-- What a person's waiting claims hold back from their balance.
SELECT
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence,
    CAST(COUNT(*) AS INTEGER) AS waiting
FROM redemptions
WHERE household_id = ? AND person_id = ? AND status = 'pending';

-- name: HeldForHousehold :many
SELECT
    person_id,
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence,
    CAST(COUNT(*) AS INTEGER) AS waiting
FROM redemptions
WHERE household_id = ? AND status = 'pending'
GROUP BY person_id;

-- name: UpdateReward :one
UPDATE rewards SET title = ?, emoji = ?, description = ?, cost_points = ?, cost_pence = ?
WHERE household_id = ? AND id = ?
RETURNING *;

-- name: ListArchivedRewards :many
SELECT * FROM rewards
WHERE household_id = ? AND archived_at IS NOT NULL
ORDER BY title;

-- name: UnarchiveReward :exec
UPDATE rewards SET archived_at = NULL WHERE household_id = ? AND id = ?;

-- name: ListGoals :many
-- What each active person is saving for (never an archived reward), and
-- whether they've already asked for it.
SELECT
    p.id AS person_id, CAST(p.goal_set_at AS INTEGER) AS set_at,
    r.id, r.title, r.emoji, r.cost_points, r.cost_pence,
    CAST(EXISTS (
        SELECT 1 FROM redemptions d
        WHERE d.household_id = p.household_id AND d.person_id = p.id
          AND d.reward_id = r.id AND d.status = 'pending'
    ) AS INTEGER) AS waiting
FROM people p
JOIN rewards r ON r.id = p.goal_reward_id AND r.household_id = p.household_id
WHERE p.household_id = ? AND p.archived_at IS NULL AND r.archived_at IS NULL;

-- name: ListPendingRedemptionsForPerson :many
-- A person's waiting claims, newest first, with what they're for.
SELECT d.id, d.points, d.pence, d.requested_at, r.title, r.emoji
FROM redemptions d
JOIN rewards r ON r.id = d.reward_id
WHERE d.household_id = sqlc.arg(household_id) AND d.person_id = sqlc.arg(person_id) AND d.status = 'pending'
ORDER BY d.requested_at DESC, d.id DESC;
