-- +goose Up
ALTER TABLE chores
    ADD COLUMN emoji TEXT NOT NULL DEFAULT '' CHECK (length(emoji) <= 16);

ALTER TABLE people
    ADD COLUMN picture_cards INTEGER NOT NULL DEFAULT 0 CHECK (picture_cards IN (0, 1));

-- +goose Down
ALTER TABLE people DROP COLUMN picture_cards;
ALTER TABLE chores DROP COLUMN emoji;
