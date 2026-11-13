package server

import (
	"cmp"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/lukeskelhorn/ingle/internal/calendar"
	"github.com/lukeskelhorn/ingle/internal/chores"
	"github.com/lukeskelhorn/ingle/internal/locale"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// The board calendar's reach: weeks back from this week, and weeks (or
// months) ahead. The calendar feeds cache enough to cover it.
const (
	calWeeksBack   = 4
	calWeeksAhead  = 13
	calMonthsAhead = 2
	// calMonthItems is how many items a month cell lists before "+N more".
	calMonthItems = 3
)

// Calendar views.
const (
	calWeek  = "week"
	calMonth = "month"
)

// calView is the board calendar: a week or a month grid.
type calView struct {
	View  string // calWeek or calMonth
	Title string // "3–9 Nov" or "November 2026"
	// Prev, Next and Today are the toolbar's URLs; Prev and Next are ""
	// at the edges of the range.
	Prev, Next, Today string
	Self              string // this view, for refreshing in place
	Week, Month       string // the same range in the other view
	// Chores is whether chores show as well as events; ChoresURL is this
	// view with them the other way.
	Chores    bool
	ChoresURL string
	// People is the person filter: everyone with a calendar of their own.
	People []calPerson
	// CalendarsURL opens the sheet for hiding calendars, when there's more
	// than one; Hidden is how many are hidden.
	CalendarsURL string
	Hidden       int
	// DayQuery is the filter for a month cell's day sheet.
	DayQuery     string
	ShowsToday   bool
	Weekdays     []string // column headings, from the week start
	Weeks        [][]calDay
	HasCalendars bool
}

// calPerson is one person in the calendar's person filter. URL shows just
// them, or everyone again when they're already chosen.
type calPerson struct {
	Person  dbq.Person
	Pressed bool
	URL     string
}

// calFilter is what the board calendar shows, carried in its URLs so
// browsing keeps it: chores or not, one person's calendars (0 for
// everyone's), and calendars hidden by id. The board drops it when the
// calendar goes idle.
type calFilter struct {
	Chores bool
	Person int64
	Hide   []int64
}

// values is f as query parameters.
func (f calFilter) values() url.Values {
	v := url.Values{"chores": {choresParam(f.Chores)}}
	if f.Person != 0 {
		v.Set("person", itoa(f.Person))
	}
	for _, id := range f.Hide {
		v.Add("hide", itoa(id))
	}
	return v
}

// keepsEvent reports whether e shows: its calendar isn't hidden, and it's
// the family's or the chosen person's.
func (f calFilter) keepsEvent(e calendar.Event) bool {
	if slices.Contains(f.Hide, e.FeedID) {
		return false
	}
	return f.Person == 0 || e.PersonID == nil || *e.PersonID == f.Person
}

// keepsHoliday reports whether h shows: everyone's, or the chosen
// person's.
func (f calFilter) keepsHoliday(h chores.Holiday) bool {
	return f.Person == 0 || h.Person == nil || h.Person.ID == f.Person
}

// keepsChore reports whether c shows for the chosen person: theirs, shared
// with them, or up for grabs.
func (f calFilter) keepsChore(c *chores.CalChore) bool {
	if f.Person == 0 {
		return true
	}
	if c.PersonID != nil {
		return *c.PersonID == f.Person
	}
	return len(c.People) == 0 || slices.Contains(c.People, f.Person)
}

// calDay is one day of the grid.
type calDay struct {
	Date    string // YYYY-MM-DD
	Num     int
	Label   string // "Mon 3 Nov"
	Today   bool
	Past    bool
	Outside bool // in a month grid, a day of the month before or after
	AllDay  []calItem
	Items   []calItem // timed events and chores, in time order
	// Shown and More are the month cell: the first few items and how many
	// didn't fit.
	Shown []calItem
	More  int
}

// calItem is an event or a chore on a day.
type calItem struct {
	Title  string
	When   string // "09:00", "until 10:00", "All day" or "" for an untimed chore
	Colour string // the person's colour, or the feed's for a family event
	Feed   string // an event's calendar
	// EventURL opens an event's sheet.
	EventURL string
	Chore    *chores.CalChore
	// Person is whose chore or calendar it is; nil for a chore up for grabs
	// or a family event.
	Person *dbq.Person
	DoneBy *dbq.Person
	People []dbq.Person // a shared chore, not done yet: who it's for
	sort   string
}

// eventOwner is whose calendar e is from, among the household's people who
// aren't archived; nil for a family calendar. Events cache only the id, so
// a person's new colour shows straight away.
func eventOwner(people map[int64]*dbq.Person, e calendar.Event) *dbq.Person {
	if e.PersonID == nil {
		return nil
	}
	return people[*e.PersonID]
}

// IsChore reports whether the item is a chore (else it's an event).
func (i calItem) IsChore() bool { return i.Chore != nil }

// Count is the day's number of items, for the month view's dots.
func (d calDay) Count() int { return len(d.AllDay) + len(d.Items) }

// calendarLocale is the household locale in its own timezone.
func (s *Server) calendarLocale(r *http.Request) locale.Locale {
	loc := s.Locale
	loc.Location = s.Settings.Location(r.Context(), hhOf(r))
	return loc
}

// calRange works out the anchor (week start or 1st of the month) for the
// requested view and date, clamped to the calendar's reach.
type calRange struct {
	view        string
	anchor      time.Time
	first, last time.Time // anchor limits
	gridFrom    time.Time // first day shown
	gridTo      time.Time // last day shown
	today       time.Time
}

func (s *Server) calRange(r *http.Request, loc locale.Locale) calRange {
	today := s.boardDay(r)
	thisWeek := loc.StartOfWeek(today)
	cr := calRange{view: calWeek, today: today}
	if r.URL.Query().Get("view") == calMonth {
		cr.view = calMonth
	}
	from := today
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.ParseInLocation(chores.DateLayout, v, loc.Location); err == nil {
			from = t
		}
	}
	switch cr.view {
	case calMonth:
		back := thisWeek.AddDate(0, 0, -7*calWeeksBack)
		cr.first = time.Date(back.Year(), back.Month(), 1, 0, 0, 0, 0, loc.Location)
		cr.last = time.Date(today.Year(), today.Month()+calMonthsAhead, 1, 0, 0, 0, 0, loc.Location)
		cr.anchor = time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, loc.Location)
	default:
		cr.first = thisWeek.AddDate(0, 0, -7*calWeeksBack)
		cr.last = thisWeek.AddDate(0, 0, 7*calWeeksAhead)
		cr.anchor = loc.StartOfWeek(from)
	}
	if cr.anchor.Before(cr.first) {
		cr.anchor = cr.first
	}
	if cr.anchor.After(cr.last) {
		cr.anchor = cr.last
	}
	cr.gridFrom = loc.StartOfWeek(cr.anchor)
	cr.gridTo = cr.gridFrom.AddDate(0, 0, 6)
	if cr.view == calMonth {
		monthEnd := cr.anchor.AddDate(0, 1, -1)
		cr.gridTo = loc.StartOfWeek(monthEnd).AddDate(0, 0, 6)
	}
	return cr
}

// calURL is the board calendar's view from t with filter f. It always
// says whether chores show, so browsing keeps the board's Chores button as
// it is.
func calURL(view string, t time.Time, f calFilter) string {
	v := f.values()
	v.Set("view", view)
	v.Set("from", t.Format(chores.DateLayout))
	return "/board/calendar?" + v.Encode()
}

func choresParam(on bool) string {
	if on {
		return "1"
	}
	return "0"
}

// calChores is whether the calendar shows chores: the board's Chores
// button (the chores param) if it's been pressed, else the household's
// setting.
func (s *Server) calChores(r *http.Request) (bool, error) {
	switch r.URL.Query().Get("chores") {
	case "1":
		return true, nil
	case "0":
		return false, nil
	}
	set, err := s.Settings.Get(r.Context(), hhOf(r))
	if err != nil {
		return false, err
	}
	return set.CalendarChores, nil
}

// calFeeds is the household's calendars, or none with the module off.
func (s *Server) calFeeds(r *http.Request) ([]calendar.Feed, error) {
	if s.Calendars == nil || !s.moduleOn(r.Context(), modCalendar) {
		return nil, nil
	}
	return s.Calendars.Feeds(r.Context(), hhOf(r))
}

// calFilter reads the board calendar's filter from r. The Calendars sheet
// sends pick=1 with the calendars to show; otherwise hide lists the ones
// hidden. Only feeds that exist are kept.
func (s *Server) calFilter(r *http.Request, feeds []calendar.Feed) (calFilter, error) {
	ch, err := s.calChores(r)
	if err != nil {
		return calFilter{}, err
	}
	q := r.URL.Query()
	f := calFilter{Chores: ch}
	if id, err := strconv.ParseInt(q.Get("person"), 10, 64); err == nil && id > 0 {
		f.Person = id
	}
	for _, fd := range feeds {
		id := itoa(fd.ID)
		if q.Has("pick") && !slices.Contains(q["show"], id) || !q.Has("pick") && slices.Contains(q["hide"], id) {
			f.Hide = append(f.Hide, fd.ID)
		}
	}
	return f, nil
}

// peopleByID indexes the household's people (archived ones aren't listed).
func (s *Server) peopleByID(r *http.Request) ([]dbq.Person, map[int64]*dbq.Person, error) {
	people, err := s.Store.Q.ListPeople(r.Context(), hhOf(r))
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[int64]*dbq.Person, len(people))
	for i := range people {
		byID[people[i].ID] = &people[i]
	}
	return people, byID, nil
}

func (s *Server) handleCalendarView(w http.ResponseWriter, r *http.Request) {
	loc := s.calendarLocale(r)
	cr := s.calRange(r, loc)
	feeds, err := s.calFeeds(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f, err := s.calFilter(r, feeds)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	people, byID, err := s.peopleByID(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	days, err := s.calendarDays(r, loc, cr.gridFrom, cr.gridTo, cr.today, f, byID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	v := calView{View: cr.view, Today: calURL(cr.view, cr.today, f), Self: calURL(cr.view, cr.anchor, f)}
	flip := f
	flip.Chores = !f.Chores
	v.Chores, v.ChoresURL = f.Chores, calURL(cr.view, cr.anchor, flip)
	v.DayQuery = f.values().Encode()
	v.Week = calURL(calWeek, cr.anchor, f)
	v.Month = calURL(calMonth, cr.anchor, f)
	if cr.view == calMonth && cr.anchor.Year() == cr.today.Year() && cr.anchor.Month() == cr.today.Month() {
		v.Week = calURL(calWeek, cr.today, f)
	}
	owners := map[int64]bool{}
	for _, fd := range feeds {
		if fd.PersonID != nil {
			owners[*fd.PersonID] = true
		}
	}
	for _, p := range people {
		if !owners[p.ID] {
			continue
		}
		pick := f
		pick.Person = p.ID
		if f.Person == p.ID {
			pick.Person = 0
		}
		v.People = append(v.People, calPerson{Person: p, Pressed: f.Person == p.ID, URL: calURL(cr.view, cr.anchor, pick)})
	}
	if len(feeds) > 1 {
		q := f.values()
		q.Set("view", cr.view)
		q.Set("from", cr.anchor.Format(chores.DateLayout))
		v.CalendarsURL, v.Hidden = "/board/calendar/calendars?"+q.Encode(), len(f.Hide)
	}
	step := func(n int) time.Time {
		if cr.view == calMonth {
			return cr.anchor.AddDate(0, n, 0)
		}
		return cr.anchor.AddDate(0, 0, 7*n)
	}
	if p := step(-1); !p.Before(cr.first) {
		v.Prev = calURL(cr.view, p, f)
	}
	if n := step(1); !n.After(cr.last) {
		v.Next = calURL(cr.view, n, f)
	}
	for i := range 7 {
		v.Weekdays = append(v.Weekdays, cr.gridFrom.AddDate(0, 0, i).Format("Mon"))
	}
	for i := 0; i < len(days); i += 7 {
		week := days[i:min(i+7, len(days))]
		for j := range week {
			d := &week[j]
			if cr.view == calMonth {
				t, _ := time.ParseInLocation(chores.DateLayout, d.Date, loc.Location)
				d.Outside = t.Month() != cr.anchor.Month()
				all := append(slices.Clone(d.AllDay), d.Items...)
				d.Shown = all[:min(len(all), calMonthItems)]
				d.More = len(all) - len(d.Shown)
			}
			if d.Today && !d.Outside {
				v.ShowsToday = true
			}
		}
		v.Weeks = append(v.Weeks, week)
	}
	if cr.view == calMonth {
		v.Title = cr.anchor.Format("January 2006")
	} else {
		v.Title = weekTitle(cr.gridFrom, cr.gridTo)
	}
	v.HasCalendars = len(feeds) > 0
	s.renderPartial(w, r, "calendar", v)
}

// weekTitle is "3–9 Nov", "30 Nov – 6 Dec" or "29 Dec 2026 – 4 Jan 2027".
func weekTitle(from, to time.Time) string {
	switch {
	case from.Year() != to.Year():
		return from.Format("2 Jan 2006") + " – " + to.Format("2 Jan 2006")
	case from.Month() != to.Month():
		return from.Format("2 Jan") + " – " + to.Format("2 Jan")
	}
	return strconv.Itoa(from.Day()) + "–" + to.Format("2 Jan")
}

// calendarDays builds each day from first to last (local midnights) with
// its holidays, events and, if f.Chores, chores, as filtered by f. byID is
// the household's people.
func (s *Server) calendarDays(r *http.Request, loc locale.Locale, first, last, today time.Time, f calFilter, byID map[int64]*dbq.Person) ([]calDay, error) {
	ctx := r.Context()
	z := loc.Location
	var cal map[string][]chores.CalChore
	if f.Chores {
		var err error
		from, to := first.Format(chores.DateLayout), last.Format(chores.DateLayout)
		if cal, err = s.Chores.Calendar(ctx, hhOf(r), today, from, to); err != nil {
			return nil, err
		}
	}
	hols, err := s.Chores.Holidays(ctx, hhOf(r), first)
	if err != nil {
		return nil, err
	}
	var events []calendar.Event
	if s.Calendars != nil && s.moduleOn(ctx, modCalendar) {
		events = s.Calendars.Upcoming(hhOf(r), first, last.AddDate(0, 0, 1))
	}

	var days []calDay
	for d := first; !d.After(last); d = d.AddDate(0, 0, 1) {
		date := d.Format(chores.DateLayout)
		next := time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, z)
		day := calDay{
			Date: date, Num: d.Day(), Label: d.Format("Mon 2 Jan"),
			Today: date == today.Format(chores.DateLayout), Past: d.Before(today),
		}
		for _, h := range hols {
			if h.StartsOn > date || date > h.EndsOn || !f.keepsHoliday(h) {
				continue
			}
			it := calItem{Title: "🏖️ Holiday", When: "All day"}
			if h.Person != nil {
				it.Title, it.Colour = "🏖️ "+h.Person.Name+" away", h.Person.Colour
			}
			day.AllDay = append(day.AllDay, it)
		}
		for _, e := range events {
			if !e.Start.Before(next) || !e.End.After(d) || !f.keepsEvent(e) {
				continue
			}
			it := calItem{Title: e.Title, Colour: e.Colour, Feed: e.Feed, Person: eventOwner(byID, e), EventURL: eventURL(e)}
			if it.Person != nil {
				it.Colour = it.Person.Colour
			}
			start, end := e.Start.In(z), e.End.In(z)
			switch {
			case e.AllDay, start.Before(d) && !end.Before(next):
				it.When = "All day"
				day.AllDay = append(day.AllDay, it)
				continue
			case start.Before(d):
				it.When, it.sort = "until "+loc.FormatTime(end), "00:00"
			default:
				it.When = loc.FormatTime(start)
				it.sort = it.When
			}
			day.Items = append(day.Items, it)
		}
		for i := range cal[date] {
			c := &cal[date][i]
			if !f.keepsChore(c) {
				continue
			}
			it := calItem{Title: c.Title, Chore: c, sort: "~"} // untimed chores last
			if c.DueTime != nil {
				it.When, it.sort = *c.DueTime, *c.DueTime
			}
			if c.PersonID != nil {
				it.Person = byID[*c.PersonID]
			}
			if c.DoneByID != nil {
				it.DoneBy = byID[*c.DoneByID]
			}
			for _, id := range c.People {
				if p := byID[id]; p != nil {
					it.People = append(it.People, *p)
				}
			}
			if it.Person != nil {
				it.Colour = it.Person.Colour
			}
			day.Items = append(day.Items, it)
		}
		slices.SortStableFunc(day.Items, func(a, b calItem) int { return cmp.Compare(a.sort, b.sort) })
		days = append(days, day)
	}
	return days, nil
}

// calDaySheet is the sheet listing one day.
type calDaySheet struct {
	Day          calDay
	HasCalendars bool
}

func (s *Server) handleCalendarDay(w http.ResponseWriter, r *http.Request) {
	loc := s.calendarLocale(r)
	d, ok := s.calDate(r, loc)
	if !ok {
		http.NotFound(w, r)
		return
	}
	feeds, err := s.calFeeds(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	f, err := s.calFilter(r, feeds)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	_, byID, err := s.peopleByID(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	days, err := s.calendarDays(r, loc, d, d, s.boardDay(r), f, byID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPartial(w, r, "cal_day", calDaySheet{Day: days[0]})
}

// eventURL opens e's sheet. Events aren't stored, so it names the event by
// its calendar, start and title.
func eventURL(e calendar.Event) string {
	return "/board/calendar/event?" + url.Values{
		"feed": {itoa(e.FeedID)}, "start": {itoa(e.Start.Unix())}, "title": {e.Title},
	}.Encode()
}

// calEventSheet is the read-only sheet for an event.
type calEventSheet struct {
	Title, When, Location, Calendar string
	Colour                          string
	Person                          *dbq.Person // nil: the family's calendar
}

func (s *Server) handleCalendarEvent(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	feed, ferr := strconv.ParseInt(q.Get("feed"), 10, 64)
	start, serr := strconv.ParseInt(q.Get("start"), 10, 64)
	if ferr != nil || serr != nil || s.Calendars == nil || !s.moduleOn(r.Context(), modCalendar) {
		http.NotFound(w, r)
		return
	}
	at := time.Unix(start, 0)
	var ev *calendar.Event
	for _, e := range s.Calendars.Upcoming(hhOf(r), at, at.Add(time.Second)) {
		if e.FeedID == feed && e.Start.Equal(at) && e.Title == q.Get("title") {
			ev = &e
			break
		}
	}
	if ev == nil {
		s.boardFailed(w, r, "That event isn't on the calendar any more.")
		return
	}
	_, byID, err := s.peopleByID(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	loc := s.calendarLocale(r)
	v := calEventSheet{
		Title: ev.Title, When: eventWhen(loc, *ev), Location: ev.Location,
		Calendar: ev.Feed, Colour: ev.Colour, Person: eventOwner(byID, *ev),
	}
	if v.Person != nil {
		v.Colour = v.Person.Colour
	}
	s.renderPartial(w, r, "cal_event", v)
}

// eventWhen is an event's day and times: "Tuesday 3 November, 09:00–10:00",
// "Tuesday 3 November, all day", "Tue 3 Nov – Thu 5 Nov" or
// "Tue 3 Nov 22:00 – Wed 4 Nov 01:00".
func eventWhen(loc locale.Locale, e calendar.Event) string {
	start, end := e.Start.In(loc.Location), e.End.In(loc.Location)
	if e.AllDay {
		last := end.AddDate(0, 0, -1)
		if !last.After(start) {
			return start.Format("Monday 2 January") + ", all day"
		}
		return start.Format("Mon 2 Jan") + " – " + last.Format("Mon 2 Jan")
	}
	if start.Year() == end.Year() && start.YearDay() == end.YearDay() {
		if end.Equal(start) {
			return start.Format("Monday 2 January") + ", " + loc.FormatTime(start)
		}
		return start.Format("Monday 2 January") + ", " + loc.FormatTime(start) + "–" + loc.FormatTime(end)
	}
	return start.Format("Mon 2 Jan") + " " + loc.FormatTime(start) + " – " + end.Format("Mon 2 Jan") + " " + loc.FormatTime(end)
}

// calCalendarsSheet is the sheet for showing and hiding calendars. Query is
// the rest of the board calendar's state, for the form to carry.
type calCalendarsSheet struct {
	Feeds []calFeedChoice
	Query map[string]string
}

type calFeedChoice struct {
	Feed   calendar.Feed
	Person *dbq.Person
	Shown  bool
}

func (s *Server) handleCalendarCalendars(w http.ResponseWriter, r *http.Request) {
	feeds, err := s.calFeeds(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if len(feeds) == 0 {
		http.NotFound(w, r)
		return
	}
	f, err := s.calFilter(r, feeds)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	_, byID, err := s.peopleByID(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	q := r.URL.Query()
	v := calCalendarsSheet{Query: map[string]string{"chores": choresParam(f.Chores), "view": q.Get("view"), "from": q.Get("from")}}
	if f.Person != 0 {
		v.Query["person"] = itoa(f.Person)
	}
	for _, fd := range feeds {
		c := calFeedChoice{Feed: fd, Shown: !slices.Contains(f.Hide, fd.ID)}
		if fd.PersonID != nil {
			c.Person = byID[*fd.PersonID]
		}
		v.Feeds = append(v.Feeds, c)
	}
	s.renderPartial(w, r, "cal_calendars", v)
}

// calChoreSheet is the read-only sheet for a chore on the calendar.
type calChoreSheet struct {
	Item   calItem
	Day    string // "Tuesday 3 November"
	Repeat string
	DoneAt string
	Chore  dbq.Chore
}

func (s *Server) handleCalendarChore(w http.ResponseWriter, r *http.Request) {
	loc := s.calendarLocale(r)
	id, ok := pathID(r)
	d, dok := s.calDate(r, loc)
	if !ok || !dok {
		http.NotFound(w, r)
		return
	}
	date := d.Format(chores.DateLayout)
	pick := chores.CalPick{At: r.URL.Query().Get("at")}
	pick.PersonID, _ = strconv.ParseInt(r.URL.Query().Get("person"), 10, 64)
	c, ch, err := s.Chores.ChoreOn(r.Context(), hhOf(r), s.boardDay(r), id, date, pick)
	if errors.Is(err, chores.ErrNotFound) {
		s.boardFailed(w, r, "That chore isn't on the calendar any more.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	people, err := s.Store.Q.ListPeople(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	v := calChoreSheet{
		Item:   calItem{Title: c.Title, Chore: &c},
		Day:    d.Format("Monday 2 January"),
		Repeat: describeSchedule(chores.ParseRecurrence(ch.Rrule), ch.DayMode, ch.StartsOn),
		Chore:  ch,
	}
	if c.DueTime != nil {
		v.Item.When = *c.DueTime
	}
	for i := range people {
		p := &people[i]
		if c.PersonID != nil && p.ID == *c.PersonID {
			v.Item.Person, v.Item.Colour = p, p.Colour
		}
		if c.DoneByID != nil && p.ID == *c.DoneByID {
			v.Item.DoneBy = p
		}
		if slices.Contains(c.People, p.ID) {
			v.Item.People = append(v.Item.People, *p)
		}
	}
	if c.DoneAt != 0 {
		v.DoneAt = loc.FormatTime(time.Unix(c.DoneAt, 0))
	}
	s.renderPartial(w, r, "cal_chore", v)
}

// calDate reads the {date} path value as a local midnight within the
// calendar's reach (with room for a month grid's edges).
func (s *Server) calDate(r *http.Request, loc locale.Locale) (time.Time, bool) {
	d, err := time.ParseInLocation(chores.DateLayout, r.PathValue("date"), loc.Location)
	if err != nil {
		return time.Time{}, false
	}
	today := s.boardDay(r)
	if d.Before(today.AddDate(0, 0, -7*(calWeeksBack+6))) || d.After(today.AddDate(0, calMonthsAhead+1, 7)) {
		return time.Time{}, false
	}
	return d, true
}
