-- +goose Up
ALTER TABLE people ADD COLUMN email TEXT COLLATE NOCASE;
ALTER TABLE people ADD COLUMN password_hash TEXT;
CREATE UNIQUE INDEX people_email_idx ON people (email);

CREATE TABLE household_settings (
    household_id       INTEGER PRIMARY KEY REFERENCES households (id) ON DELETE CASCADE,
    timezone           TEXT    NOT NULL,
    day_starts_at      TEXT    NOT NULL DEFAULT '00:00' CHECK (time(day_starts_at) IS day_starts_at || ':00'),
    admin_idle_minutes INTEGER NOT NULL DEFAULT 2 CHECK (admin_idle_minutes BETWEEN 1 AND 60),
    updated_at         INTEGER NOT NULL
) STRICT;

CREATE TABLE devices (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
    token_hash   BLOB    NOT NULL UNIQUE,
    created_by   INTEGER REFERENCES people (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    revoked_at   INTEGER
) STRICT;

CREATE INDEX devices_household_idx ON devices (household_id);

CREATE TABLE sessions (
    token_hash   BLOB    PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id    INTEGER NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    kind         TEXT    NOT NULL CHECK (kind IN ('display', 'personal')),
    device_id    INTEGER REFERENCES devices (id) ON DELETE CASCADE,
    user_agent   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    CHECK ((kind = 'display') = (device_id IS NOT NULL))
) STRICT;

CREATE INDEX sessions_person_idx ON sessions (person_id);

-- +goose Down
DROP TABLE sessions;
DROP TABLE devices;
DROP TABLE household_settings;
DROP INDEX people_email_idx;
ALTER TABLE people DROP COLUMN password_hash;
ALTER TABLE people DROP COLUMN email;
