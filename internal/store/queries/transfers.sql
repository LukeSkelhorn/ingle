-- name: CreateTransferRequest :one
INSERT INTO transfer_requests (
    household_id, person_id, pence, status, requested_by, requested_at, reviewed_by, reviewed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetTransferRequest :one
SELECT * FROM transfer_requests WHERE household_id = ? AND id = ?;

-- name: SettleTransferRequest :execrows
-- Settle a waiting request. Only a pending one changes, so two adults
-- tapping at once can't both act on it.
UPDATE transfer_requests
SET status = sqlc.arg(status), reason = sqlc.arg(reason),
    reviewed_by = sqlc.arg(reviewed_by), reviewed_at = sqlc.arg(reviewed_at)
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id) AND status = 'pending';

-- name: SetTransferLedgerEntry :exec
UPDATE transfer_requests SET ledger_entry_id = sqlc.arg(ledger_entry_id)
WHERE household_id = sqlc.arg(household_id) AND id = sqlc.arg(id);

-- name: PendingTransfersForPerson :many
SELECT * FROM transfer_requests
WHERE household_id = ? AND person_id = ? AND status = 'pending'
ORDER BY requested_at DESC, id DESC;

-- name: ListTransferDetails :many
-- Requests with who they're for and who decided: every waiting one, and the
-- rest requested since since_unix.
SELECT
    t.id, t.person_id, t.pence, t.status, t.reason, t.requested_at, t.reviewed_at,
    p.name AS person_name, p.colour AS person_colour, p.avatar AS person_avatar,
    CAST(COALESCE(rq.name, '') AS TEXT) AS requested_by_name,
    CAST(COALESCE(rv.name, '') AS TEXT) AS reviewed_by_name
FROM transfer_requests t
JOIN people p ON p.id = t.person_id
LEFT JOIN people rq ON rq.id = t.requested_by
LEFT JOIN people rv ON rv.id = t.reviewed_by
WHERE t.household_id = sqlc.arg(household_id)
  AND (t.status = 'pending' OR t.requested_at >= sqlc.arg(since_unix))
ORDER BY t.requested_at DESC, t.id DESC;

-- name: CountPendingTransfers :one
SELECT CAST(COUNT(*) AS INTEGER) AS n FROM transfer_requests t
JOIN people p ON p.id = t.person_id
WHERE t.household_id = ? AND t.status = 'pending' AND p.archived_at IS NULL;

-- name: LastSettledTransferForPerson :many
-- A person's latest request decided since since_unix: if it was declined,
-- My money says why.
SELECT * FROM transfer_requests
WHERE household_id = sqlc.arg(household_id) AND person_id = sqlc.arg(person_id)
  AND status <> 'pending' AND reviewed_at >= CAST(sqlc.arg(since_unix) AS INTEGER)
ORDER BY reviewed_at DESC, id DESC
LIMIT 1;
