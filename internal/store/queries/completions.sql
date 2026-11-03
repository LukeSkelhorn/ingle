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

-- name: ListLiveCompletionsForRange :many
-- The live (pending or approved) completion for each occurrence due in
-- [from_date, to_date], for the board.
SELECT c.id, c.occurrence_id, c.person_id, c.completed_at, c.status
FROM completions c
JOIN chore_occurrences o ON o.id = c.occurrence_id
WHERE c.household_id = sqlc.arg(household_id)
  AND o.due_on >= sqlc.arg(from_date) AND o.due_on <= sqlc.arg(to_date)
  AND c.status <> 'rejected';

-- name: DeleteCompletion :exec
DELETE FROM completions WHERE household_id = ? AND id = ?;
