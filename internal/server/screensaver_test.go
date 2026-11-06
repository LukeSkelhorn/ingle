package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, x%h, color.RGBA{R: 200, A: 255})
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// postFiles uploads several files under one field name, as <input multiple>.
func (d *device) postFiles(path, field string, files map[string][]byte) resp {
	d.h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for name, data := range files {
		fw, err := mw.CreateFormFile(field, name)
		if err != nil {
			d.h.t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	req, err := http.NewRequestWithContext(d.h.t.Context(), http.MethodPost, d.h.ts.URL+path, &buf)
	if err != nil {
		d.h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", d.h.ts.URL)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return d.do(req)
}

func slidesOf(t *testing.T, d *device) []string {
	t.Helper()
	r := d.get("/screensaver/photos")
	expect(t, r, http.StatusOK)
	var out struct{ Photos []string }
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("slides JSON: %v\n%s", err, r.body)
	}
	return out.Photos
}

var idleRe = regexp.MustCompile(`idleMs =\s*(\d+)\s*\* 1000,`)

func screensaverIdle(t *testing.T, d *device) string {
	t.Helper()
	r := d.get("/")
	expect(t, r, http.StatusOK, `data-screensaver`, `id="screensaver"`)
	m := idleRe.FindStringSubmatch(r.body)
	if m == nil {
		t.Fatalf("no screensaver idle in board page\n%s", r.body)
	}
	return m[1]
}

func TestScreensaverStrangerSeesNothing(t *testing.T) {
	h, _, _ := boardHousehold(t)
	stranger := h.device()
	for _, p := range []string{"/screensaver", "/screensaver/photos", "/photos/u/0123456789abcdef0123456789abcdef", "/photos/f/0123456789abcdef"} {
		expectRedirect(t, stranger.get(p), "/signin")
	}
	for _, p := range []string{"/admin/photos", "/admin/calendars"} {
		expectRedirect(t, stranger.get(p), "/signin")
	}
}

func TestScreensaverIdleOnlyOnDisplays(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	if got := screensaverIdle(t, wall); got != "300" {
		t.Errorf("display idle = %ss, want 300", got)
	}
	if got := screensaverIdle(t, phone); got != "0" {
		t.Errorf("phone idle = %ss, want 0 (moon button only)", got)
	}
	expect(t, phone.get("/admin/settings"), http.StatusOK, `name="screensaver_idle_minutes" value="5"`)
	expect(t, phone.post("/admin/settings", url.Values{
		"household_name": {"The Smiths"}, "timezone": {"Europe/London"}, "day_starts_at": {"00:00"}, "lock_from": {"09:00"}, "lock_until": {"15:00"},
		"admin_idle_minutes": {"2"}, "screensaver_idle_minutes": {"121"},
	}), http.StatusUnprocessableEntity, "Choose between 0 (never) and 120 minutes.")
	expect(t, phone.post("/admin/settings", url.Values{
		"household_name": {"The Smiths"}, "timezone": {"Europe/London"}, "day_starts_at": {"00:00"}, "lock_from": {"09:00"}, "lock_until": {"15:00"},
		"admin_idle_minutes": {"2"}, "screensaver_idle_minutes": {"0"},
	}), http.StatusOK, "Settings saved.")
	if got := screensaverIdle(t, wall); got != "0" {
		t.Errorf("display idle after turning off = %ss, want 0", got)
	}
	_ = h
}

func TestScreensaverPanel(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	// No calendars yet: clock and chores only.
	r := wall.get("/screensaver")
	expect(t, r, http.StatusOK, `class="saver__time">00:30<`, "Wednesday", "0 of 1", "Tap anywhere")
	if strings.Contains(r.body, "Today") {
		t.Errorf("shows a Today heading with no calendars\n%s", r.body)
	}

	// It's 00:30 BST on Wed 1 July.
	h.setICS("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:1\r\nDTSTART;TZID=Europe/London:20260701T090000\r\nDTEND;TZID=Europe/London:20260701T100000\r\nSUMMARY:Swimming\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:2\r\nDTSTART;VALUE=DATE:20260702\r\nSUMMARY:Sports day\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:3\r\nDTSTART:20260630T180000Z\r\nDTEND:20260630T190000Z\r\nSUMMARY:Already over\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:4\r\nDTSTART;TZID=Europe/London:20260703T090000\r\nSUMMARY:Too far ahead\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n")
	expect(t, phone.post("/admin/calendars", url.Values{"name": {"Family"}, "url": {h.ics.URL + "/secret-token/basic.ics"}}),
		http.StatusOK, "Family added.")

	r = wall.get("/screensaver")
	expect(t, r, http.StatusOK, "Today", "09:00</span> <span class=\"saver__title\">Swimming", "Tomorrow", "All day</span> <span class=\"saver__title\">Sports day")
	for _, no := range []string{"Already over", "Too far ahead"} {
		if strings.Contains(r.body, no) {
			t.Errorf("shows %q", no)
		}
	}

	// Finishing the chore shows on the panel.
	expect(t, wall.post("/board/occurrences/"+itoa(h.occurrence("Feed the cat").ID)+"/complete", nil), http.StatusOK)
	expect(t, wall.get("/screensaver"), http.StatusOK, "All done ✓")
}

func TestPhotosAdmin(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	expectRedirect(t, wall.get("/admin/photos"), "/admin/unlock")
	expect(t, phone.get("/admin/photos"), http.StatusOK, "No photos yet")
	if s := slidesOf(t, wall); len(s) != 0 {
		t.Fatalf("slides = %v, want none", s)
	}

	r := phone.postFiles("/admin/photos", "photos", map[string][]byte{
		"beach.jpg": testJPEG(t, 300, 200),
		"notes.txt": []byte("not a photo"),
	})
	expect(t, r, http.StatusOK, "Added 1 photo.", "notes.txt: that file isn&#39;t a JPEG, PNG or GIF image.")
	expect(t, phone.postFiles("/admin/photos", "photos", map[string][]byte{"x.txt": []byte("no")}), http.StatusUnprocessableEntity, "x.txt:")

	slides := slidesOf(t, wall)
	if len(slides) != 1 || !strings.HasPrefix(slides[0], "/photos/u/") {
		t.Fatalf("slides = %v", slides)
	}
	img := wall.get(slides[0])
	expect(t, img, http.StatusOK)
	if img.header.Get("Content-Type") != "image/jpeg" || !strings.Contains(img.header.Get("Cache-Control"), "immutable") {
		t.Errorf("headers = %v", img.header)
	}
	thumb := wall.get(slides[0] + "?s=thumb")
	if cfg, _, err := image.DecodeConfig(strings.NewReader(thumb.body)); err != nil || cfg.Width != 400 {
		t.Errorf("thumb %dx%d, %v", cfg.Width, cfg.Height, err)
	}

	// A folder photo joins the slideshow after a rescan.
	if err := os.WriteFile(filepath.Join(h.photoDir, "garden.jpg"), testJPEG(t, 120, 80), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.lib.Rescan(t.Context()); err != nil {
		t.Fatal(err)
	}
	slides = slidesOf(t, wall)
	if len(slides) != 2 || !strings.HasPrefix(slides[1], "/photos/f/") {
		t.Fatalf("slides = %v", slides)
	}
	folder := wall.get(slides[1])
	expect(t, folder, http.StatusOK)
	if folder.header.Get("ETag") == "" {
		t.Error("folder photo has no ETag")
	}
	expect(t, phone.get("/admin/photos"), http.StatusOK, "Also showing 1 photo from the photos folder")
	expect(t, wall.get("/photos/f/..%2f..%2fingle.db"), http.StatusNotFound)
	expect(t, wall.get("/photos/x/"+strings.TrimPrefix(slides[0], "/photos/u/")), http.StatusNotFound)

	id := regexp.MustCompile(`/admin/photos/(\d+)/delete`).FindStringSubmatch(phone.get("/admin/photos").body)
	if id == nil {
		t.Fatal("no delete form")
	}
	expect(t, phone.post("/admin/photos/"+id[1]+"/delete", nil), http.StatusOK, "Photo removed.")
	expect(t, wall.get(slides[0]), http.StatusNotFound)
	expect(t, phone.post("/admin/photos/"+id[1]+"/delete", nil), http.StatusNotFound, "already gone")
	expect(t, phone.get("/admin"), http.StatusOK, "added a photo", "removed a photo")
}

func TestCalendarsAdmin(t *testing.T) {
	h, wall, phone := boardHousehold(t)
	expectRedirect(t, wall.get("/admin/calendars"), "/admin/unlock")
	expect(t, phone.get("/admin/calendars"), http.StatusOK, "Add a calendar", "Secret address in iCal format")

	expect(t, phone.post("/admin/calendars", url.Values{"name": {"Family"}, "url": {"file:///etc/passwd"}}),
		http.StatusUnprocessableEntity, "Enter a calendar address starting https:// or webcal://.")
	// Nothing served yet: the address is checked before saving.
	secret := h.ics.URL + "/private-abc123/basic.ics"
	r := phone.post("/admin/calendars", url.Values{"name": {"Family"}, "url": {secret}})
	expect(t, r, http.StatusUnprocessableEntity, "Couldn&#39;t read that calendar")

	h.setICS("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nEND:VCALENDAR\r\n")
	r = phone.post("/admin/calendars", url.Values{"name": {"Family"}, "url": {secret}})
	expect(t, r, http.StatusOK, "Family added.", "Updated just now")
	if strings.Contains(r.body, "private-abc123") {
		t.Error("calendar list shows the secret address")
	}
	expect(t, phone.get("/admin/calendars"), http.StatusOK, strings.TrimPrefix(h.ics.URL, "http://")+"/…")

	h.setICS("")
	expect(t, phone.post("/admin/calendars/refresh", nil), http.StatusOK, "Calendars refreshed.", "Last try failed")

	id := regexp.MustCompile(`/admin/calendars/(\d+)/delete`).FindStringSubmatch(phone.get("/admin/calendars").body)
	if id == nil {
		t.Fatal("no remove form")
	}
	expect(t, phone.post("/admin/calendars/"+id[1]+"/delete", nil), http.StatusOK, "Calendar removed.")
	expect(t, phone.post("/admin/calendars/"+id[1]+"/delete", nil), http.StatusNotFound, "already gone")
	expect(t, phone.get("/admin"), http.StatusOK, "added a calendar", "removed a calendar")
}
