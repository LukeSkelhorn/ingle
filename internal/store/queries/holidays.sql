-- name: ListHolidays :many
-- Holidays that haven't ended before from_date, soonest first (the whole
-- household's before one person's on the same day).
SELECT * FROM holidays
WHERE household_id = sqlc.arg(household_id) AND ends_on >= sqlc.arg(from_date)
ORDER BY starts_on, person_id IS NOT NULL, id;

-- name: GetHoliday :one
SELECT * FROM holidays WHERE household_id = ? AND id = ?;

-- name: InsertHoliday :one
INSERT INTO holidays (household_id, person_id, starts_on, ends_on, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING id;

-- name: SetHolidayEnd :exec
UPDATE holidays SET ends_on = ? WHERE household_id = ? AND id = ?;

-- name: DeleteHoliday :exec
DELETE FROM holidays WHERE household_id = ? AND id = ?;

-- name: DeleteOpenOccurrence :exec
-- Drops a not-yet-started occurrence that a holiday has paused.
DELETE FROM chore_occurrences
WHERE chore_occurrences.household_id = sqlc.arg(household_id)
  AND chore_occurrences.id = sqlc.arg(id)
  AND chore_occurrences.status = 'open' AND chore_occurrences.claimed_by IS NULL
  AND NOT EXISTS (SELECT 1 FROM completions c WHERE c.occurrence_id = chore_occurrences.id);
