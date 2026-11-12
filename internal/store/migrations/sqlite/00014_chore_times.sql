-- +goose NO TRANSACTION
-- +goose Up
-- Several times a day, and showing a timed chore shortly before its time.
-- A chore's times move to chores.due_times (sorted HH:MM, comma-separated)
-- and each occurrence carries its own due_time, '' for none ('' rather than
-- NULL, so the unique key catches untimed duplicates). chore_occurrences is
-- rebuilt for the new key; ids are kept so completions still point at them.
-- show_before is how many minutes before its time an occurrence appears on
-- the board; NULL shows it all day, as before.
PRAGMA foreign_keys = OFF;
BEGIN;
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
INSERT INTO chore_occurrences_new (id, household_id, chore_id, due_on, assignee_id, claimed_by, claimed_at, status, available_from, due_time)
SELECT o.id, o.household_id, o.chore_id, o.due_on, o.assignee_id, o.claimed_by, o.claimed_at, o.status, o.available_from,
       COALESCE(c.due_time, '')
FROM chore_occurrences o JOIN chores c ON c.id = o.chore_id;
DROP TABLE chore_occurrences;
ALTER TABLE chore_occurrences_new RENAME TO chore_occurrences;
CREATE INDEX chore_occurrences_board_idx ON chore_occurrences (household_id, due_on);

ALTER TABLE chores ADD COLUMN due_times TEXT NOT NULL DEFAULT '';
ALTER TABLE chores ADD COLUMN show_before INTEGER CHECK (show_before IS NULL OR show_before IN (30, 60, 120));
UPDATE chores SET due_times = COALESCE(due_time, '');
ALTER TABLE chores DROP COLUMN due_time;
COMMIT;
PRAGMA foreign_keys = ON;

-- +goose Down
-- Going back to one time a day keeps each chore's first occurrence per day
-- (the lowest id) and deletes the rest with their completions. Their points
-- stay in the ledger, unlinked from the deleted completions.
PRAGMA foreign_keys = OFF;
BEGIN;
ALTER TABLE chores ADD COLUMN due_time TEXT CHECK (due_time IS NULL OR time(due_time) IS due_time || ':00');
UPDATE chores SET due_time = NULLIF(substr(due_times, 1, 5), '');
CREATE TEMP TABLE dropped_occurrences AS
SELECT id FROM chore_occurrences o
WHERE id <> (SELECT MIN(id) FROM chore_occurrences k WHERE k.chore_id = o.chore_id AND k.due_on = o.due_on);
UPDATE ledger_entries SET completion_id = NULL
WHERE completion_id IN (SELECT id FROM completions WHERE occurrence_id IN (SELECT id FROM dropped_occurrences));
DELETE FROM completions WHERE occurrence_id IN (SELECT id FROM dropped_occurrences);
DELETE FROM chore_occurrences WHERE id IN (SELECT id FROM dropped_occurrences);
DROP TABLE dropped_occurrences;
CREATE TABLE chore_occurrences_new (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    chore_id     INTEGER NOT NULL REFERENCES chores (id) ON DELETE CASCADE,
    due_on       TEXT    NOT NULL CHECK (date(due_on) IS due_on),
    assignee_id  INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    claimed_by   INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    claimed_at   INTEGER,
    status       TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'claimed', 'done', 'skipped')),
    available_from TEXT CHECK (
        available_from IS NULL OR (date(available_from) IS available_from AND available_from <= due_on)
    ),
    UNIQUE (chore_id, due_on),
    CHECK (assignee_id IS NULL OR claimed_by IS NULL)
) STRICT;
INSERT INTO chore_occurrences_new (id, household_id, chore_id, due_on, assignee_id, claimed_by, claimed_at, status, available_from)
SELECT id, household_id, chore_id, due_on, assignee_id, claimed_by, claimed_at, status, available_from FROM chore_occurrences;
DROP TABLE chore_occurrences;
ALTER TABLE chore_occurrences_new RENAME TO chore_occurrences;
CREATE INDEX chore_occurrences_board_idx ON chore_occurrences (household_id, due_on);
ALTER TABLE chores DROP COLUMN show_before;
ALTER TABLE chores DROP COLUMN due_times;
COMMIT;
PRAGMA foreign_keys = ON;
