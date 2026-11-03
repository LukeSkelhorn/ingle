-- +goose Up
-- Core Domus data model. Conventions:
--   * every household-scoped table carries household_id (multi-household ready)
--   * timestamps are INTEGER unix seconds (UTC)
--   * local calendar dates are TEXT 'YYYY-MM-DD' in the household timezone
--   * money is INTEGER pence; points are INTEGER

CREATE TABLE households (
    id         INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

CREATE TABLE people (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
    kind         TEXT    NOT NULL CHECK (kind IN ('adult', 'child')),
    colour       TEXT    NOT NULL CHECK (colour GLOB '#[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]'),
    avatar       TEXT,
    pin_hash     TEXT,
    sort_order   INTEGER NOT NULL DEFAULT 0,
    archived_at  INTEGER,
    created_at   INTEGER NOT NULL,
    UNIQUE (household_id, name)
) STRICT;

CREATE TABLE chores (
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
    CHECK ((mode = 'assigned') = (assignee_id IS NOT NULL))
) STRICT;

CREATE INDEX chores_household_idx ON chores (household_id);

CREATE TABLE chore_occurrences (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    chore_id     INTEGER NOT NULL REFERENCES chores (id) ON DELETE CASCADE,
    due_on       TEXT    NOT NULL CHECK (date(due_on) IS due_on),
    assignee_id  INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    claimed_by   INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    claimed_at   INTEGER,
    status       TEXT    NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'claimed', 'done', 'skipped')),
    UNIQUE (chore_id, due_on),
    CHECK (assignee_id IS NULL OR claimed_by IS NULL)
) STRICT;

CREATE INDEX chore_occurrences_board_idx ON chore_occurrences (household_id, due_on);

CREATE TABLE completions (
    id            INTEGER PRIMARY KEY,
    household_id  INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    occurrence_id INTEGER NOT NULL REFERENCES chore_occurrences (id) ON DELETE CASCADE,
    person_id     INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    completed_at  INTEGER NOT NULL,
    status        TEXT    NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    reviewed_by   INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    reviewed_at   INTEGER,
    points        INTEGER NOT NULL CHECK (points >= 0),
    pence         INTEGER NOT NULL CHECK (pence >= 0)
) STRICT;

CREATE INDEX completions_household_status_idx ON completions (household_id, status);
-- At most one live (pending or approved) completion per occurrence; rejected
-- completions stay as history and the occurrence reopens.
CREATE UNIQUE INDEX completions_live_occurrence_idx ON completions (occurrence_id) WHERE status <> 'rejected';

CREATE TABLE rewards (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    title        TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 100),
    description  TEXT    NOT NULL DEFAULT '',
    cost_points  INTEGER NOT NULL DEFAULT 0 CHECK (cost_points >= 0),
    cost_pence   INTEGER NOT NULL DEFAULT 0 CHECK (cost_pence >= 0),
    archived_at  INTEGER,
    created_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX rewards_household_idx ON rewards (household_id);

CREATE TABLE redemptions (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    reward_id    INTEGER NOT NULL REFERENCES rewards (id) ON DELETE RESTRICT,
    person_id    INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    points       INTEGER NOT NULL CHECK (points >= 0),
    pence        INTEGER NOT NULL CHECK (pence >= 0),
    status       TEXT    NOT NULL CHECK (status IN ('pending', 'approved', 'rejected')),
    requested_at INTEGER NOT NULL,
    reviewed_by  INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    reviewed_at  INTEGER
) STRICT;

CREATE INDEX redemptions_household_status_idx ON redemptions (household_id, status);

CREATE TABLE ledger_entries (
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

CREATE INDEX ledger_entries_person_idx ON ledger_entries (person_id);

CREATE TABLE activity (
    id           INTEGER PRIMARY KEY,
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    actor_id     INTEGER REFERENCES people (id) ON DELETE SET NULL,
    verb         TEXT    NOT NULL,
    subject_type TEXT    NOT NULL,
    subject_id   INTEGER NOT NULL,
    data         TEXT    CHECK (data IS NULL OR json_valid(data)),
    created_at   INTEGER NOT NULL
) STRICT;

CREATE INDEX activity_household_created_idx ON activity (household_id, created_at DESC);

-- +goose Down
DROP TABLE activity;
DROP TABLE ledger_entries;
DROP TABLE redemptions;
DROP TABLE rewards;
DROP TABLE completions;
DROP TABLE chore_occurrences;
DROP TABLE chores;
DROP TABLE people;
DROP TABLE households;
