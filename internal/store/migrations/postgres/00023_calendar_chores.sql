-- +goose Up
ALTER TABLE household_settings
    ADD COLUMN calendar_chores bigint NOT NULL DEFAULT 0 CHECK (calendar_chores IN (0, 1));

-- +goose Down
ALTER TABLE household_settings DROP COLUMN calendar_chores;
