// Package store owns the SQLite database connection and schema migrations.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens (creating if needed) the SQLite database at path, applies
// recommended pragmas and runs all pending migrations.
func Open(ctx context.Context, path string) (*sql.DB, error) {
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

// Migrate applies all pending embedded migrations.
func Migrate(ctx context.Context, db *sql.DB) error {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("migrations fs: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
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
	DB *sql.DB
	Q  *dbq.Queries
}

// New wraps an open database.
func New(db *sql.DB) *Store {
	return &Store{DB: db, Q: dbq.New(db)}
}

// WithTx runs fn inside a transaction, committing if fn returns nil and
// rolling back otherwise.
func (s *Store) WithTx(ctx context.Context, fn func(q *dbq.Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := fn(s.Q.WithTx(tx)); err != nil {
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
