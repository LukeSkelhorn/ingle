-- +goose Up
-- How many digits each PIN and the household unlock code have, so the
-- keypad shows that many dots and tries the code once they're all typed.
-- NULL means not known yet: set with the PIN or code, and filled in the
-- first time an older one unlocks successfully.
ALTER TABLE people
    ADD COLUMN pin_length INTEGER CHECK (pin_length IS NULL OR pin_length BETWEEN 4 AND 6);
ALTER TABLE household_settings
    ADD COLUMN unlock_code_length INTEGER CHECK (unlock_code_length IS NULL OR unlock_code_length BETWEEN 4 AND 6);

-- +goose Down
ALTER TABLE household_settings DROP COLUMN unlock_code_length;
ALTER TABLE people DROP COLUMN pin_length;
