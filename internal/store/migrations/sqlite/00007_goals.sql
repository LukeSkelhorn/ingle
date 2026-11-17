-- +goose Up
ALTER TABLE rewards
    ADD COLUMN emoji TEXT NOT NULL DEFAULT '' CHECK (length(emoji) <= 16);

ALTER TABLE people
    ADD COLUMN goal_reward_id INTEGER REFERENCES rewards (id) ON DELETE SET NULL;
ALTER TABLE people
    ADD COLUMN goal_set_at INTEGER;

-- +goose Down
ALTER TABLE people DROP COLUMN goal_set_at;
ALTER TABLE people DROP COLUMN goal_reward_id;
ALTER TABLE rewards DROP COLUMN emoji;
