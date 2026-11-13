-- name: CreateCalendarFeed :one
INSERT INTO calendar_feeds (household_id, name, url, colour, person_id, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListCalendarFeeds :many
SELECT * FROM calendar_feeds WHERE household_id = ? ORDER BY name, id;

-- name: GetCalendarFeed :one
SELECT * FROM calendar_feeds WHERE household_id = ? AND id = ?;

-- name: DeleteCalendarFeed :execrows
DELETE FROM calendar_feeds WHERE household_id = ? AND id = ?;

-- name: SetCalendarFeedURL :exec
UPDATE calendar_feeds SET url = ? WHERE household_id = ? AND id = ?;

-- name: UpdateCalendarFeed :execrows
UPDATE calendar_feeds SET name = ?, person_id = ? WHERE household_id = ? AND id = ?;

-- name: ClearCalendarFeedPerson :exec
-- Turns an archived person's calendars into the family's.
UPDATE calendar_feeds SET person_id = NULL WHERE household_id = ? AND person_id = ?;
