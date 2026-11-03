-- name: InsertActivity :exec
INSERT INTO activity (household_id, actor_id, verb, subject_type, subject_id, data, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListRecentActivity :many
SELECT * FROM activity
WHERE household_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;

-- name: ListRecentActivityWithActor :many
SELECT activity.*, people.name AS actor_name
FROM activity
LEFT JOIN people ON people.id = activity.actor_id
WHERE activity.household_id = ?
ORDER BY activity.created_at DESC, activity.id DESC
LIMIT ?;
