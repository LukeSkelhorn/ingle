-- +goose Up
-- Whose calendar a feed is: one person, or the family (NULL). A person's
-- events show in their colour, with their avatar.
ALTER TABLE calendar_feeds
    ADD COLUMN person_id INTEGER REFERENCES people (id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE calendar_feeds DROP COLUMN person_id;
