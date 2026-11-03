-- name: CreateChore :one
INSERT INTO chores (
    household_id, title, description, mode, assignee_id, rrule, starts_on,
    due_time, points, pence, requires_approval, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetChore :one
SELECT * FROM chores WHERE household_id = ? AND id = ?;

-- name: ListChores :many
SELECT * FROM chores
WHERE household_id = ? AND archived_at IS NULL
ORDER BY title;

-- name: UpdateChore :one
UPDATE chores SET
    title = ?, description = ?, mode = ?, assignee_id = ?, rrule = ?, starts_on = ?,
    due_time = ?, points = ?, pence = ?, requires_approval = ?, updated_at = ?
WHERE household_id = ? AND id = ?
RETURNING *;

-- name: ArchiveChore :exec
UPDATE chores SET archived_at = ?, updated_at = ? WHERE household_id = ? AND id = ?;
