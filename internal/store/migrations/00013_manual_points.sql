-- +goose NO TRANSACTION
-- +goose Up
-- Manual points adjustments: an adult gives a bonus or takes points away,
-- with a reason. They get their own kind, 'manual', so they can be listed
-- apart from chore reversals (which stay 'adjustment'). SQLite can't change a
-- CHECK constraint in place, so ledger_entries is rebuilt, as in 00011.
PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE ledger_entries_new (
    id            INTEGER PRIMARY KEY,
    household_id  INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id     INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    points        INTEGER NOT NULL,
    pence         INTEGER NOT NULL,
    kind          TEXT    NOT NULL CHECK (kind IN ('completion', 'redemption', 'adjustment', 'payout', 'manual')),
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

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN;
UPDATE ledger_entries SET kind = 'adjustment' WHERE kind = 'manual';
CREATE TABLE ledger_entries_new (
    id            INTEGER PRIMARY KEY,
    household_id  INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id     INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    points        INTEGER NOT NULL,
    pence         INTEGER NOT NULL,
    kind          TEXT    NOT NULL CHECK (kind IN ('completion', 'redemption', 'adjustment', 'payout')),
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
