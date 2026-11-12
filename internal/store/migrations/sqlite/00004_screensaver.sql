-- +goose Up
-- Screensaver: when a household display goes idle, uploaded family photos
-- for the slideshow, and ICS calendar feeds for the "what's on" snippet.

-- Idle minutes before a household display shows the screensaver; 0 = never.
ALTER TABLE household_settings
    ADD COLUMN screensaver_idle_minutes INTEGER NOT NULL DEFAULT 5
    CHECK (screensaver_idle_minutes BETWEEN 0 AND 120);

-- Photos uploaded in admin. The files (a full-size JPEG and a thumbnail)
-- live in photos/ next to the database; photos from DOMUS_PHOTOS_DIR are
-- indexed in memory and never stored here.
CREATE TABLE photos (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    file         TEXT    NOT NULL UNIQUE CHECK (length(file) = 32),
    width        INTEGER NOT NULL,
    height       INTEGER NOT NULL,
    created_by   INTEGER REFERENCES people (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX photos_household_idx ON photos (household_id);

-- Read-only ICS feeds (e.g. a Google or iCloud "secret address"). The URL
-- is a credential: admin only ever shows it masked.
CREATE TABLE calendar_feeds (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
    url          TEXT    NOT NULL,
    colour       TEXT    NOT NULL CHECK (colour GLOB '#[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]'),
    created_by   INTEGER REFERENCES people (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX calendar_feeds_household_idx ON calendar_feeds (household_id);

-- +goose Down
DROP TABLE calendar_feeds;
DROP TABLE photos;
ALTER TABLE household_settings DROP COLUMN screensaver_idle_minutes;
