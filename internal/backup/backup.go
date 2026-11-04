// Package backup writes Domus backups: a gzipped tar of a consistent
// snapshot of the database plus the uploaded photos and avatars.
//
// An archive unpacks straight into the data directory:
//
//	domus.db
//	photos/...
//	avatars/...
//
// photo-cache/ is left out because Domus rebuilds it, and the read-only
// photos folder (DOMUS_PHOTOS_DIR) isn't Domus's to back up.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dirs are the folders under the data directory that are backed up.
var Dirs = []string{"photos", "avatars"}

const (
	prefix = "domus-"
	suffix = ".tar.gz"
)

// Run writes a backup of db and dataDir's Dirs into destDir, named after
// now, and then deletes all but the newest keep backups there. It returns
// the new archive's path. Nothing is left behind if it fails.
func Run(ctx context.Context, db *sql.DB, dataDir, destDir string, keep int, now time.Time) (string, error) {
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		return "", fmt.Errorf("backup dir: %w", err)
	}
	name := prefix + now.Format("20060102-150405") + suffix
	final := filepath.Join(destDir, name)

	snap := filepath.Join(destDir, "."+name+".db")
	_ = os.Remove(snap) // VACUUM INTO refuses to overwrite
	defer os.Remove(snap)
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", snap); err != nil {
		return "", fmt.Errorf("snapshot database: %w", err)
	}

	tmp := final + ".tmp"
	if err := writeArchive(ctx, tmp, snap, dataDir); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("finish backup: %w", err)
	}
	if err := Prune(destDir, keep); err != nil {
		return final, err
	}
	return final, nil
}

func writeArchive(ctx context.Context, path, snap, dataDir string) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // our own path in the backup dir; 0600 as it holds password hashes
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("close backup: %w", cerr)
		}
	}()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	if err := addFile(tw, snap, "domus.db"); err != nil {
		return err
	}
	for _, d := range Dirs {
		root := filepath.Join(dataDir, d)
		err := filepath.WalkDir(root, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && p == root {
					return fs.SkipDir
				}
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !e.Type().IsRegular() {
				return nil // directories are implied; skip links and the like
			}
			rel, err := filepath.Rel(dataDir, p)
			if err != nil {
				return err
			}
			return addFile(tw, p, filepath.ToSlash(rel))
		})
		if err != nil {
			return fmt.Errorf("back up %s: %w", d, err)
		}
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync backup: %w", err)
	}
	return nil
}

func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path) //nolint:gosec // our snapshot, or a file found by walking the data dir
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     name,
		Size:     fi.Size(),
		Mode:     0o600,
		ModTime:  fi.ModTime(),
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

// Check reports whether backups can be written to dir, so a bad mount
// shows up in the logs at startup rather than at 3am.
func Check(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".write-test-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

// List returns the backups in dir, oldest first.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && strings.HasPrefix(n, prefix) && strings.HasSuffix(n, suffix) {
			out = append(out, filepath.Join(dir, n))
		}
	}
	sort.Strings(out) // the timestamped names sort by age
	return out, nil
}

// Prune deletes all but the newest keep backups in dir. keep <= 0 keeps
// everything.
func Prune(dir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	all, err := List(dir)
	if err != nil {
		return err
	}
	for len(all) > keep {
		if err := os.Remove(all[0]); err != nil {
			return fmt.Errorf("prune backups: %w", err)
		}
		all = all[1:]
	}
	return nil
}

// Next returns the first moment after now at clock time hh:mm in loc. On a
// day where hh:mm doesn't exist (a spring-forward gap) it's the moment the
// clocks jump to.
func Next(now time.Time, loc *time.Location, hhmm string) time.Time {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		t = time.Date(0, 1, 1, 3, 0, 0, 0, time.UTC)
	}
	local := now.In(loc)
	for d := 0; d < 3; d++ {
		y, m, day := local.AddDate(0, 0, d).Date()
		at := time.Date(y, m, day, t.Hour(), t.Minute(), 0, 0, loc)
		if at.After(now) {
			return at
		}
	}
	return now.Add(24 * time.Hour)
}
