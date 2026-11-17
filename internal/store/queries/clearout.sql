-- name: DeleteOldTransferRequests :execrows
DELETE FROM transfer_requests
WHERE household_id = sqlc.arg(household_id) AND status <> 'pending'
  AND COALESCE(reviewed_at, requested_at) < sqlc.arg(cutoff_unix);

-- name: ListLedgerToFold :many
SELECT
    person_id,
    CAST(MIN(id) AS INTEGER) AS keep_id,
    CAST(COUNT(*) AS INTEGER) AS lines,
    CAST(SUM(CASE WHEN kind = 'brought_forward' THEN 1 ELSE 0 END) AS INTEGER) AS folded,
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence
FROM ledger_entries l
WHERE l.household_id = sqlc.arg(household_id) AND l.created_at < sqlc.arg(cutoff_unix)
  AND l.kind NOT IN ('interest', 'match')
  AND NOT EXISTS (SELECT 1 FROM transfer_requests t WHERE t.ledger_entry_id = l.id)
GROUP BY person_id
ORDER BY person_id;

-- name: SetBroughtForward :exec
UPDATE ledger_entries
SET kind = 'brought_forward', points = sqlc.arg(points), pence = sqlc.arg(pence),
    completion_id = NULL, redemption_id = NULL, note = '', created_by = NULL,
    created_at = sqlc.arg(created_at)
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id);

-- name: DeleteFoldedLedger :execrows
DELETE FROM ledger_entries
WHERE ledger_entries.household_id = sqlc.arg(household_id) AND ledger_entries.person_id = sqlc.arg(person_id)
  AND ledger_entries.created_at < sqlc.arg(cutoff_unix) AND ledger_entries.id <> sqlc.arg(keep_id)
  AND ledger_entries.kind NOT IN ('interest', 'match')
  AND NOT EXISTS (SELECT 1 FROM transfer_requests t WHERE t.ledger_entry_id = ledger_entries.id);

-- name: DeleteOldRedemptions :execrows
DELETE FROM redemptions
WHERE redemptions.household_id = sqlc.arg(household_id) AND redemptions.status <> 'pending'
  AND COALESCE(redemptions.reviewed_at, redemptions.requested_at) < sqlc.arg(cutoff_unix)
  AND NOT EXISTS (SELECT 1 FROM ledger_entries l WHERE l.redemption_id = redemptions.id);

-- name: DeleteOldCompletions :execrows
DELETE FROM completions
WHERE completions.household_id = sqlc.arg(household_id) AND completions.status <> 'pending'
  AND completions.completed_at < sqlc.arg(cutoff_unix)
  AND NOT EXISTS (SELECT 1 FROM ledger_entries l WHERE l.completion_id = completions.id);

-- name: DeleteOldOccurrences :execrows
DELETE FROM chore_occurrences
WHERE chore_occurrences.household_id = sqlc.arg(household_id) AND chore_occurrences.due_on < sqlc.arg(cutoff_day)
  AND NOT EXISTS (SELECT 1 FROM completions c WHERE c.occurrence_id = chore_occurrences.id);

-- name: DeleteOldActivity :execrows
DELETE FROM activity
WHERE household_id = sqlc.arg(household_id) AND created_at < sqlc.arg(cutoff_unix);
