-- +goose Up
ALTER TABLE completions ADD COLUMN for_person_id INTEGER REFERENCES people (id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE completions DROP COLUMN for_person_id;
