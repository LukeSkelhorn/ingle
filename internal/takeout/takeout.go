// Package takeout exports a household's data and deletes it completely.
package takeout

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/lukeskelhorn/ingle/internal/avatar"
	"github.com/lukeskelhorn/ingle/internal/photos"
	"github.com/lukeskelhorn/ingle/internal/secrets"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Table says how one table is exported and deleted.
type Table struct {
	Name    string
	Key     string
	Skip    bool
	Omit    []string
	Secrets []string
}

// Tables lists every table, ordered so deleting in sequence never hits ON DELETE RESTRICT.
var Tables = []Table{
	{Name: "sessions", Key: "household_id", Skip: true},
	{Name: "devices", Key: "household_id", Omit: []string{"token_hash"}},
	{Name: "transfer_requests", Key: "household_id"},
	{Name: "interest_payments", Key: "household_id"},
	{Name: "match_entries", Key: "household_id"},
	{Name: "ledger_entries", Key: "household_id"},
	{Name: "completions", Key: "household_id"},
	{Name: "redemptions", Key: "household_id"},
	{Name: "chore_people", Key: "household_id"},
	{Name: "reward_people", Key: "household_id"},
	{Name: "chore_occurrences", Key: "household_id"},
	{Name: "holidays", Key: "household_id"},
	{Name: "countdown_people", Key: "household_id"},
	{Name: "countdowns", Key: "household_id"},
	{Name: "chores", Key: "household_id"},
	{Name: "person_bank", Key: "household_id"},
	{Name: "match_offers", Key: "household_id"},
	{Name: "activity", Key: "household_id"},
	{Name: "photos", Key: "household_id"},
	{Name: "calendar_feeds", Key: "household_id", Secrets: []string{"url"}},
	{Name: "household_lock", Key: "household_id"},
	{Name: "household_modules", Key: "household_id"},
	{Name: "household_settings", Key: "household_id", Omit: []string{"unlock_code_hash"}},
	{Name: "household_keys", Key: "household_id", Skip: true},
	{Name: "people", Key: "household_id", Omit: []string{"pin_hash", "password_hash"}},
	{Name: "rewards", Key: "household_id"},
	{Name: "households", Key: "id"},
}

// Global tables hold no household's data.
var Global = []string{"app_meta"}

// Service exports and deletes households.
type Service struct {
	st      *store.Store
	keys    *secrets.Keyring
	photos  *photos.Library
	avatars *avatar.Store
	log     *slog.Logger
	now     func() time.Time
}

// New returns a Service. keys may be nil (no master key).
func New(st *store.Store, keys *secrets.Keyring, lib *photos.Library, avatars *avatar.Store, log *slog.Logger) *Service {
	return &Service{st: st, keys: keys, photos: lib, avatars: avatars, log: log, now: time.Now}
}

// Export writes household hh's data and files to w as a zip, without credential hashes.
func (s *Service) Export(ctx context.Context, hh int64, w io.Writer) error {
	ctx = store.WithHousehold(ctx, hh)
	z := zip.NewWriter(w)
	if err := s.writeReadme(z); err != nil {
		return err
	}
	var photoFiles, avatarFiles []string
	for _, t := range Tables {
		if t.Skip {
			continue
		}
		rows, err := s.rows(ctx, t, hh)
		if err != nil {
			return err
		}
		for _, r := range rows {
			for _, c := range t.Omit {
				delete(r, c)
			}
			for _, c := range t.Secrets {
				if v, ok := r[c].(string); ok {
					if r[c], err = s.keys.Open(ctx, hh, v); err != nil {
						return fmt.Errorf("export %s.%s: %w", t.Name, c, err)
					}
				}
			}
			switch t.Name {
			case "photos":
				if f, ok := r["file"].(string); ok {
					photoFiles = append(photoFiles, f)
				}
			case "people":
				if v, ok := r["avatar"].(string); ok {
					if kind, name := avatar.Parse(&v); kind == avatar.Photo {
						avatarFiles = append(avatarFiles, name)
					}
				}
			}
		}
		f, err := z.Create("data/" + t.Name + ".json")
		if err != nil {
			return err
		}
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return fmt.Errorf("export %s: %w", t.Name, err)
		}
	}
	for _, name := range photoFiles {
		if err := s.copyIn(z, "photos/"+name+".jpg", func() (io.ReadCloser, error) {
			return s.photos.OpenUpload(ctx, hh, name, false)
		}); err != nil {
			return err
		}
	}
	for _, name := range avatarFiles {
		if err := s.copyIn(z, "avatars/"+name, func() (io.ReadCloser, error) {
			return s.avatars.Open(name)
		}); err != nil {
			return err
		}
	}
	return z.Close()
}

func (s *Service) writeReadme(z *zip.Writer) error {
	f, err := z.Create("README.txt")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(f, `Your household's data from Ingle, exported %s.

data/      one JSON file per table. Times are Unix seconds; amounts are in
           pence. Passwords, PINs and sign-in tokens aren't included.
photos/    photos uploaded for the screensaver.
avatars/   people's pictures.
`, s.now().UTC().Format(time.RFC3339))
	return err
}

func (s *Service) copyIn(z *zip.Writer, name string, open func() (io.ReadCloser, error)) error {
	r, err := open()
	if err != nil {
		s.log.Warn("export: file missing", slog.String("file", name), slog.Any("err", err))
		return nil
	}
	defer r.Close()
	f, err := z.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	return err
}

func (s *Service) rows(ctx context.Context, t Table, hh int64) ([]map[string]any, error) {
	rows, err := s.st.SQL().QueryContext(ctx, "SELECT * FROM "+t.Name+" WHERE "+t.Key+" = ?", hh)
	if err != nil {
		return nil, fmt.Errorf("export %s: %w", t.Name, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("export %s: %w", t.Name, err)
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Delete removes household hh's rows, key and files, recreating it empty if recreate is set.
func (s *Service) Delete(ctx context.Context, hh int64, recreate bool) error {
	ctx = store.WithHousehold(ctx, hh)
	var photoFiles, avatarFiles []string
	for _, t := range Tables {
		switch t.Name {
		case "photos", "people":
		default:
			continue
		}
		rows, err := s.rows(ctx, t, hh)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if f, ok := r["file"].(string); ok && t.Name == "photos" {
				photoFiles = append(photoFiles, f)
			}
			if v, ok := r["avatar"].(string); ok {
				if kind, name := avatar.Parse(&v); kind == avatar.Photo {
					avatarFiles = append(avatarFiles, name)
				}
			}
		}
	}

	err := s.st.WithSQLTx(ctx, func(db dbq.DBTX) error {
		for _, t := range Tables {
			if _, err := db.ExecContext(ctx, "DELETE FROM "+t.Name+" WHERE "+t.Key+" = ?", hh); err != nil {
				return fmt.Errorf("delete %s: %w", t.Name, err)
			}
		}
		if recreate {
			if _, err := db.ExecContext(ctx, "INSERT INTO households (id, name, created_at) VALUES (?, 'Home', ?)", hh, s.now().Unix()); err != nil {
				return fmt.Errorf("recreate household: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.keys.Forget(hh)
	for _, name := range photoFiles {
		s.photos.RemoveFiles(ctx, name)
	}
	for _, name := range avatarFiles {
		if err := s.avatars.Remove(name); err != nil {
			s.log.Warn("delete household: avatar file", slog.Any("err", err))
		}
	}
	s.log.Info("household deleted", slog.Int64("household", hh), slog.Bool("recreated", recreate),
		slog.Int("photos", len(photoFiles)), slog.Int("avatars", len(avatarFiles)))
	return nil
}
