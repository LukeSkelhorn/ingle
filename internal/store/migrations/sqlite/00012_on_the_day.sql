-- +goose Up
ALTER TABLE chores
    ADD COLUMN on_the_day INTEGER NOT NULL DEFAULT 0 CHECK (on_the_day IN (0, 1));

-- +goose Down
ALTER TABLE chores DROP COLUMN on_the_day;
