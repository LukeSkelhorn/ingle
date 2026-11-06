package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
)

func setup(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dir, "ingle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := store.New(db).EnsureHousehold(context.Background(), "The Smiths", time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"photos/a.jpg":      "photo",
		"photos/a_t.jpg":    "thumb",
		"avatars/1.png":     "face",
		"photo-cache/x.jpg": "cache",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return db, dir
}

func entries(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = b
	}
}

func TestRun(t *testing.T) {
	db, dir := setup(t)
	dest := filepath.Join(t.TempDir(), "backups")
	path, err := Run(context.Background(), db, dir, dest, 14, time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "ingle-20261104-030000.tar.gz" {
		t.Errorf("name = %s", filepath.Base(path))
	}
	got := entries(t, path)
	var names []string
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	want := []string{"avatars/1.png", "ingle.db", "photos/a.jpg", "photos/a_t.jpg"}
	if len(names) != len(want) {
		t.Fatalf("entries = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries = %v, want %v", names, want)
		}
	}
	if string(got["photos/a.jpg"]) != "photo" {
		t.Errorf("photo = %q", got["photos/a.jpg"])
	}

	// The snapshot is a working database with the household in it.
	restored := filepath.Join(t.TempDir(), "ingle.db")
	if err := os.WriteFile(restored, got["ingle.db"], 0o640); err != nil {
		t.Fatal(err)
	}
	rdb, err := store.Open(context.Background(), restored)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	var name string
	if err := rdb.QueryRowContext(context.Background(), "SELECT name FROM households").Scan(&name); err != nil || name != "The Smiths" {
		t.Errorf("restored household = %q, %v", name, err)
	}

	// Only the archive is left: no snapshot or .tmp.
	files, _ := os.ReadDir(dest)
	if len(files) != 1 {
		t.Errorf("backup dir has %d files, want 1", len(files))
	}
}

func TestPrune(t *testing.T) {
	db, dir := setup(t)
	dest := t.TempDir()
	start := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	for i := range 4 {
		if _, err := Run(context.Background(), db, dir, dest, 2, start.AddDate(0, 0, i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dest, "notes.txt"), nil, 0o640); err != nil {
		t.Fatal(err)
	}
	all, err := List(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || filepath.Base(all[0]) != "ingle-20261103-030000.tar.gz" || filepath.Base(all[1]) != "ingle-20261104-030000.tar.gz" {
		t.Errorf("kept %v", all)
	}
	if _, err := os.Stat(filepath.Join(dest, "notes.txt")); err != nil {
		t.Errorf("pruned a file that isn't a backup: %v", err)
	}
}

func TestRunFailureLeavesNothing(t *testing.T) {
	db, dir := setup(t)
	dest := t.TempDir()
	db.Close() // the snapshot fails
	if _, err := Run(context.Background(), db, dir, dest, 14, time.Now()); err == nil {
		t.Fatal("Run on a closed database succeeded")
	}
	files, _ := os.ReadDir(dest)
	if len(files) != 0 {
		t.Errorf("left %d files behind", len(files))
	}
}

func TestNext(t *testing.T) {
	london, _ := time.LoadLocation("Europe/London")
	for _, tc := range []struct {
		now  time.Time
		at   string
		want time.Time
	}{
		{time.Date(2026, 11, 4, 2, 0, 0, 0, london), "03:00", time.Date(2026, 11, 4, 3, 0, 0, 0, london)},
		{time.Date(2026, 11, 4, 3, 0, 0, 0, london), "03:00", time.Date(2026, 11, 5, 3, 0, 0, 0, london)},
		{time.Date(2026, 11, 4, 22, 0, 0, 0, london), "03:00", time.Date(2026, 11, 5, 3, 0, 0, 0, london)},
		// Spring forward: 01:30 doesn't exist on 29 March 2026.
		{time.Date(2026, 3, 29, 0, 30, 0, 0, london), "01:30", time.Date(2026, 3, 29, 2, 30, 0, 0, london)},
	} {
		if got := Next(tc.now, london, tc.at); !got.Equal(tc.want) {
			t.Errorf("Next(%v, %s) = %v, want %v", tc.now, tc.at, got, tc.want)
		}
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	if err := Check(filepath.Join(dir, "new")); err != nil {
		t.Errorf("Check(new dir) = %v", err)
	}
	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if err := Check(ro); err == nil {
			t.Error("Check(read-only dir) succeeded")
		}
	}
	files, _ := os.ReadDir(filepath.Join(dir, "new"))
	if len(files) != 0 {
		t.Errorf("Check left %d files", len(files))
	}
}

// Backups from before the rename to Ingle are still pruned, oldest first,
// alongside the new ones.
func TestPruneLegacyNames(t *testing.T) {
	db, dir := setup(t)
	dest := t.TempDir()
	for _, n := range []string{"domus-20261101-030000.tar.gz", "domus-20261102-030000.tar.gz"} {
		if err := os.WriteFile(filepath.Join(dest, n), nil, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := Run(context.Background(), db, dir, dest, 2, time.Date(2026, 11, 3, 3, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	all, err := List(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || filepath.Base(all[0]) != "domus-20261102-030000.tar.gz" || filepath.Base(all[1]) != "ingle-20261103-030000.tar.gz" {
		t.Errorf("kept %v", all)
	}
}
