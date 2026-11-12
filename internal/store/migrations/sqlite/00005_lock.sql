-- +goose Up
-- Locking the household displays when the house is empty: a shared unlock
-- code, an optional weekly schedule and idle timeout, and the current state.

-- unlock_code_hash is an argon2id hash of the household's 4–6 digit code
-- (adult PINs also unlock). lock_days is a weekday bitmask (bit 0 = Sunday);
-- 0 turns the schedule off. A window with lock_from > lock_until runs
-- overnight. lock_idle_hours 0 turns the idle lock off.
ALTER TABLE household_settings ADD COLUMN unlock_code_hash TEXT;
ALTER TABLE household_settings
    ADD COLUMN lock_days INTEGER NOT NULL DEFAULT 0 CHECK (lock_days BETWEEN 0 AND 127);
ALTER TABLE household_settings
    ADD COLUMN lock_from TEXT NOT NULL DEFAULT '09:00' CHECK (time(lock_from) IS lock_from || ':00');
ALTER TABLE household_settings
    ADD COLUMN lock_until TEXT NOT NULL DEFAULT '15:00' CHECK (time(lock_until) IS lock_until || ':00');
ALTER TABLE household_settings
    ADD COLUMN lock_idle_hours INTEGER NOT NULL DEFAULT 0 CHECK (lock_idle_hours BETWEEN 0 AND 72);

-- Whether the displays are locked. schedule_applied is the start (unix) of
-- the last schedule window acted on, so an early unlock holds until the
-- next window and a restart doesn't re-lock.
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
