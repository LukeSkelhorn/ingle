package chores

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lukeskelhorn/ingle/internal/emoji"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// Countdowns count the days to a birthday, a holiday or a big day. A
// countdown is one-off (over after its day) or yearly (the same day and
// month every year). Everyone's birthday counts down too, every year,
// without a countdowns row: it's set on the person.

// MaxCountdownTitle is the longest a countdown's title can be, in
// characters.
const MaxCountdownTitle = 60

// Countdown is one countdown, worked out for a day.
type Countdown struct {
	ID       int64 // 0 for a birthday
	Title    string
	Emoji    string
	People   []dbq.Person // who it's for (the birthday person), if anyone
	OnDate   string       // as saved, YYYY-MM-DD; for a birthday, the next one
	Yearly   bool
	Birthday bool
	AddedBy  int64  // who added it, 0 if not known
	Next     string // the day it counts to, YYYY-MM-DD
	Days     int    // days from today to Next; below 0 once a one-off has passed
	Turns    int    // the age a birthday person turns, 0 if not known
	today    string
}

// Person is the first person it's for, nil for nobody: their colour tints
// it.
func (c Countdown) Person() *dbq.Person {
	if len(c.People) == 0 {
		return nil
	}
	return &c.People[0]
}

// Passed reports whether a one-off countdown's day has gone.
func (c Countdown) Passed() bool { return c.Days < 0 }

// Today reports whether the day is today.
func (c Countdown) Today() bool { return c.Days == 0 }

// Label says how long to go, short enough for the board's strip: "Today!",
// "Tomorrow", "3 days", "5 weeks", "4 months".
func (c Countdown) Label() string {
	switch d := c.Days; {
	case d < 0:
		return "Passed"
	case d == 0:
		return "Today!"
	case d == 1:
		return "Tomorrow"
	case d < 14:
		return strconv.Itoa(d) + " days"
	case d < 91:
		return strconv.Itoa(d/7) + " weeks"
	default:
		return plural(monthsBetween(c.today, c.Next), "month")
	}
}

// Sentence is the countdown in words, for the screensaver: "Sam's birthday
// in 3 days", "Christmas tomorrow", "Christmas is today!".
func (c Countdown) Sentence() string {
	switch c.Days {
	case 0:
		return c.Title + " is today!"
	case 1:
		return c.Title + " tomorrow"
	}
	return c.Title + " in " + strings.ToLower(c.Label())
}

// DaysWord is "day" or "days" after the big number on a countdown card.
func (c Countdown) DaysWord() string {
	if c.Days == 1 {
		return "day to go"
	}
	return "days to go"
}

// When is the day it counts to, e.g. "Sat 2 Aug", with the year when it
// isn't this year's.
func (c Countdown) When() string { return dayLabel(c.Next, c.today) }

// Kind says what sort of countdown it is, for Admin › Countdowns.
func (c Countdown) Kind() string {
	switch {
	case c.Birthday:
		return "Birthday"
	case c.Yearly:
		return "Every year"
	}
	return "One day"
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// monthsBetween is the whole months from one date to a later one, at least
// 1.
func monthsBetween(from, to string) int {
	a, err1 := time.Parse(DateLayout, from)
	b, err2 := time.Parse(DateLayout, to)
	if err1 != nil || err2 != nil {
		return 1
	}
	n := (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month())
	if b.Day() < a.Day() {
		n--
	}
	return max(n, 1)
}

// nextOn is the next date, from today on, with month and day: this year's,
// or next year's once this year's has gone. 29 February falls on 28
// February in years without one.
func nextOn(month time.Month, day int, today time.Time) time.Time {
	at := func(y int) time.Time {
		if month == time.February && day == 29 && !leap(y) {
			return time.Date(y, time.February, 28, 0, 0, 0, 0, time.UTC)
		}
		return time.Date(y, month, day, 0, 0, 0, 0, time.UTC)
	}
	d := at(today.Year())
	if d.Before(today) {
		d = at(today.Year() + 1)
	}
	return d
}

func leap(y int) bool { return y%4 == 0 && (y%100 != 0 || y%400 == 0) }

func daysBetween(from, to time.Time) int {
	return int(math.Round(to.Sub(from).Hours() / 24))
}

// Countdowns lists the household's countdowns and birthdays, soonest first,
// for today (the board day). Passed one-off countdowns come last when
// withPassed is set, and are left out otherwise.
func (s *Service) Countdowns(ctx context.Context, householdID int64, boardDay time.Time, withPassed bool) ([]Countdown, error) {
	q := s.st.Q
	rows, err := q.ListCountdowns(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("list countdowns: %w", err)
	}
	people, err := q.ListPeople(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	links, err := q.ListCountdownPeople(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("list countdown people: %w", err)
	}
	return countdownViews(rows, people, links, boardDay, withPassed), nil
}

// CountdownsOn lists the countdowns and birthdays that fall from first to
// last (local midnights, under a year apart), by day (YYYY-MM-DD), for the
// board calendar. Yearly ones and birthdays fall every year; a birthday
// knows the age turned that year. Their Days and Label count from first,
// not from today.
func (s *Service) CountdownsOn(ctx context.Context, householdID int64, first, last time.Time) (map[string][]Countdown, error) {
	all, err := s.Countdowns(ctx, householdID, first, true)
	if err != nil {
		return nil, err
	}
	from, to := first.Format(DateLayout), last.Format(DateLayout)
	out := map[string][]Countdown{}
	for _, c := range all {
		if from <= c.Next && c.Next <= to {
			out[c.Next] = append(out[c.Next], c)
		}
	}
	return out, nil
}

func countdownViews(rows []dbq.Countdown, people []dbq.Person, links []dbq.ListCountdownPeopleRow, boardDay time.Time, withPassed bool) []Countdown {
	today := time.Date(boardDay.Year(), boardDay.Month(), boardDay.Day(), 0, 0, 0, 0, time.UTC)
	todayS := today.Format(DateLayout)
	byID := make(map[int64]*dbq.Person, len(people))
	for i := range people {
		byID[people[i].ID] = &people[i]
	}
	forID := make(map[int64][]dbq.Person)
	for _, l := range links {
		if p := byID[l.PersonID]; p != nil {
			forID[l.CountdownID] = append(forID[l.CountdownID], *p)
		}
	}
	var out []Countdown
	for _, r := range rows {
		on, err := time.Parse(DateLayout, r.OnDate)
		if err != nil {
			continue
		}
		c := Countdown{ID: r.ID, Title: r.Title, Emoji: r.Emoji, OnDate: r.OnDate, Yearly: r.Yearly == 1, today: todayS}
		c.People = forID[r.ID]
		if r.AddedBy != nil {
			c.AddedBy = *r.AddedBy
		}
		next := on
		if c.Yearly {
			next = nextOn(on.Month(), on.Day(), today)
		}
		c.Next, c.Days = next.Format(DateLayout), daysBetween(today, next)
		if c.Passed() && !withPassed {
			continue
		}
		out = append(out, c)
	}
	for i := range people {
		p := &people[i]
		month, day, ok := parseBirthday(p.Birthday)
		if !ok {
			continue
		}
		next := nextOn(month, day, today)
		c := Countdown{
			Title: possessive(p.Name) + " birthday", Emoji: "🎂", People: []dbq.Person{*p}, Birthday: true, Yearly: true,
			Next: next.Format(DateLayout), Days: daysBetween(today, next), today: todayS,
		}
		c.OnDate = c.Next
		if p.BirthYear != nil {
			c.Turns = next.Year() - int(*p.BirthYear)
		}
		out = append(out, c)
	}
	slices.SortStableFunc(out, func(a, b Countdown) int {
		if a.Passed() != b.Passed() {
			if a.Passed() {
				return 1
			}
			return -1
		}
		if a.Passed() {
			return b.Days - a.Days // most recently passed first
		}
		if a.Days != b.Days {
			return a.Days - b.Days
		}
		if a.Birthday != b.Birthday {
			if a.Birthday {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Title, b.Title)
	})
	return out
}

// possessive is "Sam's", or "James's".
func possessive(name string) string { return name + "'s" }

// parseBirthday reads a saved birthday, MM-DD.
func parseBirthday(b *string) (time.Month, int, bool) {
	if b == nil {
		return 0, 0, false
	}
	t, err := time.Parse(DateLayout, "2000-"+*b)
	if err != nil {
		return 0, 0, false
	}
	return t.Month(), t.Day(), true
}

// Birthday is a birthday as typed on Admin › People: day and month, and
// the year if it's known. All empty is no birthday.
type Birthday struct {
	Day, Month, Year string
}

// BirthdayOf is p's birthday for the form.
func BirthdayOf(p dbq.Person) Birthday {
	month, day, ok := parseBirthday(p.Birthday)
	if !ok {
		return Birthday{}
	}
	b := Birthday{Day: strconv.Itoa(day), Month: strconv.Itoa(int(month))}
	if p.BirthYear != nil {
		b.Year = strconv.FormatInt(*p.BirthYear, 10)
	}
	return b
}

// Parse checks a birthday from the form. It returns nil values for no
// birthday, or a message for the form.
func (b Birthday) Parse(today time.Time) (md *string, year *int64, msg string) {
	b.Day, b.Month, b.Year = strings.TrimSpace(b.Day), strings.TrimSpace(b.Month), strings.TrimSpace(b.Year)
	if b.Day == "" && b.Month == "" && b.Year == "" {
		return nil, nil, ""
	}
	day, err1 := strconv.Atoi(b.Day)
	month, err2 := strconv.Atoi(b.Month)
	if err1 != nil || err2 != nil || month < 1 || month > 12 || day < 1 || day > 31 {
		return nil, nil, "Choose the day and the month."
	}
	y := 2000 // a leap year, so 29 February is allowed without a year
	if b.Year != "" {
		n, err := strconv.Atoi(b.Year)
		if err != nil || n < 1900 || n > today.Year() {
			return nil, nil, "Enter the year they were born, like 2017, or leave it empty."
		}
		y = n
	}
	t := time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Day() != day {
		return nil, nil, "That day isn't in " + time.Month(month).String() + "."
	}
	if b.Year != "" && t.After(today) {
		return nil, nil, "That birthday hasn't happened yet."
	}
	v := fmt.Sprintf("%02d-%02d", month, day)
	md = &v
	if b.Year != "" {
		n := int64(y)
		year = &n
	}
	return md, year, ""
}

// SetBirthday saves a person's birthday (nil for none) and birth year (nil
// if not known).
func (s *Service) SetBirthday(ctx context.Context, householdID, personID int64, md *string, year *int64) error {
	if md == nil {
		year = nil
	}
	if err := s.st.Q.SetPersonBirthday(ctx, dbq.SetPersonBirthdayParams{
		Birthday: md, BirthYear: year, HouseholdID: householdID, ID: personID,
	}); err != nil {
		return fmt.Errorf("set birthday: %w", err)
	}
	return nil
}

// SetCanAddCountdowns lets a child add countdowns from the board (or stops
// them).
func (s *Service) SetCanAddCountdowns(ctx context.Context, householdID, personID int64, can bool) error {
	if err := s.st.Q.SetPersonCanAddCountdowns(ctx, dbq.SetPersonCanAddCountdownsParams{
		CanAddCountdowns: boolInt(can), HouseholdID: householdID, ID: personID,
	}); err != nil {
		return fmt.Errorf("set can add countdowns: %w", err)
	}
	return nil
}

// CountdownInput is a countdown being added or changed.
type CountdownInput struct {
	Title     string
	Emoji     string
	OnDate    string // YYYY-MM-DD
	Yearly    bool
	PeopleIDs []int64 // who it's for, if anyone
}

// Countdown is one saved countdown.
func (s *Service) Countdown(ctx context.Context, householdID, id int64) (dbq.Countdown, error) {
	c, err := s.st.Q.GetCountdown(ctx, dbq.GetCountdownParams{HouseholdID: householdID, ID: id})
	return c, notFound(err)
}

func (s *Service) checkCountdown(ctx context.Context, q *dbq.Queries, householdID int64, in *CountdownInput, boardDay time.Time) error {
	errs := valid.Errors{}
	in.Title = strings.TrimSpace(in.Title)
	switch {
	case in.Title == "":
		errs.Add("title", "Give it a name, like Holiday to Spain.")
	case utf8.RuneCountInString(in.Title) > MaxCountdownTitle:
		errs.Add("title", fmt.Sprintf("Keep it to %d letters or fewer.", MaxCountdownTitle))
	}
	if in.Emoji != "" && !emoji.Valid(in.Emoji) {
		errs.Add("emoji", "Choose one emoji.")
	}
	on, err := time.Parse(DateLayout, in.OnDate)
	today := boardDay.Format(DateLayout)
	switch {
	case err != nil:
		errs.Add("on_date", "Choose the day.")
	case !in.Yearly && in.OnDate < today:
		errs.Add("on_date", "That day has already been. Tick Every year if it comes round again.")
	case on.Year() > boardDay.Year()+20:
		errs.Add("on_date", "Choose a day in the next 20 years.")
	}
	slices.Sort(in.PeopleIDs)
	in.PeopleIDs = slices.Compact(in.PeopleIDs)
	for _, id := range in.PeopleIDs {
		if _, err := getPerson(ctx, q, householdID, id); err != nil {
			errs.Add("people_ids", "Choose people in the household.")
			break
		}
	}
	return errs.Err()
}

// CountdownPeople is who a saved countdown is for, in board order.
func (s *Service) CountdownPeople(ctx context.Context, householdID, id int64) ([]int64, error) {
	ids, err := s.st.Q.ListPeopleForCountdown(ctx, dbq.ListPeopleForCountdownParams{HouseholdID: householdID, CountdownID: id})
	if err != nil {
		return nil, fmt.Errorf("list countdown people: %w", err)
	}
	return ids, nil
}

func setCountdownPeople(ctx context.Context, q *dbq.Queries, householdID, id int64, people []int64) error {
	if err := q.DeleteCountdownPeople(ctx, dbq.DeleteCountdownPeopleParams{HouseholdID: householdID, CountdownID: id}); err != nil {
		return fmt.Errorf("clear countdown people: %w", err)
	}
	for _, p := range people {
		if err := q.AddCountdownPerson(ctx, dbq.AddCountdownPersonParams{HouseholdID: householdID, CountdownID: id, PersonID: p}); err != nil {
			return fmt.Errorf("add countdown person: %w", err)
		}
	}
	return nil
}

// AddCountdown adds a countdown. Errors are valid.Errors for the form.
func (s *Service) AddCountdown(ctx context.Context, householdID, actorID int64, in CountdownInput, boardDay time.Time) (int64, error) {
	var id int64
	err := s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if err := s.checkCountdown(ctx, q, householdID, &in, boardDay); err != nil {
			return err
		}
		var err error
		id, err = q.InsertCountdown(ctx, dbq.InsertCountdownParams{
			HouseholdID: householdID, Title: in.Title, Emoji: in.Emoji, OnDate: in.OnDate, Yearly: boolInt(in.Yearly),
			AddedBy: &actorID, CreatedAt: s.now().Unix(),
		})
		if err != nil {
			return fmt.Errorf("insert countdown: %w", err)
		}
		if err := setCountdownPeople(ctx, q, householdID, id, in.PeopleIDs); err != nil {
			return err
		}
		return s.logCountdown(ctx, q, householdID, actorID, "countdown.added", id)
	})
	return id, err
}

// UpdateCountdown changes a countdown. Errors are valid.Errors for the form.
func (s *Service) UpdateCountdown(ctx context.Context, householdID, actorID, id int64, in CountdownInput, boardDay time.Time) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.GetCountdown(ctx, dbq.GetCountdownParams{HouseholdID: householdID, ID: id}); err != nil {
			return notFound(err)
		}
		if err := s.checkCountdown(ctx, q, householdID, &in, boardDay); err != nil {
			return err
		}
		if err := q.UpdateCountdown(ctx, dbq.UpdateCountdownParams{
			Title: in.Title, Emoji: in.Emoji, OnDate: in.OnDate, Yearly: boolInt(in.Yearly),
			HouseholdID: householdID, ID: id,
		}); err != nil {
			return fmt.Errorf("update countdown: %w", err)
		}
		if err := setCountdownPeople(ctx, q, householdID, id, in.PeopleIDs); err != nil {
			return err
		}
		return s.logCountdown(ctx, q, householdID, actorID, "countdown.changed", id)
	})
}

// RemoveCountdown deletes a countdown.
func (s *Service) RemoveCountdown(ctx context.Context, householdID, actorID, id int64) error {
	return s.st.WithTx(ctx, func(q *dbq.Queries) error {
		if _, err := q.GetCountdown(ctx, dbq.GetCountdownParams{HouseholdID: householdID, ID: id}); err != nil {
			return notFound(err)
		}
		if err := q.DeleteCountdown(ctx, dbq.DeleteCountdownParams{HouseholdID: householdID, ID: id}); err != nil {
			return fmt.Errorf("delete countdown: %w", err)
		}
		return s.logCountdown(ctx, q, householdID, actorID, "countdown.removed", id)
	})
}

func (s *Service) logCountdown(ctx context.Context, q *dbq.Queries, householdID, actorID int64, verb string, id int64) error {
	return store.LogActivity(ctx, q, store.Activity{
		HouseholdID: householdID, ActorID: &actorID, Verb: verb,
		SubjectType: "countdown", SubjectID: id, At: s.now(),
	})
}
