-- +goose Up
-- Countdowns: days to go until a birthday, a holiday or a big day.
-- A person can have a birthday (MM-DD, and the year if it's known); it
-- counts down every year without a countdowns row. A child with
-- can_add_countdowns may add countdowns from the board after their PIN.
ALTER TABLE people ADD COLUMN birthday TEXT
    CHECK (birthday IS NULL OR date('2000-' || birthday) IS '2000-' || birthday);
ALTER TABLE people ADD COLUMN birth_year INTEGER CHECK (birth_year IS NULL OR birth_year BETWEEN 1900 AND 2999);
ALTER TABLE people ADD COLUMN can_add_countdowns INTEGER NOT NULL DEFAULT 0 CHECK (can_add_countdowns IN (0, 1));

-- on_date is a local date. A yearly countdown counts to the same day and
-- month every year; a one-off one is over after on_date.
CREATE TABLE countdowns (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    title        TEXT    NOT NULL,
    emoji        TEXT    NOT NULL DEFAULT '',
    on_date      TEXT    NOT NULL CHECK (date(on_date) IS on_date),
    yearly       INTEGER NOT NULL DEFAULT 0 CHECK (yearly IN (0, 1)),
    person_id    INTEGER REFERENCES people (id) ON DELETE SET NULL,
    added_by     INTEGER REFERENCES people (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX countdowns_household_idx ON countdowns (household_id, on_date);

-- +goose Down
DROP TABLE countdowns;
ALTER TABLE people DROP COLUMN can_add_countdowns;
ALTER TABLE people DROP COLUMN birth_year;
ALTER TABLE people DROP COLUMN birthday;
