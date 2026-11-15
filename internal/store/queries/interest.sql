-- name: HasInterestPayment :one
SELECT CAST(COUNT(*) AS INTEGER) FROM interest_payments
WHERE household_id = ? AND person_id = ? AND period = ?;

-- name: LastInterestPayment :one
-- A person's latest month, for the fraction carried into the next.
SELECT * FROM interest_payments
WHERE household_id = ? AND person_id = ?
ORDER BY period DESC
LIMIT 1;

-- name: InsertInterestPayment :exec
INSERT INTO interest_payments (
    household_id, person_id, period, points, pence, carry_points, carry_pence, ledger_entry_id, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: InterestTotalsForPerson :one
-- What a person has earned in interest, all told.
SELECT
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence
FROM ledger_entries
WHERE household_id = ? AND person_id = ? AND kind = 'interest';

-- name: UpdateInterestSettings :one
UPDATE household_settings
SET interest_rate_bp = ?, interest_money = ?, interest_points = ?,
    interest_cap_pence = ?, interest_cap_points = ?, interest_since = ?, updated_at = ?
WHERE household_id = ?
RETURNING *;

-- name: SetPersonInterestOptOut :exec
UPDATE people SET interest_opt_out = ? WHERE household_id = ? AND id = ?;
