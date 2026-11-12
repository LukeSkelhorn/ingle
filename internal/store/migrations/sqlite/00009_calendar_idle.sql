-- +goose Up
-- How long a display shows the board calendar, untouched, before going back
-- to the chores; 0 means it stays.
ALTER TABLE household_settings
    ADD COLUMN calendar_idle_minutes INTEGER NOT NULL DEFAULT 2 CHECK (calendar_idle_minutes BETWEEN 0 AND 60);

-- +goose Down
ALTER TABLE household_settings DROP COLUMN calendar_idle_minutes;
