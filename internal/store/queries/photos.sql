-- name: CreatePhoto :one
INSERT INTO photos (household_id, file, width, height, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetPhoto :one
SELECT * FROM photos WHERE household_id = ? AND id = ?;

-- name: GetPhotoByFile :one
SELECT * FROM photos WHERE household_id = ? AND file = ?;

-- name: ListPhotos :many
SELECT * FROM photos WHERE household_id = ? ORDER BY created_at DESC, id DESC;

-- name: DeletePhoto :execrows
DELETE FROM photos WHERE household_id = ? AND id = ?;
