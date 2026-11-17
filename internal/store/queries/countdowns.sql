-- name: ListCountdowns :many
SELECT * FROM countdowns WHERE household_id = ? ORDER BY on_date, id;

-- name: GetCountdown :one
SELECT * FROM countdowns WHERE household_id = ? AND id = ?;

-- name: InsertCountdown :one
INSERT INTO countdowns (household_id, title, emoji, on_date, yearly, added_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateCountdown :exec
UPDATE countdowns SET title = ?, emoji = ?, on_date = ?, yearly = ?
WHERE household_id = ? AND id = ?;

-- name: DeleteCountdown :exec
DELETE FROM countdowns WHERE household_id = ? AND id = ?;

-- name: ListCountdownPeople :many
SELECT cp.countdown_id, cp.person_id
FROM countdown_people cp
JOIN people p ON p.id = cp.person_id
WHERE cp.household_id = ? AND p.archived_at IS NULL
ORDER BY cp.countdown_id, p.sort_order, p.id;

-- name: ListPeopleForCountdown :many
SELECT cp.person_id
FROM countdown_people cp
JOIN people p ON p.id = cp.person_id
WHERE cp.household_id = ? AND cp.countdown_id = ? AND p.archived_at IS NULL
ORDER BY p.sort_order, p.id;

-- name: DeleteCountdownPeople :exec
DELETE FROM countdown_people WHERE household_id = ? AND countdown_id = ?;

-- name: AddCountdownPerson :exec
INSERT INTO countdown_people (household_id, countdown_id, person_id) VALUES (?, ?, ?);

-- name: SetPersonBirthday :exec
UPDATE people SET birthday = ?, birth_year = ? WHERE household_id = ? AND id = ?;

-- name: SetPersonCanAddCountdowns :exec
UPDATE people SET can_add_countdowns = ? WHERE household_id = ? AND id = ?;
