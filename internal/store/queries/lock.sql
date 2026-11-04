-- name: GetLock :one
SELECT * FROM household_lock WHERE household_id = ?;

-- name: SetLock :exec
INSERT INTO household_lock (household_id, locked, reason, locked_by, changed_at, schedule_applied)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (household_id) DO UPDATE SET
    locked = excluded.locked,
    reason = excluded.reason,
    locked_by = excluded.locked_by,
    changed_at = excluded.changed_at,
    schedule_applied = excluded.schedule_applied;

-- name: ListUnlockPINs :many
SELECT id, pin_hash FROM people
WHERE household_id = ? AND kind = 'adult' AND archived_at IS NULL AND pin_hash IS NOT NULL
ORDER BY id;
