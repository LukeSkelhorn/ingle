-- +goose Up
-- Each household's data key, which encrypts its secrets (calendar links for
-- now). It's stored wrapped by the master key (INGLE_MASTER_KEY), which is
-- held outside the database. Deleting the household deletes its key, so
-- anything encrypted with it, including in old backups, can't be read.
-- (00019 is Postgres only.)
CREATE TABLE household_keys (
    household_id INTEGER PRIMARY KEY REFERENCES households (id) ON DELETE CASCADE,
    wrapped_key  BLOB    NOT NULL,
    created_at   INTEGER NOT NULL
) STRICT;

-- +goose Down
DROP TABLE household_keys;
