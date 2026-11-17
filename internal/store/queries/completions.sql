-- name: CreateCompletion :one
INSERT INTO completions (household_id, occurrence_id, person_id, for_person_id, completed_at, status, points, pence)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
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
SELECT c.id, c.occurrence_id, c.person_id, c.for_person_id, c.completed_at, c.status
FROM completions c
JOIN chore_occurrences o ON o.id = c.occurrence_id
WHERE c.household_id = sqlc.arg(household_id)
  AND o.due_on >= sqlc.arg(from_date)
  AND (o.due_on <= sqlc.arg(to_date) OR o.available_from <= sqlc.arg(to_date))
  AND c.status <> 'rejected';

-- name: DeleteCompletion :exec
DELETE FROM completions WHERE household_id = ? AND id = ?;

-- name: ReverseCompletion :execrows
UPDATE completions
SET status = 'rejected', reviewed_by = sqlc.arg(reviewed_by), reviewed_at = sqlc.arg(reviewed_at)
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id) AND status = 'approved';

-- name: ListCompletionDetails :many
SELECT
    c.id, c.occurrence_id, c.person_id, c.completed_at, c.status, c.reviewed_at, c.points, c.pence,
    p.name AS person_name, p.colour AS person_colour, p.avatar AS person_avatar,
    r.name AS reviewer_name, f.name AS for_name,
    ch.title, ch.emoji, o.due_on, o.due_time
FROM completions c
JOIN chore_occurrences o ON o.id = c.occurrence_id
JOIN chores ch ON ch.id = o.chore_id
JOIN people p ON p.id = c.person_id
LEFT JOIN people r ON r.id = c.reviewed_by
LEFT JOIN people f ON f.id = c.for_person_id
WHERE c.household_id = sqlc.arg(household_id)
  AND (c.status = 'pending' OR c.completed_at >= sqlc.arg(since_unix))
ORDER BY c.status = 'pending' DESC, c.completed_at DESC, c.id DESC;

-- name: GetCompletionDetail :one
SELECT
    c.id, c.occurrence_id, c.person_id, c.completed_at, c.status, c.reviewed_at, c.points, c.pence,
    p.name AS person_name, p.colour AS person_colour, p.avatar AS person_avatar,
    r.name AS reviewer_name, f.name AS for_name,
    ch.title, ch.emoji, o.due_on, o.due_time
FROM completions c
JOIN chore_occurrences o ON o.id = c.occurrence_id
JOIN chores ch ON ch.id = o.chore_id
JOIN people p ON p.id = c.person_id
LEFT JOIN people r ON r.id = c.reviewed_by
LEFT JOIN people f ON f.id = c.for_person_id
WHERE c.household_id = sqlc.arg(household_id) AND c.id = sqlc.arg(id);
