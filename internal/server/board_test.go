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

	"github.com/lukeskelhorn/ingle/internal/store/dbq"
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
	if err := h.st.SQL().QueryRowContext(h.ctx(),
		"SELECT id FROM chore_occurrences WHERE chore_id = ? AND due_on = '2026-07-01'", c.ID).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	return must(h.st.Q.GetOccurrence(h.ctx(), dbq.GetOccurrenceParams{HouseholdID: h.hh, ID: id}))
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

// poolOf returns the chores in an Up for grabs sheet (GET /board/pool).
func poolOf(body string) string {
	i := strings.Index(body, `class="grabs"`)
	if i < 0 {
		return ""
	}
	return body[i:]
}

func TestBoardShowsColumnsAndPool(t *testing.T) {
	_, wall, _ := boardHousehold(t)
	r := wall.get("/")
	expect(t, r, http.StatusOK, `id="pool-btn"`, "Up for grabs", `<span class="grabs-btn__count">1</span>`,
		`id="board"`, "/board/events", `id="sheet"`)
	// The pool is in the sheet, not on the board.
	if strings.Contains(r.body, "Hoover the lounge") {
		t.Error("pool chore shown on the board")
	}
	pool := poolOf(wall.get("/board/pool").body)
	for _, want := range []string{"Up for grabs", "Hoover the lounge", "5 pts · £0.50", `data-refresh="/board/pool"`} {
		if !strings.Contains(pool, want) {
			t.Errorf("pool sheet lacks %q\n%s", want, pool)
		}
	}
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
	// A board refresh carries the header button out of band.
	expect(t, wall.get("/board"), http.StatusOK, `id="board"`, "Feed the cat", `id="pool-btn"`, `hx-swap-oob="true"`)
}

func TestBoardStrangerSeesNothing(t *testing.T) {
	h, _, _ := boardHousehold(t)
	stranger := h.device()
	for _, p := range []string{"/board", "/board/events", "/board/pool", "/board/occurrences/1/pick", "/board/people/1", "/board/redemptions/1/review"} {
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
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: h.person("Alfie").ID})); b.Points != 0 {
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
	expect(t, r, http.StatusOK, `<span id="pool-btn" hidden hx-swap-oob="true"></span>`)
	expect(t, wall.get("/board/pool"), http.StatusOK, "Nothing up for grabs right now.")
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
	// A stale pick goes back to the fresh list, not the board.
	r = phone.get(base + "/pick")
	expect(t, r, http.StatusConflict, `class="grabs"`, "Nothing up for grabs right now.")
	if strings.Contains(r.body, `id="board"`) {
		t.Error("stale pick sent the board into the sheet")
	}
	if tt := toastOf(t, r); tt.Message != "Someone's already on that one." {
		t.Errorf("stale pick toast = %+v", tt)
	}

	// Give it back.
	r = wall.post(base+"/release", nil)
	expect(t, r, http.StatusOK, `<span class="grabs-btn__count">1</span>`)
	if pool := poolOf(wall.get("/board/pool").body); !strings.Contains(pool, "Hoover the lounge") {
		t.Errorf("released chore not back in the pool\n%s", pool)
	}

	// Done straight from the pool, from a phone, by Alfie.
	r = phone.post(base+"/complete", url.Values{"person": {itoa(alfie.ID)}})
	expect(t, r, http.StatusOK, `<span id="pool-btn" hidden hx-swap-oob="true"></span>`, "5 pts · £0.50")
	if !strings.Contains(section(r.body, "Alfie"), "Hoover the lounge") {
		t.Error("done pool chore not shown under Alfie")
	}
}

func TestBoardEvents(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	occ := h.occurrence("Feed the cat")

	ctx, cancel := context.WithCancel(h.ctx())
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
	req, err = http.NewRequestWithContext(h.ctx(), http.MethodPost, h.ts.URL+"/board/occurrences/"+itoa(occ.ID)+"/complete", http.NoBody)
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
		return must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID})).Points
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
	ctx, cancel := context.WithTimeout(h.ctx(), 5*time.Second)
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
	expect(t, phone.get("/admin"), http.StatusOK, `href="/admin/completions"`, "Ingle v9.8.7")
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
	balance := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID}))
	if balance.Points != 5 {
		t.Errorf("points = %d after approve, want 5", balance.Points)
	}
	expect(t, phone.post("/admin/completions/"+pending[0]+"/approve", nil), http.StatusConflict, "already checked")

	r = phone.post("/admin/completions/"+reversible[0]+"/reverse", nil)
	expect(t, r, http.StatusOK, "Reversed", "Not counted · Mum")
	balance = must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID}))
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
	rewards := must(h.st.Q.ListRewards(h.ctx(), h.hh))
	for _, rw := range rewards {
		if rw.Title == "Sweets" {
			expectRedirect(t, phone.post("/admin/rewards/"+itoa(rw.ID)+"/archive", nil), "/admin/rewards")
		}
	}

	// 40 pts and £1 from last week (it's Wednesday 1 July; the week began
	// Monday 29 June), plus Feed the cat (2 pts) today.
	if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
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
	for _, rw := range must(h.st.Q.ListRewards(h.ctx(), h.hh)) {
		switch rw.Title {
		case "Screen time":
			screen = rw.ID
		case "Lego set":
			lego = rw.ID
		}
	}
	claim := "/board/rewards/" + itoa(screen) + "/claim"
	if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
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

	waiting := must(h.st.Q.ListPendingRedemptions(h.ctx(), h.hh))
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
	id = itoa(must(h.st.Q.ListPendingRedemptions(h.ctx(), h.hh))[0].ID)
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
	if n := len(must(h.st.Q.ListPendingRedemptions(h.ctx(), h.hh))); n != 0 {
		t.Errorf("%d claims waiting after an adult claimed", n)
	}

	// The feed names the reward.
	expect(t, phone.get("/admin"), http.StatusOK, "Alfie</strong> claimed Screen time")
}

func TestAdminRewardClaims(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Screen time"}, "cost_points": {"30"}}), "/admin/rewards")
	screen := must(h.st.Q.ListRewards(h.ctx(), h.hh))[0]
	if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
		HouseholdID: h.hh, PersonID: alfie.ID, Points: 70, Kind: "completion", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	claim := "/board/rewards/" + itoa(screen.ID) + "/claim"
	for range 2 {
		expect(t, wall.post(claim, url.Values{"person": {itoa(alfie.ID)}}), http.StatusOK)
	}
	expect(t, phone.get("/admin"), http.StatusOK, "<strong>2</strong><span>Waiting for approval")
	pending := must(h.st.Q.ListPendingRedemptions(h.ctx(), h.hh))
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
	for _, rw := range must(h.st.Q.ListRewards(h.ctx(), h.hh)) {
		switch rw.Title {
		case "Lego set":
			lego = rw.ID
		case "Sweets":
			sweets = rw.ID
		}
	}
	if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
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
	if _, err := h.st.Q.InsertLedgerEntry(h.ctx(), dbq.InsertLedgerEntryParams{
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
	pending := must(h.st.Q.ListPendingRedemptions(h.ctx(), h.hh))
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
	lego := must(h.st.Q.ListRewards(h.ctx(), h.hh))[0]
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

func TestBoardSharedChore(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	mum, alfie, isla := h.person("Mum"), h.person("Alfie"), h.person("Isla")
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"School run"}, "mode": {"shared"}, "people_ids": {itoa(mum.ID), itoa(isla.ID)},
		"repeat": {"daily"}, "starts_on": {"2026-07-01"}, "points": {"0"},
	}), "/admin/chores")
	occ := h.occurrence("School run")

	r := wall.get("/board")
	for _, p := range []dbq.Person{mum, isla} {
		sec := section(r.body, p.Name)
		if !strings.Contains(sec, "School run") || !strings.Contains(sec, `hx-vals='{"person": "`+itoa(p.ID)+`"}'`) {
			t.Errorf("%s's column has no School run tapping as them:\n%s", p.Name, sec)
		}
	}
	if strings.Contains(section(r.body, "Alfie"), "School run") || strings.Contains(poolOf(wall.get("/board/pool").body), "School run") {
		t.Error("School run shown for Alfie or in Up for grabs")
	}
	if !strings.Contains(section(r.body, "Mum"), `aria-label="Shared with Isla"`) {
		t.Error("Mum's card doesn't say who it's shared with")
	}

	// Alfie isn't one of them.
	r = wall.post("/board/occurrences/"+itoa(occ.ID)+"/complete", url.Values{"person": {itoa(alfie.ID)}})
	if r.status != http.StatusConflict || !toastOf(t, r).Error {
		t.Errorf("Alfie completing = %d %+v", r.status, toastOf(t, r))
	}
	// Without saying who, it can't tell.
	r = wall.post("/board/occurrences/"+itoa(occ.ID)+"/complete", nil)
	if tt := toastOf(t, r); tt.Message != "Choose who did it." {
		t.Errorf("no person toast = %+v", tt)
	}

	r = wall.post("/board/occurrences/"+itoa(occ.ID)+"/complete", url.Values{"person": {itoa(isla.ID)}})
	expect(t, r, http.StatusOK)
	if tt := toastOf(t, r); tt.Message != "Nice one, Isla! Done for everyone." {
		t.Errorf("toast = %+v", tt)
	}
	if strings.Contains(section(r.body, "Mum"), "School run") {
		t.Error("still in Mum's column after Isla did it")
	}
	if !strings.Contains(section(r.body, "Isla"), "School run") {
		t.Error("not ticked in Isla's column")
	}
}

// The page script keeps each column's scroll position across refreshes by
// person, and starts afresh when the board day changes.
func TestBoardScrollHooks(t *testing.T) {
	h, wall, _ := boardHousehold(t)
	alfie := h.person("Alfie")
	expect(t, wall.get("/board"), http.StatusOK,
		`id="board" class="board__body" data-day="2026-07-01"`,
		`aria-label="Alfie" data-person="`+itoa(alfie.ID)+`"`)
	expect(t, wall.get("/"), http.StatusOK, "htmx:beforeSwap", "has-more")
}

func TestBoardOnTheDay(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie, isla := h.person("Alfie"), h.person("Isla")
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Bins out"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"once"},
		"starts_on": {"2026-07-01"}, "points": {"3"}, "due_time": {"08:00"}, "on_the_day": {"1"},
	}), "/admin/chores")
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Milk out"}, "mode": {"pool"}, "repeat": {"once"},
		"starts_on": {"2026-07-01"}, "points": {"1"}, "on_the_day": {"1"},
	}), "/admin/chores")
	bins, milk := h.occurrence("Bins out"), h.occurrence("Milk out")

	r := phone.get("/admin/chores")
	expect(t, r, http.StatusOK, "Only on the day")
	expect(t, phone.get("/admin/chores/"+itoa(h.chore("Bins out").ID)), http.StatusOK,
		`name="on_the_day" value="1" checked`)

	// 00:30 BST: on the board until 08:00, refreshing then.
	r = wall.get("/board")
	expect(t, r, http.StatusOK, "Until 08:00", `data-next-in="27001"`)
	expect(t, wall.get("/board/pool"), http.StatusOK, "Milk out", "Today only")
	if strings.Contains(section(r.body, "Alfie"), "by 08:00</span><span class=\"chip\">3") {
		t.Error("shows both by and until")
	}

	h.advance(8 * time.Hour) // 08:30
	r = wall.get("/board")
	if strings.Contains(r.body, "Bins out") {
		t.Error("bins still on the board at 08:30")
	}
	r = wall.post("/board/occurrences/"+itoa(bins.ID)+"/complete", nil)
	expect(t, r, http.StatusConflict)
	if tt := toastOf(t, r); !tt.Error || tt.Message != "Too late for that one." {
		t.Errorf("late toast = %+v", tt)
	}

	// Next day it's under Missed.
	h.advance(24 * time.Hour)
	if strings.Contains(wall.get("/board/pool").body, "Milk out") {
		t.Error("milk carried over")
	}
	r = phone.get("/admin/completions")
	expect(t, r, http.StatusOK, "Missed", "Bins out", "Milk out",
		`action="/admin/completions/missed/`+itoa(bins.ID)+`/done"`, `name="person" value="`+itoa(alfie.ID)+`"`, "Who did it?")
	expect(t, wall.post("/admin/completions/missed/"+itoa(bins.ID)+"/done", url.Values{"person": {itoa(alfie.ID)}}), http.StatusSeeOther)
	expect(t, phone.post("/admin/completions/missed/"+itoa(milk.ID)+"/done", nil), http.StatusBadRequest, "Choose who did it.")
	expect(t, phone.post("/admin/completions/missed/"+itoa(bins.ID)+"/done", url.Values{"person": {itoa(alfie.ID)}}), http.StatusOK, "Marked done. Points added.")
	r = phone.post("/admin/completions/missed/"+itoa(milk.ID)+"/done", url.Values{"person": {itoa(isla.ID)}})
	expect(t, r, http.StatusOK, "Marked done.")
	if strings.Contains(r.body, "<h2>Missed</h2>") {
		t.Error("Missed still shown after marking both")
	}
	for p, want := range map[int64]int64{alfie.ID: 3, isla.ID: 1} {
		if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: p})); b.Points != want {
			t.Errorf("person %d points = %d, want %d", p, b.Points, want)
		}
	}
}

func TestMissedDismiss(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	for _, title := range []string{"Bins out", "Milk out"} {
		expectRedirect(t, phone.post("/admin/chores", url.Values{
			"title": {title}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"once"},
			"starts_on": {"2026-07-01"}, "points": {"3"}, "due_time": {"08:00"}, "on_the_day": {"1"},
		}), "/admin/chores")
	}
	bins, milk := h.occurrence("Bins out"), h.occurrence("Milk out")
	h.advance(24 * time.Hour)

	r := phone.get("/admin/completions")
	expect(t, r, http.StatusOK, "Dismiss all 2 missed chores?",
		`action="/admin/completions/missed/`+itoa(bins.ID)+`/dismiss"`, `aria-label="Dismiss Bins out"`)

	// Signed-out display: refused.
	expect(t, wall.post("/admin/completions/missed/"+itoa(bins.ID)+"/dismiss", nil), http.StatusSeeOther)

	// One, then undo.
	r = phone.post("/admin/completions/missed/"+itoa(bins.ID)+"/dismiss", nil)
	expect(t, r, http.StatusOK, "Dismissed.", `action="/admin/completions/missed/restore"`, `name="id" value="`+itoa(bins.ID)+`"`)
	missedList := func(body string) string {
		_, rest, _ := strings.Cut(body, "<h2>Missed</h2>")
		list, _, _ := strings.Cut(rest, "</section>")
		return list
	}
	if l := missedList(r.body); !strings.Contains(l, "Milk out") || strings.Contains(l, "Bins out") {
		t.Errorf("after dismissing bins, Missed = %q", l)
	}
	if strings.Contains(r.body, "Dismiss all") {
		t.Error("Dismiss all shown for one chore")
	}
	expect(t, phone.post("/admin/completions/missed/"+itoa(bins.ID)+"/dismiss", nil), http.StatusConflict, "not on the missed list any more")
	expect(t, phone.post("/admin/completions/missed/restore", url.Values{"id": {itoa(bins.ID)}}), http.StatusOK, "Back on the missed list.", "Bins out")

	// All, then undo.
	r = phone.post("/admin/completions/missed/dismiss", url.Values{"id": {itoa(bins.ID), itoa(milk.ID)}})
	expect(t, r, http.StatusOK, "Dismissed 2 missed chores.")
	if strings.Contains(r.body, "<h2>Missed</h2>") {
		t.Error("Missed still shown after dismiss all")
	}
	expect(t, phone.post("/admin/completions/missed/dismiss", nil), http.StatusBadRequest, "Nothing to dismiss.")
	expect(t, phone.post("/admin/completions/missed/restore", url.Values{"id": {"x"}}), http.StatusBadRequest, "Nothing to undo.")
	expect(t, phone.post("/admin/completions/missed/restore", url.Values{"id": {itoa(bins.ID), itoa(milk.ID)}}), http.StatusOK, "Bins out", "Milk out")
	if b := must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID})); b.Points != 0 {
		t.Errorf("points = %d, want 0", b.Points)
	}
}

// Give / Take points from the ⋯ menu on Admin › People: the pop-up, its
// mistakes, the flash with Undo, and the balance on each row.
func TestAdjustPointsFromPeopleList(t *testing.T) {
	h, _, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	id := itoa(alfie.ID)
	balance := func() int64 {
		t.Helper()
		return must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID})).Points
	}

	// The list: each row has a balance and a ⋯ menu; no pop-up yet.
	r := phone.get("/admin/people")
	expect(t, r, http.StatusOK, `class="list__balance">0 pts</span>`, `aria-label="More for Alfie"`,
		`href="/admin/people?adjust=`+id+`&amp;direction=give">Give points`,
		`href="/admin/people?adjust=`+id+`&amp;direction=take">Take points away`,
		`href="/admin/people/`+id+`">Edit`)
	if strings.Contains(r.body, `id="adjust"`) {
		t.Error("pop-up open without ?adjust")
	}

	// Give points opens the pop-up with Give chosen.
	expect(t, phone.get("/admin/people?adjust="+id+"&direction=give"), http.StatusOK,
		`id="adjust"`, "Give points to Alfie", `name="direction" value="give" checked`,
		`<input type="hidden" name="from" value="people">`, `action="/admin/people/`+id+`/points"`)
	expect(t, phone.get("/admin/people?adjust="+id+"&direction=take"), http.StatusOK,
		"Take points from Alfie", `name="direction" value="take" checked`)
	expect(t, phone.get("/admin/people?adjust=99999"), http.StatusNotFound)

	// A mistake keeps the pop-up open with what was typed.
	r = phone.post("/admin/people/"+id+"/points", url.Values{"from": {"people"}, "direction": {"give"}, "amount": {"5"}, "reason": {" "}})
	expect(t, r, http.StatusUnprocessableEntity, `id="adjust"`, "Say what they did and why", `name="amount" value="5" checked`)
	if b := balance(); b != 0 {
		t.Fatalf("balance after a mistake = %d, want 0", b)
	}

	// Saved: back on the list, no pop-up, flash with Undo, new balance.
	r = phone.post("/admin/people/"+id+"/points", url.Values{"from": {"people"}, "direction": {"give"}, "amount": {"5"}, "reason": {"Tidied without being asked"}})
	expect(t, r, http.StatusOK, "<h1>People</h1>", "Gave 5 points to Alfie.", `class="list__balance">5 pts</span>`)
	if strings.Contains(r.body, `id="adjust"`) {
		t.Error("pop-up still open after saving")
	}
	if b := balance(); b != 5 {
		t.Fatalf("balance = %d, want 5", b)
	}
	var entry int64
	if err := h.st.SQL().QueryRowContext(h.ctx(), "SELECT MAX(id) FROM ledger_entries WHERE kind = 'manual'").Scan(&entry); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.body, `action="/admin/people/`+id+`/points/`+itoa(entry)+`/undo"`) {
		t.Error("no undo form on the list")
	}

	// Undo from the list returns to the list.
	r = phone.post("/admin/people/"+id+"/points/"+itoa(entry)+"/undo", url.Values{"from": {"people"}})
	expect(t, r, http.StatusOK, "<h1>People</h1>", "Undone.", `class="list__balance">0 pts</span>`)
	if b := balance(); b != 0 {
		t.Errorf("after undo balance = %d, want 0", b)
	}

	// Money shows too, once they have some.
	occ := h.occurrence("Hoover the lounge")
	expect(t, phone.post("/board/occurrences/"+itoa(occ.ID)+"/complete", url.Values{"person": {id}}), http.StatusOK)
	expect(t, phone.get("/admin/people"), http.StatusOK, `class="list__balance">5 pts · £0.50</span>`)
}

func TestAdjustPointsAdmin(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	page := "/admin/people/" + itoa(alfie.ID)
	balance := func() int64 {
		t.Helper()
		return must(h.st.Q.BalanceForPerson(h.ctx(), dbq.BalanceForPersonParams{HouseholdID: h.hh, PersonID: alfie.ID})).Points
	}
	expect(t, phone.get(page), http.StatusOK, "Adjust points", `name="amount" value="5" checked`, "Alfie has <strong class=\"points-balance\">0 pts</strong>")

	// Signed-out display: refused.
	expect(t, wall.post(page+"/points", url.Values{"direction": {"take"}, "amount": {"5"}, "reason": {"Hit his sister"}}), http.StatusSeeOther)

	// A quick amount, taken away, goes below zero.
	r := phone.post(page+"/points", url.Values{"direction": {"take"}, "amount": {"5"}, "reason": {"Hit his sister. Hitting hurts."}})
	expect(t, r, http.StatusOK, "Took 5 points from Alfie.", "−5 pts", "Hit his sister. Hitting hurts.", `<datalist id="reason-suggestions">`)
	if b := balance(); b != -5 {
		t.Fatalf("balance = %d, want -5", b)
	}
	var entry int64
	if err := h.st.SQL().QueryRowContext(h.ctx(), "SELECT MAX(id) FROM ledger_entries WHERE kind = 'manual'").Scan(&entry); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.body, `action="/admin/people/`+itoa(alfie.ID)+`/points/`+itoa(entry)+`/undo"`) {
		t.Error("no undo form")
	}

	// The board shows it, reason and all.
	expect(t, wall.get("/board"), http.StatusOK, "−5 pts")
	expect(t, wall.get("/board/people/"+itoa(alfie.ID)), http.StatusOK, "Points changes", "−5", "Hit his sister. Hitting hurts.", "This week: −5 pts")

	// Undo, then it can't be undone again.
	expect(t, phone.post(page+"/points/"+itoa(entry)+"/undo", nil), http.StatusOK, "Undone.")
	if b := balance(); b != 0 {
		t.Errorf("after undo balance = %d, want 0", b)
	}
	expect(t, phone.post(page+"/points/"+itoa(entry)+"/undo", nil), http.StatusConflict, "can&#39;t be undone any more")

	// A custom amount, given.
	expect(t, phone.post(page+"/points", url.Values{"direction": {"give"}, "amount": {"custom"}, "amount_custom": {"12"}, "reason": {"Helped Gran with the shopping"}}),
		http.StatusOK, "Gave 12 points to Alfie.", "&#43;12", `name="direction" value="give" checked`)
	if b := balance(); b != 12 {
		t.Errorf("balance = %d, want 12", b)
	}

	// Mistakes keep what was typed.
	expect(t, phone.post(page+"/points", url.Values{"direction": {"take"}, "amount": {"custom"}, "amount_custom": {"0"}, "reason": {"Rude"}}),
		http.StatusUnprocessableEntity, "Choose how many points", `value="Rude"`)
	expect(t, phone.post(page+"/points", url.Values{"direction": {"take"}, "amount": {"3"}, "reason": {" "}}),
		http.StatusUnprocessableEntity, "Choose how many points", "Say what they did and why")
	if b := balance(); b != 12 {
		t.Errorf("balance after errors = %d, want 12", b)
	}
}

func TestPointsFormatting(t *testing.T) {
	for n, want := range map[int64]string{0: "0 pts", 1: "1 pt", -1: "−1 pt", 12: "12 pts", -5: "−5 pts"} {
		if got := pts(n); got != want {
			t.Errorf("pts(%d) = %q, want %q", n, got, want)
		}
	}
	for n, want := range map[int64]string{5: "+5", -5: "−5"} {
		if got := signedPts(n); got != want {
			t.Errorf("signedPts(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestChoreTimes(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	alfie := h.person("Alfie")
	form := url.Values{
		"title": {"Brush teeth"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"daily"},
		"starts_on": {"2026-07-01"}, "points": {"1"}, "due_time": {"19:30", "07:30", ""}, "show_before": {"60"},
	}
	// The new-chore form starts at an hour before.
	expect(t, phone.get("/admin/chores/new"), http.StatusOK, `<option value="60" selected>1 hour before</option>`, "+ Another time")
	expectRedirect(t, phone.post("/admin/chores", form), "/admin/chores")
	teeth := h.chore("Brush teeth")
	if teeth.DueTimes != "07:30,19:30" || teeth.ShowBefore == nil || *teeth.ShowBefore != 60 {
		t.Fatalf("saved %q / %v", teeth.DueTimes, teeth.ShowBefore)
	}
	expect(t, phone.get("/admin/chores"), http.StatusOK, "by 07:30 and 19:30 · shows 1 hour before")
	expect(t, phone.get("/admin/chores/"+itoa(teeth.ID)), http.StatusOK,
		`name="due_time" value="07:30"`, `name="due_time" value="19:30"`, `<option value="60" selected>`)

	// A flexible chore can only have one time.
	bad := url.Values{
		"title": {"Wash the car"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"weekly"},
		"days": {"2", "4"}, "day_mode": {"any"}, "starts_on": {"2026-07-01"}, "due_time": {"10:00", "15:00"},
	}
	expect(t, phone.post("/admin/chores", bad), http.StatusUnprocessableEntity, "can have one time")

	// 00:30 local: neither is on the board yet, and the board knows when to
	// look again (06:30, six hours away).
	r := wall.get("/board")
	if col := section(r.body, "Alfie"); strings.Contains(col, "Brush teeth") {
		t.Errorf("00:30 column shows Brush teeth: %s", col)
	}
	expect(t, r, http.StatusOK, `data-next-in="21601"`)

	h.advance(6 * time.Hour) // 06:30
	col := section(wall.get("/board").body, "Alfie")
	if !strings.Contains(col, "by 07:30") || strings.Contains(col, "by 19:30") {
		t.Errorf("06:30 column: %s", col)
	}
	var morning int64
	if err := h.st.SQL().QueryRowContext(h.ctx(),
		"SELECT id FROM chore_occurrences WHERE chore_id = ? AND due_on = '2026-07-01' AND due_time = '07:30'", teeth.ID).Scan(&morning); err != nil {
		t.Fatal(err)
	}
	expect(t, wall.post("/board/occurrences/"+itoa(morning)+"/complete", nil), http.StatusOK)
	// My money's history says which time it was for.
	expect(t, phone.get("/admin/people/"+itoa(alfie.ID)+"/money"), http.StatusOK, "Brush teeth · 07:30")

	h.advance(12 * time.Hour) // 18:30
	col = section(wall.get("/board").body, "Alfie")
	if !strings.Contains(col, "by 19:30") {
		t.Errorf("18:30 column: %s", col)
	}
	expect(t, phone.get("/admin/completions"), http.StatusOK, "Brush teeth · 07:30")
}

func TestEachChore(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	mum, alfie, isla := h.person("Mum"), h.person("Alfie"), h.person("Isla")
	expect(t, phone.get("/admin/chores/new"), http.StatusOK, `value="each"`, "Each of these people", `name="adult_points"`)

	// Nobody chosen.
	expect(t, phone.post("/admin/chores", url.Values{
		"title": {"Exercise"}, "mode": {"each"}, "each_who": {"chosen"}, "repeat": {"daily"}, "starts_on": {"2026-07-01"},
	}), http.StatusUnprocessableEntity, "Choose who does it, or Everyone.")

	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Brush teeth"}, "mode": {"each"}, "each_who": {"everyone"}, "repeat": {"daily"},
		"starts_on": {"2026-07-01"}, "points": {"2"}, "money": {"0.10"}, "adult_points": {"1"},
	}), "/admin/chores")
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Exercise"}, "mode": {"each"}, "each_who": {"chosen"}, "each_ids": {itoa(mum.ID), itoa(isla.ID)},
		// people_ids belong to "Any of these people" and are ignored here.
		"people_ids": {itoa(alfie.ID)},
		"repeat":     {"daily"}, "starts_on": {"2026-07-01"}, "points": {"3"}, "adult_points": {"2"},
	}), "/admin/chores")
	teeth, exercise := h.chore("Brush teeth"), h.chore("Exercise")
	if teeth.Mode != "each" || teeth.ForEveryone != 1 || teeth.AdultPoints != 1 || teeth.Pence != 10 {
		t.Fatalf("teeth = %+v", teeth)
	}
	expect(t, phone.get("/admin/chores"), http.StatusOK, "Everyone · Every day", "Each: Mum and Isla · Every day")
	expect(t, phone.get("/admin/chores/"+itoa(exercise.ID)), http.StatusOK,
		`name="each_who" value="chosen" checked`, `name="each_ids" value="`+itoa(mum.ID)+`" checked`,
		`name="each_ids" value="`+itoa(isla.ID)+`" checked`, `name="adult_points" value="2"`)
	expect(t, phone.get("/admin/chores/"+itoa(teeth.ID)), http.StatusOK, `name="each_who" value="everyone" checked`)
	// Alfie's filter includes the Everyone chore, not Exercise.
	if body := phone.get("/admin/chores?person=" + itoa(alfie.ID)).body; !strings.Contains(body, "Brush teeth") || strings.Contains(body, "Exercise") {
		t.Errorf("Alfie's chores list:\n%s", body)
	}

	// On the board: everyone has their own, worth what they'd earn.
	r := wall.get("/board")
	for _, p := range []dbq.Person{mum, alfie, isla} {
		if !strings.Contains(section(r.body, p.Name), "Brush teeth") {
			t.Errorf("%s has no Brush teeth", p.Name)
		}
	}
	if strings.Contains(section(r.body, "Alfie"), "Exercise") || strings.Contains(poolOf(wall.get("/board/pool").body), "Brush teeth") {
		t.Error("Exercise shown for Alfie, or Brush teeth up for grabs")
	}
	if mumCol := section(r.body, "Mum"); strings.Contains(mumCol, "£0.10") || !strings.Contains(mumCol, ">1 pt<") {
		t.Errorf("Mum's Brush teeth should be 1 pt and no money:\n%s", mumCol)
	}
	if !strings.Contains(section(r.body, "Alfie"), "2 pts · £0.10") {
		t.Errorf("Alfie's Brush teeth should be 2 pts · £0.10")
	}

	copyOf := func(p dbq.Person) int64 {
		var id int64
		if err := h.st.SQL().QueryRowContext(h.ctx(),
			"SELECT id FROM chore_occurrences WHERE chore_id = ? AND due_on = '2026-07-01' AND assignee_id = ?", teeth.ID, p.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	expect(t, wall.post("/board/occurrences/"+itoa(copyOf(alfie))+"/complete", nil), http.StatusOK)
	r = wall.get("/board")
	if !strings.Contains(section(r.body, "Isla"), "Brush teeth") {
		t.Error("Alfie doing his took Isla's off")
	}

	// Someone new gets the Everyone chore straight away.
	expectPersonCreated(t, phone.post("/admin/people", url.Values{
		"name": {"Dad"}, "kind": {"adult"}, "colour": {"#4A90D9"}, "avatar_mode": {"none"}, "sort_order": {"3"},
	}))
	if !strings.Contains(section(wall.get("/board").body, "Dad"), "Brush teeth") {
		t.Error("Dad has no Brush teeth")
	}
	// Archiving Isla takes her copies still to do with her, and her off Exercise.
	expectRedirect(t, phone.post("/admin/people/"+itoa(isla.ID)+"/archive", nil), "/admin/people")
	var left int
	if err := h.st.SQL().QueryRowContext(h.ctx(),
		"SELECT count(*) FROM chore_occurrences WHERE for_person = ? AND status = 'open'", isla.ID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("Isla has %d open copies left", left)
	}
	expect(t, phone.get("/admin/chores"), http.StatusOK, "Each: Mum · Every day")

	// The calendar sheet opens the copy tapped.
	expect(t, wall.get("/board/calendar?chores=1"), http.StatusOK,
		`/board/calendar/chores/`+itoa(teeth.ID)+`/2026-07-02?person=`+itoa(mum.ID)+`&amp;at="`)
	expect(t, wall.get("/board/calendar/chores/"+itoa(teeth.ID)+"/2026-07-02?person="+itoa(alfie.ID)), http.StatusOK, "Alfie", "2 pts · £0.10")
}
