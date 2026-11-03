-- name: CreateCompletion :one
INSERT INTO completions (household_id, occurrence_id, person_id, completed_at, status, points, pence)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetCompletion :one
SELECT * FROM completions WHERE household_id = ? AND id = ?;

-- name: ReviewCompletion :execrows
UPDATE completions
SET status = sqlc.arg(status), reviewed_by = sqlc.arg(reviewed_by), reviewed_at = sqlc.arg(reviewed_at)
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id) AND status = 'pending';

-- name: ListPendingCompletions :many
SELECT * FROM completions
WHERE household_id = ? AND status = 'pending'
ORDER BY completed_at;
