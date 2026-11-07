-- +goose Up
-- A child's real bank balance, as Ingle last knew it: typed in by an adult
-- (source 'manual') or, later, read from Monzo ('monzo'). updated_at is when
-- someone last checked the real bank; payouts from Ingle add to pence
-- without moving it, so the "check their balance" nudge still comes round.
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
