-- name: CreateCalendarFeed :one
INSERT INTO calendar_feeds (household_id, name, url, colour, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListCalendarFeeds :many
SELECT * FROM calendar_feeds WHERE household_id = ? ORDER BY name, id;

-- name: ListAllCalendarFeeds :many
SELECT * FROM calendar_feeds ORDER BY household_id, id;

-- name: GetCalendarFeed :one
SELECT * FROM calendar_feeds WHERE household_id = ? AND id = ?;

-- name: DeleteCalendarFeed :execrows
DELETE FROM calendar_feeds WHERE household_id = ? AND id = ?;
