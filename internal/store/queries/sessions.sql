-- name: CreateSession :exec
INSERT INTO sessions (token_hash, household_id, person_id, kind, device_id, user_agent, created_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSessionByToken :one
SELECT sqlc.embed(sessions), sqlc.embed(people)
FROM sessions
JOIN people ON people.id = sessions.person_id
WHERE sessions.token_hash = ?;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = ? WHERE token_hash = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash = ?;

-- name: DeleteSessionForHousehold :execrows
DELETE FROM sessions WHERE household_id = ? AND token_hash = ?;

-- name: DeleteSessionsForPerson :exec
DELETE FROM sessions WHERE household_id = ? AND person_id = ?;

-- name: DeleteOtherSessionsForPerson :exec
DELETE FROM sessions
WHERE household_id = sqlc.arg(household_id) AND person_id = sqlc.arg(person_id)
  AND token_hash <> sqlc.arg(keep_token_hash);

-- name: DeleteSessionsForDevice :exec
DELETE FROM sessions WHERE device_id = ?;

-- name: ListPersonalSessions :many
SELECT sessions.token_hash, sessions.person_id, sessions.user_agent, sessions.created_at,
       sessions.last_seen_at, people.name AS person_name
FROM sessions
JOIN people ON people.id = sessions.person_id
WHERE sessions.household_id = ? AND sessions.kind = 'personal'
ORDER BY sessions.last_seen_at DESC;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions
WHERE (kind = 'personal' AND last_seen_at < sqlc.arg(personal_before))
   OR (kind = 'display' AND last_seen_at < sqlc.arg(display_before));
