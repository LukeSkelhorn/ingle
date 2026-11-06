-- +goose NO TRANSACTION
-- +goose Up
-- Shared chores: mode 'shared' is done once by any of a few chosen people,
-- listed in chore_people. SQLite can't change a CHECK constraint in place, so
-- chores is rebuilt. Foreign keys are off for the swap so dropping the old
-- table doesn't cascade to its occurrences (the database has one connection,
-- so the pragma applies to these statements).
PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE chores_new (
    id                INTEGER PRIMARY KEY,
    household_id      INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    title             TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 100),
    description       TEXT    NOT NULL DEFAULT '',
    mode              TEXT    NOT NULL CHECK (mode IN ('assigned', 'pool', 'shared')),
    assignee_id       INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    rrule             TEXT,
    starts_on         TEXT    NOT NULL CHECK (date(starts_on) IS starts_on),
    due_time          TEXT    CHECK (due_time IS NULL OR time(due_time) IS due_time || ':00'),
    points            INTEGER NOT NULL DEFAULT 0 CHECK (points >= 0),
    pence             INTEGER NOT NULL DEFAULT 0 CHECK (pence >= 0),
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    archived_at       INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    emoji             TEXT    NOT NULL DEFAULT '' CHECK (length(emoji) <= 16),
    day_mode          TEXT    NOT NULL DEFAULT 'each' CHECK (day_mode IN ('each', 'any')),
    CHECK ((mode = 'assigned') = (assignee_id IS NOT NULL))
) STRICT;
INSERT INTO chores_new (id, household_id, title, description, mode, assignee_id, rrule, starts_on, due_time, points, pence, requires_approval, archived_at, created_at, updated_at, emoji, day_mode)
SELECT id, household_id, title, description, mode, assignee_id, rrule, starts_on, due_time, points, pence, requires_approval, archived_at, created_at, updated_at, emoji, day_mode FROM chores;
DROP TABLE chores;
ALTER TABLE chores_new RENAME TO chores;
CREATE INDEX chores_household_idx ON chores (household_id);

CREATE TABLE chore_people (
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    chore_id     INTEGER NOT NULL REFERENCES chores (id) ON DELETE CASCADE,
    person_id    INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    PRIMARY KEY (chore_id, person_id)
) STRICT;
CREATE INDEX chore_people_household_idx ON chore_people (household_id);
COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
PRAGMA foreign_keys = OFF;
BEGIN;
DROP TABLE chore_people;
UPDATE chores SET mode = 'pool' WHERE mode = 'shared';
CREATE TABLE chores_new (
    id                INTEGER PRIMARY KEY,
    household_id      INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    title             TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 100),
    description       TEXT    NOT NULL DEFAULT '',
    mode              TEXT    NOT NULL CHECK (mode IN ('assigned', 'pool')),
    assignee_id       INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    rrule             TEXT,
    starts_on         TEXT    NOT NULL CHECK (date(starts_on) IS starts_on),
    due_time          TEXT    CHECK (due_time IS NULL OR time(due_time) IS due_time || ':00'),
    points            INTEGER NOT NULL DEFAULT 0 CHECK (points >= 0),
    pence             INTEGER NOT NULL DEFAULT 0 CHECK (pence >= 0),
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    archived_at       INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    emoji             TEXT    NOT NULL DEFAULT '' CHECK (length(emoji) <= 16),
    day_mode          TEXT    NOT NULL DEFAULT 'each' CHECK (day_mode IN ('each', 'any')),
    CHECK ((mode = 'assigned') = (assignee_id IS NOT NULL))
) STRICT;
INSERT INTO chores_new (id, household_id, title, description, mode, assignee_id, rrule, starts_on, due_time, points, pence, requires_approval, archived_at, created_at, updated_at, emoji, day_mode)
SELECT id, household_id, title, description, mode, assignee_id, rrule, starts_on, due_time, points, pence, requires_approval, archived_at, created_at, updated_at, emoji, day_mode FROM chores;
DROP TABLE chores;
ALTER TABLE chores_new RENAME TO chores;
CREATE INDEX chores_household_idx ON chores (household_id);
COMMIT;
PRAGMA foreign_keys = ON;
