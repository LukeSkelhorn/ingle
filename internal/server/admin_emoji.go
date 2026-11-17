package server

import (
	"strings"

	"github.com/lukeskelhorn/ingle/internal/emoji"
)

type emojiField struct {
	Value   string
	Grid    []string
	Suggest string
	Offered bool
	kind    emoji.Kind
}

func newEmojiField(kind emoji.Kind, value string) emojiField {
	grid := emoji.Chores
	switch kind {
	case emoji.Reward:
		grid = emoji.Rewards
	case emoji.Countdown:
		grid = emoji.Countdowns
	}
	return emojiField{Value: value, Grid: grid, Suggest: emoji.SuggestionsJSON(kind), kind: kind}
}

// Typed is the emoji for the "type one" box when it isn't in the grid.
func (e emojiField) Typed() string {
	if emoji.In(e.Grid, e.Value) {
		return ""
	}
	return e.Value
}

func (e *emojiField) read(fm *form) {
	e.Value = fm.str("emoji")
	if typed := strings.TrimSpace(fm.str("emoji_typed")); typed != "" {
		e.Value = typed
	}
	e.Offered = fm.str("suggest_offered") != ""
}

func (e *emojiField) offerSuggestion(title string) (string, bool) {
	if e.Value != "" || e.Offered {
		return "", false
	}
	s := emoji.Suggest(title, e.kind)
	if s == "" {
		return "", false
	}
	e.Value, e.Offered = s, true
	return s, true
}

func suggestionFlash(e, what string) string {
	return "We picked " + e + " for this " + what + ". Save again to keep it, or choose None."
}
