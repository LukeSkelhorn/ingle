-- name: GetPersonBank :one
SELECT * FROM person_bank WHERE household_id = ? AND person_id = ?;

-- name: UpsertPersonBank :exec
INSERT INTO person_bank (person_id, household_id, label, pence, source, updated_at, updated_by)
VALUES (sqlc.arg(person_id), sqlc.arg(household_id), sqlc.arg(label), sqlc.arg(pence), sqlc.arg(source), sqlc.arg(updated_at), sqlc.arg(updated_by))
ON CONFLICT (person_id) DO UPDATE SET
    label = excluded.label, pence = excluded.pence, source = excluded.source,
    updated_at = excluded.updated_at, updated_by = excluded.updated_by;

-- name: DeletePersonBank :execrows
DELETE FROM person_bank WHERE household_id = ? AND person_id = ?;

-- name: AddToPersonBank :exec
-- Money Ingle has just sent there. updated_at stays: nobody checked.
UPDATE person_bank SET pence = pence + sqlc.arg(pence)
WHERE household_id = sqlc.arg(household_id) AND person_id = sqlc.arg(person_id);

-- name: StalePersonBanks :many
-- Children whose balance nobody has checked since before_unix.
SELECT p.id AS person_id, p.name, b.updated_at
FROM person_bank b
JOIN people p ON p.id = b.person_id
WHERE b.household_id = sqlc.arg(household_id) AND b.updated_at < sqlc.arg(before_unix)
  AND p.archived_at IS NULL AND p.kind = 'child'
ORDER BY b.updated_at, p.sort_order, p.id;
