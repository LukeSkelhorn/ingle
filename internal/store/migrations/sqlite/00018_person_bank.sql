-- +goose Up
CREATE TABLE person_bank (
    person_id    INTEGER PRIMARY KEY REFERENCES people (id) ON DELETE CASCADE,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    label        TEXT    NOT NULL CHECK (length(label) BETWEEN 1 AND 40),
    pence        INTEGER NOT NULL CHECK (pence >= 0),
    source       TEXT    NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'monzo')),
    updated_at   INTEGER NOT NULL,
    updated_by   INTEGER REFERENCES people (id) ON DELETE SET NULL
) STRICT;

CREATE INDEX person_bank_household ON person_bank (household_id, updated_at);

-- +goose Down
DROP TABLE person_bank;
