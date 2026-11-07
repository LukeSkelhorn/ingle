-- name: InsertActivity :exec
INSERT INTO activity (household_id, actor_id, verb, subject_type, subject_id, data, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListRecentActivity :many
SELECT * FROM activity
WHERE household_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListRecentActivityWithActor :many
-- subject_title names the reward for reward claims ("claimed Lego set");
-- for bank transfers, subject_pence is the amount and subject_person who
-- it was for.
SELECT activity.*, people.name AS actor_name, rewards.title AS subject_title,
    transfer_requests.pence AS subject_pence, tp.name AS subject_person
FROM activity
LEFT JOIN people ON people.id = activity.actor_id
LEFT JOIN redemptions ON activity.subject_type = 'redemption' AND redemptions.id = activity.subject_id
LEFT JOIN rewards ON rewards.id = redemptions.reward_id
LEFT JOIN transfer_requests ON activity.subject_type = 'transfer' AND transfer_requests.id = activity.subject_id
LEFT JOIN people tp ON tp.id = transfer_requests.person_id
WHERE activity.household_id = ?
ORDER BY activity.created_at DESC, activity.id DESC
LIMIT ?;
