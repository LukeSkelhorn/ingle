-- name: ListModules :many
SELECT module_id, enabled FROM household_modules WHERE household_id = ? ORDER BY module_id;

-- name: SetModule :exec
INSERT INTO household_modules (household_id, module_id, enabled, changed_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (household_id, module_id) DO UPDATE SET
    enabled = excluded.enabled,
    changed_at = CASE WHEN enabled = excluded.enabled THEN changed_at ELSE excluded.changed_at END;
