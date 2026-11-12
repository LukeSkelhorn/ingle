-- +goose Up
-- Modules: which features a household has switched on (Admin › Modules).
-- A household with no rows hasn't chosen yet, and every module is on, so an
-- upgrade changes nothing until an adult picks. Turning a module off keeps
-- its data; module_id is the module's ID in code (e.g. 'calendar').
CREATE TABLE household_modules (
    household_id INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    module_id    TEXT    NOT NULL CHECK (module_id <> ''),
    enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    changed_at   INTEGER NOT NULL,
    PRIMARY KEY (household_id, module_id)
) STRICT;

-- +goose Down
DROP TABLE household_modules;
