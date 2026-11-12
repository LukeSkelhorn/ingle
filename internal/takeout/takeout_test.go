package takeout_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/avatar"
	"github.com/lukeskelhorn/ingle/internal/photos"
	"github.com/lukeskelhorn/ingle/internal/secrets"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/store/storetest"
	"github.com/lukeskelhorn/ingle/internal/takeout"
)

type fixture struct {
	t       *testing.T
	st      *store.Store
	svc     *takeout.Service
	keys    *secrets.Keyring
	lib     *photos.Library
	avatars *avatar.Store
	data    string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	data := t.TempDir()
	st := store.New(storetest.OpenScoped(t, filepath.Join(data, "ingle.db")))
	master := make([]byte, secrets.KeySize)
	_, _ = rand.Read(master)
	keys, err := secrets.New(st, master)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	lib, err := photos.New(st, data, "", log)
	if err != nil {
		t.Fatal(err)
	}
	avatars, err := avatar.NewStore(filepath.Join(data, "avatars"))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, st: st, svc: takeout.New(st, keys, lib, avatars, log), keys: keys, lib: lib, avatars: avatars, data: data}
}

type seeded struct {
	id               int64
	photo, avatar    string
	calendar, person string
}

// household makes a household with a little of everything: an adult with
// a password, PIN and picture, a child, a chore done and paid, a display
// with a session, a calendar link and a photo.
func (f *fixture) household(name string) seeded {
	t := f.t
	t.Helper()
	now := time.Now().Unix()
	h, err := f.st.Q.CreateHousehold(store.AsSystem(t.Context()), dbq.CreateHouseholdParams{Name: name, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	ctx := store.WithHousehold(t.Context(), h.ID)
	pic, err := f.avatars.SavePhoto(bytes.NewReader(jpegBytes(t)))
	if err != nil {
		t.Fatal(err)
	}
	pin, pw, av := "pinhash-"+name, "pwhash-"+name, avatar.PhotoValue(pic)
	email := strings.ToLower(name) + "@example.org"
	adult, err := f.st.Q.CreatePerson(ctx, dbq.CreatePersonParams{
		HouseholdID: h.ID, Name: "Adult " + name, Kind: "adult", Colour: "#4A90D9", Avatar: &av, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	four := int64(4)
	if err := f.st.Q.SetPersonPIN(ctx, dbq.SetPersonPINParams{PinHash: &pin, PinLength: &four, HouseholdID: h.ID, ID: adult.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Q.SetPersonLogin(ctx, dbq.SetPersonLoginParams{Email: &email, PasswordHash: &pw, HouseholdID: h.ID, ID: adult.ID}); err != nil {
		t.Fatal(err)
	}
	kid, err := f.st.Q.CreatePerson(ctx, dbq.CreatePersonParams{HouseholdID: h.ID, Name: "Kid " + name, Kind: "child", Colour: "#46A758", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	sql := f.st.SQL()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := sql.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO household_settings (household_id, timezone, updated_at) VALUES (?, 'Europe/London', ?)`, h.ID, now)
	exec(`INSERT INTO chores (household_id, title, mode, assignee_id, starts_on, points, created_at, updated_at) VALUES (?, 'Chore '||?, 'assigned', ?, '2026-11-12', 2, ?, ?)`, h.ID, name, kid.ID, now, now)
	exec(`INSERT INTO chore_occurrences (household_id, chore_id, due_on, assignee_id, status) SELECT household_id, id, '2026-11-12', assignee_id, 'done' FROM chores WHERE household_id = ?`, h.ID)
	exec(`INSERT INTO completions (household_id, occurrence_id, person_id, completed_at, status, points, pence) SELECT household_id, id, assignee_id, ?, 'approved', 2, 0 FROM chore_occurrences WHERE household_id = ?`, now, h.ID)
	exec(`INSERT INTO ledger_entries (household_id, person_id, points, pence, kind, completion_id, created_at) SELECT household_id, person_id, 2, 0, 'completion', id, ? FROM completions WHERE household_id = ?`, now, h.ID)
	exec(`INSERT INTO rewards (household_id, title, cost_points, created_at) VALUES (?, 'Treat', 5, ?)`, h.ID, now)
	exec(`INSERT INTO activity (household_id, actor_id, verb, subject_type, subject_id, created_at) VALUES (?, ?, 'person.created', 'person', ?, ?)`, h.ID, adult.ID, kid.ID, now)

	dev, err := f.st.Q.CreateDevice(ctx, dbq.CreateDeviceParams{HouseholdID: h.ID, Name: "Kitchen", TokenHash: []byte("devtoken-" + name), CreatedAt: now, LastSeenAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.Q.CreateSession(ctx, dbq.CreateSessionParams{TokenHash: []byte("session-" + name), HouseholdID: h.ID, PersonID: adult.ID, Kind: "display", DeviceID: &dev.ID, CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	cal := "https://calendar.example/private-" + name + "/basic.ics"
	sealed, err := f.keys.Seal(ctx, h.ID, cal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Q.CreateCalendarFeed(ctx, dbq.CreateCalendarFeedParams{HouseholdID: h.ID, Name: "Family", Url: sealed, Colour: "#4A90D9", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p, err := f.lib.Upload(ctx, h.ID, adult.ID, bytes.NewReader(jpegBytes(t)))
	if err != nil {
		t.Fatal(err)
	}
	return seeded{id: h.ID, photo: p.File, avatar: pic, calendar: cal, person: "Kid " + name}
}

func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 64, 48)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestExport(t *testing.T) {
	f := newFixture(t)
	a, b := f.household("Smiths"), f.household("Joneses")

	var buf bytes.Buffer
	if err := f.svc.Export(t.Context(), a.id, &buf); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, zf := range z.File {
		r, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r)
		_ = r.Close()
		files[zf.Name] = string(body)
	}

	all := strings.Join(slices.Collect(func(yield func(string) bool) {
		for _, v := range files {
			if !yield(v) {
				return
			}
		}
	}), "\n")
	for _, want := range []string{a.person, "Chore Smiths", a.calendar, "Kitchen"} {
		if !strings.Contains(all, want) {
			t.Errorf("export is missing %q", want)
		}
	}
	for _, absent := range []string{"Joneses", b.person, "pinhash", "pwhash", "devtoken", "session-", "enc:v1:", "wrapped_key"} {
		if strings.Contains(all, absent) {
			t.Errorf("export has %q", absent)
		}
	}
	if _, ok := files["data/sessions.json"]; ok {
		t.Error("export has sessions")
	}
	if _, ok := files["data/household_keys.json"]; ok {
		t.Error("export has the household's key")
	}
	var people []map[string]any
	if err := json.Unmarshal([]byte(files["data/people.json"]), &people); err != nil || len(people) != 2 {
		t.Fatalf("people.json = %d people, %v", len(people), err)
	}
	for _, p := range people {
		if _, ok := p["pin_hash"]; ok {
			t.Errorf("person %v has pin_hash", p["name"])
		}
	}
	if len(files["photos/"+a.photo+".jpg"]) == 0 || len(files["avatars/"+a.avatar]) == 0 {
		t.Errorf("export is missing the photo or avatar picture; files: %v", slices.Sorted(func(yield func(string) bool) {
			for k := range files {
				if !yield(k) {
					return
				}
			}
		}))
	}
}

func TestDelete(t *testing.T) {
	f := newFixture(t)
	a, b := f.household("Smiths"), f.household("Joneses")
	ctx := t.Context()
	before := f.counts(ctx, b.id)

	if err := f.svc.Delete(ctx, a.id, false); err != nil {
		t.Fatal(err)
	}
	for table, n := range f.counts(ctx, a.id) {
		if n != 0 {
			t.Errorf("%s: %d rows left for the deleted household", table, n)
		}
	}
	if after := f.counts(ctx, b.id); !mapsEqual(before, after) {
		t.Errorf("the other household changed:\n before %v\n after  %v", before, after)
	}
	for _, p := range []string{filepath.Join(f.data, "photos", a.photo+".jpg"), filepath.Join(f.data, "avatars", a.avatar)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists (%v)", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(f.data, "avatars", b.avatar)); err != nil {
		t.Errorf("the other household's avatar: %v", err)
	}
	// The other household's secrets still open.
	feeds, _ := f.st.Q.ListCalendarFeeds(store.WithHousehold(ctx, b.id), b.id)
	if len(feeds) != 1 {
		t.Fatalf("B's feeds = %d", len(feeds))
	}
	if got, err := f.keys.Open(ctx, b.id, feeds[0].Url); err != nil || got != b.calendar {
		t.Errorf("B's calendar = %q, %v", got, err)
	}
}

func TestDeleteAndRecreate(t *testing.T) {
	f := newFixture(t)
	a := f.household("Smiths")
	ctx := store.WithHousehold(t.Context(), a.id)
	if err := f.svc.Delete(ctx, a.id, true); err != nil {
		t.Fatal(err)
	}
	h, err := f.st.Q.GetHousehold(ctx, a.id)
	if err != nil || h.Name != "Home" {
		t.Fatalf("recreated household = %+v, %v", h, err)
	}
	if people, _ := f.st.Q.ListPeople(ctx, a.id); len(people) != 0 {
		t.Errorf("recreated household has %d people", len(people))
	}
	// Its new key is a different one: the old secrets don't open.
	if _, err := f.keys.Seal(ctx, a.id, "new"); err != nil {
		t.Fatal(err)
	}
}

// counts is each table's rows for hh, read as hh.
func (f *fixture) counts(ctx context.Context, hh int64) map[string]int {
	f.t.Helper()
	ctx = store.WithHousehold(ctx, hh)
	out := map[string]int{}
	for _, tb := range takeout.Tables {
		var n int
		if err := f.st.SQL().QueryRowContext(ctx, "SELECT count(*) FROM "+tb.Name+" WHERE "+tb.Key+" = ?", hh).Scan(&n); err != nil {
			f.t.Fatalf("%s: %v", tb.Name, err)
		}
		out[tb.Name] = n
	}
	return out
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestEveryTableListed fails when a table is added without saying how it's
// exported and deleted: add it to takeout.Tables (or Global).
func TestEveryTableListed(t *testing.T) {
	f := newFixture(t)
	q := `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version'`
	if f.st.Engine == store.Postgres {
		q = `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name <> 'goose_db_version'`
	}
	rows, err := f.st.SQL().QueryContext(store.AsSystem(t.Context()), q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var listed []string
	for _, tb := range takeout.Tables {
		listed = append(listed, tb.Name)
	}
	listed = append(listed, takeout.Global...)
	n := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		n++
		if !slices.Contains(listed, name) {
			t.Errorf("table %s isn't in takeout.Tables", name)
		}
	}
	if n != len(listed) {
		t.Errorf("%d tables in the schema, %d listed", n, len(listed))
	}
}
