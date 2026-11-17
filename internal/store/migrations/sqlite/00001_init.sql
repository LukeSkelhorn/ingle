-- +goose Up
CREATE TABLE app_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;

INSERT INTO app_meta (key, value) VALUES ('schema_owner', 'domus');

-- +goose Down
DROP TABLE app_meta;
