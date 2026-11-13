-- +goose Up
-- See migrations/sqlite/00022_calendar_owner.sql.
ALTER TABLE calendar_feeds
    ADD COLUMN person_id bigint REFERENCES people (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE calendar_feeds DROP COLUMN person_id;
