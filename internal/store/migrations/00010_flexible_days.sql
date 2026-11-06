-- +goose Up
-- Flexible-day chores: 'each' is due on every day of its schedule; 'any' is
-- due once a week (Mon–Sun) on whichever of those days suits.
ALTER TABLE chores
    ADD COLUMN day_mode TEXT NOT NULL DEFAULT 'each' CHECK (day_mode IN ('each', 'any'));

-- For an 'any' chore, one occurrence covers the week's window: it is on the
-- board from available_from (the first allowed day) to due_on (the last).
-- NULL for everything else.
ALTER TABLE chore_occurrences
    ADD COLUMN available_from TEXT CHECK (
        available_from IS NULL OR (date(available_from) IS available_from AND available_from <= due_on)
    );

-- +goose Down
ALTER TABLE chore_occurrences DROP COLUMN available_from;
ALTER TABLE chores DROP COLUMN day_mode;
