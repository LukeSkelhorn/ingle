package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// rebind rewrites SQLite placeholders for Postgres: each bare ? becomes the
// next $n, and ?N (sqlc's numbered form) becomes $N. Text inside quotes and
// -- comments is left alone. The queries come from sqlc, so the set is
// small and fixed; results are cached.
func rebind(query string) string {
	if v, ok := rebound.Load(query); ok {
		return v.(string)
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(query) {
				if query[j] == c {
					if j+1 < len(query) && query[j+1] == c { // doubled quote
						j += 2
						continue
					}
					break
				}
				j++
			}
			b.WriteString(query[i:min(j+1, len(query))])
			i = j
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			j := strings.IndexByte(query[i:], '\n')
			if j < 0 {
				j = len(query) - i
			}
			b.WriteString(query[i : i+j])
			i += j - 1
		case c == '?':
			j := i + 1
			for j < len(query) && query[j] >= '0' && query[j] <= '9' {
				j++
			}
			b.WriteByte('$')
			if j > i+1 {
				b.WriteString(query[i+1 : j])
			} else {
				n++
				b.WriteString(strconv.Itoa(n))
			}
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	out := b.String()
	rebound.Store(query, out)
	return out
}

var rebound sync.Map // query string -> rebound string

// pgConn wraps a *sql.DB or *sql.Tx so sqlc's SQLite queries run on Postgres.
type pgConn struct{ db dbq.DBTX }

func (c pgConn) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return c.db.ExecContext(ctx, rebind(q), args...)
}

func (c pgConn) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	return c.db.PrepareContext(ctx, rebind(q))
}

func (c pgConn) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return c.db.QueryContext(ctx, rebind(q), args...)
}

func (c pgConn) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return c.db.QueryRowContext(ctx, rebind(q), args...)
}
