-- +goose NO TRANSACTION
-- +goose Up
PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE ledger_entries_new (
    id            INTEGER PRIMARY KEY,
    household_id  INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id     INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    points        INTEGER NOT NULL,
    pence         INTEGER NOT NULL,
    kind          TEXT    NOT NULL CHECK (kind IN ('completion', 'redemption', 'adjustment', 'payout', 'manual', 'interest', 'match')),
    completion_id INTEGER UNIQUE REFERENCES completions (id) ON DELETE RESTRICT,
    redemption_id INTEGER UNIQUE REFERENCES redemptions (id) ON DELETE RESTRICT,
    note          TEXT    NOT NULL DEFAULT '',
    created_by    INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    created_at    INTEGER NOT NULL
) STRICT;
INSERT INTO ledger_entries_new (id, household_id, person_id, points, pence, kind, completion_id, redemption_id, note, created_by, created_at)
SELECT id, household_id, person_id, points, pence, kind, completion_id, redemption_id, note, created_by, created_at FROM ledger_entries;
DROP TABLE ledger_entries;
ALTER TABLE ledger_entries_new RENAME TO ledger_entries;
CREATE INDEX ledger_entries_person_idx ON ledger_entries (person_id);

CREATE TABLE match_offers (
    person_id    INTEGER PRIMARY KEY REFERENCES people (id) ON DELETE CASCADE,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    add_pence    INTEGER NOT NULL CHECK (add_pence >= 0),
    per_pence    INTEGER NOT NULL CHECK (per_pence >= 0 AND (add_pence = 0 OR per_pence > 0)),
    add_points   INTEGER NOT NULL CHECK (add_points >= 0),
    per_points   INTEGER NOT NULL CHECK (per_points >= 0 AND (add_points = 0 OR per_points > 0)),
    cap_pence    INTEGER NOT NULL DEFAULT 0 CHECK (cap_pence >= 0),
    cap_points   INTEGER NOT NULL DEFAULT 0 CHECK (cap_points >= 0),
    set_by       INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    updated_at   INTEGER NOT NULL
) STRICT;
CREATE INDEX match_offers_household_idx ON match_offers (household_id);

CREATE TABLE match_entries (
    id              INTEGER PRIMARY KEY,
    household_id    INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id       INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    ledger_entry_id INTEGER NOT NULL UNIQUE REFERENCES ledger_entries (id) ON DELETE CASCADE,
    completion_id   INTEGER REFERENCES completions (id) ON DELETE SET NULL,
    reward_id       INTEGER NOT NULL,
    goal_set_at     INTEGER NOT NULL,
    points          INTEGER NOT NULL,
    pence           INTEGER NOT NULL,
    created_at      INTEGER NOT NULL
) STRICT;
CREATE INDEX match_entries_person_idx ON match_entries (person_id);
CREATE INDEX match_entries_completion_idx ON match_entries (completion_id);
COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN;
DROP TABLE match_entries;
DROP TABLE match_offers;
DELETE FROM ledger_entries WHERE kind = 'match';
CREATE TABLE ledger_entries_new (
    id            INTEGER PRIMARY KEY,
    household_id  INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id     INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    points        INTEGER NOT NULL,
    pence         INTEGER NOT NULL,
    kind          TEXT    NOT NULL CHECK (kind IN ('completion', 'redemption', 'adjustment', 'payout', 'manual', 'interest')),
    completion_id INTEGER UNIQUE REFERENCES completions (id) ON DELETE RESTRICT,
    redemption_id INTEGER UNIQUE REFERENCES redemptions (id) ON DELETE RESTRICT,
    note          TEXT    NOT NULL DEFAULT '',
    created_by    INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    created_at    INTEGER NOT NULL
) STRICT;
INSERT INTO ledger_entries_new (id, household_id, person_id, points, pence, kind, completion_id, redemption_id, note, created_by, created_at)
SELECT id, household_id, person_id, points, pence, kind, completion_id, redemption_id, note, created_by, created_at FROM ledger_entries;
DROP TABLE ledger_entries;
ALTER TABLE ledger_entries_new RENAME TO ledger_entries;
CREATE INDEX ledger_entries_person_idx ON ledger_entries (person_id);
COMMIT;
PRAGMA foreign_keys = ON;
