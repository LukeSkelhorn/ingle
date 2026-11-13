-- +goose Up
-- Holiday mode: dates when chores are paused, for the whole household
-- (person_id NULL) or one person. starts_on and ends_on are local dates,
-- both included; chores start again the day after ends_on.
CREATE TABLE holidays (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id    INTEGER REFERENCES people (id) ON DELETE CASCADE,
    starts_on    TEXT    NOT NULL CHECK (date(starts_on) IS starts_on),
    ends_on      TEXT    NOT NULL CHECK (date(ends_on) IS ends_on),
    created_at   INTEGER NOT NULL,
    CHECK (ends_on >= starts_on)
) STRICT;
CREATE INDEX holidays_household_idx ON holidays (household_id, ends_on);

-- +goose Down
DROP TABLE holidays;
