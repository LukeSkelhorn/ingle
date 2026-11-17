-- +goose Up
ALTER TABLE calendar_feeds
    ADD COLUMN person_id INTEGER REFERENCES people (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE calendar_feeds DROP COLUMN person_id;
