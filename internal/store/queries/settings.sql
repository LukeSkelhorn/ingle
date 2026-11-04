-- name: GetSettings :one
SELECT * FROM household_settings WHERE household_id = ?;

-- name: InsertDefaultSettings :exec
INSERT INTO household_settings (household_id, timezone, updated_at)
VALUES (?, ?, ?)
ON CONFLICT (household_id) DO NOTHING;

-- name: UpdateSettings :one
UPDATE household_settings
SET timezone = ?, day_starts_at = ?, admin_idle_minutes = ?, screensaver_idle_minutes = ?, updated_at = ?
WHERE household_id = ?
RETURNING *;
