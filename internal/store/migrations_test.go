package store_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Postgres migrations only add things, so the previous version still runs
// on the new schema while a deploy rolls out, and after a rollback.
// Removing or renaming waits for a later release, once nothing uses the old
// thing, and is marked "-- contract: <why>". SQLite isn't checked: it
// rebuilds tables to change them, and a self-hosted server runs one version
// at a time.
func TestMigrationsOnlyAdd(t *testing.T) {
	files, err := filepath.Glob("migrations/postgres/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v, %v", files, err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range unsafeSQL(string(b)) {
			t.Errorf("%s: %s. Add things in one release and remove them in a later one, marked \"-- contract: <why>\"", f, p)
		}
	}
}

func TestUnsafeSQL(t *testing.T) {
	for sql, want := range map[string]int{
		"-- +goose Up\nALTER TABLE people ADD COLUMN nickname TEXT NOT NULL DEFAULT '';\n-- +goose Down\nALTER TABLE people DROP COLUMN nickname;\n":             0,
		"-- +goose Up\nCREATE TABLE pets (id INTEGER PRIMARY KEY);\nCREATE INDEX pets_id ON pets (id);\n":                                                        0,
		"-- +goose Up\nALTER TABLE people DROP COLUMN nickname;\n":                                                                                               1,
		"-- +goose Up\n-- contract: 00031 stopped using it\nALTER TABLE people DROP COLUMN nickname;\n":                                                          0,
		"-- +goose Up\nALTER TABLE people RENAME COLUMN name TO full_name;\n":                                                                                    1,
		"-- +goose Up\nALTER TABLE people ADD COLUMN age INTEGER NOT NULL;\n":                                                                                    1,
		"-- +goose Up\nALTER TABLE people ALTER COLUMN age SET NOT NULL;\n":                                                                                      1,
		"-- +goose Up\nALTER TABLE people ALTER COLUMN age TYPE BIGINT;\n":                                                                                       1,
		"-- +goose Up\nDROP TABLE pets;\nALTER TABLE people DROP CONSTRAINT people_name_key;\n":                                                                  2,
		"-- +goose Up\nALTER TABLE ledger DROP CONSTRAINT ledger_kind_check;\nALTER TABLE ledger ADD CONSTRAINT ledger_kind_check CHECK (kind IN ('a', 'b'));\n": 0,
	} {
		if got := unsafeSQL(sql); len(got) != want {
			t.Errorf("%q: %v, want %d", sql, got, want)
		}
	}
}

var (
	contract = regexp.MustCompile(`(?im)^\s*--\s*contract:\s*\S`)
	comment  = regexp.MustCompile(`--[^\n]*`)
	unsafe   = []struct {
		re   *regexp.Regexp
		what string
	}{
		{regexp.MustCompile(`(?i)\bDROP\s+(TABLE|COLUMN)\b`), "it drops something"},
		{regexp.MustCompile(`(?i)\bRENAME\b`), "it renames something"},
		{regexp.MustCompile(`(?i)\bALTER\s+COLUMN\s+\S+\s+(SET\s+DATA\s+)?TYPE\b`), "it changes a column's type"},
		{regexp.MustCompile(`(?i)\bSET\s+NOT\s+NULL\b`), "it makes a column required"},
	}
	dropConstraint = regexp.MustCompile(`(?i)\bDROP\s+CONSTRAINT\s+(IF\s+EXISTS\s+)?(\w+)`)
	required       = regexp.MustCompile(`(?i)\bADD\s+(COLUMN\s+)?\S+[^;]*\bNOT\s+NULL\b`)
	defaults       = regexp.MustCompile(`(?i)\bDEFAULT\b`)
)

// unsafeSQL says what in a migration's Up section the previous version
// couldn't run on.
func unsafeSQL(sql string) []string {
	up, _, _ := strings.Cut(sql, "-- +goose Down")
	if contract.MatchString(up) {
		return nil
	}
	up = comment.ReplaceAllString(up, "")
	var out []string
	// Dropping a constraint and adding it back, as a wider CHECK, is fine.
	for _, m := range dropConstraint.FindAllStringSubmatch(up, -1) {
		if !regexp.MustCompile(`(?i)\bADD\s+CONSTRAINT\s+` + m[2] + `\b`).MatchString(up) {
			out = append(out, "it drops a constraint")
		}
	}
	for stmt := range strings.SplitSeq(up, ";") {
		for _, u := range unsafe {
			if u.re.MatchString(stmt) {
				out = append(out, u.what)
			}
		}
		if required.MatchString(stmt) && !defaults.MatchString(stmt) {
			out = append(out, "it adds a required column with no default")
		}
	}
	return out
}
