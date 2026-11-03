-- name: InsertActivity :exec
INSERT INTO activity (household_id, actor_id, verb, subject_type, subject_id, data, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: ListRecentActivity :many
SELECT * FROM activity
WHERE household_id = ?
ORDER BY created_at DESC, id DESC
LIMIT ?;
