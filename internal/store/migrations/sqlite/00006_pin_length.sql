-- +goose Up
ALTER TABLE people
    ADD COLUMN pin_length INTEGER CHECK (pin_length IS NULL OR pin_length BETWEEN 4 AND 6);
ALTER TABLE household_settings
    ADD COLUMN unlock_code_length INTEGER CHECK (unlock_code_length IS NULL OR unlock_code_length BETWEEN 4 AND 6);

-- +goose Down
ALTER TABLE household_settings DROP COLUMN unlock_code_length;
ALTER TABLE people DROP COLUMN pin_length;
