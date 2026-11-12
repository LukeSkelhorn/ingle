package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/stdlib"
)

// On Postgres, row-level security keeps each household's rows apart: every
// table's policy only matches rows whose household_id is the one set for
// the transaction (app.household_id). A query that forgets its
// WHERE household_id = ... gets nothing back, not another family's data.
//
// The household comes from the context: the server puts the request's
// household there (WithHousehold), and background jobs put each household
// there in turn. A statement with no household fails, rather than running
// unscoped. The few lookups made before the household is known (a display
// or session token, an email at sign-in, the list of households) use
// AsSystem, which may read those rows only.
//
// SQLite has no row-level security. There, these are no-ops.

type householdKey struct{}

type systemKey struct{}

// WithHousehold returns ctx scoped to household id.
func WithHousehold(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, householdKey{}, id)
}

// HouseholdFrom returns the household ctx is scoped to, or 0.
func HouseholdFrom(ctx context.Context) int64 {
	id, _ := ctx.Value(householdKey{}).(int64)
	return id
}

// AsSystem returns ctx for a lookup made before the household is known. On
// Postgres it may read households, displays, sessions and people, and
// create a household, but not change anything else. Only the call sites
// listed in TestAsSystemCallers use it.
func AsSystem(ctx context.Context) context.Context {
	return context.WithValue(ctx, systemKey{}, true)
}

func isSystem(ctx context.Context) bool {
	v, _ := ctx.Value(systemKey{}).(bool)
	return v
}

// ErrNoHousehold is returned for a Postgres statement whose context has no
// household and isn't AsSystem.
var ErrNoHousehold = errors.New("store: no household in context (use store.WithHousehold or store.AsSystem)")

// scopeSQL is run (simple protocol, one round trip) at the start of each
// transaction. Both values are formatted here from an int64 and a bool, so
// there's nothing to escape.
func scopeSQL(ctx context.Context, strict bool) (string, error) {
	hh, sys := HouseholdFrom(ctx), isSystem(ctx)
	if hh == 0 && !sys && strict {
		return "", ErrNoHousehold
	}
	h := ""
	if hh != 0 {
		h = strconv.FormatInt(hh, 10)
	}
	s := "off"
	if sys {
		s = "on"
	}
	return fmt.Sprintf("SELECT set_config('app.household_id', '%s', true), set_config('app.system', '%s', true)", h, s), nil
}

// scopedConnector opens pgx connections that scope every statement to the
// context's household (see scopedConn).
type scopedConnector struct {
	inner  driver.Connector
	strict bool
}

func (c scopedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &scopedConn{Conn: conn.(*stdlib.Conn), strict: c.strict}, nil
}

func (c scopedConnector) Driver() driver.Driver { return c.inner.Driver() }

// scopedConn runs each statement in a transaction that starts by setting
// the household (SET LOCAL, so it works through a transaction-mode pooler
// such as PgBouncer):
//
//   - In a transaction (Store.WithTx), the household is set once, at BEGIN.
//   - Outside one, the statement gets its own: BEGIN and the setting in one
//     round trip, then the statement, then COMMIT, sent when its rows are
//     closed.
type scopedConn struct {
	*stdlib.Conn
	strict bool
	inTx   bool
	bad    bool
}

var (
	_ driver.ExecerContext  = (*scopedConn)(nil)
	_ driver.QueryerContext = (*scopedConn)(nil)
	_ driver.ConnBeginTx    = (*scopedConn)(nil)
	_ driver.Validator      = (*scopedConn)(nil)
)

// Prepare isn't used by sqlc's queries. A prepared statement would run
// outside the household scope, so it's refused.
func (c *scopedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("store: prepared statements aren't supported on Postgres")
}

func (c *scopedConn) PrepareContext(context.Context, string) (driver.Stmt, error) {
	return c.Prepare("")
}

func (c *scopedConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *scopedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	scope, err := scopeSQL(ctx, c.strict)
	if err != nil {
		return nil, err
	}
	tx, err := c.Conn.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if err := c.simple(ctx, scope); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	c.inTx = true
	return scopedTx{tx: tx, c: c}, nil
}

func (c *scopedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.inTx {
		return c.Conn.ExecContext(ctx, query, args)
	}
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	res, err := c.Conn.ExecContext(ctx, query, args)
	if err != nil {
		_ = c.end(ctx, "ROLLBACK") // a failure marks the connection bad
		return nil, err
	}
	if err := c.end(ctx, "COMMIT"); err != nil {
		return nil, err
	}
	return res, nil
}

func (c *scopedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.inTx {
		return c.Conn.QueryContext(ctx, query, args)
	}
	if err := c.begin(ctx); err != nil {
		return nil, err
	}
	rows, err := c.Conn.QueryContext(ctx, query, args)
	if err != nil {
		_ = c.end(ctx, "ROLLBACK") // a failure marks the connection bad
		return nil, err
	}
	return &scopedRows{Rows: rows.(*stdlib.Rows), c: c, ctx: context.WithoutCancel(ctx)}, nil
}

// IsValid lets database/sql drop a connection whose COMMIT failed.
func (c *scopedConn) IsValid() bool { return !c.bad && !c.Conn.Conn().IsClosed() }

func (c *scopedConn) begin(ctx context.Context) error {
	scope, err := scopeSQL(ctx, c.strict)
	if err != nil {
		return err
	}
	return c.simple(ctx, "BEGIN; "+scope)
}

func (c *scopedConn) end(ctx context.Context, verb string) error {
	if err := c.simple(context.WithoutCancel(ctx), verb); err != nil {
		c.bad = true
		return err
	}
	return nil
}

func (c *scopedConn) simple(ctx context.Context, sql string) error {
	_, err := c.Conn.Conn().PgConn().Exec(ctx, sql).ReadAll()
	return err
}

type scopedTx struct {
	tx driver.Tx
	c  *scopedConn
}

func (t scopedTx) Commit() error {
	t.c.inTx = false
	return t.tx.Commit()
}

func (t scopedTx) Rollback() error {
	t.c.inTx = false
	return t.tx.Rollback()
}

// scopedRows commits its statement's transaction when closed.
type scopedRows struct {
	*stdlib.Rows
	c      *scopedConn
	ctx    context.Context
	closed bool
}

func (r *scopedRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	err := r.Rows.Close()
	if cerr := r.c.end(r.ctx, "COMMIT"); err == nil {
		err = cerr
	}
	return err
}
