-- name: InsertLedgerEntry :one
INSERT INTO ledger_entries (
    household_id, person_id, points, pence, kind, completion_id, redemption_id, note, created_by, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: BalanceForPerson :one
SELECT
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence
FROM ledger_entries
WHERE household_id = ? AND person_id = ?;

-- name: BalancesForHousehold :many
SELECT
    p.id AS person_id,
    CAST(COALESCE(SUM(l.points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(l.pence), 0) AS INTEGER) AS pence
FROM people p
LEFT JOIN ledger_entries l ON l.person_id = p.id
WHERE p.household_id = ? AND p.archived_at IS NULL
GROUP BY p.id
ORDER BY p.sort_order, p.name;

-- name: DeleteLedgerForCompletion :exec
DELETE FROM ledger_entries WHERE household_id = ? AND completion_id = ?;

-- name: EarnedSinceForPerson :one
-- What a person has earned since since_unix: completions, less any
-- adjustments (reversals). Spending (redemptions, payouts) isn't counted.
SELECT
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence
FROM ledger_entries
WHERE household_id = sqlc.arg(household_id) AND person_id = sqlc.arg(person_id)
  AND kind IN ('completion', 'adjustment')
  AND created_at >= sqlc.arg(since_unix);
