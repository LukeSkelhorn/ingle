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
-- What a person's waiting reward claims and bank requests hold back from
-- their balance. waiting counts the reward claims only.
SELECT
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence,
    CAST(COALESCE(SUM(claim), 0) AS INTEGER) AS waiting
FROM (
    SELECT d.points, d.pence, 1 AS claim FROM redemptions d
    WHERE d.household_id = sqlc.arg(household_id) AND d.person_id = sqlc.arg(person_id) AND d.status = 'pending'
    UNION ALL
    SELECT 0, t.pence, 0 FROM transfer_requests t
    WHERE t.household_id = sqlc.arg(household_id) AND t.person_id = sqlc.arg(person_id) AND t.status = 'pending'
);

-- name: HeldForHousehold :many
-- HeldForPerson for everyone.
SELECT
    person_id,
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence,
    CAST(COALESCE(SUM(claim), 0) AS INTEGER) AS waiting
FROM (
    SELECT d.person_id, d.points, d.pence, 1 AS claim FROM redemptions d
    WHERE d.household_id = sqlc.arg(household_id) AND d.status = 'pending'
    UNION ALL
    SELECT t.person_id, 0, t.pence, 0 FROM transfer_requests t
    WHERE t.household_id = sqlc.arg(household_id) AND t.status = 'pending'
)
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
-- What each active person is saving for (never an archived reward, or one
-- they may not have), and whether they've already asked for it.
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
WHERE p.household_id = ? AND p.archived_at IS NULL AND r.archived_at IS NULL
  AND (r.everyone = 1 OR EXISTS (
      SELECT 1 FROM reward_people rp WHERE rp.reward_id = r.id AND rp.person_id = p.id))
  AND (r.cost_pence = 0 OR p.kind <> 'adult');

-- name: ListPendingRedemptionsForPerson :many
-- A person's waiting claims, newest first, with what they're for.
SELECT d.id, d.points, d.pence, d.requested_at, r.title, r.emoji
FROM redemptions d
JOIN rewards r ON r.id = d.reward_id
WHERE d.household_id = sqlc.arg(household_id) AND d.person_id = sqlc.arg(person_id) AND d.status = 'pending'
ORDER BY d.requested_at DESC, d.id DESC;

-- name: ListRewardsForPerson :many
-- The active rewards a person may have, ordered like ListRewards. A person
-- may have a reward when it's for everyone or they're one of its people,
-- and (as adults have no money) it costs no money or they aren't an adult.
-- RewardAllowed, ClearGoalsNotAllowed and ListGoals apply the same rule.
SELECT * FROM rewards r
WHERE r.household_id = sqlc.arg(household_id) AND r.archived_at IS NULL
  AND (r.everyone = 1 OR EXISTS (
      SELECT 1 FROM reward_people rp WHERE rp.reward_id = r.id AND rp.person_id = sqlc.arg(person_id)))
  AND (r.cost_pence = 0 OR NOT EXISTS (
      SELECT 1 FROM people p
      WHERE p.household_id = r.household_id AND p.id = sqlc.arg(person_id) AND p.kind = 'adult'))
ORDER BY r.cost_points, r.cost_pence, r.title;

-- name: RewardAllowed :one
-- 1 if a person may have the reward, else 0.
SELECT CAST(EXISTS (
    SELECT 1 FROM rewards r JOIN people p ON p.household_id = r.household_id
    WHERE r.household_id = sqlc.arg(household_id) AND r.id = sqlc.arg(reward_id) AND p.id = sqlc.arg(person_id)
      AND (r.everyone = 1 OR EXISTS (
          SELECT 1 FROM reward_people rp WHERE rp.reward_id = r.id AND rp.person_id = p.id))
      AND (r.cost_pence = 0 OR p.kind <> 'adult')
) AS INTEGER) AS allowed;

-- name: ClearGoalsNotAllowed :exec
-- After a reward changes, anyone saving for it who may no longer have it
-- stops.
UPDATE people SET goal_reward_id = NULL, goal_set_at = NULL
WHERE people.household_id = sqlc.arg(household_id) AND people.goal_reward_id = sqlc.arg(reward_id)
  AND NOT EXISTS (
      SELECT 1 FROM rewards r
      WHERE r.id = people.goal_reward_id AND r.household_id = people.household_id
        AND (r.everyone = 1 OR EXISTS (
            SELECT 1 FROM reward_people rp WHERE rp.reward_id = r.id AND rp.person_id = people.id))
        AND (r.cost_pence = 0 OR people.kind <> 'adult'));

-- name: SetRewardEveryone :exec
UPDATE rewards SET everyone = ? WHERE household_id = ? AND id = ?;

-- name: ListPeopleForReward :many
-- A reward's chosen people who are still active, in board order.
SELECT rp.person_id
FROM reward_people rp
JOIN people p ON p.id = rp.person_id
WHERE rp.household_id = ? AND rp.reward_id = ? AND p.archived_at IS NULL
ORDER BY p.sort_order, p.id;

-- name: ListRewardPeople :many
-- Every reward's chosen people who are still active, in board order.
SELECT rp.reward_id, rp.person_id
FROM reward_people rp
JOIN people p ON p.id = rp.person_id
WHERE rp.household_id = ? AND p.archived_at IS NULL
ORDER BY rp.reward_id, p.sort_order, p.id;

-- name: DeleteRewardPeople :exec
DELETE FROM reward_people WHERE household_id = ? AND reward_id = ?;

-- name: AddRewardPerson :exec
INSERT INTO reward_people (household_id, reward_id, person_id) VALUES (?, ?, ?);
