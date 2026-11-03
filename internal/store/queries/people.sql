-- name: CreatePerson :one
INSERT INTO people (household_id, name, kind, colour, avatar, sort_order, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPerson :one
SELECT * FROM people WHERE household_id = ? AND id = ?;

-- name: ListPeople :many
SELECT * FROM people
WHERE household_id = ? AND archived_at IS NULL
ORDER BY sort_order, name;

-- name: UpdatePerson :one
UPDATE people SET name = ?, kind = ?, colour = ?, avatar = ?, sort_order = ?
WHERE household_id = ? AND id = ?
RETURNING *;

-- name: SetPersonPIN :exec
UPDATE people SET pin_hash = ? WHERE household_id = ? AND id = ?;

-- name: ArchivePerson :exec
UPDATE people SET archived_at = ? WHERE household_id = ? AND id = ?;
