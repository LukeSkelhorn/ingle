package chores

import (
	"errors"
	"testing"

	"github.com/lukeskelhorn/domus/internal/valid"
)

func TestChoreEmoji(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	day := f.svc.now() // Mon 2 Nov 2026
	in := Input{
		Title: "Feed the dog", Emoji: " 🐶 ", Mode: ModeAssigned, AssigneeID: &f.kid.ID,
		Repeat: Recurrence{Kind: RepeatDaily}, StartsOn: "2026-11-02", Points: 1,
	}
	c, err := f.svc.CreateChore(ctx, f.hh, f.adult.ID, in, day)
	if err != nil || c.Emoji != "🐶" {
		t.Fatalf("CreateChore = %q, %v", c.Emoji, err)
	}
	if card := f.boardAt("2026-11-02").column("Alfie").Cards[0]; card.Emoji != "🐶" || card.PictureEmoji() != "🐶" {
		t.Errorf("card emoji = %q / %q", card.Emoji, card.PictureEmoji())
	}
	in.Emoji = "🦕" // not in the grid: typed
	if c, err = f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, in, day); err != nil || c.Emoji != "🦕" {
		t.Fatalf("UpdateChore = %q, %v", c.Emoji, err)
	}
	for _, bad := range []string{"ab", "🐶🐱", "dog"} {
		in.Emoji = bad
		_, err := f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, in, day)
		var ve valid.Errors
		if !errors.As(err, &ve) || ve["emoji"] == "" {
			t.Errorf("emoji %q: err = %v, want an emoji error", bad, err)
		}
	}
	in.Emoji = ""
	if c, err = f.svc.UpdateChore(ctx, f.hh, f.adult.ID, c.ID, in, day); err != nil || c.Emoji != "" {
		t.Fatalf("clearing = %q, %v", c.Emoji, err)
	}
}

func TestPictureEmoji(t *testing.T) {
	for _, c := range []struct {
		card Card
		want string
	}{
		{Card{Title: "Feed the dog", Emoji: "🦮"}, "🦮"},
		{Card{Title: "Feed the dog"}, "🐶"},
		{Card{Title: "Practise trumpet"}, FallbackEmoji},
	} {
		if got := c.card.PictureEmoji(); got != c.want {
			t.Errorf("PictureEmoji(%q, %q) = %q, want %q", c.card.Title, c.card.Emoji, got, c.want)
		}
	}
}
