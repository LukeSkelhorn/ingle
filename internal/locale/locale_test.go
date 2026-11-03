package locale

import (
	"math"
	"testing"
	"time"
)

func TestFormatMoney(t *testing.T) {
	l := UK()
	tests := []struct {
		in   int64
		want string
	}{
		{0, "£0.00"},
		{5, "£0.05"},
		{150, "£1.50"},
		{-250, "-£2.50"},
		{123456789, "£1,234,567.89"},
		{math.MinInt64, "-£92,233,720,368,547,758.08"},
	}
	for _, tt := range tests {
		if got := l.FormatMoney(tt.in); got != tt.want {
			t.Errorf("FormatMoney(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}

	l.Currency = "XYZ"
	if got := l.FormatMoney(100); got != "XYZ 1.00" {
		t.Errorf("unknown currency = %q", got)
	}
}

func TestFormatDateAcrossDST(t *testing.T) {
	l := UK()
	tests := []struct {
		utc      time.Time
		wantDate string
		wantTime string
	}{
		// BST (UTC+1): 23:30 UTC on 30 June is already 1 July in London.
		{time.Date(2026, 6, 30, 23, 30, 0, 0, time.UTC), "01/07/2026", "00:30"},
		// GMT (UTC+0) in winter.
		{time.Date(2026, 12, 31, 23, 30, 0, 0, time.UTC), "31/12/2026", "23:30"},
		// Clocks go back 25 Oct 2026 at 02:00 BST -> 01:00 GMT.
		{time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), "25/10/2026", "01:30"},
		{time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC), "25/10/2026", "01:30"},
	}
	for _, tt := range tests {
		if got := l.FormatDate(tt.utc); got != tt.wantDate {
			t.Errorf("FormatDate(%v) = %q, want %q", tt.utc, got, tt.wantDate)
		}
		if got := l.FormatTime(tt.utc); got != tt.wantTime {
			t.Errorf("FormatTime(%v) = %q, want %q", tt.utc, got, tt.wantTime)
		}
	}
}

func TestStartOfWeek(t *testing.T) {
	l := UK()
	tests := []struct {
		name string
		in   time.Time
		want string
	}{
		{"monday itself", time.Date(2026, 11, 2, 12, 0, 0, 0, l.Location), "02/11/2026"},
		{"tuesday", time.Date(2026, 11, 3, 9, 0, 0, 0, l.Location), "02/11/2026"},
		{"sunday belongs to previous week", time.Date(2026, 11, 8, 23, 59, 0, 0, l.Location), "02/11/2026"},
		{"week spanning clocks going back", time.Date(2026, 10, 27, 8, 0, 0, 0, l.Location), "26/10/2026"},
		{"utc input late sunday is monday in BST", time.Date(2026, 6, 28, 23, 30, 0, 0, time.UTC), "29/06/2026"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := l.StartOfWeek(tt.in)
			if got.Weekday() != time.Monday {
				t.Errorf("weekday = %v, want Monday", got.Weekday())
			}
			if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
				t.Errorf("not midnight: %v", got)
			}
			if d := l.FormatDate(got); d != tt.want {
				t.Errorf("StartOfWeek = %s, want %s", d, tt.want)
			}
		})
	}

	l.WeekStart = time.Sunday
	if got := l.FormatDate(l.StartOfWeek(time.Date(2026, 11, 3, 9, 0, 0, 0, l.Location))); got != "01/11/2026" {
		t.Errorf("sunday start = %s, want 01/11/2026", got)
	}
}

func TestParseMoney(t *testing.T) {
	l := UK()
	good := map[string]int64{
		"": 0, "0": 0, "1.50": 150, "£1.50": 150, "£ 2": 200, "0.5": 50, ".75": 75, "1,000.00": 100000, "12": 1200,
	}
	for in, want := range good {
		got, err := l.ParseMoney(in)
		if err != nil || got != want {
			t.Errorf("ParseMoney(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"-1", "1.234", "1.", "abc", "£", "1.5x", "$1"} {
		if got, err := l.ParseMoney(in); err == nil {
			t.Errorf("ParseMoney(%q) = %d, want error", in, got)
		}
	}
	for pence, want := range map[int64]string{0: "0.00", 5: "0.05", 150: "1.50", 100000: "1000.00", -250: "-2.50"} {
		if got := FormatMoneyInput(pence); got != want {
			t.Errorf("FormatMoneyInput(%d) = %q, want %q", pence, got, want)
		}
	}
}
