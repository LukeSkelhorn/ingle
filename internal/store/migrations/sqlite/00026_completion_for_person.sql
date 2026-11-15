-- +goose Up
-- An adult can do a child's chore and earn it themselves. The completion's
-- person_id is whoever did it (and got the points); for_person_id is the
-- child whose chore it was, or NULL when they did their own.
ALTER TABLE completions ADD COLUMN for_person_id INTEGER REFERENCES people (id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE completions DROP COLUMN for_person_id;
