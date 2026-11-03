package chores

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Recurrence kinds offered in the admin form.
const (
	RepeatOnce        = "once"
	RepeatDaily       = "daily"
	RepeatWeekdays    = "weekdays"
	RepeatWeekly      = "weekly"
	RepeatFortnightly = "fortnightly"
	RepeatMonthly     = "monthly"
	// RepeatCustom is an RRULE that doesn't match a preset (e.g. written by
	// hand in the database). It is shown read-only and kept as-is.
	RepeatCustom = "custom"
)

// LastDayOfMonth is the MonthDay value meaning "the last day of the month".
const LastDayOfMonth = -1

// Errors from Recurrence.RRule.
var (
	ErrNoDays     = errors.New("choose at least one day")
	ErrBadMonthly = errors.New("choose a day of the month from 1 to 28, or the last day")
	ErrBadRepeat  = errors.New("choose how often it repeats")
)

// Recurrence is the friendly form of a chore's schedule.
type Recurrence struct {
	Kind     string
	Days     []time.Weekday // weekly and fortnightly
	MonthDay int            // monthly: 1–28 or LastDayOfMonth
	Raw      string         // custom: the RRULE as stored
}

// WeekOrder lists weekdays Monday first, the UK convention.
var WeekOrder = []time.Weekday{
	time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday,
}

var weekdayCodes = map[time.Weekday]string{
	time.Monday: "MO", time.Tuesday: "TU", time.Wednesday: "WE", time.Thursday: "TH",
	time.Friday: "FR", time.Saturday: "SA", time.Sunday: "SU",
}

const weekdaysRule = "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"

// RRule returns the RRULE for r, or nil for a one-off chore.
func (r Recurrence) RRule() (*string, error) {
	var rule string
	switch r.Kind {
	case RepeatOnce:
		return nil, nil
	case RepeatDaily:
		rule = "FREQ=DAILY"
	case RepeatWeekdays:
		rule = weekdaysRule
	case RepeatWeekly, RepeatFortnightly:
		days := byDay(r.Days)
		if days == "" {
			return nil, ErrNoDays
		}
		rule = "FREQ=WEEKLY;BYDAY=" + days
		if r.Kind == RepeatFortnightly {
			rule = "FREQ=WEEKLY;INTERVAL=2;BYDAY=" + days
		}
	case RepeatMonthly:
		if r.MonthDay != LastDayOfMonth && (r.MonthDay < 1 || r.MonthDay > 28) {
			return nil, ErrBadMonthly
		}
		rule = "FREQ=MONTHLY;BYMONTHDAY=" + strconv.Itoa(r.MonthDay)
	case RepeatCustom:
		if err := ValidateRRule(r.Raw); err != nil {
			return nil, err
		}
		rule = r.Raw
	default:
		return nil, ErrBadRepeat
	}
	return &rule, nil
}

// ParseRecurrence maps a stored RRULE back to a preset so the edit form can
// be prefilled. Anything that isn't exactly a preset comes back as
// RepeatCustom with Raw set.
func ParseRecurrence(rule *string) Recurrence {
	if rule == nil {
		return Recurrence{Kind: RepeatOnce}
	}
	custom := Recurrence{Kind: RepeatCustom, Raw: *rule}
	parts := map[string]string{}
	for p := range strings.SplitSeq(*rule, ";") {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return custom
		}
		if _, dup := parts[k]; dup {
			return custom
		}
		parts[k] = v
	}
	only := func(keys ...string) bool {
		if len(parts) != len(keys) {
			return false
		}
		for _, k := range keys {
			if _, ok := parts[k]; !ok {
				return false
			}
		}
		return true
	}

	switch {
	case only("FREQ") && parts["FREQ"] == "DAILY":
		return Recurrence{Kind: RepeatDaily}
	case only("FREQ", "BYDAY") && parts["FREQ"] == "WEEKLY":
		days, ok := parseByDay(parts["BYDAY"])
		if !ok {
			return custom
		}
		if parts["BYDAY"] == "MO,TU,WE,TH,FR" {
			return Recurrence{Kind: RepeatWeekdays, Days: days}
		}
		return Recurrence{Kind: RepeatWeekly, Days: days}
	case only("FREQ", "INTERVAL", "BYDAY") && parts["FREQ"] == "WEEKLY" && parts["INTERVAL"] == "2":
		days, ok := parseByDay(parts["BYDAY"])
		if !ok {
			return custom
		}
		return Recurrence{Kind: RepeatFortnightly, Days: days}
	case only("FREQ", "BYMONTHDAY") && parts["FREQ"] == "MONTHLY":
		n, err := strconv.Atoi(parts["BYMONTHDAY"])
		if err != nil || (n != LastDayOfMonth && (n < 1 || n > 28)) {
			return custom
		}
		return Recurrence{Kind: RepeatMonthly, MonthDay: n}
	}
	return custom
}

// byDay returns the BYDAY list for days in Monday-first order.
func byDay(days []time.Weekday) string {
	var codes []string
	for _, d := range WeekOrder {
		if slices.Contains(days, d) {
			codes = append(codes, weekdayCodes[d])
		}
	}
	return strings.Join(codes, ",")
}

// parseByDay accepts only plain, canonical (Monday-first, no repeats) codes.
func parseByDay(s string) ([]time.Weekday, bool) {
	var days []time.Weekday
	for c := range strings.SplitSeq(s, ",") {
		found := false
		for d, code := range weekdayCodes {
			if code == c {
				days = append(days, d)
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	if byDay(days) != s {
		return nil, false
	}
	return days, true
}
