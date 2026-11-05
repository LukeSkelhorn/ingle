package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

// boardHousehold sets up Mum (display + phone), Alfie and Isla, an assigned
// chore for Alfie and a pool chore, all due today (Wed 1 July).
func boardHousehold(t *testing.T) (h *harness, wall, phone *device) {
	t.Helper()
	h = newHarness(t)
	h.setUp()
	phone = h.phone()
	for _, p := range []url.Values{
		{"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"emoji"}, "emoji": {"🦖"}, "sort_order": {"1"}},
		{"name": {"Isla"}, "kind": {"child"}, "colour": {"#E54666"}, "avatar_mode": {"none"}, "sort_order": {"2"}},
	} {
		expectPersonCreated(t, phone.post("/admin/people", p))
	}
	alfie := h.person("Alfie")
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Feed the cat"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"daily"},
		"starts_on": {"2026-07-01"}, "points": {"2"}, "due_time": {"08:00"},
	}), "/admin/chores")
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Hoover the lounge"}, "mode": {"pool"}, "repeat": {"once"}, "starts_on": {"2026-07-01"},
		"points": {"5"}, "money": {"0.50"},
	}), "/admin/chores")
	wall = h.setUpDisplayLocked()
	return h, wall, phone
}

func (h *harness) occurrence(title string) dbq.ChoreOccurrence {
	h.t.Helper()
	c := h.chore(title)
	var id int64
	if err := h.st.DB.QueryRowContext(h.t.Context(),
		"SELECT id FROM chore_occurrences WHERE chore_id = ? AND due_on = '2026-07-01'", c.ID).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return must(h.st.Q.GetOccurrence(h.t.Context(), dbq.GetOccurrenceParams{HouseholdID: h.hh, ID: id}))
}

func toastOf(t *testing.T, r resp) toast {
	t.Helper()
	var m map[string]toast
	if err := json.Unmarshal([]byte(r.header.Get("HX-Trigger")), &m); err != nil {
		t.Fatalf("HX-Trigger %q: %v", r.header.Get("HX-Trigger"), err)
	}
	return m["toast"]
}

// section returns the HTML of the column labelled name.
func section(body, name string) string {
	i := strings.Index(body, `aria-label="`+name+`"`)
	if i < 0 {
		return ""
	}
	rest := body[i:]
	if j := strings.Index(rest, "</section>"); j >= 0 {
		return rest[:j]
	}
	return rest
}

func poolOf(body string) string {
	i := strings.Index(body, `class="pool"`)
	j := strings.Index(body, `class="columns"`)
	if i < 0 || j < i {
		return ""
	}
	return body[i:j]
}

func TestBoardShowsColumnsAndPool(t *testing.T) {
	_, wall, _ := boardHousehold(t)
	r := wall.get("/")
	expect(t, r, http.StatusOK, "Up for grabs", "Hoover the lounge", "5 pts · £0.50",
		`id="board"`, "/board/events", `id="sheet"`)
	alfie := section(r.body, "Alfie")
	for _, want := range []string{"🦖", "Feed the cat", "by 08:00", "0 of 1 done today", "0 pts"} {
		if !strings.Contains(alfie, want) {
			t.Errorf("Alfie's column lacks %q\n%s", want, alfie)
		}
	}
	// Adults are on the board too.
	if section(r.body, "Mum") == "" {
		t.Error("no column for Mum")
	}
	if !strings.Contains(section(r.body, "Isla"), "Nothing today.") {
		t.Error("Isla's empty column has no empty state")
	}
	expect(t, wall.get("/board"), http.StatusOK, `id="board"`, "Feed the cat")
}

func TestBoardStrangerSeesNothing(t *testing.T) {
	h, _, _ := boardHousehold(t)
	stranger := h.device()
	for _, p := range []string{"/board", "/board/events", "/board/occurrences/1/pick", "/board/people/1", "/board/redemptions/1/review"} {
		expectRedirect(t, stranger.get(p), "/signin")
	}
	for _, p := range []string{"/board/occurrences/1/complete", "/board/rewards/1/claim", "/board/redemptions/1/approve", "/board/redemptions/1/cancel", "/board/people/1/goal", "/board/people/1/goal/clear"} {
		expectRedirect(t, stranger.post(p, nil), "/signin")
	}
}

func TestBoardCompleteAndUndo(t *testing.T) {
	h, wall, _ := boardHousehold(t)
	occ := h.occurrence("Feed the cat")

	r := wall.post("/board/occurrences/"+itoa(occ.ID)+"/complete", nil)
	expect(t, r, http.StatusOK, "1 of 1 done today", "chore--done", "chore--just", "2 pts")
	tt := toastOf(t, r)
	if tt.Message != "Nice one, Alfie!" || !strings.HasPrefix(tt.Undo, "/board/completions/") {
		t.Errorf("toast = %+v", tt)
	}
	// It's already done.
	r = wall.post("/board/occurrences/"+itoa(occ.ID)+"/complete", nil)
	expect(t, r, http.StatusConflict, "chore--done")
	if tt := toastOf(t, r); !tt.Error || tt.Message != "That's already done." {
		t.Errorf("repeat toast = %+v", tt)
	}

	h.advance(10 * time.Second)
	r = wall.post(tt.Undo, nil)
	expect(t, r, http.StatusOK, "0 of 1 done today")
	if strings.Contains(section(r.body, "Alfie"), "chore--done") {
		t.Error("still done after undo")
	}
	if b := must(h.st.Q.BalanceForPerson(t.Context(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: h.person("Alfie").ID})); b.Points != 0 {
		t.Errorf("points after undo = %d", b.Points)
	}

	// Too late.
	r = wall.post("/board/occurrences/"+itoa(occ.ID)+"/complete", nil)
	undo := toastOf(t, r).Undo
	h.advance(time.Minute)
	r = wall.post(undo, nil)
	expect(t, r, http.StatusConflict)
	if tt := toastOf(t, r); tt.Message != "Too late to undo. Ask a grown-up." {
		t.Errorf("late undo toast = %+v", tt)
	}
}

func TestBoardPoolPickClaimRelease(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	occ := h.occurrence("Hoover the lounge")
	isla := h.person("Isla")
	alfie := h.person("Alfie")
	base := "/board/occurrences/" + itoa(occ.ID)

	expect(t, wall.get(base+"/pick"), http.StatusOK, "Hoover the lounge", "Who's doing this?", "Isla", "Alfie", "Mum")

	// No person chosen.
	r := wall.post(base+"/claim", nil)
	expect(t, r, http.StatusConflict)
	if tt := toastOf(t, r); tt.Message != "Choose who's doing it." {
		t.Errorf("toast = %+v", tt)
	}
	expect(t, wall.post(base+"/complete", nil), http.StatusConflict)

	r = wall.post(base+"/claim", url.Values{"person": {itoa(isla.ID)}})
	expect(t, r, http.StatusOK, "Nothing up for grabs right now.")
	if !strings.Contains(section(r.body, "Isla"), "Hoover the lounge") || !strings.Contains(section(r.body, "Isla"), "Give back") {
		t.Errorf("claimed chore not in Isla's column\n%s", section(r.body, "Isla"))
	}
	if tt := toastOf(t, r); tt.Message != "Isla is on it." {
		t.Errorf("toast = %+v", tt)
	}

	// A stale screen tries to claim it for Alfie.
	r = phone.post(base+"/claim", url.Values{"person": {itoa(alfie.ID)}})
	expect(t, r, http.StatusConflict)
	if tt := toastOf(t, r); tt.Message != "Someone's already on that one." {
		t.Errorf("toast = %+v", tt)
	}
	expect(t, phone.get(base+"/pick"), http.StatusConflict)

	// Give it back.
	r = wall.post(base+"/release", nil)
	expect(t, r, http.StatusOK)
	if !strings.Contains(poolOf(r.body), "Hoover the lounge") {
		t.Errorf("released chore not back in the pool\n%s", poolOf(r.body))
	}

	// Done straight from the pool, from a phone, by Alfie.
	r = phone.post(base+"/complete", url.Values{"person": {itoa(alfie.ID)}})
	expect(t, r, http.StatusOK, "Nothing up for grabs right now.", "5 pts · £0.50")
	if !strings.Contains(section(r.body, "Alfie"), "Hoover the lounge") {
		t.Error("done pool chore not shown under Alfie")
	}
}

func TestBoardEvents(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	occ := h.occurrence("Feed the cat")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.ts.URL+"/board/events?client=phone1", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := phone.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	lines := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	nextEvent := func(wait time.Duration) string {
		t.Helper()
		timeout := time.After(wait)
		for {
			select {
			case l, ok := <-lines:
				if !ok {
					return "closed"
				}
				// Lock events have their own tests.
				if strings.HasPrefix(l, "event: ") && l != "event: lock" {
					return strings.TrimPrefix(l, "event: ")
				}
			case <-timeout:
				return ""
			}
		}
	}

	// The phone's own tap doesn't echo back to it…
	req, err = http.NewRequestWithContext(t.Context(), http.MethodPost, h.ts.URL+"/board/occurrences/"+itoa(occ.ID)+"/complete", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", h.ts.URL)
	req.Header.Set(boardClientHeader, "phone1")
	expect(t, phone.do(req), http.StatusOK)
	if ev := nextEvent(300 * time.Millisecond); ev != "" {
		t.Fatalf("got %q for own change, want nothing", ev)
	}

	// …but a tap on the wall does.
	expect(t, wall.post("/board/occurrences/"+itoa(h.occurrence("Hoover the lounge").ID)+"/complete",
		url.Values{"person": {itoa(h.person("Isla").ID)}}), http.StatusOK)
	if ev := nextEvent(2 * time.Second); ev != "board" {
		t.Fatalf("event = %q, want board", ev)
	}

	// So does an admin change.
	expectPersonCreated(t, phone.post("/admin/people", url.Values{
		"name": {"Gran"}, "kind": {"adult"}, "colour": {"#46A758"}, "avatar_mode": {"none"},
	}))
	if ev := nextEvent(2 * time.Second); ev != "board" {
		t.Fatalf("event after admin change = %q, want board", ev)
	}
}

func TestBoardAdultReverse(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	occ := h.occurrence("Feed the cat")
	alfie := h.person("Alfie")
	balance := func() int64 {
		return must(h.st.Q.BalanceForPerson(t.Context(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID})).Points
	}

	r := wall.post("/board/occurrences/"+itoa(occ.ID)+"/complete", nil)
	expect(t, r, http.StatusOK)
	review := strings.Replace(toastOf(t, r).Undo, "/undo", "/review", 1)
	reverse := strings.Replace(review, "/review", "/reverse", 1)
	h.advance(10 * time.Minute)

	// Locked display: done chores aren't tappable and adult actions refuse.
	if strings.Contains(section(wall.get("/board").body, "Alfie"), review) {
		t.Error("locked display offers review")
	}
	r = wall.post(reverse, nil)
	expect(t, r, http.StatusForbidden, "chore--done")
	if tt := toastOf(t, r); !tt.Error || tt.Message != needAdult {
		t.Errorf("toast = %+v", tt)
	}
	expect(t, wall.get(review), http.StatusConflict)

	// Unlock on the wall: the done chore opens the review sheet.
	expectRedirect(t, wall.post("/admin/unlock", url.Values{"person": {itoa(h.person("Mum").ID)}, "pin": {"2468"}}), "/admin")
	if !strings.Contains(section(wall.get("/board").body, "Alfie"), review) {
		t.Fatal("unlocked display doesn't offer review")
	}
	expect(t, wall.get(review), http.StatusOK, "Feed the cat", "Alfie", "Reverse", "2 pts")

	// A phone is listening; reversing tells it.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	events := openEvents(t, ctx, phone)
	r = wall.post(reverse, nil)
	expect(t, r, http.StatusOK, "0 of 1 done today")
	if tt := toastOf(t, r); tt.Message != "Reversed. Points taken back." {
		t.Errorf("toast = %+v", tt)
	}
	if strings.Contains(section(r.body, "Alfie"), "chore--done") {
		t.Error("still done after reverse")
	}
	if b := balance(); b != 0 {
		t.Errorf("points = %d after reverse", b)
	}
	waitEvent(t, events)

	// Again: already not counted.
	r = wall.post(reverse, nil)
	expect(t, r, http.StatusConflict)
	if tt := toastOf(t, r); tt.Message != "That's already not counted." {
		t.Errorf("toast = %+v", tt)
	}
}

func TestAdminCompletions(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Tidy bedroom"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"once"},
		"starts_on": {"2026-07-01"}, "points": {"3"}, "requires_approval": {"1"},
	}), "/admin/chores")
	cat := h.occurrence("Feed the cat")
	tidy := h.occurrence("Tidy bedroom")
	expect(t, wall.post("/board/occurrences/"+itoa(cat.ID)+"/complete", nil), http.StatusOK)
	r := wall.post("/board/occurrences/"+itoa(tidy.ID)+"/complete", nil)
	expect(t, r, http.StatusOK, "Waiting for a grown-up")

	expect(t, wall.get("/admin/completions"), http.StatusSeeOther)
	expect(t, phone.get("/admin"), http.StatusOK, `href="/admin/completions"`, "Domus v9.8.7")
	r = phone.get("/admin/completions")
	expect(t, r, http.StatusOK, "Waiting for a grown-up", "Tidy bedroom", "Last 7 days", "Feed the cat", "Reverse")

	ids := func(body, verb string) []string {
		var out []string
		for _, m := range regexp.MustCompile(`/admin/completions/(\d+)/`+verb).FindAllStringSubmatch(body, -1) {
			out = append(out, m[1])
		}
		return out
	}
	pending := ids(r.body, "approve")
	reversible := ids(r.body, "reverse")
	if len(pending) != 1 || len(reversible) != 1 {
		t.Fatalf("approve %v, reverse %v", pending, reversible)
	}

	expect(t, phone.post("/admin/completions/"+pending[0]+"/approve", nil), http.StatusOK, "Approved.")
	balance := must(h.st.Q.BalanceForPerson(t.Context(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID}))
	if balance.Points != 5 {
		t.Errorf("points = %d after approve, want 5", balance.Points)
	}
	expect(t, phone.post("/admin/completions/"+pending[0]+"/approve", nil), http.StatusConflict, "already checked")

	r = phone.post("/admin/completions/"+reversible[0]+"/reverse", nil)
	expect(t, r, http.StatusOK, "Reversed", "Not counted · Mum")
	balance = must(h.st.Q.BalanceForPerson(t.Context(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID}))
	if balance.Points != 3 {
		t.Errorf("points = %d after reverse, want 3", balance.Points)
	}
	expect(t, wall.get("/board"), http.StatusOK, "1 of 2 done today")
	expect(t, phone.get("/admin"), http.StatusOK, "reversed a chore", "approved a chore")

	// A week later it's too late to reverse the approved one.
	h.advance(8 * 24 * time.Hour)
	r = phone.get("/admin/completions")
	if len(ids(r.body, "reverse")) != 0 {
		t.Error("old completion still reversible")
	}
	expect(t, phone.post("/admin/completions/"+pending[0]+"/reverse", nil), http.StatusConflict, "over a week ago")
}

// openEvents subscribes d to the board's event stream and returns its event
// names.
func openEvents(t *testing.T, ctx context.Context, d *device) <-chan string {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.h.ts.URL+"/board/events?client=listener", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream: %d", res.StatusCode)
	}
	events := make(chan string, 16)
	go func() {
		defer close(events)
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			if l := sc.Text(); strings.HasPrefix(l, "event: ") && l != "event: lock" {
				events <- strings.TrimPrefix(l, "event: ")
			}
		}
	}()
	return events
}

func waitEvent(t *testing.T, events <-chan string) {
	t.Helper()
	select {
	case e := <-events:
		if e != "board" {
			t.Errorf("event = %q, want board", e)
		}
	case <-time.After(3 * time.Second):
		t.Error("no board event")
	}
}

func TestPersonSheet(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	sheet := "/board/people/" + itoa(alfie.ID)

	// The column header opens the sheet.
	if col := section(wall.get("/").body, "Alfie"); !strings.Contains(col, `hx-get="`+sheet+`"`) {
		t.Errorf("Alfie's column doesn't open his sheet\n%s", col)
	}

	// Rewards: one he'll afford, one he won't, one archived.
	for _, v := range []url.Values{
		{"title": {"Film night pick"}, "cost_points": {"40"}},
		{"title": {"Comic"}, "cost_points": {"60"}, "money": {"2.50"}},
		{"title": {"Sweets"}, "cost_points": {"10"}},
	} {
		expectRedirect(t, phone.post("/admin/rewards", v), "/admin/rewards")
	}
	rewards := must(h.st.Q.ListRewards(t.Context(), h.hh))
	for _, rw := range rewards {
		if rw.Title == "Sweets" {
			expectRedirect(t, phone.post("/admin/rewards/"+itoa(rw.ID)+"/archive", nil), "/admin/rewards")
		}
	}

	// 40 pts and £1 from last week (it's Wednesday 1 July; the week began
	// Monday 29 June), plus Feed the cat (2 pts) today.
	if _, err := h.st.Q.InsertLedgerEntry(t.Context(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: alfie.ID, Points: 40, Pence: 100, Kind: "completion",
		CreatedAt: time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	expect(t, wall.post("/board/occurrences/"+itoa(h.occurrence("Feed the cat").ID)+"/complete", nil), http.StatusOK)

	r := wall.get(sheet)
	expect(t, r, http.StatusOK, "Alfie", "🦖", "42 pts", "£1.00", "This week: 2 pts",
		`data-refresh="`+sheet+`"`, "Film night pick", "Comic", "18 pts more · £1.50 more")
	if strings.Contains(r.body, "Sweets") {
		t.Error("archived reward on the sheet")
	}
	film := r.body[strings.Index(r.body, "Film night pick")-200:]
	if !strings.Contains(film[:200], "reward--affordable") {
		t.Errorf("Film night pick not marked affordable\n%s", film[:300])
	}
	comic := r.body[strings.Index(r.body, "Comic")-200:]
	if strings.Contains(comic[:200], "reward--affordable") {
		t.Error("Comic marked affordable")
	}

	// Phones (adults) can open it too; a stranger can't.
	expect(t, phone.get(sheet), http.StatusOK, "42 pts")
	expectRedirect(t, h.device().get(sheet), "/signin")

	// Isla has nothing yet.
	expect(t, wall.get("/board/people/"+itoa(h.person("Isla").ID)), http.StatusOK, "0 pts", "This week: nothing yet", "40 pts more")

	// Unknown and archived people are gone from the board.
	for _, p := range []string{"/board/people/9999", "/board/people/x"} {
		if got := wall.get(p).status; got != http.StatusConflict && got != http.StatusNotFound {
			t.Errorf("GET %s = %d", p, got)
		}
	}
	r = wall.get("/board/people/9999")
	if got := toastOf(t, r); !got.Error || got.Message == "" {
		t.Errorf("unknown person toast = %+v", got)
	}
}

func TestClaimRewardFromTheBoard(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	sheet := "/board/people/" + itoa(alfie.ID)
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Screen time"}, "cost_points": {"30"}}), "/admin/rewards")
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Lego set"}, "cost_points": {"500"}}), "/admin/rewards")
	var screen, lego int64
	for _, rw := range must(h.st.Q.ListRewards(t.Context(), h.hh)) {
		switch rw.Title {
		case "Screen time":
			screen = rw.ID
		case "Lego set":
			lego = rw.ID
		}
	}
	claim := "/board/rewards/" + itoa(screen) + "/claim"
	if _, err := h.st.Q.InsertLedgerEntry(t.Context(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: alfie.ID, Points: 70, Kind: "completion", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// Affordable rewards have Claim; the rest say what's missing.
	r := wall.get(sheet)
	expect(t, r, http.StatusOK, `hx-post="`+claim+`"`, `"person": "`+itoa(alfie.ID)+`"`)
	if strings.Contains(r.body, "/board/rewards/"+itoa(lego)+"/claim") {
		t.Error("Lego set (unaffordable) has Claim")
	}

	// No adult at the wall: it waits, and the points are held.
	r = wall.post(claim, url.Values{"person": {itoa(alfie.ID)}})
	expect(t, r, http.StatusOK, `id="board"`, "🎁 1 waiting", "40 pts")
	if tt := toastOf(t, r); tt.Error || !strings.Contains(tt.Message, "grown-up") {
		t.Errorf("toast = %+v", tt)
	}
	expect(t, wall.get(sheet), http.StatusOK, "40 pts", "(30 pts held)", "Waiting for a grown-up", "/cancel")
	expect(t, wall.post(claim, nil), http.StatusBadRequest)
	r = wall.post("/board/rewards/"+itoa(lego)+"/claim", url.Values{"person": {itoa(alfie.ID)}})
	expect(t, r, http.StatusConflict)
	if tt := toastOf(t, r); !tt.Error || !strings.Contains(tt.Message, "Not enough") {
		t.Errorf("unaffordable toast = %+v", tt)
	}

	waiting := must(h.st.Q.ListPendingRedemptions(t.Context(), h.hh))
	if len(waiting) != 1 {
		t.Fatalf("waiting = %d", len(waiting))
	}
	id := itoa(waiting[0].ID)
	review := "/board/redemptions/" + id + "/review"

	// The wall can't approve, but can cancel.
	expect(t, wall.get(review), http.StatusConflict)
	expect(t, wall.post("/board/redemptions/"+id+"/approve", nil), http.StatusForbidden)
	expect(t, wall.post("/board/redemptions/"+id+"/cancel", nil), http.StatusOK, "70 pts")
	if strings.Contains(wall.get("/board").body, "waiting") {
		t.Error("cancelled claim still waiting on the board")
	}

	// Claim again, then unlock on the wall: the sheet offers review.
	expect(t, wall.post(claim, url.Values{"person": {itoa(alfie.ID)}}), http.StatusOK, "🎁 1 waiting")
	id = itoa(must(h.st.Q.ListPendingRedemptions(t.Context(), h.hh))[0].ID)
	review = "/board/redemptions/" + id + "/review"
	expectRedirect(t, wall.post("/admin/unlock", url.Values{"person": {itoa(h.person("Mum").ID)}, "pin": {"2468"}}), "/admin")
	expect(t, wall.get(sheet), http.StatusOK, `hx-get="`+review+`"`)
	expect(t, wall.get(review), http.StatusOK, "Screen time", "Alfie", "Approve", "Decline", "30 pts")
	expect(t, wall.post("/board/redemptions/"+id+"/approve", nil), http.StatusOK, "40 pts")
	expect(t, wall.post("/board/redemptions/"+id+"/approve", nil), http.StatusConflict)
	expect(t, wall.get(review), http.StatusConflict)

	// With an adult there, a claim is approved at once.
	r = wall.post(claim, url.Values{"person": {itoa(alfie.ID)}})
	expect(t, r, http.StatusOK, "10 pts")
	if tt := toastOf(t, r); !strings.Contains(tt.Message, "Enjoy, Alfie") {
		t.Errorf("toast = %+v", tt)
	}
	if n := len(must(h.st.Q.ListPendingRedemptions(t.Context(), h.hh))); n != 0 {
		t.Errorf("%d claims waiting after an adult claimed", n)
	}

	// The feed names the reward.
	expect(t, phone.get("/admin"), http.StatusOK, "Alfie</strong> claimed Screen time")
}

func TestAdminRewardClaims(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Screen time"}, "cost_points": {"30"}}), "/admin/rewards")
	screen := must(h.st.Q.ListRewards(t.Context(), h.hh))[0]
	if _, err := h.st.Q.InsertLedgerEntry(t.Context(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: alfie.ID, Points: 70, Kind: "completion", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	claim := "/board/rewards/" + itoa(screen.ID) + "/claim"
	for range 2 {
		expect(t, wall.post(claim, url.Values{"person": {itoa(alfie.ID)}}), http.StatusOK)
	}
	expect(t, phone.get("/admin"), http.StatusOK, "<strong>2</strong><span>Waiting for approval")
	pending := must(h.st.Q.ListPendingRedemptions(t.Context(), h.hh))
	expect(t, phone.get("/admin/completions"), http.StatusOK, "Rewards waiting", "Screen time",
		"/admin/rewards/claims/"+itoa(pending[0].ID)+"/approve")

	expect(t, phone.post("/admin/rewards/claims/"+itoa(pending[0].ID)+"/approve", nil), http.StatusOK, "Reward approved.")
	expect(t, phone.post("/admin/rewards/claims/"+itoa(pending[1].ID)+"/decline", nil), http.StatusOK, "Reward declined")
	expect(t, phone.post("/admin/rewards/claims/"+itoa(pending[1].ID)+"/decline", nil), http.StatusConflict, "already checked")
	r := phone.get("/admin/completions")
	if strings.Contains(r.body, "Rewards waiting") {
		t.Error("settled claims still listed")
	}
	expect(t, wall.get("/board/people/"+itoa(alfie.ID)), http.StatusOK, "40 pts")
	expect(t, phone.get("/admin"), http.StatusOK, "declined a claim for Screen time")
}

func TestSaveForAReward(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	sheet := "/board/people/" + itoa(alfie.ID)
	goal := sheet + "/goal"
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Lego set"}, "emoji": {"🧱"}, "cost_points": {"100"}}), "/admin/rewards")
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Sweets"}, "cost_points": {"10"}}), "/admin/rewards")
	expect(t, phone.post("/admin/rewards", url.Values{"title": {"Odd"}, "emoji": {"x"}, "cost_points": {"1"}}), http.StatusUnprocessableEntity, "Use a single emoji")
	var lego, sweets int64
	for _, rw := range must(h.st.Q.ListRewards(t.Context(), h.hh)) {
		switch rw.Title {
		case "Lego set":
			lego = rw.ID
		case "Sweets":
			sweets = rw.ID
		}
	}
	if _, err := h.st.Q.InsertLedgerEntry(t.Context(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: alfie.ID, Points: 60, Kind: "completion", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// Only rewards they can't afford yet offer saving.
	r := wall.get(sheet)
	expect(t, r, http.StatusOK, "🧱", `hx-post="`+goal+`" hx-vals='{"reward": "`+itoa(lego)+`"}'`)
	if strings.Contains(r.body, `"reward": "`+itoa(sweets)+`"`) {
		t.Error("affordable Sweets offers Save for this")
	}

	r = wall.post(goal, url.Values{"reward": {itoa(lego)}})
	expect(t, r, http.StatusOK, `id="board"`)
	if tt := toastOf(t, r); tt.Message != "Saving for Lego set 🧱" {
		t.Errorf("toast = %+v", tt)
	}
	col := section(r.body, "Alfie")
	for _, want := range []string{`class="goal"`, "🧱", "Lego set", "60 of 100 pts", "width: 60%"} {
		if !strings.Contains(col, want) {
			t.Errorf("column missing %q", want)
		}
	}
	if strings.Contains(col, "data-celebrate") {
		t.Error("celebrating before the goal is reached")
	}
	expect(t, wall.get(sheet), http.StatusOK, "Saving for", "Stop saving", "Your goal")
	expect(t, wall.post(goal, nil), http.StatusBadRequest)
	expect(t, wall.post(goal, url.Values{"reward": {"9999"}}), http.StatusConflict)

	// Reached: Claim on the column, and a key to celebrate once.
	if _, err := h.st.Q.InsertLedgerEntry(t.Context(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: alfie.ID, Points: 40, Kind: "completion", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	col = section(wall.get("/board").body, "Alfie")
	setAt := *h.person("Alfie").GoalSetAt
	for _, want := range []string{
		"goal--reached", `data-celebrate="` + itoa(alfie.ID) + ":" + itoa(lego) + ":" + itoa(setAt) + `"`,
		`hx-post="/board/rewards/` + itoa(lego) + `/claim"`, "saved enough!",
	} {
		if !strings.Contains(col, want) {
			t.Errorf("reached column missing %q", want)
		}
	}

	// Claimed: waiting. Approved: the goal is gone.
	col = section(wall.post("/board/rewards/"+itoa(lego)+"/claim", url.Values{"person": {itoa(alfie.ID)}}).body, "Alfie")
	if !strings.Contains(col, "goal--waiting") || strings.Contains(col, "data-celebrate") {
		t.Errorf("claimed goal should wait without celebrating: %s", col)
	}
	pending := must(h.st.Q.ListPendingRedemptions(t.Context(), h.hh))
	expect(t, phone.post("/admin/rewards/claims/"+itoa(pending[0].ID)+"/approve", nil), http.StatusOK)
	if strings.Contains(section(wall.get("/board").body, "Alfie"), `class="goal`) {
		t.Error("goal still shown after approval")
	}

	// Stop saving.
	expect(t, wall.post(goal, url.Values{"reward": {itoa(sweets)}}), http.StatusOK, "Sweets")
	r = wall.post(goal+"/clear", nil)
	expect(t, r, http.StatusOK)
	if tt := toastOf(t, r); tt.Message != "Goal removed" {
		t.Errorf("toast = %+v", tt)
	}
	if h.person("Alfie").GoalRewardID != nil {
		t.Error("goal not cleared")
	}
}

func TestAdminPersonGoal(t *testing.T) {
	h, _, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Lego set"}, "emoji": {"🧱"}, "cost_points": {"100"}}), "/admin/rewards")
	lego := must(h.st.Q.ListRewards(t.Context(), h.hh))[0]
	page := "/admin/people/" + itoa(alfie.ID)
	expect(t, phone.get(page), http.StatusOK, "Saving for", `<option value="`+itoa(lego.ID)+`">🧱 Lego set (100 pts)</option>`)

	form := url.Values{"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"emoji"}, "emoji": {"🦖"}, "sort_order": {"1"}}
	form.Set("goal", itoa(lego.ID))
	expect(t, phone.post(page, form), http.StatusOK, "Saved.", `<option value="`+itoa(lego.ID)+`" selected>`)
	first := h.person("Alfie")
	if first.GoalRewardID == nil || *first.GoalRewardID != lego.ID {
		t.Fatalf("goal = %v", first.GoalRewardID)
	}
	// Saving again doesn't restart it.
	h.advance(time.Hour)
	expect(t, phone.post(page, form), http.StatusOK)
	if again := h.person("Alfie"); *again.GoalSetAt != *first.GoalSetAt {
		t.Error("re-saving the same goal reset it")
	}
	// A form without the field leaves it alone; an empty choice clears it.
	form.Del("goal")
	expect(t, phone.post(page, form), http.StatusOK)
	if h.person("Alfie").GoalRewardID == nil {
		t.Error("goal cleared by a form without it")
	}
	form.Set("goal", "")
	expect(t, phone.post(page, form), http.StatusOK)
	if h.person("Alfie").GoalRewardID != nil {
		t.Error("goal not cleared")
	}

	// Archiving the reward clears goals quietly.
	form.Set("goal", itoa(lego.ID))
	expect(t, phone.post(page, form), http.StatusOK)
	expectRedirect(t, phone.post("/admin/rewards/"+itoa(lego.ID)+"/archive", nil), "/admin/rewards")
	if h.person("Alfie").GoalRewardID != nil {
		t.Error("archived reward still a goal")
	}
	expect(t, phone.post(page, form), http.StatusOK, "that reward has gone")
}

func TestToastHeaderIsASCII(t *testing.T) {
	rec := httptest.NewRecorder()
	setToast(rec, toast{Message: "Enjoy, Zoë! 🎁"})
	h := rec.Header().Get("HX-Trigger")
	for _, c := range []byte(h) {
		if c >= 0x80 {
			t.Fatalf("HX-Trigger has non-ASCII: %q", h)
		}
	}
	var m map[string]toast
	if err := json.Unmarshal([]byte(h), &m); err != nil || m["toast"].Message != "Enjoy, Zoë! 🎁" {
		t.Errorf("round trip = %+v, %v (%q)", m, err, h)
	}
}
