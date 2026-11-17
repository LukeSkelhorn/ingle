-- +goose Up
CREATE TABLE household_keys (
    household_id INTEGER PRIMARY KEY REFERENCES households (id) ON DELETE CASCADE,
    wrapped_key  BLOB    NOT NULL,
    created_at   INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE household_keys;
