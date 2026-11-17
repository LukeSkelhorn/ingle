package chores

import (
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

const (
	// MaxTimes is how many times a day one chore can have.
	MaxTimes = 6
	// DefaultShowBefore is what a new chore's form starts with: on the board an hour before its time.
	DefaultShowBefore int64 = 60
)

// ShowBeforeOptions are the minutes a timed chore can appear before its time.
var ShowBeforeOptions = []int64{120, 60, 30}

// Times lists c's due times, sorted, or nil for an untimed chore.
func Times(c dbq.Chore) []string {
	if c.DueTimes == "" {
		return nil
	}
	return strings.Split(c.DueTimes, ",")
}

func times(c dbq.Chore) []string {
	if ts := Times(c); ts != nil {
		return ts
	}
	return []string{""}
}

func optTime(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

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
