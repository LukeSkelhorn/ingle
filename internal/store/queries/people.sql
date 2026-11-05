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
UPDATE people SET pin_hash = ?, pin_length = ? WHERE household_id = ? AND id = ?;

-- name: LearnPINLength :exec
-- Records an older PIN's length the first time it unlocks.
UPDATE people SET pin_length = ? WHERE household_id = ? AND id = ? AND pin_length IS NULL;

-- name: ArchivePerson :exec
UPDATE people SET archived_at = ? WHERE household_id = ? AND id = ?;

-- name: GetPersonByEmail :one
SELECT * FROM people WHERE email = ? AND archived_at IS NULL;

-- name: SetPersonLogin :exec
UPDATE people SET email = ?, password_hash = ? WHERE household_id = ? AND id = ?;

-- name: SetPersonEmail :exec
UPDATE people SET email = ? WHERE household_id = ? AND id = ?;

-- name: ClearPersonPIN :exec
UPDATE people SET pin_hash = NULL, pin_length = NULL WHERE household_id = ? AND id = ?;

-- name: ClearPersonLogin :exec
UPDATE people SET email = NULL, password_hash = NULL WHERE household_id = ? AND id = ?;

-- name: ListArchivedPeople :many
SELECT * FROM people
WHERE household_id = ? AND archived_at IS NOT NULL
ORDER BY name;

-- name: UnarchivePerson :exec
UPDATE people SET archived_at = NULL WHERE household_id = ? AND id = ?;

-- name: CountAdultsWithCredential :one
SELECT COUNT(*) FROM people
WHERE household_id = ? AND kind = 'adult' AND archived_at IS NULL
  AND (pin_hash IS NOT NULL OR password_hash IS NOT NULL);

-- name: CountActiveAssignedChores :one
SELECT COUNT(*) FROM chores
WHERE household_id = ? AND assignee_id = ? AND archived_at IS NULL;
