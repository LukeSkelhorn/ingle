-- +goose Up
-- Placeholder migration so the migration pipeline is exercised end to end.
-- The real schema (people, chores, rewards) lands in DOM-2.
CREATE TABLE app_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

INSERT INTO app_meta (key, value) VALUES ('schema_owner', 'domus');

-- +goose Down
DROP TABLE app_meta;
