-- +goose Up
-- See migrations/sqlite/00025_reward_people.sql.
ALTER TABLE rewards ADD COLUMN everyone bigint NOT NULL DEFAULT 1 CHECK (everyone IN (0, 1));

CREATE TABLE reward_people (
    household_id bigint NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    reward_id    bigint NOT NULL REFERENCES rewards (id) ON DELETE CASCADE,
    person_id    bigint NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    PRIMARY KEY (reward_id, person_id)
);
CREATE INDEX reward_people_household_idx ON reward_people (household_id);

ALTER TABLE reward_people ENABLE ROW LEVEL SECURITY;
ALTER TABLE reward_people FORCE ROW LEVEL SECURITY;
CREATE POLICY household ON reward_people
    USING (household_id = ingle_household())
    WITH CHECK (household_id = ingle_household());

-- +goose Down
DROP TABLE reward_people;
ALTER TABLE rewards DROP COLUMN everyone;
