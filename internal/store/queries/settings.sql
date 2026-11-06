-- name: GetSettings :one
SELECT * FROM household_settings WHERE household_id = ?;

-- name: InsertDefaultSettings :exec
INSERT INTO household_settings (household_id, timezone, updated_at)
VALUES (?, ?, ?)
ON CONFLICT (household_id) DO NOTHING;

-- name: UpdateSettings :one
UPDATE household_settings
SET timezone = ?, day_starts_at = ?, admin_idle_minutes = ?, screensaver_idle_minutes = ?,
    calendar_idle_minutes = ?,
    lock_days = ?, lock_from = ?, lock_until = ?, lock_idle_hours = ?, updated_at = ?
WHERE household_id = ?
RETURNING *;

-- name: SetUnlockCode :exec
UPDATE household_settings SET unlock_code_hash = ?, unlock_code_length = ?, updated_at = ? WHERE household_id = ?;

-- name: LearnUnlockCodeLength :exec
-- Records an older household code's length the first time it unlocks.
UPDATE household_settings SET unlock_code_length = ?
WHERE household_id = ? AND unlock_code_hash IS NOT NULL AND unlock_code_length IS NULL;
