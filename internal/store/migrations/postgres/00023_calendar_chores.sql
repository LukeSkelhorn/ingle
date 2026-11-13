-- +goose Up
-- Whether the board calendar shows chores as well as events by default;
-- anyone at the board can still turn them on or off for a while.
ALTER TABLE household_settings
    ADD COLUMN calendar_chores bigint NOT NULL DEFAULT 0 CHECK (calendar_chores IN (0, 1));

-- +goose Down
ALTER TABLE household_settings DROP COLUMN calendar_chores;
