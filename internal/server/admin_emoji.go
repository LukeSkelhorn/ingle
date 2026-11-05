package server

import (
	"strings"

	"github.com/lukeskelhorn/domus/internal/emoji"
)

// emojiField is the emoji picker on the chore and reward forms: a grid, a
// box to type any emoji, and a suggestion from the title. The page offers
// the suggestion as the title is typed; without JavaScript the first save
// offers it instead (see offerSuggestion).
type emojiField struct {
	Value   string // the chosen emoji, "" for none
	Grid    []string
	Suggest string // the suggestion map, as JSON, for the page
	Offered bool   // a suggestion has been offered (by the page or a save)
	kind    emoji.Kind
}

func newEmojiField(kind emoji.Kind, value string) emojiField {
	grid := emoji.Chores
	if kind == emoji.Reward {
		grid = emoji.Rewards
	}
	return emojiField{Value: value, Grid: grid, Suggest: emoji.SuggestionsJSON(kind), kind: kind}
}

// Typed is the value for the "type one" box: the emoji when it isn't in
// the grid.
func (e emojiField) Typed() string {
	if emoji.In(e.Grid, e.Value) {
		return ""
	}
	return e.Value
}

// read takes the chosen emoji from the form: a typed one wins over the grid.
func (e *emojiField) read(fm *form) {
	e.Value = fm.str("emoji")
	if typed := strings.TrimSpace(fm.str("emoji_typed")); typed != "" {
		e.Value = typed
	}
	e.Offered = fm.str("suggest_offered") != ""
}

// offerSuggestion picks a suggested emoji for title when none was chosen and
// none has been offered yet. The caller shows the form again with it
// selected instead of saving, so the adult can keep it or choose None.
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
