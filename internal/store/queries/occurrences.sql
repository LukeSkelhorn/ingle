-- name: InsertOccurrence :execrows
INSERT INTO chore_occurrences (household_id, chore_id, due_on, assignee_id)
VALUES (?, ?, ?, ?)
ON CONFLICT (chore_id, due_on) DO NOTHING;

-- name: GetOccurrence :one
SELECT * FROM chore_occurrences WHERE household_id = ? AND id = ?;

-- name: ListBoard :many
SELECT
    o.id, o.chore_id, o.due_on, o.assignee_id, o.claimed_by, o.claimed_at, o.status,
    c.title, c.description, c.mode, c.due_time, c.points, c.pence, c.requires_approval
FROM chore_occurrences o
JOIN chores c ON c.id = o.chore_id
WHERE o.household_id = sqlc.arg(household_id)
  AND o.due_on >= sqlc.arg(from_date) AND o.due_on <= sqlc.arg(to_date)
  AND c.archived_at IS NULL
ORDER BY o.due_on, c.due_time, c.title;

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
