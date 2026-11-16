-- name: ListCountdowns :many
SELECT * FROM countdowns WHERE household_id = ? ORDER BY on_date, id;

-- name: GetCountdown :one
SELECT * FROM countdowns WHERE household_id = ? AND id = ?;

-- name: InsertCountdown :one
INSERT INTO countdowns (household_id, title, emoji, on_date, yearly, person_id, added_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateCountdown :exec
UPDATE countdowns SET title = ?, emoji = ?, on_date = ?, yearly = ?, person_id = ?
WHERE household_id = ? AND id = ?;

-- name: DeleteCountdown :exec
DELETE FROM countdowns WHERE household_id = ? AND id = ?;

-- name: SetPersonBirthday :exec
UPDATE people SET birthday = ?, birth_year = ? WHERE household_id = ? AND id = ?;

-- name: SetPersonCanAddCountdowns :exec
UPDATE people SET can_add_countdowns = ? WHERE household_id = ? AND id = ?;
