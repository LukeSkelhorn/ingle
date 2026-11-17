-- +goose Up
CREATE TABLE household_modules (
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    module_id    TEXT    NOT NULL CHECK (module_id <> ''),
    enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    changed_at   INTEGER NOT NULL,
    PRIMARY KEY (household_id, module_id)
) STRICT;

-- +goose Down
DROP TABLE household_modules;
