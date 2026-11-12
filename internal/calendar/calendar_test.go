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
		_, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "Family", srv.URL+path)
		var ve valid.Errors
		if !errors.As(err, &ve) || !strings.Contains(ve["url"], want) {
			t.Errorf("AddFeed(%s) = %v, want url error containing %q", path, err, want)
		}
		if strings.Contains(fmt.Sprint(err), srv.URL) {
			t.Errorf("error %v leaks the URL", err)
		}
	}
	status.Store(http.StatusOK)

	feed, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "Family", srv.URL+"/family.ics")
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
	if _, err := f.svc.AddFeed(t.Context(), f.hh, f.mum.ID, "Family", srv.URL+"/family.ics"); err != nil {
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
	if _, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "Old", secret); err != nil {
		t.Fatal(err)
	}

	master := make([]byte, secrets.KeySize)
	_, _ = rand.Read(master)
	k, err := secrets.New(f.st, master)
	if err != nil {
		t.Fatal(err)
	}
	f.svc.SetSecrets(k)
	if _, err := f.svc.AddFeed(ctx, f.hh, f.mum.ID, "New", secret); err != nil {
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
