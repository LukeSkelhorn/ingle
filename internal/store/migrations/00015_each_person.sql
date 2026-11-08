-- +goose NO TRANSACTION
-- +goose Up
-- "Each of these people": mode 'each' puts a copy of a chore on each chosen
-- person's column, and each of them does their own. The people are in
-- chore_people, or, with for_everyone, every active person (so people added
-- later get it too). adult_points is what an adult earns for it; adults get
-- no money for these.
--
-- Each person's copy is its own occurrence, so the occurrence key gains
-- for_person: the person for an 'each' chore, 0 otherwise. (Not assignee_id:
-- it's NULL for pool chores, and reassigning a one-person chore shouldn't
-- give the new person a second copy of a day that's already done.)
--
-- SQLite can't change a CHECK or a UNIQUE constraint in place, so both
-- tables are rebuilt, keeping ids. Foreign keys are off for the swap so
-- dropping the old tables doesn't cascade (the database has one connection,
-- so the pragma applies to these statements).
PRAGMA foreign_keys = OFF;
BEGIN;
CREATE TABLE chores_new (
    id                INTEGER PRIMARY KEY,
    household_id      INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    title             TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 100),
    description       TEXT    NOT NULL DEFAULT '',
    mode              TEXT    NOT NULL CHECK (mode IN ('assigned', 'pool', 'shared', 'each')),
    assignee_id       INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    rrule             TEXT,
    starts_on         TEXT    NOT NULL CHECK (date(starts_on) IS starts_on),
    points            INTEGER NOT NULL DEFAULT 0 CHECK (points >= 0),
    pence             INTEGER NOT NULL DEFAULT 0 CHECK (pence >= 0),
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    archived_at       INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    emoji             TEXT    NOT NULL DEFAULT '' CHECK (length(emoji) <= 16),
    day_mode          TEXT    NOT NULL DEFAULT 'each' CHECK (day_mode IN ('each', 'any')),
    on_the_day        INTEGER NOT NULL DEFAULT 0 CHECK (on_the_day IN (0, 1)),
    due_times         TEXT    NOT NULL DEFAULT '',
    show_before       INTEGER CHECK (show_before IS NULL OR show_before IN (30, 60, 120)),
    for_everyone      INTEGER NOT NULL DEFAULT 0 CHECK (for_everyone IN (0, 1)),
    adult_points      INTEGER NOT NULL DEFAULT 0 CHECK (adult_points >= 0),
    CHECK ((mode = 'assigned') = (assignee_id IS NOT NULL))
) STRICT;
INSERT INTO chores_new (id, household_id, title, description, mode, assignee_id, rrule, starts_on, points, pence,
                        requires_approval, archived_at, created_at, updated_at, emoji, day_mode, on_the_day,
                        due_times, show_before)
SELECT id, household_id, title, description, mode, assignee_id, rrule, starts_on, points, pence,
       requires_approval, archived_at, created_at, updated_at, emoji, day_mode, on_the_day,
       due_times, show_before
FROM chores;
DROP TABLE chores;
ALTER TABLE chores_new RENAME TO chores;
CREATE INDEX chores_household_idx ON chores (household_id);

CREATE TABLE chore_occurrences_new (
    id             INTEGER PRIMARY KEY,
    household_id   INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    chore_id       INTEGER NOT NULL REFERENCES chores (id) ON DELETE CASCADE,
    due_on         TEXT    NOT NULL CHECK (date(due_on) IS due_on),
    assignee_id    INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    claimed_by     INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    claimed_at     INTEGER,
    status         TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'claimed', 'done', 'skipped')),
    available_from TEXT    CHECK (
        available_from IS NULL OR (date(available_from) IS available_from AND available_from <= due_on)
    ),
    due_time       TEXT    NOT NULL DEFAULT '' CHECK (due_time = '' OR time(due_time) IS due_time || ':00'),
    for_person     INTEGER NOT NULL DEFAULT 0,
    UNIQUE (chore_id, due_on, due_time, for_person),
    CHECK (assignee_id IS NULL OR claimed_by IS NULL)
) STRICT;
INSERT INTO chore_occurrences_new (id, household_id, chore_id, due_on, assignee_id, claimed_by, claimed_at, status,
                                   available_from, due_time)
SELECT id, household_id, chore_id, due_on, assignee_id, claimed_by, claimed_at, status, available_from, due_time
FROM chore_occurrences;
DROP TABLE chore_occurrences;
ALTER TABLE chore_occurrences_new RENAME TO chore_occurrences;
CREATE INDEX chore_occurrences_board_idx ON chore_occurrences (household_id, due_on);
COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
-- Going back, an 'each' chore becomes a shared one (done once by any of its
-- people; Everyone becomes today's active people). Each chore keeps one
-- occurrence per day and time (the lowest id), unassigned; the other
-- people's copies are deleted with their completions. Their points stay in
-- the ledger, unlinked from the deleted completions.
PRAGMA foreign_keys = OFF;
BEGIN;
INSERT OR IGNORE INTO chore_people (household_id, chore_id, person_id)
SELECT c.household_id, c.id, p.id
FROM chores c JOIN people p ON p.household_id = c.household_id AND p.archived_at IS NULL
WHERE c.mode = 'each' AND c.for_everyone = 1;
CREATE TEMP TABLE dropped_occurrences AS
SELECT id FROM chore_occurrences o
WHERE id <> (SELECT MIN(id) FROM chore_occurrences k
             WHERE k.chore_id = o.chore_id AND k.due_on = o.due_on AND k.due_time = o.due_time);
UPDATE ledger_entries SET completion_id = NULL
WHERE completion_id IN (SELECT id FROM completions WHERE occurrence_id IN (SELECT id FROM dropped_occurrences));
DELETE FROM completions WHERE occurrence_id IN (SELECT id FROM dropped_occurrences);
DELETE FROM chore_occurrences WHERE id IN (SELECT id FROM dropped_occurrences);
DROP TABLE dropped_occurrences;
UPDATE chore_occurrences SET assignee_id = NULL
WHERE chore_id IN (SELECT id FROM chores WHERE mode = 'each');

CREATE TABLE chore_occurrences_new (
    id             INTEGER PRIMARY KEY,
    household_id   INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    chore_id       INTEGER NOT NULL REFERENCES chores (id) ON DELETE CASCADE,
    due_on         TEXT    NOT NULL CHECK (date(due_on) IS due_on),
    assignee_id    INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    claimed_by     INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    claimed_at     INTEGER,
    status         TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'claimed', 'done', 'skipped')),
    available_from TEXT    CHECK (
        available_from IS NULL OR (date(available_from) IS available_from AND available_from <= due_on)
    ),
    due_time       TEXT    NOT NULL DEFAULT '' CHECK (due_time = '' OR time(due_time) IS due_time || ':00'),
    UNIQUE (chore_id, due_on, due_time),
    CHECK (assignee_id IS NULL OR claimed_by IS NULL)
) STRICT;
INSERT INTO chore_occurrences_new (id, household_id, chore_id, due_on, assignee_id, claimed_by, claimed_at, status,
                                   available_from, due_time)
SELECT id, household_id, chore_id, due_on, assignee_id, claimed_by, claimed_at, status, available_from, due_time
FROM chore_occurrences;
DROP TABLE chore_occurrences;
ALTER TABLE chore_occurrences_new RENAME TO chore_occurrences;
CREATE INDEX chore_occurrences_board_idx ON chore_occurrences (household_id, due_on);

UPDATE chores SET mode = 'shared' WHERE mode = 'each';
CREATE TABLE chores_new (
    id                INTEGER PRIMARY KEY,
    household_id      INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    title             TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 100),
    description       TEXT    NOT NULL DEFAULT '',
    mode              TEXT    NOT NULL CHECK (mode IN ('assigned', 'pool', 'shared')),
    assignee_id       INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    rrule             TEXT,
    starts_on         TEXT    NOT NULL CHECK (date(starts_on) IS starts_on),
    points            INTEGER NOT NULL DEFAULT 0 CHECK (points >= 0),
    pence             INTEGER NOT NULL DEFAULT 0 CHECK (pence >= 0),
    requires_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_approval IN (0, 1)),
    archived_at       INTEGER,
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    emoji             TEXT    NOT NULL DEFAULT '' CHECK (length(emoji) <= 16),
    day_mode          TEXT    NOT NULL DEFAULT 'each' CHECK (day_mode IN ('each', 'any')),
    on_the_day        INTEGER NOT NULL DEFAULT 0 CHECK (on_the_day IN (0, 1)),
    due_times         TEXT    NOT NULL DEFAULT '',
    show_before       INTEGER CHECK (show_before IS NULL OR show_before IN (30, 60, 120)),
    CHECK ((mode = 'assigned') = (assignee_id IS NOT NULL))
) STRICT;
INSERT INTO chores_new (id, household_id, title, description, mode, assignee_id, rrule, starts_on, points, pence,
                        requires_approval, archived_at, created_at, updated_at, emoji, day_mode, on_the_day,
                        due_times, show_before)
SELECT id, household_id, title, description, mode, assignee_id, rrule, starts_on, points, pence,
       requires_approval, archived_at, created_at, updated_at, emoji, day_mode, on_the_day,
       due_times, show_before
FROM chores;
DROP TABLE chores;
ALTER TABLE chores_new RENAME TO chores;
CREATE INDEX chores_household_idx ON chores (household_id);
COMMIT;
PRAGMA foreign_keys = ON;
