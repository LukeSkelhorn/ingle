-- +goose NO TRANSACTION
-- +goose Up
-- Keep history: a household can clear out history older than 12 or 24
-- months (0 keeps everything). Ledger lines older than that are added up
-- into one 'brought_forward' line per person, so balances don't change.
-- SQLite can't change a CHECK constraint in place, so ledger_entries is
-- rebuilt, as in 00028.
PRAGMA foreign_keys = OFF;
BEGIN;
ALTER TABLE household_settings
    ADD COLUMN keep_history_months INTEGER NOT NULL DEFAULT 0 CHECK (keep_history_months IN (0, 12, 24));
CREATE TABLE ledger_entries_new (
    id            INTEGER PRIMARY KEY,
    household_id  INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id     INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    points        INTEGER NOT NULL,
    pence         INTEGER NOT NULL,
    kind          TEXT    NOT NULL CHECK (kind IN ('completion', 'redemption', 'adjustment', 'payout', 'manual', 'interest', 'match', 'brought_forward')),
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
DELETE FROM ledger_entries WHERE kind = 'brought_forward';
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
ALTER TABLE household_settings DROP COLUMN keep_history_months;
COMMIT;
PRAGMA foreign_keys = ON;
