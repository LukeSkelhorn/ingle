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
-- What a person has earned since since_unix, net: completions, reversals and
-- manual adjustments (bonuses and points taken away). Spending (redemptions,
-- payouts) isn't counted.
SELECT
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence
FROM ledger_entries
WHERE household_id = sqlc.arg(household_id) AND person_id = sqlc.arg(person_id)
  AND kind IN ('completion', 'adjustment', 'manual')
  AND created_at >= sqlc.arg(since_unix);

-- name: ListManualAdjustments :many
-- A person's manual adjustments (bonuses and points taken away) since
-- since_unix, newest first, with who made each one.
SELECT l.id, l.points, l.note, l.created_at, COALESCE(p.name, '') AS by_name
FROM ledger_entries l
LEFT JOIN people p ON p.id = l.created_by
WHERE l.household_id = sqlc.arg(household_id) AND l.person_id = sqlc.arg(person_id)
  AND l.kind = 'manual' AND l.created_at >= sqlc.arg(since_unix)
ORDER BY l.created_at DESC, l.id DESC
LIMIT sqlc.arg(lim);

-- name: RecentManualNotes :many
-- The last few distinct reasons used, to suggest on the form.
SELECT note FROM ledger_entries
WHERE household_id = ? AND kind = 'manual'
GROUP BY note
ORDER BY MAX(created_at) DESC
LIMIT 10;

-- name: DeleteManualEntry :execrows
-- Undoes a manual adjustment made since since_unix.
DELETE FROM ledger_entries
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id)
  AND kind = 'manual' AND created_at >= sqlc.arg(since_unix);

-- name: ListLedgerHistory :many
-- A person's ledger, newest first, for My money: entries since since_unix
-- (0 for any) and older than before_id (math.MaxInt64 for any), with the
-- chore or reward each one was for, and the chore's time ('' for none).
SELECT
    l.id, l.kind, l.points, l.pence, l.note, l.created_at,
    CAST(COALESCE(ch.title, rw.title, '') AS TEXT) AS title,
    CAST(COALESCE(ch.emoji, rw.emoji, '') AS TEXT) AS emoji,
    CAST(COALESCE(o.due_time, '') AS TEXT) AS due_time
FROM ledger_entries l
LEFT JOIN completions c ON c.id = l.completion_id
LEFT JOIN chore_occurrences o ON o.id = c.occurrence_id
LEFT JOIN chores ch ON ch.id = o.chore_id
LEFT JOIN redemptions d ON d.id = l.redemption_id
LEFT JOIN rewards rw ON rw.id = d.reward_id
WHERE l.household_id = sqlc.arg(household_id) AND l.person_id = sqlc.arg(person_id)
  AND l.created_at >= sqlc.arg(since_unix)
  AND l.id < sqlc.arg(before_id)
ORDER BY l.id DESC
LIMIT sqlc.arg(lim);
