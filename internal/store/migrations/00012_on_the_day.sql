-- +goose Up
-- Only on the day: a chore that can't be done late (putting the bins out).
-- Once its time has passed it comes off the board and counts as missed.
ALTER TABLE chores
    ADD COLUMN on_the_day INTEGER NOT NULL DEFAULT 0 CHECK (on_the_day IN (0, 1));

-- +goose Down
ALTER TABLE chores DROP COLUMN on_the_day;
