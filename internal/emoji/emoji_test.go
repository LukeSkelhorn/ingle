package emoji

import (
	"encoding/json"
	"testing"
)

func TestValid(t *testing.T) {
	for _, s := range []string{
		"🐶", "🛏️", "♻️", "⭐", "🦕",
		"👍🏽",         // skin tone
		"👨‍👩‍👧‍👦",    // ZWJ family
		"🧑🏽‍🚀",       // skin tone and ZWJ
		"🏳️‍🌈",       // flag with ZWJ
		"🇬🇧",         // regional indicators
		"1️⃣", "#️⃣", // keycaps
		"🏴󠁧󠁢󠁳󠁣󠁴󠁿", // subdivision flag (tags)
		"©️",
	} {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false", s)
		}
	}
	for _, s := range []string{
		"", "a", "ab", "1", "🐶🐱", "🐶 ", " 🐶", "dog🐶", "🇬", "🏽", "🐶\u200d", "\u200d🐶", "🇬🇧🇫🇷",
		"👨‍👩‍👧‍👦👨‍👩‍👧‍👦👨‍👩‍👧‍👦", // too long
	} {
		if Valid(s) {
			t.Errorf("Valid(%q) = true", s)
		}
	}
	for _, grid := range [][]string{Chores, Rewards} {
		for _, e := range grid {
			if !Valid(e) {
				t.Errorf("grid emoji %q isn't Valid", e)
			}
		}
	}
}

func TestSuggest(t *testing.T) {
	for _, c := range []struct {
		title string
		kind  Kind
		want  string
	}{
		{"Feed the dog", Chore, "🐶"},
		{"Make your BED", Chore, "🛏️"},
		{"Brush teeth (night)", Chore, "🪥"},
		{"Water the plants", Chore, "🪴"},
		{"Lay the table", Chore, "🍽️"},
		{"Wipe the table", Chore, "🧽"},
		{"Bins out", Chore, "🗑️"},
		{"Hotdog stand", Chore, ""}, // whole words only
		{"Practise the trumpet", Chore, ""},
		{"Lego set", Reward, "🧱"},
		{"Extra 30 min screen time", Reward, "📱"},
		{"Ice cream trip", Reward, "🍦"},
		{"Feed the dog", Reward, ""},
	} {
		if got := Suggest(c.title, c.kind); got != c.want {
			t.Errorf("Suggest(%q, %d) = %q, want %q", c.title, c.kind, got, c.want)
		}
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(SuggestionsJSON(Chore)), &m); err != nil || m["dog"] != "🐶" {
		t.Errorf("SuggestionsJSON = %v, %v", m, err)
	}
	for _, byWord := range suggestions {
		for key, e := range byWord {
			if !Valid(e) {
				t.Errorf("suggestion %q → %q isn't Valid", key, e)
			}
			if words(key) != key {
				t.Errorf("suggestion key %q isn't normalised", key)
			}
		}
	}
}
