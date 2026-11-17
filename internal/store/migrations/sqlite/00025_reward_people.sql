-- +goose Up
ALTER TABLE rewards ADD COLUMN everyone INTEGER NOT NULL DEFAULT 1 CHECK (everyone IN (0, 1));

CREATE TABLE reward_people (
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    reward_id    INTEGER NOT NULL REFERENCES rewards (id) ON DELETE CASCADE,
    person_id    INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    PRIMARY KEY (reward_id, person_id)
) STRICT;
CREATE INDEX reward_people_household_idx ON reward_people (household_id);

-- +goose Down
DROP TABLE reward_people;
ALTER TABLE rewards DROP COLUMN everyone;
