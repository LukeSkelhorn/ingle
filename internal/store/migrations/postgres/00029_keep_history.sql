-- +goose Up
-- See migrations/sqlite/00029_keep_history.sql.
ALTER TABLE household_settings
    ADD COLUMN keep_history_months bigint NOT NULL DEFAULT 0 CHECK (keep_history_months IN (0, 12, 24));
ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_kind_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_kind_check
    CHECK (kind IN ('completion', 'redemption', 'adjustment', 'payout', 'manual', 'interest', 'match', 'brought_forward'));

-- +goose Down
ALTER TABLE ledger_entries NO FORCE ROW LEVEL SECURITY;
DELETE FROM ledger_entries WHERE kind = 'brought_forward';
ALTER TABLE ledger_entries FORCE ROW LEVEL SECURITY;
ALTER TABLE ledger_entries DROP CONSTRAINT ledger_entries_kind_check;
ALTER TABLE ledger_entries ADD CONSTRAINT ledger_entries_kind_check
    CHECK (kind IN ('completion', 'redemption', 'adjustment', 'payout', 'manual', 'interest', 'match'));
ALTER TABLE household_settings DROP COLUMN keep_history_months;
