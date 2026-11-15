-- +goose Up
-- Adults earn a chore's adults' points and no money, whoever the chore is
-- for (before, only "each" chores had adults' points). Start the others at
-- their points so adults keep earning what they did.
UPDATE chores SET adult_points = points WHERE mode <> 'each';

-- +goose Down
UPDATE chores SET adult_points = 0 WHERE mode <> 'each';
