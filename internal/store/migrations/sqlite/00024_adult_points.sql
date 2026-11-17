-- +goose Up
UPDATE chores SET adult_points = points WHERE mode <> 'each';

-- +goose Down
UPDATE chores SET adult_points = 0 WHERE mode <> 'each';
