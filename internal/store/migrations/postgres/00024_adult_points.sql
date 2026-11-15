-- +goose Up
-- Adults earn a chore's adults' points and no money, whoever the chore is
-- for (before, only "each" chores had adults' points). Start the others at
-- their points so adults keep earning what they did. Row-level security is
-- forced on chores, so lift it for the update or no household's rows match.
ALTER TABLE chores NO FORCE ROW LEVEL SECURITY;
UPDATE chores SET adult_points = points WHERE mode <> 'each';
ALTER TABLE chores FORCE ROW LEVEL SECURITY;

-- +goose Down
ALTER TABLE chores NO FORCE ROW LEVEL SECURITY;
UPDATE chores SET adult_points = 0 WHERE mode <> 'each';
ALTER TABLE chores FORCE ROW LEVEL SECURITY;
