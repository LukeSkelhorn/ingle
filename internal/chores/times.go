package chores

import (
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// A chore can be due at several times a day (each is its own occurrence),
// and a timed one can stay off the board until shortly before its time.
const (
	// MaxTimes is how many times a day one chore can have.
	MaxTimes = 6
	// DefaultShowBefore is what a new chore's form starts with: on the board
	// an hour before its time.
	DefaultShowBefore int64 = 60
)

// ShowBeforeOptions are the minutes a timed chore can appear before its
// time; nil (not listed) shows it all day.
var ShowBeforeOptions = []int64{120, 60, 30}

// Times lists c's due times, sorted, or nil for an untimed chore.
func Times(c dbq.Chore) []string {
	if c.DueTimes == "" {
		return nil
	}
	return strings.Split(c.DueTimes, ",")
}

// times returns c's due times, or one "" for an untimed chore: what it
// has one occurrence per day for.
func times(c dbq.Chore) []string {
	if ts := Times(c); ts != nil {
		return ts
	}
	return []string{""}
}

// optTime is an occurrence's due time, nil for none.
func optTime(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// appearsAt is when an undone occurrence due on dueOn at dueTime first
// shows on the board: showBefore minutes before its time. It's false when
// it shows all day (no time, or no showBefore).
func appearsAt(dueOn, dueTime string, showBefore *int64, loc *time.Location, dayStart time.Duration) (time.Time, bool) {
	if dueTime == "" || showBefore == nil {
		return time.Time{}, false
	}
	at, ok := cutoff(dueOn, &dueTime, loc, dayStart)
	if !ok {
		return time.Time{}, false
	}
	return at.Add(-time.Duration(*showBefore) * time.Minute), true
}
