package server

import (
	"archive/zip"
	"bytes"
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

	// The wrong name keeps everything.
	expect(t, wall.post("/admin/household/delete", url.Values{"confirm": {"The Smith"}}), http.StatusUnprocessableEntity, "Type the household")
	expect(t, wall.get("/admin/people"), http.StatusOK, "Alfie")

	// The right one (any case) deletes it, signs out and starts again.
	expectRedirect(t, wall.post("/admin/household/delete", url.Values{"confirm": {" the smiths "}}), "/")
	expectRedirect(t, wall.get("/"), "/setup")
	expectRedirect(t, wall.get("/admin/people"), "/setup")
	if people, err := h.st.Q.ListPeople(h.ctx(), h.hh); err != nil || len(people) != 0 {
		t.Errorf("people after delete = %d, %v", len(people), err)
	}

	// Set up again, from scratch.
	again := h.device()
	expectRedirect(t, again.post("/setup", setupValues), "/admin")
	expectNot(t, again.get("/admin/people"), "Alfie")
}

// Deleting one household of many leaves the others alone, and needs that
// household's own admin.
func TestHouseholdDeleteLeavesOthers(t *testing.T) {
	x := newTwoHouseholds(t)
	expectNot(t, x.bW.get("/admin/settings"), "starts again at first-run setup")

	// B's admin can't export or delete A: it's B's own household it gets.
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
