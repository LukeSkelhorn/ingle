package server

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func zipFiles(t *testing.T, body string) map[string]string {
	t.Helper()
	z, err := zip.NewReader(strings.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		_ = r.Close()
		out[f.Name] = b.String()
	}
	return out
}

func TestHouseholdExportAndDelete(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	expectPersonCreated(t, wall.post("/admin/people", url.Values{"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"none"}}))

	expect(t, wall.get("/admin/settings"), http.StatusOK, "Your household's data", "Download our data", "starts again at first-run setup")

	r := wall.get("/admin/household/export")
	if r.status != http.StatusOK || r.header.Get("Content-Type") != "application/zip" ||
		!strings.Contains(r.header.Get("Content-Disposition"), `filename="ingle-the-smiths-`) {
		t.Fatalf("export = %d %v", r.status, r.header)
	}
	files := zipFiles(t, r.body)
	if !strings.Contains(files["data/people.json"], "Alfie") || !strings.Contains(files["data/households.json"], "The Smiths") {
		t.Errorf("export is missing the household's people: %s", files["data/people.json"])
	}
	if strings.Contains(r.body, "password_hash") || strings.Contains(r.body, "pin_hash") {
		t.Error("export has password or PIN hashes")
	}

	expect(t, wall.post("/admin/household/delete", url.Values{"confirm": {"The Smith"}}), http.StatusUnprocessableEntity, "Type the household")
	expect(t, wall.get("/admin/people"), http.StatusOK, "Alfie")

	expectRedirect(t, wall.post("/admin/household/delete", url.Values{"confirm": {" the smiths "}}), "/")
	expectRedirect(t, wall.get("/"), "/setup")
	expectRedirect(t, wall.get("/admin/people"), "/setup")
	if people, err := h.st.Q.ListPeople(h.ctx(), h.hh); err != nil || len(people) != 0 {
		t.Errorf("people after delete = %d, %v", len(people), err)
	}

	again := h.device()
	expectRedirect(t, again.post("/setup", setupValues), "/admin")
	expectNot(t, again.get("/admin/people"), "Alfie")
}

func TestHouseholdDeleteLeavesOthers(t *testing.T) {
	x := newTwoHouseholds(t)
	expectNot(t, x.bW.get("/admin/settings"), "starts again at first-run setup")

	files := zipFiles(t, x.bW.get("/admin/household/export").body)
	if strings.Contains(files["data/people.json"], "Alfie") || !strings.Contains(files["data/people.json"], "Bea") {
		t.Errorf("B's export people: %s", files["data/people.json"])
	}

	expectRedirect(t, x.aW.post("/admin/household/delete", url.Values{"confirm": {"The Smiths"}}), "/")
	if _, err := x.h.st.Q.GetHousehold(x.h.ctx(), x.a); err == nil {
		t.Error("household A still exists")
	}
	expect(t, x.bW.get("/admin/people"), http.StatusOK, "Bea")
	expect(t, x.bW.get("/board"), http.StatusOK, "Walk the dog")
}

func TestHouseholdDeleteHooks(t *testing.T) {
	x := newTwoHouseholds(t)
	var calls []string
	fail := true
	x.h.srv.HouseholdDeleting = func(_ context.Context, hh int64) error {
		calls = append(calls, fmt.Sprintf("deleting %d", hh))
		if fail {
			return errors.New("not now")
		}
		return nil
	}
	x.h.srv.HouseholdDeleted = func(_ context.Context, hh int64) {
		calls = append(calls, fmt.Sprintf("deleted %d", hh))
	}

	expect(t, x.aW.post("/admin/household/delete", url.Values{"confirm": {"The Smiths"}}), http.StatusBadGateway, "Nothing has been removed")
	if _, err := x.h.st.Q.GetHousehold(x.h.ctx(), x.a); err != nil {
		t.Errorf("household gone after the hook refused: %v", err)
	}

	fail = false
	expectRedirect(t, x.aW.post("/admin/household/delete", url.Values{"confirm": {"The Smiths"}}), "/")
	if _, err := x.h.st.Q.GetHousehold(x.h.ctx(), x.a); err == nil {
		t.Error("household A still exists")
	}
	want := fmt.Sprintf("deleting %d,deleting %d,deleted %d", x.a, x.a, x.a)
	if got := strings.Join(calls, ","); got != want {
		t.Errorf("hooks = %s, want %s", got, want)
	}
}
