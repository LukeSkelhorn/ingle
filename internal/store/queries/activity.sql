-- name: InsertActivity :exec
INSERT INTO activity (household_id, actor_id, verb, subject_type, subject_id, data, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListRecentActivity :many
SELECT * FROM activity
WHERE household_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListRecentActivityWithActor :many
-- subject_title names the reward for reward claims ("claimed Lego set").
SELECT activity.*, people.name AS actor_name, rewards.title AS subject_title
FROM activity
LEFT JOIN people ON people.id = activity.actor_id
LEFT JOIN redemptions ON activity.subject_type = 'redemption' AND redemptions.id = activity.subject_id
LEFT JOIN rewards ON rewards.id = redemptions.reward_id
WHERE activity.household_id = ?
ORDER BY activity.created_at DESC, activity.id DESC
LIMIT ?;
