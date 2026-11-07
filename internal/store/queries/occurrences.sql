-- name: InsertOccurrence :execrows
INSERT INTO chore_occurrences (household_id, chore_id, due_on, due_time, assignee_id, available_from, for_person)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (chore_id, due_on, due_time, for_person) DO NOTHING;

-- name: GetOccurrence :one
SELECT * FROM chore_occurrences WHERE household_id = ? AND id = ?;

-- name: ListBoard :many
-- Occurrences on the board at some point in [from_date, to_date]: due in the
-- range, or (flexible-day chores) with their window overlapping it.
SELECT
    o.id, o.chore_id, o.due_on, o.available_from, o.assignee_id, o.claimed_by, o.claimed_at, o.status,
    o.due_time, o.for_person, c.show_before,
    c.title, c.emoji, c.description, c.mode, c.points, c.pence, c.adult_points, c.requires_approval,
    c.rrule, c.starts_on, c.day_mode, c.on_the_day
FROM chore_occurrences o
JOIN chores c ON c.id = o.chore_id
WHERE o.household_id = sqlc.arg(household_id)
  AND o.due_on >= sqlc.arg(from_date)
  AND (o.due_on <= sqlc.arg(to_date) OR o.available_from <= sqlc.arg(to_date))
  AND c.archived_at IS NULL
ORDER BY o.due_on, o.due_time = '', o.due_time, c.title;

-- name: ClaimOccurrence :execrows
UPDATE chore_occurrences
SET claimed_by = sqlc.arg(person_id), claimed_at = sqlc.arg(claimed_at), status = 'claimed'
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id)
  AND status = 'open' AND claimed_by IS NULL AND assignee_id IS NULL;

-- name: ReleaseOccurrence :execrows
UPDATE chore_occurrences
SET claimed_by = NULL, claimed_at = NULL, status = 'open'
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id)
  AND status = 'claimed' AND claimed_by = sqlc.arg(person_id);

-- name: SetOccurrenceStatus :exec
UPDATE chore_occurrences SET status = ? WHERE household_id = ? AND id = ?;

-- name: ReopenOccurrence :exec
UPDATE chore_occurrences
SET status = CASE WHEN claimed_by IS NULL THEN 'open' ELSE 'claimed' END
WHERE household_id = ? AND id = ?;

-- name: DeleteFutureOpenOccurrences :execrows
-- Drops not-yet-started occurrences of a chore from from_date onwards so an
-- edited schedule or assignee can be re-materialised. Claimed, done and
-- previously-completed occurrences are kept.
DELETE FROM chore_occurrences
WHERE chore_occurrences.household_id = sqlc.arg(household_id)
  AND chore_occurrences.chore_id = sqlc.arg(chore_id)
  AND chore_occurrences.due_on >= sqlc.arg(from_date)
  AND chore_occurrences.status = 'open' AND chore_occurrences.claimed_by IS NULL
  AND NOT EXISTS (SELECT 1 FROM completions c WHERE c.occurrence_id = chore_occurrences.id);

-- name: AssignOpenOccurrences :exec
-- Gives a chore's not-yet-started occurrences to person_id (a shared chore
-- that's down to one person becomes theirs).
UPDATE chore_occurrences
SET assignee_id = sqlc.arg(person_id)
WHERE chore_occurrences.household_id = sqlc.arg(household_id)
  AND chore_occurrences.chore_id = sqlc.arg(chore_id)
  AND chore_occurrences.status = 'open' AND chore_occurrences.claimed_by IS NULL
  AND NOT EXISTS (SELECT 1 FROM completions c WHERE c.occurrence_id = chore_occurrences.id);

-- name: DismissOccurrence :execrows
-- Takes a missed Only on the day occurrence off Admin > Completions. It
-- stays missed (never done); claimed_by is kept so a restore puts it back
-- as it was.
UPDATE chore_occurrences SET status = 'skipped'
WHERE household_id = ? AND id = ? AND status IN ('open', 'claimed');

-- name: RestoreOccurrence :execrows
-- Undoes DismissOccurrence.
UPDATE chore_occurrences
SET status = CASE WHEN claimed_by IS NULL THEN 'open' ELSE 'claimed' END
WHERE household_id = ? AND id = ? AND status = 'skipped';

-- name: DeleteOpenEachOccurrencesForPerson :exec
-- Drops a person's not-yet-started copies of 'each' chores (they've left
-- the household, so they stop getting them).
DELETE FROM chore_occurrences
WHERE chore_occurrences.household_id = sqlc.arg(household_id)
  AND chore_occurrences.for_person = sqlc.arg(person_id)
  AND chore_occurrences.status = 'open'
  AND NOT EXISTS (SELECT 1 FROM completions c WHERE c.occurrence_id = chore_occurrences.id);
