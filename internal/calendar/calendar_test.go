package calendar

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/secrets"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/store/storetest"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

var london, _ = time.LoadLocation("Europe/London")

func ics(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		strings.ReplaceAll(strings.Join(events, ""), "\n", "\r\n") + "END:VCALENDAR\r\n"
}

func expand(t *testing.T, cal string, from, to time.Time) []Event {
	t.Helper()
	c, err := Parse(strings.NewReader(cal))
	if err != nil {
		t.Fatal(err)
	}
	return Expand(c, from, to, london)
}

func titles(es []Event) string {
	var out []string
	for _, e := range es {
		out = append(out, e.Title+"@"+e.Start.In(london).Format("02 15:04"))
	}
	return strings.Join(out, ", ")
}

func day(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, london) }

func TestExpandSingleAndAllDay(t *testing.T) {
	cal := ics(`BEGIN:VEVENT
UID:a
DTSTART;TZID=Europe/London:20261024T090000
DTEND;TZID=Europe/London:20261024T100000
SUMMARY:Swimming
LOCATION: Leisure centre\, pool 2
END:VEVENT
`, `BEGIN:VEVENT
UID:b
DTSTART:20261025T090000Z
DURATION:PT1H
SUMMARY:After the clocks change
END:VEVENT
`, `BEGIN:VEVENT
UID:c
DTSTART;VALUE=DATE:20261024
DTEND;VALUE=DATE:20261025
SUMMARY:Grandma visiting
END:VEVENT
`, `BEGIN:VEVENT
UID:d
DTSTART;TZID=Europe/London:20261024T120000
SUMMARY:Cancelled lunch
STATUS:CANCELLED
END:VEVENT
`, `BEGIN:VEVENT
UID:e
DTSTART;TZID=GMT Standard Time:20261024T150000
DTEND;TZID=GMT Standard Time:20261024T160000
SUMMARY:Outlook meeting
END:VEVENT
`, `BEGIN:VEVENT
UID:f
DTSTART;TZID=Europe/London:20261027T090000
SUMMARY:Next week
END:VEVENT
`)
	got := expand(t, cal, day(24), day(26))
	// 09:00Z on 25 Oct is 09:00 GMT (clocks went back at 02:00).
	want := "Grandma visiting@24 00:00, Swimming@24 09:00, Outlook meeting@24 15:00, After the clocks change@25 09:00"
	if titles(got) != want {
		t.Errorf("got  %s\nwant %s", titles(got), want)
	}
	if !got[0].AllDay || got[1].AllDay {
		t.Errorf("all-day flags wrong: %+v", got[:2])
	}
	if got[1].Location != "Leisure centre, pool 2" || got[0].Location != "" {
		t.Errorf("locations = %q, %q", got[0].Location, got[1].Location)
	}
	if d := got[1].End.Sub(got[1].Start); d != time.Hour {
		t.Errorf("Swimming lasts %v", d)
	}
}

func TestExpandRecurring(t *testing.T) {
	cal := ics(`BEGIN:VEVENT
UID:club
DTSTART;TZID=Europe/London:20260901T160000
DTEND;TZID=Europe/London:20260901T170000
RRULE:FREQ=DAILY;UNTIL=20261231T235959Z
EXDATE;TZID=Europe/London:20261023T160000,20261026T160000
SUMMARY:Football club
END:VEVENT
`, `BEGIN:VEVENT
UID:club
RECURRENCE-ID;TZID=Europe/London:20261024T160000
DTSTART;TZID=Europe/London:20261024T180000
DTEND;TZID=Europe/London:20261024T190000
SUMMARY:Football club (late)
END:VEVENT
`, `BEGIN:VEVENT
UID:club
RECURRENCE-ID;TZID=Europe/London:20261025T160000
DTSTART;TZID=Europe/London:20261025T160000
SUMMARY:Football club
STATUS:CANCELLED
END:VEVENT
`, `BEGIN:VEVENT
UID:bins
DTSTART;VALUE=DATE:20261001
RRULE:FREQ=WEEKLY;BYDAY=TU
SUMMARY:Bin day
END:VEVENT
`)
	got := expand(t, cal, day(22), day(28))
	// 16:00 local stays 16:00 across the clocks going back on the 25th.
	want := "Football club@22 16:00, Football club (late)@24 18:00, Bin day@27 00:00, Football club@27 16:00"
	if titles(got) != want {
		t.Errorf("got  %s\nwant %s", titles(got), want)
	}
}

type fixture struct {
	st  *store.Store
	svc *Service
	hh  int64
	mum dbq.Person
}

func newFixture(t *testing.T, client *http.Client) *fixture {
	t.Helper()
	ctx := t.Context()
	db := storetest.Open(t, filepath.Join(t.TempDir(), "ingle.db"))
	st := store.New(db)
	h, err := st.EnsureHousehold(ctx, "Home", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	mum, err := st.Q.CreatePerson(ctx, dbq.CreatePersonParams{HouseholdID: h.ID, Name: "Mum", Kind: "adult", Colour: "#4A90D9", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(st, func(context.Context, int64) *time.Location { return london }, client, slog.New(slog.DiscardHandler))
	svc.SetClock(func() time.Time { return time.Date(2026, 10, 24, 8, 0, 0, 0, london) })
	return &fixture{st: st, svc: svc, hh: h.ID, mum: mum}
}

func TestNormaliseURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"webcal://p01-caldav.icloud.com/published/2/abc", "https://p01-caldav.icloud.com/published/2/abc"},
		{"  https://calendar.google.com/x/basic.ics\n", "https://calendar.google.com/x/basic.ics"},
		{"WEBCAL://Example.com/a.ics", "https://Example.com/a.ics"},
	} {
		if got, err := NormaliseURL(tc.in); err != nil || got != tc.want {
			t.Errorf("NormaliseURL(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "file:///etc/passwd", "ftp://x/y.ics", "https://", "calendar.google.com/x.ics"} {
		if _, err := NormaliseURL(bad); err == nil {
			t.Errorf("NormaliseURL(%q) accepted", bad)
		}
	}
	if m := Mask("https://calendar.google.com/calendar/ical/secret/basic.ics"); m != "calendar.google.com/…" {
		t.Errorf("Mask = %q", m)
	}
}

func TestFeeds(t *testing.T) {
	var body atomic.Value
	body.Store(ics(`BEGIN:VEVENT
UID:a
DTSTART;TZID=Europe/London:20261024T090000
DTEND;TZID=Europe/London:20261024T100000
SUMMARY:Swimming
END:VEVENT
`))
	var status atomic.Int32
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "Ingle" {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/big.ics":
			_, _ = w.Write([]byte(strings.Repeat("x", MaxFeedBytes+1)))
		case "/html":
			_, _ = w.Write([]byte("<html>sign in</html>"))
		default:
			w.WriteHeader(int(status.Load()))
			_, _ = fmt.Fprint(w, body.Load())
		}
	}))
	defer srv.Close()
	f := newFixture(t, srv.Client())
	ctx := t.Context()

	for path, want := range map[string]string{
		"/big.ics": "too big", "/html": "isn't an iCal", "/missing": "",
	} {
		if path == "/missing" {
			status.Store(http.StatusNotFound)
			want = "404"
		}
		_, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "Family", srv.URL+path, nil)
		var ve valid.Errors
		if !errors.As(err, &ve) || !strings.Contains(ve["url"], want) {
			t.Errorf("AddFeed(%s) = %v, want url error containing %q", path, err, want)
		}
		if strings.Contains(fmt.Sprint(err), srv.URL) {
			t.Errorf("error %v leaks the URL", err)
		}
	}
	status.Store(http.StatusOK)

	feed, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "Family", srv.URL+"/family.ics", nil)
	if err != nil {
		t.Fatalf("AddFeed: %v", err)
	}
	from, to := day(24), day(26)
	if got := f.svc.Upcoming(f.hh, from, to); len(got) != 1 || got[0].Title != "Swimming" || got[0].Colour != feed.Colour {
		t.Fatalf("Upcoming = %+v", got)
	}
	if got := f.svc.Upcoming(f.hh+1, from, to); len(got) != 0 {
		t.Errorf("other household sees %+v", got)
	}

	// A failing refresh keeps the last good events and records the error.
	status.Store(http.StatusInternalServerError)
	if err := f.svc.RefreshHousehold(ctx, f.hh); err != nil {
		t.Fatal(err)
	}
	feeds, _ := f.svc.Feeds(ctx, f.hh)
	if len(feeds) != 1 || !strings.Contains(feeds[0].Error, "500") || feeds[0].FetchedAt.IsZero() ||
		strings.Contains(feeds[0].Masked, "family.ics") {
		t.Errorf("Feeds = %+v", feeds)
	}
	if got := f.svc.Upcoming(f.hh, from, to); len(got) != 1 {
		t.Errorf("events lost on a failed refresh: %+v", got)
	}

	// A good refresh picks up changes and clears the error.
	status.Store(http.StatusOK)
	body.Store(ics())
	if err := f.svc.RefreshHousehold(ctx, f.hh); err != nil {
		t.Fatal(err)
	}
	feeds, _ = f.svc.Feeds(ctx, f.hh)
	if feeds[0].Error != "" || len(f.svc.Upcoming(f.hh, from, to)) != 0 {
		t.Errorf("after good refresh: %+v", feeds)
	}

	if err := f.svc.RemoveFeed(ctx, f.hh, f.mum.ID, feed.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RemoveFeed(ctx, f.hh, f.mum.ID, feed.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second RemoveFeed = %v", err)
	}
}

// The cache covers the board calendar's range: weeks back, months ahead.
func TestFeedKeepsMonths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, ics(`BEGIN:VEVENT
UID:swim
DTSTART;TZID=Europe/London:20260801T090000
DTEND;TZID=Europe/London:20260801T100000
RRULE:FREQ=WEEKLY
SUMMARY:Swimming
END:VEVENT
`))
	}))
	defer srv.Close()
	f := newFixture(t, srv.Client())
	if _, err := f.svc.AddFeed(t.Context(), f.hh, f.mum.ID, "Family", srv.URL+"/family.ics", nil); err != nil {
		t.Fatal(err)
	}
	// Now is 24 Oct; 5 weeks back and 13 weeks ahead are both cached.
	for _, d := range []time.Time{time.Date(2026, 9, 19, 0, 0, 0, 0, london), time.Date(2027, 1, 23, 0, 0, 0, 0, london)} {
		got := f.svc.Upcoming(f.hh, d, d.AddDate(0, 0, 1))
		if len(got) != 1 || got[0].Feed != "Family" {
			t.Errorf("Upcoming(%s) = %+v, want Swimming from Family", d.Format("2 Jan"), got)
		}
	}
}

// With a master key, a feed's address is stored encrypted, and one saved
// before the key was set is encrypted on the next refresh.
func TestFeedAddressEncrypted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = io.WriteString(w, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n")
	}))
	defer srv.Close()
	f := newFixture(t, srv.Client())
	ctx := t.Context()
	secret := srv.URL + "/private-abc123/basic.ics"

	// Saved before there was a key: stored as it is.
	if _, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "Old", secret, nil); err != nil {
		t.Fatal(err)
	}

	master := make([]byte, secrets.KeySize)
	_, _ = rand.Read(master)
	k, err := secrets.New(f.st, master)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SetSecrets(k)
	if _, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "New", secret, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.RefreshHousehold(ctx, f.hh); err != nil {
		t.Fatal(err)
	}

	rows, err := f.st.Q.ListCalendarFeeds(ctx, f.hh)
	if err != nil || len(rows) != 2 {
		t.Fatalf("feeds = %d, %v", len(rows), err)
	}
	for _, r := range rows {
		if !secrets.IsSealed(r.Url) || strings.Contains(r.Url, "abc123") {
			t.Errorf("%s stored as %q, want it encrypted", r.Name, r.Url)
		}
	}
	feeds, err := f.svc.Feeds(ctx, f.hh)
	if err != nil || len(feeds) != 2 {
		t.Fatalf("Feeds = %v, %v", feeds, err)
	}
	for _, fd := range feeds {
		if fd.Error != "" || fd.FetchedAt.IsZero() || !strings.HasPrefix(fd.Masked, strings.TrimPrefix(srv.URL, "https://")) {
			t.Errorf("feed %+v: want fetched and masked from the real address", fd)
		}
	}
}

// A calendar can be one person's or the family's; the owner is checked, and
// editing or archiving changes the cached events straight away.
func TestFeedOwner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, ics(`BEGIN:VEVENT
UID:a
DTSTART;TZID=Europe/London:20261024T090000
DTEND;TZID=Europe/London:20261024T100000
SUMMARY:Meeting
END:VEVENT
`))
	}))
	defer srv.Close()
	f := newFixture(t, srv.Client())
	ctx := t.Context()
	dad, err := f.st.Q.CreatePerson(ctx, dbq.CreatePersonParams{HouseholdID: f.hh, Name: "Dad", Kind: "adult", Colour: "#E5533D", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.st.Q.CreateHousehold(ctx, dbq.CreateHouseholdParams{Name: "Next door", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	stranger, err := f.st.Q.CreatePerson(ctx, dbq.CreatePersonParams{HouseholdID: other.ID, Name: "Pat", Kind: "adult", Colour: "#E5533D", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	from, to := day(24), day(25)
	owner := func() *int64 {
		t.Helper()
		got := f.svc.Upcoming(f.hh, from, to)
		if len(got) != 1 {
			t.Fatalf("Upcoming = %+v", got)
		}
		return got[0].PersonID
	}

	if _, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "Work", srv.URL, &stranger.ID); !hasErr(err, "person") {
		t.Errorf("AddFeed with another household's person = %v, want a person error", err)
	}
	feed, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "Work", srv.URL, &dad.ID)
	if err != nil {
		t.Fatal(err)
	}
	if feed.PersonID == nil || *feed.PersonID != dad.ID {
		t.Errorf("AddFeed owner = %v, want Dad", feed.PersonID)
	}
	if p := owner(); p == nil || *p != dad.ID {
		t.Errorf("event owner = %v, want Dad", p)
	}
	if got := f.svc.Upcoming(f.hh, from, to)[0].FeedID; got != feed.ID {
		t.Errorf("event feed id = %d, want %d", got, feed.ID)
	}

	// Edit: the name and owner change, and the events follow.
	if err := f.svc.UpdateFeed(ctx, f.hh, f.mum.ID, feed.ID, "", &stranger.ID); !hasErr(err, "name") || !hasErr(err, "person") {
		t.Errorf("UpdateFeed bad = %v", err)
	}
	if err := f.svc.UpdateFeed(ctx, other.ID, f.mum.ID, feed.ID, "Theirs", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateFeed from another household = %v, want ErrNotFound", err)
	}
	if err := f.svc.UpdateFeed(ctx, f.hh, f.mum.ID, feed.ID, " Mum's work ", &f.mum.ID); err != nil {
		t.Fatal(err)
	}
	if p := owner(); p == nil || *p != f.mum.ID {
		t.Errorf("event owner after edit = %v, want Mum", p)
	}
	if got := f.svc.Upcoming(f.hh, from, to)[0].Feed; got != "Mum's work" {
		t.Errorf("event feed name = %q", got)
	}
	feeds, _ := f.svc.Feeds(ctx, f.hh)
	if len(feeds) != 1 || feeds[0].Name != "Mum's work" || feeds[0].PersonID == nil || *feeds[0].PersonID != f.mum.ID {
		t.Errorf("Feeds = %+v", feeds)
	}

	// Archived people can't be given a calendar, and forgetting someone
	// makes their calendars the family's.
	at := int64(2)
	if err := f.st.Q.ArchivePerson(ctx, dbq.ArchivePersonParams{ArchivedAt: &at, HouseholdID: f.hh, ID: dad.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.UpdateFeed(ctx, f.hh, f.mum.ID, feed.ID, "Work", &dad.ID); !hasErr(err, "person") {
		t.Errorf("UpdateFeed to an archived person = %v, want a person error", err)
	}
	f.svc.ForgetPerson(other.ID, f.mum.ID)
	if owner() == nil {
		t.Error("ForgetPerson for another household changed this one")
	}
	f.svc.ForgetPerson(f.hh, f.mum.ID)
	if p := owner(); p != nil {
		t.Errorf("event owner after ForgetPerson = %v, want the family", *p)
	}

	// Back to the family.
	if err := f.svc.UpdateFeed(ctx, f.hh, f.mum.ID, feed.ID, "Work", nil); err != nil {
		t.Fatal(err)
	}
	if feeds, _ := f.svc.Feeds(ctx, f.hh); feeds[0].PersonID != nil {
		t.Errorf("owner = %v, want the family", *feeds[0].PersonID)
	}
}

func hasErr(err error, field string) bool {
	var ve valid.Errors
	return errors.As(err, &ve) && ve[field] != ""
}
