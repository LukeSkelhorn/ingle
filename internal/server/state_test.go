package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestHouseholdState(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()

	var mu sync.Mutex
	st := HouseholdState{}
	var fail error
	h.srv.State = func(_ context.Context, hh int64) (HouseholdState, error) {
		mu.Lock()
		defer mu.Unlock()
		if hh != h.hh {
			t.Errorf("state asked for household %d, want %d", hh, h.hh)
		}
		return st, fail
	}
	set := func(s HouseholdState, err error) {
		mu.Lock()
		st, fail = s, err
		mu.Unlock()
	}

	adult := url.Values{"name": {"Gran"}, "kind": {"adult"}, "colour": {"#3E63DD"}, "avatar_mode": {"none"}}

	// A notice alone shows on admin pages but changes nothing.
	set(HouseholdState{Notice: "Five days left.", NoticeLabel: "Choose", NoticePath: "/admin/elsewhere"}, nil)
	expect(t, phone.get("/admin"), http.StatusOK, `class="notice"`, "Five days left.", `href="/admin/elsewhere">Choose<`)
	if r := phone.get("/"); strings.Contains(r.body, "Five days left.") {
		t.Error("board shows a notice when not read-only")
	}
	expectPersonCreated(t, phone.post("/admin/people", adult))

	// Read-only: seen, not changed.
	set(HouseholdState{ReadOnly: true, Notice: "Paused for now."}, nil)
	expect(t, phone.get("/admin"), http.StatusOK, "notice--stop", "Paused for now.")
	expect(t, phone.get("/"), http.StatusOK, "notice--stop", "Paused for now.")
	expect(t, phone.get("/admin/people"), http.StatusOK)
	expect(t, phone.post("/admin/people", url.Values{"name": {"Grandad"}, "kind": {"adult"}, "colour": {"#3E63DD"}, "avatar_mode": {"none"}}),
		http.StatusForbidden, "Can't change that", "Paused for now.")
	if r := phone.get("/admin/people"); strings.Contains(r.body, "Grandad") {
		t.Error("a person was added while read-only")
	}

	req, _ := http.NewRequestWithContext(h.ctx(), http.MethodPost, h.ts.URL+"/board/occurrences/1/complete", http.NoBody)
	req.Header.Set("HX-Request", "true")
	req.Header.Set(boardClientHeader, "test")
	r := phone.do(req)
	if r.status != http.StatusConflict || !strings.Contains(r.header.Get("HX-Trigger"), "Paused for now.") {
		t.Errorf("board tick while read-only = %d %q", r.status, r.header.Get("HX-Trigger"))
	}

	// Signing out and locking still work.
	expectRedirect(t, phone.post("/lock", nil), "/")
	if r := phone.post("/admin/sessions/others", nil); r.status == http.StatusForbidden {
		t.Error("signing out other sessions refused while read-only")
	}

	// With no notice, a refusal still says why.
	set(HouseholdState{ReadOnly: true}, nil)
	expect(t, phone.post("/admin/people", adult), http.StatusForbidden, readOnlyText)

	// An error fails open.
	set(HouseholdState{ReadOnly: true}, errors.New("down"))
	expect(t, phone.get("/admin"), http.StatusOK)
	if r := phone.get("/admin"); strings.Contains(r.body, `class="notice`) {
		t.Error("notice shown after an error")
	}
}
