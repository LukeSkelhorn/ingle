-- +goose Up
-- See migrations/sqlite/00020_household_keys.sql.
CREATE TABLE household_keys (
    household_id bigint PRIMARY KEY REFERENCES households (id) ON DELETE CASCADE,
    wrapped_key  bytea  NOT NULL,
    created_at   bigint NOT NULL
);

ALTER TABLE household_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE household_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY household ON household_keys
    USING (household_id = ingle_household())
    WITH CHECK (household_id = ingle_household());

-- +goose Down
DROP TABLE household_keys;
