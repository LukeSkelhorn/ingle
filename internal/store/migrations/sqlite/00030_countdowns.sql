-- +goose Up
ALTER TABLE people ADD COLUMN birthday TEXT
    CHECK (birthday IS NULL OR date('2000-' || birthday) IS '2000-' || birthday);
ALTER TABLE people ADD COLUMN birth_year INTEGER CHECK (birth_year IS NULL OR birth_year BETWEEN 1900 AND 2999);
ALTER TABLE people ADD COLUMN can_add_countdowns INTEGER NOT NULL DEFAULT 0 CHECK (can_add_countdowns IN (0, 1));

CREATE TABLE countdowns (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    title        TEXT    NOT NULL,
    emoji        TEXT    NOT NULL DEFAULT '',
    on_date      TEXT    NOT NULL CHECK (date(on_date) IS on_date),
    yearly       INTEGER NOT NULL DEFAULT 0 CHECK (yearly IN (0, 1)),
    added_by     INTEGER REFERENCES people (id) ON DELETE SET NULL,
    created_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX countdowns_household_idx ON countdowns (household_id, on_date);

CREATE TABLE countdown_people (
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    countdown_id INTEGER NOT NULL REFERENCES countdowns (id) ON DELETE CASCADE,
    person_id    INTEGER NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    PRIMARY KEY (countdown_id, person_id)
) STRICT;
CREATE INDEX countdown_people_household_idx ON countdown_people (household_id);

-- +goose Down
DROP TABLE countdown_people;
DROP TABLE countdowns;
ALTER TABLE people DROP COLUMN can_add_countdowns;
ALTER TABLE people DROP COLUMN birth_year;
ALTER TABLE people DROP COLUMN birthday;
