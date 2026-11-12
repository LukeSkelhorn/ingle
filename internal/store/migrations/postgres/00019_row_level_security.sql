-- +goose Up
-- Row-level security: each household sees only its own rows, even if a
-- query forgets its WHERE household_id = ... (see internal/store/household.go).
-- Postgres only, so SQLite has no 00019; the next migration for both is 00020.
--
-- The app sets app.household_id (and app.system) with SET LOCAL at the start
-- of every transaction. FORCE applies the policies to the tables' owner too,
-- so only a superuser or a BYPASSRLS role skips them; the app is neither.
-- TestRowLevelSecurityCoverage fails if a table is added without this.

CREATE FUNCTION ingle_household() RETURNS bigint
    LANGUAGE sql STABLE
    AS $$ SELECT nullif(current_setting('app.household_id', true), '')::bigint $$;

-- System lookups are made before the household is known: a display or
-- session token, an email at sign-in, the list of households.
CREATE FUNCTION ingle_system() RETURNS boolean
    LANGUAGE sql STABLE
    AS $$ SELECT coalesce(current_setting('app.system', true), '') = 'on' $$;

-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'people', 'rewards', 'chores', 'chore_occurrences', 'completions',
        'redemptions', 'ledger_entries', 'activity', 'household_settings',
        'devices', 'sessions', 'photos', 'calendar_feeds', 'household_lock',
        'chore_people', 'household_modules', 'transfer_requests', 'person_bank'
    ] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('CREATE POLICY household ON %I
            USING (household_id = ingle_household())
            WITH CHECK (household_id = ingle_household())', t);
    END LOOP;
END
$$;
-- +goose StatementEnd

ALTER TABLE households ENABLE ROW LEVEL SECURITY;
ALTER TABLE households FORCE ROW LEVEL SECURITY;
CREATE POLICY household ON households
    USING (id = ingle_household())
    WITH CHECK (id = ingle_household());
CREATE POLICY system_create ON households FOR INSERT WITH CHECK (ingle_system());

-- What a system lookup may read.
CREATE POLICY system_read ON households FOR SELECT USING (ingle_system());
CREATE POLICY system_read ON devices FOR SELECT USING (ingle_system());
CREATE POLICY system_read ON sessions FOR SELECT USING (ingle_system());
CREATE POLICY system_read ON people FOR SELECT USING (ingle_system());

-- app_meta holds no household data (just the schema's owner).
ALTER TABLE app_meta ENABLE ROW LEVEL SECURITY;
ALTER TABLE app_meta FORCE ROW LEVEL SECURITY;
CREATE POLICY read ON app_meta FOR SELECT USING (true);

-- +goose Down
DROP POLICY read ON app_meta;
ALTER TABLE app_meta NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app_meta DISABLE ROW LEVEL SECURITY;
DROP POLICY system_read ON people;
DROP POLICY system_read ON sessions;
DROP POLICY system_read ON devices;
DROP POLICY system_read ON households;
DROP POLICY system_create ON households;
DROP POLICY household ON households;
ALTER TABLE households NO FORCE ROW LEVEL SECURITY;
ALTER TABLE households DISABLE ROW LEVEL SECURITY;
-- +goose StatementBegin
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'people', 'rewards', 'chores', 'chore_occurrences', 'completions',
        'redemptions', 'ledger_entries', 'activity', 'household_settings',
        'devices', 'sessions', 'photos', 'calendar_feeds', 'household_lock',
        'chore_people', 'household_modules', 'transfer_requests', 'person_bank'
    ] LOOP
        EXECUTE format('DROP POLICY household ON %I', t);
        EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
        EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
    END LOOP;
END
$$;
-- +goose StatementEnd
DROP FUNCTION ingle_system();
DROP FUNCTION ingle_household();
