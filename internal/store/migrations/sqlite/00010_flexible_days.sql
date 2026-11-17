-- +goose Up
ALTER TABLE chores
    ADD COLUMN day_mode TEXT NOT NULL DEFAULT 'each' CHECK (day_mode IN ('each', 'any'));

ALTER TABLE chore_occurrences
    ADD COLUMN available_from TEXT CHECK (
        available_from IS NULL OR (date(available_from) IS available_from AND available_from <= due_on)
    );

-- +goose Down
ALTER TABLE chore_occurrences DROP COLUMN available_from;
ALTER TABLE chores DROP COLUMN day_mode;
