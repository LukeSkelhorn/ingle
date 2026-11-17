-- name: GetMatchOffer :one
SELECT * FROM match_offers WHERE household_id = ? AND person_id = ?;

-- name: ListMatchOffers :many
SELECT m.*, CAST(p.name AS TEXT) AS set_by_name
FROM match_offers m
JOIN people p ON p.id = m.set_by
WHERE m.household_id = ?;

-- name: UpsertMatchOffer :exec
INSERT INTO match_offers (
    person_id, household_id, add_pence, per_pence, add_points, per_points, cap_pence, cap_points, set_by, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (person_id) DO UPDATE SET
    add_pence = excluded.add_pence, per_pence = excluded.per_pence,
    add_points = excluded.add_points, per_points = excluded.per_points,
    cap_pence = excluded.cap_pence, cap_points = excluded.cap_points,
    set_by = excluded.set_by, updated_at = excluded.updated_at;

-- name: DeleteMatchOffer :execrows
DELETE FROM match_offers WHERE household_id = ? AND person_id = ?;

-- name: InsertMatchEntry :exec
INSERT INTO match_entries (
    household_id, person_id, ledger_entry_id, completion_id, reward_id, goal_set_at, points, pence, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: MatchHeldForPerson :one
SELECT
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence
FROM match_entries
WHERE household_id = ? AND person_id = ?;

-- name: MatchForGoal :one
SELECT
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence
FROM match_entries
WHERE household_id = sqlc.arg(household_id) AND person_id = sqlc.arg(person_id)
  AND reward_id = sqlc.arg(reward_id) AND goal_set_at = sqlc.arg(goal_set_at);

-- name: ListGoalMatches :many
SELECT
    m.person_id,
    CAST(COALESCE(SUM(m.points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(m.pence), 0) AS INTEGER) AS pence
FROM match_entries m
JOIN people p ON p.id = m.person_id
WHERE m.household_id = ? AND p.archived_at IS NULL
  AND m.reward_id = p.goal_reward_id AND m.goal_set_at = p.goal_set_at
GROUP BY m.person_id;

-- name: MatchForCompletion :one
SELECT
    CAST(COALESCE(SUM(points), 0) AS INTEGER) AS points,
    CAST(COALESCE(SUM(pence), 0) AS INTEGER) AS pence,
    CAST(COALESCE(MAX(reward_id), 0) AS INTEGER) AS reward_id,
    CAST(COALESCE(MAX(goal_set_at), 0) AS INTEGER) AS goal_set_at
FROM match_entries
WHERE household_id = ? AND completion_id = ?;

-- name: DeleteMatchForCompletion :exec
DELETE FROM ledger_entries
WHERE ledger_entries.household_id = sqlc.arg(household_id) AND ledger_entries.kind = 'match'
  AND ledger_entries.id IN (
    SELECT me.ledger_entry_id FROM match_entries me
    WHERE me.household_id = sqlc.arg(household_id) AND me.completion_id = sqlc.arg(completion_id));

-- name: MoveGoalMatch :exec
UPDATE match_entries SET goal_set_at = sqlc.arg(new_set_at)
WHERE household_id = sqlc.arg(household_id) AND person_id = sqlc.arg(person_id)
  AND reward_id = sqlc.arg(reward_id) AND goal_set_at = sqlc.arg(old_set_at);
