-- +goose Up
-- RLS is forced on chores, so lift it or no household's rows match.
ALTER TABLE chores NO FORCE ROW LEVEL SECURITY;
UPDATE chores SET adult_points = points WHERE mode <> 'each';
ALTER TABLE chores FORCE ROW LEVEL SECURITY;

-- +goose Down
ALTER TABLE chores NO FORCE ROW LEVEL SECURITY;
UPDATE chores SET adult_points = 0 WHERE mode <> 'each';
ALTER TABLE chores FORCE ROW LEVEL SECURITY;
