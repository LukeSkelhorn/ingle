// Package calendar reads the household's ICS feeds (Google, iCloud or
// Outlook "secret address" links) so the screensaver and the board
// calendar can show what's on.
// Feeds are fetched in the background and a few months of events are kept
// in memory; nothing about the events is stored in the database.
package calendar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/lukeskelhorn/ingle/internal/household"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// Limits for fetching a feed.
const (
	FetchTimeout = 20 * time.Second
	MaxFeedBytes = 5 << 20
	// RefreshEvery is how often the background loop re-fetches feeds.
	RefreshEvery = 15 * time.Minute
	// KeepBehind and KeepAhead bound the events cached per feed: enough for
	// the board calendar (back to the month 4 weeks ago, 3 months ahead,
	// padded to whole weeks), and the screensaver keeps working if a feed
	// stops answering.
	KeepBehind = 75 * 24 * time.Hour
	KeepAhead  = 100 * 24 * time.Hour
)

// ErrNotFound is returned for an unknown feed.
var ErrNotFound = errors.New("calendar not found")

// Feed is a feed for admin, with its URL masked and its fetch status.
type Feed struct {
	ID        int64
	Name      string
	Colour    string
	Masked    string // e.g. "calendar.google.com/…"
	FetchedAt time.Time
	Error     string
}

type feedState struct {
	householdID int64
	colour      string
	events      []Event
	fetchedAt   time.Time // last success
	err         string    // last failure, cleared on success
}

// Service manages feeds and their cached events.
type Service struct {
	st     *store.Store
	client *http.Client
	log    *slog.Logger
	now    func() time.Time
	zone   func(ctx context.Context, householdID int64) *time.Location

	mu    sync.RWMutex
	state map[int64]*feedState // by feed id
}

// New returns a Service. zone gives a household's timezone, used for
// events with floating times (settings.Service.Location fits). client may
// be nil for a default with FetchTimeout.
func New(st *store.Store, zone func(ctx context.Context, householdID int64) *time.Location, client *http.Client, log *slog.Logger) *Service {
	if client == nil {
		client = &http.Client{Timeout: FetchTimeout}
	}
	return &Service{st: st, client: client, log: log, now: time.Now, zone: zone, state: map[int64]*feedState{}}
}

// SetClock replaces the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// NormaliseURL checks a pasted feed address: webcal:// becomes https://,
// and only http and https are allowed.
func NormaliseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 2000 {
		return "", errors.New("that address is too long")
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(raw), "webcal://"); ok {
		raw = "https://" + raw[len(raw)-len(rest):]
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("enter a calendar address starting https:// or webcal://")
	}
	return u.String(), nil
}

// Mask shows enough of a feed URL to recognise it without revealing the
// secret part.
func Mask(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "…"
	}
	return u.Host + "/…"
}

// Feeds lists the household's feeds with their status.
func (s *Service) Feeds(ctx context.Context, householdID int64) ([]Feed, error) {
	rows, err := s.st.Q.ListCalendarFeeds(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Feed, 0, len(rows))
	for _, r := range rows {
		f := Feed{ID: r.ID, Name: r.Name, Colour: r.Colour, Masked: Mask(r.Url)}
		if st := s.state[r.ID]; st != nil {
			f.FetchedAt, f.Error = st.fetchedAt, st.err
		}
		out = append(out, f)
	}
	return out, nil
}

// AddFeed validates and fetches a feed, and saves it only if it could be
// read, so a mistyped address is caught straight away. Problems are
// returned as valid.Errors on "name" or "url".
func (s *Service) AddFeed(ctx context.Context, householdID, actorID int64, name, rawURL string) (Feed, error) {
	errs := valid.Errors{}
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 50 {
		errs.Add("name", "Enter a name (up to 50 characters).")
	}
	u, err := NormaliseURL(rawURL)
	if err != nil {
		errs.Add("url", capitalise(err.Error())+".")
	}
	if err := errs.Err(); err != nil {
		return Feed{}, err
	}
	events, err := s.fetch(ctx, householdID, u)
	if err != nil {
		errs.Add("url", "Couldn't read that calendar: "+err.Error()+".")
		return Feed{}, errs.Err()
	}

	existing, err := s.st.Q.ListCalendarFeeds(ctx, householdID)
	if err != nil {
		return Feed{}, fmt.Errorf("list calendars: %w", err)
	}
	colour := household.Colours[(len(existing)+5)%len(household.Colours)]
	now := s.now()
	var row dbq.CalendarFeed
	err = s.st.WithTx(ctx, func(q *dbq.Queries) error {
		var err error
		row, err = q.CreateCalendarFeed(ctx, dbq.CreateCalendarFeedParams{
			HouseholdID: householdID, Name: name, Url: u, Colour: colour,
			CreatedBy: &actorID, CreatedAt: now.Unix(),
		})
		if err != nil {
			return fmt.Errorf("create calendar: %w", err)
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &actorID, Verb: "calendar.added",
			SubjectType: "calendar", SubjectID: row.ID, At: now,
		})
	})
	if err != nil {
		return Feed{}, err
	}
	s.store(row, events, nil)
	return Feed{ID: row.ID, Name: row.Name, Colour: row.Colour, Masked: Mask(row.Url), FetchedAt: now}, nil
}

// RemoveFeed deletes a feed and forgets its events.
func (s *Service) RemoveFeed(ctx context.Context, householdID, actorID, feedID int64) error {
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		n, err := q.DeleteCalendarFeed(ctx, dbq.DeleteCalendarFeedParams{HouseholdID: householdID, ID: feedID})
		if err != nil {
			return fmt.Errorf("delete calendar: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &actorID, Verb: "calendar.removed",
			SubjectType: "calendar", SubjectID: feedID, At: s.now(),
		})
	})
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.state, feedID)
	s.mu.Unlock()
	return nil
}

// Refresh fetches every feed (of every household). A failed fetch keeps the
// feed's previous events and records the error for admin.
func (s *Service) Refresh(ctx context.Context) error {
	rows, err := s.st.Q.ListAllCalendarFeeds(ctx)
	if err != nil {
		return fmt.Errorf("list calendars: %w", err)
	}
	live := make(map[int64]bool, len(rows))
	for _, r := range rows {
		live[r.ID] = true
		events, err := s.fetch(ctx, r.HouseholdID, r.Url)
		if err != nil {
			// Never log the URL: it's a credential.
			s.log.WarnContext(ctx, "calendar fetch failed", slog.Int64("feed", r.ID), slog.String("err", err.Error()))
		}
		s.store(r, events, err)
	}
	s.mu.Lock()
	for id := range s.state {
		if !live[id] {
			delete(s.state, id)
		}
	}
	s.mu.Unlock()
	return nil
}

// RefreshHousehold fetches one household's feeds (admin's "Refresh now").
func (s *Service) RefreshHousehold(ctx context.Context, householdID int64) error {
	rows, err := s.st.Q.ListCalendarFeeds(ctx, householdID)
	if err != nil {
		return fmt.Errorf("list calendars: %w", err)
	}
	for _, r := range rows {
		events, err := s.fetch(ctx, r.HouseholdID, r.Url)
		s.store(r, events, err)
	}
	return nil
}

func (s *Service) store(r dbq.CalendarFeed, events []Event, fetchErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.state[r.ID]
	if st == nil {
		st = &feedState{}
		s.state[r.ID] = st
	}
	st.householdID, st.colour = r.HouseholdID, r.Colour
	if fetchErr != nil {
		st.err = fetchErr.Error()
		return
	}
	for i := range events {
		events[i].Colour, events[i].Feed = r.Colour, r.Name
	}
	st.events, st.fetchedAt, st.err = events, s.now(), ""
}

// Upcoming returns the household's events overlapping [from, to), merged
// across feeds and sorted.
func (s *Service) Upcoming(householdID int64, from, to time.Time) []Event {
	s.mu.RLock()
	var out []Event
	for _, st := range s.state {
		if st.householdID != householdID {
			continue
		}
		for _, e := range st.events {
			if e.Start.Before(to) && e.End.After(from) {
				out = append(out, e)
			}
		}
	}
	s.mu.RUnlock()
	sortEvents(out)
	return out
}

func (s *Service) fetch(ctx context.Context, householdID int64, u string) ([]Event, error) {
	ctx, cancel := context.WithTimeout(ctx, FetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errors.New("bad address")
	}
	req.Header.Set("User-Agent", "Ingle")
	req.Header.Set("Accept", "text/calendar, */*;q=0.5")
	resp, err := s.client.Do(req)
	if err != nil {
		// *url.Error repeats the URL; report only the cause.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("it took too long to answer")
		}
		return nil, fmt.Errorf("couldn't connect (%w)", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the server said %q", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxFeedBytes+1))
	if err != nil {
		return nil, errors.New("the download was interrupted")
	}
	if len(body) > MaxFeedBytes {
		return nil, errors.New("it's too big (5 MB maximum)")
	}
	cal, err := Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, errors.New("that isn't an iCal (.ics) calendar")
	}
	now := s.now()
	return Expand(cal, now.Add(-KeepBehind), now.Add(KeepAhead), s.zone(ctx, householdID)), nil
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
