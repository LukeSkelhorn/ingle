// Package store owns the database connection and schema migrations.
//
// Self-hosted installs use SQLite. Hosted uses Postgres: the same sqlc
// queries run on both, with placeholders rewritten for Postgres (see
// rebind.go), and each engine has its own migrations.
package store

import (
	"cmp"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib" // also registers the "pgx" driver
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrations embed.FS

// Engine is the database behind a Store.
type Engine int

// The engines: SQLite for self-hosted, Postgres for Hosted.
const (
	SQLite Engine = iota
	Postgres
)

func (e Engine) String() string {
	if e == Postgres {
		return "postgres"
	}
	return "sqlite"
}

// IsPostgresURL reports whether dsn names a Postgres database rather than
// a SQLite file.
func IsPostgresURL(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// EngineOf reports which engine db was opened with.
func EngineOf(db *sql.DB) Engine {
	if fmt.Sprintf("%T", db.Driver()) == "*stdlib.Driver" {
		return Postgres
	}
	return SQLite
}

// Open opens the database and runs all pending migrations. dsn is either a
// postgres:// URL or the path of a SQLite file, which is created if needed.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	if IsPostgresURL(dsn) {
		return openPostgres(ctx, dsn)
	}
	return openSQLite(ctx, strings.TrimPrefix(dsn, "sqlite:"))
}

// PostgresOptions configures OpenPostgres.
type PostgresOptions struct {
	// URL is what the app connects as. In production that's a role that
	// isn't the tables' owner and can't bypass row-level security.
	URL string
	// MigrateURL, if set, is the owner role that runs migrations. The URL's
	// role is then granted what the app needs on the tables. Empty means
	// URL runs them (local development).
	MigrateURL string
	// AllowUnscoped lets statements run with no household in their context.
	// It's for tests of other packages that connect as a superuser, which
	// skips row-level security anyway. Never set it in the app.
	AllowUnscoped bool
}

func openPostgres(ctx context.Context, url string) (*sql.DB, error) {
	return OpenPostgres(ctx, PostgresOptions{URL: url})
}

// OpenPostgres runs pending migrations, then returns a pool whose every
// statement is scoped to its context's household (see household.go).
func OpenPostgres(ctx context.Context, o PostgresOptions) (*sql.DB, error) {
	app, err := pgx.ParseConfig(o.URL)
	if err != nil {
		return nil, fmt.Errorf("postgres url: %w", err)
	}
	ownerURL := cmp.Or(o.MigrateURL, o.URL)
	owner, err := sql.Open("pgx", ownerURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	defer owner.Close()
	if err := owner.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if err := Migrate(ctx, owner); err != nil {
		return nil, err
	}
	if o.MigrateURL != "" {
		if err := grantApp(ctx, owner, app.User); err != nil {
			return nil, err
		}
	}

	db := sql.OpenDB(scopedConnector{inner: stdlib.GetConnector(*app), strict: !o.AllowUnscoped})
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return db, nil
}

// grantApp gives role what the app needs: rows in every table (row-level
// security still applies) but not the migrations table.
func grantApp(ctx context.Context, owner *sql.DB, role string) error {
	var schema string
	if err := owner.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return fmt.Errorf("current schema: %w", err)
	}
	sc, r := pgx.Identifier{schema}.Sanitize(), pgx.Identifier{role}.Sanitize() //nolint:misspell // pgx's name
	for _, q := range []string{
		"GRANT USAGE ON SCHEMA " + sc + " TO " + r,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA " + sc + " TO " + r,
		"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA " + sc + " TO " + r,
		"REVOKE ALL ON goose_db_version FROM " + r,
	} {
		if _, err := owner.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("grant to %s: %w", role, err)
		}
	}
	return nil
}

// CanBypassRLS reports whether db's role skips row-level security (a
// superuser, or a role with BYPASSRLS). The app's role shouldn't.
func CanBypassRLS(ctx context.Context, db *sql.DB) (bool, error) {
	if EngineOf(db) != Postgres {
		return false, nil
	}
	var bypass bool
	err := db.QueryRowContext(AsSystem(ctx),
		"SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user").Scan(&bypass)
	return bypass, err
}

func openSQLite(ctx context.Context, path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}

	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite allows a single writer; one connection avoids SQLITE_BUSY
	// surprises for a small household app.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := Migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Migrate applies all pending embedded migrations for db's engine.
func Migrate(ctx context.Context, db *sql.DB) error {
	dir, dialect := "migrations/sqlite", goose.DialectSQLite3
	if EngineOf(db) == Postgres {
		dir, dialect = "migrations/postgres", goose.DialectPostgres
	}
	sub, err := fs.Sub(migrations, dir)
	if err != nil {
		return fmt.Errorf("migrations fs: %w", err)
	}
	provider, err := goose.NewProvider(dialect, db, sub)
	if err != nil {
		return fmt.Errorf("goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// Store bundles the database handle with the sqlc-generated queries.
type Store struct {
	DB     *sql.DB
	Q      *dbq.Queries
	Engine Engine
}

// New wraps an open database.
func New(db *sql.DB) *Store {
	s := &Store{DB: db, Engine: EngineOf(db)}
	s.Q = dbq.New(s.conn(db))
	return s
}

// SQL runs hand-written SQL (written for SQLite, with ? placeholders) on
// either engine. Prefer sqlc queries; this is for tests and tools.
func (s *Store) SQL() dbq.DBTX { return s.conn(s.DB) }

// conn adapts db (a *sql.DB or *sql.Tx) to the store's engine.
func (s *Store) conn(db dbq.DBTX) dbq.DBTX {
	if s.Engine == Postgres {
		return pgConn{db: db}
	}
	return db
}

// WithTx runs fn inside a transaction, committing if fn returns nil and
// rolling back otherwise.
func (s *Store) WithTx(ctx context.Context, fn func(q *dbq.Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(dbq.New(s.conn(tx))); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// WithSQLTx is WithTx for hand-written SQL (with ? placeholders), e.g.
// statements over every table.
func (s *Store) WithSQLTx(ctx context.Context, fn func(db dbq.DBTX) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(s.conn(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// EnsureHousehold returns the first household, creating one called name if
// the database has none. Self-hosted installs have exactly one household.
func (s *Store) EnsureHousehold(ctx context.Context, name string, now time.Time) (dbq.Household, error) {
	ctx = AsSystem(ctx)
	h, err := s.Q.GetFirstHousehold(ctx)
	if err == nil {
		return h, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return dbq.Household{}, fmt.Errorf("get household: %w", err)
	}
	h, err = s.Q.CreateHousehold(ctx, dbq.CreateHouseholdParams{Name: name, CreatedAt: now.Unix()})
	if err != nil {
		return dbq.Household{}, fmt.Errorf("create household: %w", err)
	}
	return h, nil
}

// Activity describes one row for the activity log.
type Activity struct {
	HouseholdID int64
	ActorID     *int64
	Verb        string // e.g. "person.created"
	SubjectType string // e.g. "person"
	SubjectID   int64
	At          time.Time
}

// LogActivity writes an activity row using q (usually inside a transaction).
func LogActivity(ctx context.Context, q *dbq.Queries, a Activity) error {
	if err := q.InsertActivity(ctx, dbq.InsertActivityParams{
		HouseholdID: a.HouseholdID,
		ActorID:     a.ActorID,
		Verb:        a.Verb,
		SubjectType: a.SubjectType,
		SubjectID:   a.SubjectID,
		CreatedAt:   a.At.Unix(),
	}); err != nil {
		return fmt.Errorf("activity %s: %w", a.Verb, err)
	}
	return nil
}
