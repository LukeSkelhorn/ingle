-- +goose Up
ALTER TABLE household_settings ADD COLUMN unlock_code_hash TEXT;
ALTER TABLE household_settings
    ADD COLUMN lock_days INTEGER NOT NULL DEFAULT 0 CHECK (lock_days BETWEEN 0 AND 127);
ALTER TABLE household_settings
    ADD COLUMN lock_from TEXT NOT NULL DEFAULT '09:00' CHECK (time(lock_from) IS lock_from || ':00');
ALTER TABLE household_settings
    ADD COLUMN lock_until TEXT NOT NULL DEFAULT '15:00' CHECK (time(lock_until) IS lock_until || ':00');
ALTER TABLE household_settings
    ADD COLUMN lock_idle_hours INTEGER NOT NULL DEFAULT 0 CHECK (lock_idle_hours BETWEEN 0 AND 72);

CREATE TABLE household_lock (
    household_id     INTEGER PRIMARY KEY REFERENCES households (id) ON DELETE CASCADE,
    locked           INTEGER NOT NULL DEFAULT 0 CHECK (locked IN (0, 1)),
    reason           TEXT    NOT NULL DEFAULT '' CHECK (reason IN ('', 'manual', 'remote', 'schedule', 'idle')),
    locked_by        INTEGER REFERENCES people (id) ON DELETE SET NULL,
    changed_at       INTEGER NOT NULL DEFAULT 0,
    schedule_applied INTEGER NOT NULL DEFAULT 0
) STRICT;

-- +goose Down
DROP TABLE household_lock;
ALTER TABLE household_settings DROP COLUMN lock_idle_hours;
ALTER TABLE household_settings DROP COLUMN lock_until;
ALTER TABLE household_settings DROP COLUMN lock_from;
ALTER TABLE household_settings DROP COLUMN lock_days;
ALTER TABLE household_settings DROP COLUMN unlock_code_hash;
