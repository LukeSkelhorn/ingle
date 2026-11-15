-- +goose NO TRANSACTION
-- +goose Up
-- Bank of Mum and Dad: children earn a little interest each month on the
-- lowest balance they kept. Interest is its own ledger kind, 'interest'.
-- SQLite can't change a CHECK constraint in place, so ledger_entries is
-- rebuilt, as in 00013.
PRAGMA foreign_keys = OFF;
BEGIN;
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

-- The household's interest settings. The rate is in basis points a month
-- (100 = 1%); 0 means interest is off. A cap of 0 means no cap.
-- interest_since is when interest was last turned on: the first month
-- counts from then, never before.
ALTER TABLE household_settings ADD COLUMN interest_rate_bp INTEGER NOT NULL DEFAULT 0 CHECK (interest_rate_bp BETWEEN 0 AND 1000);
ALTER TABLE household_settings ADD COLUMN interest_money INTEGER NOT NULL DEFAULT 1 CHECK (interest_money IN (0, 1));
ALTER TABLE household_settings ADD COLUMN interest_points INTEGER NOT NULL DEFAULT 1 CHECK (interest_points IN (0, 1));
ALTER TABLE household_settings ADD COLUMN interest_cap_pence INTEGER NOT NULL DEFAULT 0 CHECK (interest_cap_pence >= 0);
ALTER TABLE household_settings ADD COLUMN interest_cap_points INTEGER NOT NULL DEFAULT 0 CHECK (interest_cap_points >= 0);
ALTER TABLE household_settings ADD COLUMN interest_since INTEGER;

-- A child an adult has switched off interest for.
ALTER TABLE people ADD COLUMN interest_opt_out INTEGER NOT NULL DEFAULT 0 CHECK (interest_opt_out IN (0, 1));

-- One row per child per month once that month's interest is worked out,
-- even when it came to nothing, so a month is never paid twice. carry_*
-- hold the fraction left over, in 1/10000ths of a penny or point, for next
-- month.
CREATE TABLE interest_payments (
    id              INTEGER PRIMARY KEY,
    household_id    INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id       INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    period          TEXT    NOT NULL CHECK (period GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]'),
    points          INTEGER NOT NULL,
    pence           INTEGER NOT NULL,
    carry_points    INTEGER NOT NULL,
    carry_pence     INTEGER NOT NULL,
    ledger_entry_id INTEGER UNIQUE REFERENCES ledger_entries (id) ON DELETE SET NULL,
    created_at      INTEGER NOT NULL,
    UNIQUE (person_id, period)
) STRICT;
CREATE INDEX interest_payments_household_idx ON interest_payments (household_id);
COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN;
DROP TABLE interest_payments;
ALTER TABLE people DROP COLUMN interest_opt_out;
ALTER TABLE household_settings DROP COLUMN interest_since;
ALTER TABLE household_settings DROP COLUMN interest_cap_points;
ALTER TABLE household_settings DROP COLUMN interest_cap_pence;
ALTER TABLE household_settings DROP COLUMN interest_points;
ALTER TABLE household_settings DROP COLUMN interest_money;
ALTER TABLE household_settings DROP COLUMN interest_rate_bp;
DELETE FROM ledger_entries WHERE kind = 'interest';
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
