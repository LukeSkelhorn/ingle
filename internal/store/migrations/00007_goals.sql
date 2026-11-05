-- +goose Up
-- A reward's emoji, shown on the board next to its title.
ALTER TABLE rewards
    ADD COLUMN emoji TEXT NOT NULL DEFAULT '' CHECK (length(emoji) <= 16);

-- The one reward each person is saving for. goal_set_at is when they
-- picked it, so a display celebrates reaching a goal once per pick.
ALTER TABLE people
    ADD COLUMN goal_reward_id INTEGER REFERENCES rewards (id) ON DELETE SET NULL;
ALTER TABLE people
    ADD COLUMN goal_set_at INTEGER;

-- +goose Down
ALTER TABLE people DROP COLUMN goal_set_at;
ALTER TABLE people DROP COLUMN goal_reward_id;
ALTER TABLE rewards DROP COLUMN emoji;
