package server

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestChoreEmojiForm(t *testing.T) {
	h, _, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	expect(t, phone.get("/admin/chores/new"), http.StatusOK, "data-emoji-picker", `name="emoji_typed"`, "&#34;dog&#34;:&#34;🐶&#34;")

	chore := func(title string, extra url.Values) url.Values {
		v := url.Values{
			"title": {title}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"daily"},
			"starts_on": {"2026-07-01"}, "points": {"1"},
		}
		for k, vs := range extra {
			v[k] = vs
		}
		return v
	}
	count := func() int { return len(must(h.st.Q.ListChores(t.Context(), h.hh))) }
	before := count()

	// Without the page's script, the first save offers the suggestion.
	r := phone.post("/admin/chores", chore("Feed the dog", url.Values{"suggest_offered": {""}}))
	expect(t, r, http.StatusOK, "We picked 🐶", `value="🐶" checked`, `name="suggest_offered" value="1"`)
	if count() != before {
		t.Fatal("saved before the suggestion was seen")
	}
	expectRedirect(t, phone.post("/admin/chores", chore("Feed the dog", url.Values{"emoji": {"🐶"}, "suggest_offered": {"1"}})), "/admin/chores")
	if c := h.chore("Feed the dog"); c.Emoji != "🐶" {
		t.Errorf("emoji = %q", c.Emoji)
	}
	// None, once offered, is kept.
	expectRedirect(t, phone.post("/admin/chores", chore("Walk the dog", url.Values{"emoji": {""}, "suggest_offered": {"1"}})), "/admin/chores")
	if c := h.chore("Walk the dog"); c.Emoji != "" {
		t.Errorf("emoji = %q, want none", c.Emoji)
	}
	// A typed emoji wins over the grid.
	expectRedirect(t, phone.post("/admin/chores", chore("Lizard", url.Values{"emoji": {"🐶"}, "emoji_typed": {"🦎"}})), "/admin/chores")
	lizard := h.chore("Lizard")
	if lizard.Emoji != "🦎" {
		t.Errorf("typed emoji = %q", lizard.Emoji)
	}
	expect(t, phone.get("/admin/chores/"+itoa(lizard.ID)), http.StatusOK, `name="emoji_typed" value="🦎"`)
	expect(t, phone.get("/admin/chores"), http.StatusOK, "🦎 Lizard")
	expect(t, phone.post("/admin/chores", chore("Odd", url.Values{"emoji_typed": {"ab"}})), http.StatusUnprocessableEntity, "Use a single emoji")

	// Rewards get the same picker and suggestions.
	r = phone.post("/admin/rewards", url.Values{"title": {"Lego tower"}, "cost_points": {"10"}, "suggest_offered": {""}})
	expect(t, r, http.StatusOK, "We picked 🧱", "emoji_typed")
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Dino park"}, "emoji_typed": {"🦕"}, "cost_points": {"10"}}), "/admin/rewards")
	expect(t, phone.get("/admin/rewards"), http.StatusOK, "🦕 Dino park")
}

func TestPictureCards(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	expect(t, phone.get("/admin/people/"+itoa(alfie.ID)), http.StatusOK, `name="picture_cards"`)
	expect(t, phone.post("/admin/people/"+itoa(alfie.ID), url.Values{
		"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"emoji"}, "emoji": {"🦖"},
		"sort_order": {"1"}, "picture_cards": {"1"},
	}), http.StatusOK, "Saved.", `name="picture_cards" value="1" checked`)
	if h.person("Alfie").PictureCards != 1 {
		t.Fatal("picture cards not saved")
	}

	// Alfie's column is tiles; "Feed the cat" has no emoji, so its tile
	// uses the suggestion. The pool stays as it was.
	body := wall.get("/board").body
	col := section(body, "Alfie")
	if !strings.Contains(col, "chores chores--pictures") || !strings.Contains(col, `<span class="chore__emoji" aria-hidden="true">🐱</span>`) {
		t.Errorf("Alfie's column isn't picture cards: %s", col)
	}
	if pool := poolOf(body); strings.Contains(pool, "chore__emoji") || strings.Contains(pool, "chores--pictures") {
		t.Errorf("pool changed: %s", pool)
	}

	// A chore's own emoji shows on ordinary cards and the pick sheet too.
	hoover := h.chore("Hoover the lounge")
	if _, err := h.st.DB.ExecContext(t.Context(), "UPDATE chores SET emoji = '🧹' WHERE id = ?", hoover.ID); err != nil {
		t.Fatal(err)
	}
	occ := h.occurrence("Hoover the lounge")
	if pool := poolOf(wall.get("/board").body); !strings.Contains(pool, `<span class="chore__emoji" aria-hidden="true">🧹</span>`) {
		t.Errorf("pool card has no emoji: %s", pool)
	}
	expect(t, wall.get("/board/occurrences/"+itoa(occ.ID)+"/pick"), http.StatusOK, `<span class="picker__emoji" aria-hidden="true">🧹</span>`)

	// Done tiles keep the emoji.
	cat := h.occurrence("Feed the cat")
	col = section(wall.post("/board/occurrences/"+itoa(cat.ID)+"/complete", nil).body, "Alfie")
	if !strings.Contains(col, "chore__check--on") || !strings.Contains(col, "🐱") {
		t.Errorf("done tile: %s", col)
	}
}
