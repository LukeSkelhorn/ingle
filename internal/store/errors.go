package store

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsUnique reports whether err is a UNIQUE violation on table.column, on
// either engine. On Postgres it matches the constraint or index name, which
// contains the column (e.g. people_household_id_name_key, people_email_idx).
func IsUnique(err error, table, column string) bool {
	if err == nil {
		return false
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return pgErr.Code == "23505" && pgErr.TableName == table && strings.Contains(pgErr.ConstraintName, column)
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") && strings.Contains(msg, table+"."+column)
}
