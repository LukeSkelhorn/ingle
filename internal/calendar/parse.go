package calendar

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	ical "github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// Event is one occurrence of a calendar event.
type Event struct {
	Title    string
	Start    time.Time
	End      time.Time
	AllDay   bool
	Location string
	Colour   string
	Feed     string
	FeedID   int64
	PersonID *int64
}

const maxEvents = 3000

// Parse decodes an iCalendar stream.
func Parse(r io.Reader) (*ical.Calendar, error) {
	cal, err := ical.NewDecoder(r).Decode()
	if err != nil {
		return nil, fmt.Errorf("not a valid calendar (%w)", err)
	}
	return cal, nil
}

// Expand returns the events in cal that overlap [from, to), with recurrences expanded.
func Expand(cal *ical.Calendar, from, to time.Time, loc *time.Location) []Event {
	type key struct {
		uid string
		at  int64
	}
	overridden := map[key]bool{}
	var out []Event
	add := func(e Event) {
		if len(out) < maxEvents && e.Start.Before(to) && e.End.After(from) {
			out = append(out, e)
		}
	}

	events := cal.Events()
	for _, ev := range events {
		rid := ev.Props.Get(ical.PropRecurrenceID)
		if rid == nil {
			continue
		}
		uid, _ := ev.Props.Text(ical.PropUID)
		if at, err := parseTime(rid, loc); err == nil {
			overridden[key{uid, at.Unix()}] = true
		}
		if e, ok := single(ev, loc); ok {
			add(e)
		}
	}
	for _, ev := range events {
		if ev.Props.Get(ical.PropRecurrenceID) != nil {
			continue
		}
		base, ok := single(ev, loc)
		if !ok {
			continue
		}
		opt, err := ev.Props.RecurrenceRule()
		if err != nil {
			continue
		}
		if opt == nil {
			add(base)
			continue
		}
		uid, _ := ev.Props.Text(ical.PropUID)
		dur := base.End.Sub(base.Start)
		opt.Dtstart = base.Start
		rule, err := rrule.NewRRule(*opt)
		if err != nil {
			continue
		}
		var set rrule.Set
		set.RRule(rule)
		set.DTStart(base.Start)
		for _, p := range ev.Props.Values(ical.PropExceptionDates) {
			for _, t := range parseTimes(&p, loc) {
				set.ExDate(t)
			}
		}
		for _, p := range ev.Props.Values(ical.PropRecurrenceDates) {
			for _, t := range parseTimes(&p, loc) {
				set.RDate(t)
			}
		}
		for _, start := range set.Between(from.Add(-dur), to, true) {
			if overridden[key{uid, start.Unix()}] {
				continue
			}
			e := base
			e.Start, e.End = start, start.Add(dur)
			if e.AllDay {
				// Keep all-day events on whole local days across clock changes.
				e.End = start.AddDate(0, 0, int(dur.Round(24*time.Hour)/(24*time.Hour)))
			}
			add(e)
		}
	}
	sortEvents(out)
	return out
}

func sortEvents(es []Event) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if a.AllDay != b.AllDay {
			return a.AllDay
		}
		return a.Title < b.Title
	})
}

func single(ev ical.Event, loc *time.Location) (Event, bool) {
	if st, _ := ev.Status(); st == ical.EventCancelled {
		return Event{}, false
	}
	sp := ev.Props.Get(ical.PropDateTimeStart)
	if sp == nil {
		return Event{}, false
	}
	start, err := parseTime(sp, loc)
	if err != nil {
		return Event{}, false
	}
	allDay := isDate(sp)
	var end time.Time
	switch {
	case ev.Props.Get(ical.PropDateTimeEnd) != nil:
		end, err = parseTime(ev.Props.Get(ical.PropDateTimeEnd), loc)
		if err != nil {
			return Event{}, false
		}
	case ev.Props.Get(ical.PropDuration) != nil:
		d, err := ev.Props.Get(ical.PropDuration).Duration()
		if err != nil {
			return Event{}, false
		}
		end = start.Add(d)
	case allDay:
		end = start.AddDate(0, 0, 1)
	default:
		end = start
	}
	if end.Before(start) {
		end = start
	}
	if end.Equal(start) && allDay {
		end = start.AddDate(0, 0, 1)
	}
	title, _ := ev.Props.Text(ical.PropSummary)
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Busy"
	}
	where, _ := ev.Props.Text(ical.PropLocation)
	return Event{Title: title, Start: start, End: end, AllDay: allDay, Location: strings.TrimSpace(where)}, true
}

func isDate(p *ical.Prop) bool {
	return p.ValueType() == ical.ValueDate || (p.ValueType() == ical.ValueDefault && len(p.Value) == len("20060102"))
}

func parseTime(p *ical.Prop, loc *time.Location) (time.Time, error) {
	t, err := p.DateTime(loc)
	if err == nil {
		return t, nil
	}
	tzid := p.Params.Get(ical.PropTimezoneID)
	if tzid == "" {
		return time.Time{}, err
	}
	fallback := loc
	if name, ok := windowsZones[tzid]; ok {
		if l, lerr := time.LoadLocation(name); lerr == nil {
			fallback = l
		}
	}
	q := *p
	q.Params = ical.Params{}
	for k, v := range p.Params {
		if k != ical.PropTimezoneID {
			q.Params[k] = v
		}
	}
	return q.DateTime(fallback)
}

func parseTimes(p *ical.Prop, loc *time.Location) []time.Time {
	var out []time.Time
	for v := range strings.SplitSeq(p.Value, ",") {
		q := *p
		q.Value = strings.TrimSpace(v)
		if t, err := parseTime(&q, loc); err == nil {
			out = append(out, t)
		}
	}
	return out
}

var windowsZones = map[string]string{
	"GMT Standard Time":              "Europe/London",
	"Greenwich Standard Time":        "Atlantic/Reykjavik",
	"W. Europe Standard Time":        "Europe/Berlin",
	"Romance Standard Time":          "Europe/Paris",
	"Central Europe Standard Time":   "Europe/Budapest",
	"Central European Standard Time": "Europe/Warsaw",
	"E. Europe Standard Time":        "Europe/Chisinau",
	"FLE Standard Time":              "Europe/Kiev",
	"GTB Standard Time":              "Europe/Bucharest",
	"Eastern Standard Time":          "America/New_York",
	"Central Standard Time":          "America/Chicago",
	"Mountain Standard Time":         "America/Denver",
	"Pacific Standard Time":          "America/Los_Angeles",
	"AUS Eastern Standard Time":      "Australia/Sydney",
	"India Standard Time":            "Asia/Kolkata",
	"China Standard Time":            "Asia/Shanghai",
	"Tokyo Standard Time":            "Asia/Tokyo",
	"UTC":                            "UTC",
}
