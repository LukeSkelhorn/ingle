-- +goose Up
-- Bank transfers: a child asks for some of their money to go to their real
-- bank, and an adult (who does the transfer in their banking app) approves
-- or declines. A pending request holds its pence, as a waiting reward claim
-- does. Approving writes the payout to the ledger, linked by ledger_entry_id.
-- An adult can also pay out directly: that's stored as already approved.
CREATE TABLE transfer_requests (
    id              INTEGER PRIMARY KEY,
    household_id    INTEGER NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    person_id       INTEGER NOT NULL REFERENCES people (id) ON DELETE RESTRICT,
    pence           INTEGER NOT NULL CHECK (pence > 0),
    status          TEXT    NOT NULL CHECK (status IN ('pending', 'approved', 'declined', 'cancelled')),
    reason          TEXT    NOT NULL DEFAULT '',
    requested_by    INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    requested_at    INTEGER NOT NULL,
    reviewed_by     INTEGER REFERENCES people (id) ON DELETE RESTRICT,
    reviewed_at     INTEGER,
    ledger_entry_id INTEGER UNIQUE REFERENCES ledger_entries (id) ON DELETE RESTRICT
) STRICT;

CREATE INDEX transfer_requests_status ON transfer_requests (household_id, status);
CREATE INDEX transfer_requests_person ON transfer_requests (person_id, requested_at);

-- +goose Down
DROP TABLE transfer_requests;
