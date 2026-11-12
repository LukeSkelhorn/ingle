-- name: GetHouseholdKey :one
SELECT wrapped_key FROM household_keys WHERE household_id = ?;

-- name: CreateHouseholdKey :exec
-- Two requests may make a new household's key at once; the first wins and
-- both then read it back.
INSERT INTO household_keys (household_id, wrapped_key, created_at)
VALUES (?, ?, ?)
ON CONFLICT (household_id) DO NOTHING;
