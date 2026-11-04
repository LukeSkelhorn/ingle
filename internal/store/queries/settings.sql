-- name: GetSettings :one
SELECT * FROM household_settings WHERE household_id = ?;

-- name: InsertDefaultSettings :exec
INSERT INTO household_settings (household_id, timezone, updated_at)
VALUES (?, ?, ?)
ON CONFLICT (household_id) DO NOTHING;

-- name: UpdateSettings :one
UPDATE household_settings
SET timezone = ?, day_starts_at = ?, admin_idle_minutes = ?, screensaver_idle_minutes = ?,
    lock_days = ?, lock_from = ?, lock_until = ?, lock_idle_hours = ?, updated_at = ?
WHERE household_id = ?
RETURNING *;

-- name: SetUnlockCode :exec
UPDATE household_settings SET unlock_code_hash = ?, updated_at = ? WHERE household_id = ?;
