-- name: CreateChore :one
INSERT INTO chores (
    household_id, title, emoji, description, mode, assignee_id, rrule, starts_on,
    due_times, show_before, points, pence, requires_approval, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetChore :one
SELECT * FROM chores WHERE household_id = ? AND id = ?;

-- name: ListChores :many
SELECT * FROM chores
WHERE household_id = ? AND archived_at IS NULL
ORDER BY title;

-- name: UpdateChore :one
UPDATE chores SET
    title = ?, emoji = ?, description = ?, mode = ?, assignee_id = ?, rrule = ?, starts_on = ?,
    due_times = ?, show_before = ?, points = ?, pence = ?, requires_approval = ?, updated_at = ?
WHERE household_id = ? AND id = ?
RETURNING *;

-- name: SetChoreDayMode :one
UPDATE chores SET day_mode = ? WHERE household_id = ? AND id = ?
RETURNING *;

-- name: SetChoreOnTheDay :one
UPDATE chores SET on_the_day = ? WHERE household_id = ? AND id = ?
RETURNING *;

-- name: ArchiveChore :exec
UPDATE chores SET archived_at = ?, updated_at = ? WHERE household_id = ? AND id = ?;

-- name: ListChorePeople :many
-- Who each active shared chore is for: the chosen people still active, in
-- board order.
SELECT cp.chore_id, cp.person_id
FROM chore_people cp
JOIN chores c ON c.id = cp.chore_id
JOIN people p ON p.id = cp.person_id
WHERE cp.household_id = ? AND c.archived_at IS NULL AND p.archived_at IS NULL
ORDER BY cp.chore_id, p.sort_order, p.id;

-- name: ListPeopleForChore :many
SELECT cp.person_id
FROM chore_people cp
JOIN people p ON p.id = cp.person_id
WHERE cp.household_id = ? AND cp.chore_id = ? AND p.archived_at IS NULL
ORDER BY p.sort_order, p.id;

-- name: DeleteChorePeople :exec
DELETE FROM chore_people WHERE household_id = ? AND chore_id = ?;

-- name: AddChorePerson :exec
INSERT INTO chore_people (household_id, chore_id, person_id) VALUES (?, ?, ?);

-- name: RemovePersonFromSharedChores :exec
DELETE FROM chore_people WHERE household_id = ? AND person_id = ?;

-- name: ListSharedChoresWithOnePerson :many
-- Active shared chores left with a single chosen person, and who that is.
SELECT c.id AS chore_id, cp.person_id
FROM chores c
JOIN chore_people cp ON cp.chore_id = c.id
WHERE c.household_id = ? AND c.mode = 'shared' AND c.archived_at IS NULL
  AND (SELECT COUNT(*) FROM chore_people x WHERE x.chore_id = c.id) = 1;

-- name: MakeChoreAssigned :exec
UPDATE chores SET mode = 'assigned', assignee_id = sqlc.arg(person_id), updated_at = sqlc.arg(updated_at)
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id);
