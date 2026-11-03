package chores

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestRecurrenceRoundTrip(t *testing.T) {
	tests := []struct {
		rec  Recurrence
		want string
	}{
		{Recurrence{Kind: RepeatDaily}, "FREQ=DAILY"},
		{Recurrence{Kind: RepeatWeekdays}, "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"},
		{Recurrence{Kind: RepeatWeekly, Days: []time.Weekday{time.Sunday, time.Monday, time.Thursday}}, "FREQ=WEEKLY;BYDAY=MO,TH,SU"},
		{Recurrence{Kind: RepeatFortnightly, Days: []time.Weekday{time.Saturday}}, "FREQ=WEEKLY;INTERVAL=2;BYDAY=SA"},
		{Recurrence{Kind: RepeatMonthly, MonthDay: 15}, "FREQ=MONTHLY;BYMONTHDAY=15"},
		{Recurrence{Kind: RepeatMonthly, MonthDay: LastDayOfMonth}, "FREQ=MONTHLY;BYMONTHDAY=-1"},
	}
	for _, tt := range tests {
		rule, err := tt.rec.RRule()
		if err != nil || rule == nil || *rule != tt.want {
			t.Errorf("%+v.RRule() = %v, %v; want %s", tt.rec, rule, err, tt.want)
			continue
		}
		if err := ValidateRRule(*rule); err != nil {
			t.Errorf("generated %s is invalid: %v", *rule, err)
		}
		back := ParseRecurrence(rule)
		if back.Kind != tt.rec.Kind || back.MonthDay != tt.rec.MonthDay {
			t.Errorf("ParseRecurrence(%s) = %+v, want %+v", *rule, back, tt.rec)
		}
		if tt.rec.Kind == RepeatWeekly || tt.rec.Kind == RepeatFortnightly {
			want := slices.Clone(tt.rec.Days)
			got := slices.Clone(back.Days)
			slices.Sort(want)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("days = %v, want %v", got, want)
			}
		}
	}

	if rule, err := (Recurrence{Kind: RepeatOnce}).RRule(); rule != nil || err != nil {
		t.Errorf("once = %v, %v", rule, err)
	}
	if got := ParseRecurrence(nil); got.Kind != RepeatOnce {
		t.Errorf("ParseRecurrence(nil) = %+v", got)
	}
}

func TestRecurrenceErrors(t *testing.T) {
	if _, err := (Recurrence{Kind: RepeatWeekly}).RRule(); !errors.Is(err, ErrNoDays) {
		t.Errorf("weekly no days: %v", err)
	}
	for _, d := range []int{0, 29, -2} {
		if _, err := (Recurrence{Kind: RepeatMonthly, MonthDay: d}).RRule(); !errors.Is(err, ErrBadMonthly) {
			t.Errorf("monthly %d: %v", d, err)
		}
	}
	if _, err := (Recurrence{Kind: "hourly"}).RRule(); !errors.Is(err, ErrBadRepeat) {
		t.Errorf("unknown kind: %v", err)
	}
}

func TestParseCustomRecurrence(t *testing.T) {
	for _, rule := range []string{
		"FREQ=WEEKLY;BYDAY=TH,MO",         // non-canonical order
		"FREQ=DAILY;COUNT=5",              // extra part
		"FREQ=WEEKLY;INTERVAL=3;BYDAY=MO", // not fortnightly
		"FREQ=MONTHLY;BYDAY=1MO",          // first Monday
		"FREQ=YEARLY",                     // no preset
		"FREQ=MONTHLY;BYMONTHDAY=31",      // outside picker
		"garbage",
	} {
		got := ParseRecurrence(&rule)
		if got.Kind != RepeatCustom || got.Raw != rule {
			t.Errorf("ParseRecurrence(%q) = %+v, want custom", rule, got)
		}
	}
}
