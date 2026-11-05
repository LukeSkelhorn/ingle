package server

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lukeskelhorn/domus/internal/auth"
	"github.com/lukeskelhorn/domus/internal/avatar"
	"github.com/lukeskelhorn/domus/internal/calendar"
	"github.com/lukeskelhorn/domus/internal/chores"
	"github.com/lukeskelhorn/domus/internal/household"
	"github.com/lukeskelhorn/domus/internal/locale"
	"github.com/lukeskelhorn/domus/internal/lock"
	"github.com/lukeskelhorn/domus/internal/photos"
	"github.com/lukeskelhorn/domus/internal/settings"
	"github.com/lukeskelhorn/domus/internal/store"
	"github.com/lukeskelhorn/domus/internal/store/dbq"
)

type harness struct {
	t    *testing.T
	ts   *httptest.Server
	st   *store.Store
	hh   int64
	auth *auth.Service
	// photoDir is the read-only photos folder (DOMUS_PHOTOS_DIR).
	photoDir string
	lib      *photos.Library
	lock     *lock.Service
	// ics serves calendar feeds: icsBody at any path.
	ics *httptest.Server

	mu      sync.Mutex
	now     time.Time
	icsBody string
}

func (h *harness) setICS(body string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.icsBody = body
}

func (h *harness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(dir, "domus.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.New(db)

	uk := locale.UK()
	// 30 June 2026 23:30 UTC is 1 July 00:30 BST.
	h := &harness{t: t, st: st, now: time.Date(2026, 6, 30, 23, 30, 0, 0, time.UTC)}

	hh, err := st.EnsureHousehold(ctx, "Home", h.clock())
	if err != nil {
		t.Fatal(err)
	}
	h.hh = hh.ID
	set := settings.New(st)
	if err := set.Ensure(ctx, hh.ID, uk.Location); err != nil {
		t.Fatal(err)
	}
	avatars, err := avatar.NewStore(filepath.Join(dir, "avatars"))
	if err != nil {
		t.Fatal(err)
	}
	h.auth = auth.New(st, set)
	h.auth.SetClock(h.clock)
	choreSvc := chores.New(st, set.Location)
	choreSvc.SetClock(h.clock)
	hhSvc := household.New(st)
	hhSvc.SetClock(h.clock)
	log := slog.New(slog.DiscardHandler)
	h.photoDir = t.TempDir()
	if h.lib, err = photos.New(st, dir, h.photoDir, log); err != nil {
		t.Fatal(err)
	}
	h.ics = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		body := h.icsBody
		h.mu.Unlock()
		if body == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/calendar")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(h.ics.Close)
	cals := calendar.New(st, set.Location, h.ics.Client(), log)
	cals.SetClock(h.clock)

	h.lock = lock.New(st, set, h.auth.EndDisplaySessions)
	h.lock.SetClock(h.clock)
	srv, err := New(Deps{
		Store: st, HouseholdID: hh.ID, Auth: h.auth, Settings: set, Chores: choreSvc,
		Household: hhSvc, Avatars: avatars, Photos: h.lib, Calendars: cals, Lock: h.lock, Locale: uk, Log: log,
		Version: "v9.8.7",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.SetClock(h.clock)
	h.ts = httptest.NewServer(srv.Handler())
	t.Cleanup(h.ts.Close)
	return h
}

// device is a browser with its own cookie jar (the wall tablet, a phone…).
type device struct {
	h *harness
	c *http.Client
}

func (h *harness) device() *device {
	jar, err := cookiejar.New(nil)
	if err != nil {
		h.t.Fatal(err)
	}
	return &device{h: h, c: &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

type resp struct {
	status   int
	header   http.Header
	body     string
	location string
}

func (d *device) do(req *http.Request) resp {
	d.h.t.Helper()
	r, err := d.c.Do(req)
	if err != nil {
		d.h.t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		d.h.t.Fatal(err)
	}
	return resp{status: r.StatusCode, header: r.Header, body: string(b), location: r.Header.Get("Location")}
}

func (d *device) get(path string) resp {
	d.h.t.Helper()
	req, err := http.NewRequestWithContext(d.h.t.Context(), http.MethodGet, d.h.ts.URL+path, http.NoBody)
	if err != nil {
		d.h.t.Fatal(err)
	}
	return d.do(req)
}

func (d *device) post(path string, form url.Values) resp {
	d.h.t.Helper()
	req, err := http.NewRequestWithContext(d.h.t.Context(), http.MethodPost, d.h.ts.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		d.h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", d.h.ts.URL)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	return d.do(req)
}

func (d *device) postMultipart(path string, fields map[string]string, fileField, fileName string, file []byte) resp {
	d.h.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	if fileField != "" {
		fw, err := mw.CreateFormFile(fileField, fileName)
		if err != nil {
			d.h.t.Fatal(err)
		}
		_, _ = fw.Write(file)
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

func expect(t *testing.T, r resp, status int, contains ...string) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d (location %q)\n%s", r.status, status, r.location, r.body)
	}
	for _, c := range contains {
		if !strings.Contains(r.body, c) {
			t.Errorf("body does not contain %q\n%s", c, r.body)
		}
	}
}

func expectRedirect(t *testing.T, r resp, to string) {
	t.Helper()
	if r.status != http.StatusSeeOther || r.location != to {
		t.Fatalf("got %d → %q, want 303 → %q\n%s", r.status, r.location, to, r.body)
	}
}

var setupValues = url.Values{
	"household_name":   {"The Smiths"},
	"name":             {"Mum"},
	"colour":           {"#0090FF"},
	"emoji":            {"🦊"},
	"email":            {"mum@example.com"},
	"password":         {"correct horse battery"},
	"password_confirm": {"correct horse battery"},
	"pin":              {"2468"},
	"use_as_display":   {"1"},
	"display_name":     {"Kitchen"},
}

// setUp runs first-run setup from a new device, registering it as the
// household display, and returns it (unlocked as Mum).
func (h *harness) setUp() *device {
	h.t.Helper()
	d := h.device()
	expectRedirect(h.t, d.post("/setup", setupValues), "/admin")
	return d
}

func (h *harness) phone() *device {
	h.t.Helper()
	d := h.device()
	expectRedirect(h.t, d.post("/signin", url.Values{"email": {"mum@example.com"}, "password": {"correct horse battery"}}), "/admin")
	return d
}

func TestOpenRoutes(t *testing.T) {
	h := newHarness(t)
	d := h.device()
	tests := []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/healthz", "text/plain", "ok v9.8.7"},
		{"/manifest.webmanifest", "application/manifest+json", `"display": "standalone"`},
		{"/sw.js", "text/javascript", "domus-static"},
		{"/static/htmx.min.js", "javascript", "htmx"},
		{"/static/icons/icon-192.png", "image/png", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			r := d.get(tt.path)
			expect(t, r, http.StatusOK, tt.contains)
			if ct := r.header.Get("Content-Type"); !strings.Contains(ct, tt.contentType) {
				t.Errorf("Content-Type = %q, want %q", ct, tt.contentType)
			}
		})
	}
}

func TestEverythingGoesToSetupFirst(t *testing.T) {
	h := newHarness(t)
	d := h.device()
	for _, p := range []string{"/", "/admin", "/signin", "/admin/unlock", "/clock"} {
		expectRedirect(t, d.get(p), "/setup")
	}
	expect(t, d.get("/setup"), http.StatusOK, "Welcome to Domus", `name="pin"`)
}

func TestSetupValidation(t *testing.T) {
	h := newHarness(t)
	d := h.device()
	bad := url.Values{}
	for k, v := range setupValues {
		bad[k] = v
	}
	bad.Set("password_confirm", "something else entirely")
	bad.Set("pin", "12")
	expect(t, d.post("/setup", bad), http.StatusUnprocessableEntity, "The passwords don&#39;t match.")

	bad.Set("password_confirm", bad.Get("password"))
	expect(t, d.post("/setup", bad), http.StatusUnprocessableEntity, "Use 4 to 6 digits.")
	// Still not set up.
	expectRedirect(t, d.get("/"), "/setup")
}

func TestSetupRegistersDisplayAndSignsIn(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()

	expect(t, wall.get("/admin"), http.StatusOK, "Hello Mum", "The Smiths", "set up the household")
	// The board shows on the display, with the admin link.
	expect(t, wall.get("/"), http.StatusOK, "The Smiths", "01/07/2026", `href="/admin"`)
	expect(t, wall.get("/clock"), http.StatusOK, "00:30")

	// Setup is gone once done.
	expect(t, wall.get("/setup"), http.StatusNotFound)
	expect(t, wall.post("/setup", setupValues), http.StatusNotFound)
	expect(t, h.device().get("/setup"), http.StatusNotFound)
}

func TestSetupWithoutDisplay(t *testing.T) {
	h := newHarness(t)
	d := h.device()
	form := url.Values{}
	for k, v := range setupValues {
		form[k] = v
	}
	form.Del("use_as_display")
	expectRedirect(t, d.post("/setup", form), "/admin")
	expect(t, d.get("/admin"), http.StatusOK, "Hello Mum")
	// A personal device: its menu offers Sign out, not Lock.
	expect(t, d.get("/admin"), http.StatusOK, ">Sign out<")
}

func TestUnknownDeviceOnlySeesSignIn(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	stranger := h.device()
	for _, p := range []string{"/", "/clock", "/avatars/0123456789abcdef0123456789abcdef.jpg", "/admin", "/admin/people"} {
		expectRedirect(t, stranger.get(p), "/signin")
	}
	// Not a display, so there's no PIN pad either.
	expectRedirect(t, stranger.get("/admin/unlock"), "/signin")
	expect(t, stranger.post("/admin/unlock", url.Values{"person": {"1"}, "pin": {"2468"}}), http.StatusSeeOther)
	expectRedirect(t, stranger.get("/admin"), "/signin")
	r := stranger.get("/signin")
	expect(t, r, http.StatusOK, "Sign in", `name="password"`)
	// No household data on the sign-in page.
	if strings.Contains(r.body, "The Smiths") || strings.Contains(r.body, "Mum") {
		t.Error("sign-in page shows household data to an unknown device")
	}
}

func TestSignInFromPhone(t *testing.T) {
	h := newHarness(t)
	h.setUp()

	phone := h.device()
	expect(t, phone.post("/signin", url.Values{"email": {"mum@example.com"}, "password": {"wrong password!"}}),
		http.StatusUnauthorized, "don&#39;t match")
	expectRedirect(t, phone.post("/signin", url.Values{"email": {"MUM@example.com"}, "password": {"correct horse battery"}}), "/admin")
	expect(t, phone.get("/admin"), http.StatusOK, "Hello Mum", ">Sign out<")
	expect(t, phone.get("/"), http.StatusOK, "The Smiths")

	// Personal sessions last 30 days of inactivity, rolling.
	h.advance(29 * 24 * time.Hour)
	expect(t, phone.get("/admin"), http.StatusOK)
	h.advance(29 * 24 * time.Hour)
	expect(t, phone.get("/admin"), http.StatusOK)
	h.advance(31 * 24 * time.Hour)
	expectRedirect(t, phone.get("/admin"), "/signin")
}

func TestSignInLockout(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.device()
	bad := url.Values{"email": {"mum@example.com"}, "password": {"wrong password!"}}
	for range auth.MaxFailures - 1 {
		expect(t, phone.post("/signin", bad), http.StatusUnauthorized, "don&#39;t match")
	}
	expect(t, phone.post("/signin", bad), http.StatusUnauthorized, "Too many attempts")
	// Even the right password is refused while locked out…
	good := url.Values{"email": {"mum@example.com"}, "password": {"correct horse battery"}}
	expect(t, phone.post("/signin", good), http.StatusUnauthorized, "Too many attempts")
	// …until the lockout passes.
	h.advance(auth.LockoutPeriod)
	expectRedirect(t, phone.post("/signin", good), "/admin")
}

func TestSignOut(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()
	expectRedirect(t, phone.post("/signout", nil), "/signin")
	expectRedirect(t, phone.get("/admin"), "/signin")
}

func TestDisplayUnlockLockAndIdle(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()

	expect(t, wall.get("/admin"), http.StatusOK, ">Lock<", "var idleMs =  120  * 1000")
	expectRedirect(t, wall.post("/admin/lock", nil), "/")
	expectRedirect(t, wall.get("/admin"), "/admin/unlock")
	// The board is still visible, with a link to unlock.
	expect(t, wall.get("/"), http.StatusOK, `href="/admin/unlock"`)
	// Displays use PINs, not passwords.
	expectRedirect(t, wall.get("/signin"), "/admin/unlock")

	r := wall.get("/admin/unlock")
	expect(t, r, http.StatusOK, "Who's unlocking?", "Mum")
	mumID := regexp.MustCompile(`/admin/unlock\?person=(\d+)`).FindStringSubmatch(r.body)[1]
	expect(t, wall.get("/admin/unlock?person="+mumID), http.StatusOK, "Hi Mum", `name="pin"`, "pinpad__dots", `data-pinpad data-length="4"`, "/static/pinpad.js")

	expect(t, wall.post("/admin/unlock", url.Values{"person": {mumID}, "pin": {"1111"}}), http.StatusUnauthorized, "Wrong PIN")
	expectRedirect(t, wall.post("/admin/unlock", url.Values{"person": {mumID}, "pin": {"2468"}}), "/admin")
	expect(t, wall.get("/admin"), http.StatusOK)

	// Activity keeps it unlocked; two idle minutes locks it.
	h.advance(90 * time.Second)
	expect(t, wall.get("/admin/people"), http.StatusOK)
	h.advance(90 * time.Second)
	expect(t, wall.get("/admin"), http.StatusOK)
	h.advance(2 * time.Minute)
	expectRedirect(t, wall.get("/admin"), "/admin/unlock")
}

func TestPINLockout(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	expectRedirect(t, wall.post("/admin/lock", nil), "/")
	mum := h.person("Mum")
	id := itoa(mum.ID)
	for range auth.MaxFailures - 1 {
		expect(t, wall.post("/admin/unlock", url.Values{"person": {id}, "pin": {"0000"}}), http.StatusUnauthorized, "Wrong PIN")
	}
	expect(t, wall.post("/admin/unlock", url.Values{"person": {id}, "pin": {"0000"}}), http.StatusUnauthorized, "Too many wrong PINs")
	expect(t, wall.post("/admin/unlock", url.Values{"person": {id}, "pin": {"2468"}}), http.StatusUnauthorized, "Too many wrong PINs")
	expect(t, wall.get("/admin/unlock?person="+id), http.StatusOK, "Try again in a few minutes")
	h.advance(auth.LockoutPeriod)
	expectRedirect(t, wall.post("/admin/unlock", url.Values{"person": {id}, "pin": {"2468"}}), "/admin")
}

func TestDisplaySessionDoesNotWorkElsewhere(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	u, _ := url.Parse(h.ts.URL)
	var sess *http.Cookie
	for _, c := range wall.c.Jar.Cookies(u) {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("no session cookie on the display")
	}
	// Copying the display's admin session to another device doesn't work
	// without the display cookie too.
	thief := h.device()
	thief.c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: sess.Value}})
	expectRedirect(t, thief.get("/admin"), "/signin")
}

func TestCrossOriginPostRejected(t *testing.T) {
	h := newHarness(t)
	phone := h.device()
	expectRedirect(t, phone.post("/setup", setupValues), "/admin")

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.ts.URL+"/admin/rewards",
		strings.NewReader(url.Values{"title": {"Sweets"}, "cost_points": {"5"}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	expect(t, phone.do(req), http.StatusForbidden)
	n, err := h.st.Q.ListRewards(t.Context(), h.hh)
	if err != nil || len(n) != 0 {
		t.Fatalf("rewards = %v, %v; want none", n, err)
	}
}

func TestPeopleAdmin(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()

	// Validation.
	expect(t, phone.post("/admin/people", url.Values{"name": {""}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"none"}}),
		http.StatusUnprocessableEntity, "Enter a name")

	expectPersonCreated(t, phone.post("/admin/people", url.Values{
		"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"emoji"}, "emoji": {"🦖"}, "sort_order": {"1"},
	}))
	expect(t, phone.post("/admin/people", url.Values{
		"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"none"},
	}), http.StatusUnprocessableEntity, "already has that name")

	expect(t, phone.get("/admin/people"), http.StatusOK, "Alfie", "🦖", "Child")
	alfie := h.person("Alfie")
	// Kids have no PIN or sign-in section.
	r := phone.get("/admin/people/" + itoa(alfie.ID))
	expect(t, r, http.StatusOK, "Archive Alfie")
	if strings.Contains(r.body, "/pin\"") {
		t.Error("child page offers a PIN form")
	}
	// …and can't be given one.
	expect(t, phone.post("/admin/people/"+itoa(alfie.ID)+"/pin", url.Values{"pin": {"1234"}, "pin_confirm": {"1234"}}),
		http.StatusConflict, "Only adults")

	// Kids never appear on the unlock screen.
	wall := h.setUpDisplayLocked()
	r = wall.get("/admin/unlock")
	if strings.Contains(r.body, "Alfie") {
		t.Error("child listed on the unlock screen")
	}

	// Archive and restore.
	expectRedirect(t, phone.post("/admin/people/"+itoa(alfie.ID)+"/archive", nil), "/admin/people")
	expect(t, phone.get("/admin/people"), http.StatusOK, "Archived (1)")
	expectRedirect(t, phone.post("/admin/people/"+itoa(alfie.ID)+"/restore", nil), "/admin/people/"+itoa(alfie.ID))

	// The last adult who can sign in can't be archived.
	mum := h.person("Mum")
	expect(t, phone.post("/admin/people/"+itoa(mum.ID)+"/archive", nil), http.StatusConflict, "At least one adult")
}

// setUpDisplayLocked returns the wall display from a fresh login of an
// existing household: it registers a new display from a phone.
func (h *harness) setUpDisplayLocked() *device {
	h.t.Helper()
	d := h.phone()
	expectRedirect(h.t, d.post("/admin/devices/register", url.Values{"name": {"Hallway"}}), "/")
	return d
}

func TestAddAdultWithCredentials(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()
	expectPersonCreated(t, phone.post("/admin/people", url.Values{
		"name": {"Dad"}, "kind": {"adult"}, "colour": {"#3E63DD"}, "avatar_mode": {"none"},
	}))
	dad := itoa(h.person("Dad").ID)

	expect(t, phone.post("/admin/people/"+dad+"/pin", url.Values{"pin": {"1357"}, "pin_confirm": {"1358"}}),
		http.StatusUnprocessableEntity, "The PINs don&#39;t match.")
	expect(t, phone.post("/admin/people/"+dad+"/pin", url.Values{"pin": {"1357"}, "pin_confirm": {"1357"}}),
		http.StatusOK, "PIN saved.", "A PIN is set.")
	expect(t, phone.post("/admin/people/"+dad+"/login", url.Values{
		"email": {"mum@example.com"}, "password": {"dads password 1"}, "password_confirm": {"dads password 1"},
	}), http.StatusUnprocessableEntity, "already in use")
	expect(t, phone.post("/admin/people/"+dad+"/login", url.Values{
		"email": {"dad@example.com"}, "password": {"dads password 1"}, "password_confirm": {"dads password 1"},
	}), http.StatusOK, "Sign-in saved.")

	dadPhone := h.device()
	expectRedirect(t, dadPhone.post("/signin", url.Values{"email": {"dad@example.com"}, "password": {"dads password 1"}}), "/admin")
	expect(t, dadPhone.get("/admin"), http.StatusOK, "Hello Dad")

	// Changing Dad's password signs him out elsewhere.
	expect(t, phone.post("/admin/people/"+dad+"/login", url.Values{
		"email": {"dad@example.com"}, "password": {"new dad password"}, "password_confirm": {"new dad password"},
	}), http.StatusOK, "Sign-in saved.")
	expectRedirect(t, dadPhone.get("/admin"), "/signin")

	// Now Mum isn't the last admin, so her PIN can go.
	mum := itoa(h.person("Mum").ID)
	expect(t, phone.post("/admin/people/"+mum+"/pin/clear", nil), http.StatusOK, "PIN removed.")
	// Mum's own phone session survives changing her own credentials.
	expect(t, phone.get("/admin"), http.StatusOK)
}

func TestPhotoAvatar(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	mum := h.person("Mum")

	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for x := range 640 {
		for y := range 480 {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 200, A: 255})
		}
	}
	var png1 bytes.Buffer
	if err := png.Encode(&png1, img); err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{"name": "Mum", "kind": "adult", "colour": "#0090FF", "avatar_mode": "photo", "sort_order": "0"}
	expect(t, wall.postMultipart("/admin/people/"+itoa(mum.ID), fields, "photo", "x.txt", []byte("not an image")),
		http.StatusUnprocessableEntity, "isn&#39;t a JPEG, PNG or GIF")
	expect(t, wall.postMultipart("/admin/people/"+itoa(mum.ID), fields, "photo", "me.png", png1.Bytes()), http.StatusOK, "Saved.")

	mum = h.person("Mum")
	kind, name := avatar.Parse(mum.Avatar)
	if kind != avatar.Photo {
		t.Fatalf("avatar = %v, want photo", mum.Avatar)
	}
	r := wall.get("/avatars/" + name)
	expect(t, r, http.StatusOK)
	if ct := r.header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	cfg, _, err := image.DecodeConfig(strings.NewReader(r.body))
	if err != nil || cfg.Width != avatar.Size || cfg.Height != avatar.Size {
		t.Errorf("served avatar %dx%d (%v), want %d square", cfg.Width, cfg.Height, err, avatar.Size)
	}
	expect(t, wall.get("/avatars/..%2fdomus.db"), http.StatusNotFound)
	expect(t, wall.get("/avatars/nope.jpg"), http.StatusNotFound)

	// Saving again without a new file keeps the photo; switching to an emoji
	// removes the old file.
	expect(t, wall.postMultipart("/admin/people/"+itoa(mum.ID), fields, "", "", nil), http.StatusOK, "Saved.")
	expect(t, wall.get("/avatars/"+name), http.StatusOK)
	fields["avatar_mode"], fields["emoji"] = "emoji", "🐼"
	expect(t, wall.postMultipart("/admin/people/"+itoa(mum.ID), fields, "", "", nil), http.StatusOK, "Saved.")
	expect(t, wall.get("/avatars/"+name), http.StatusNotFound)
}

func TestChoresAdmin(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()
	expectPersonCreated(t, phone.post("/admin/people", url.Values{
		"name": {"Alfie"}, "kind": {"child"}, "colour": {"#46A758"}, "avatar_mode": {"none"},
	}))
	alfie := h.person("Alfie")

	expect(t, phone.get("/admin/chores/new"), http.StatusOK, "What needs doing?", "Alfie")

	// Weekly with no days, and a bad money value.
	expect(t, phone.post("/admin/chores", url.Values{
		"title": {"Bins"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"weekly"},
		"starts_on": {"2026-07-01"}, "money": {"1.234"},
	}), http.StatusUnprocessableEntity, "Enter an amount like 0.50.")
	expect(t, phone.post("/admin/chores", url.Values{
		"title": {"Bins"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"weekly"},
		"starts_on": {"2026-07-01"}, "money": {"0.50"},
	}), http.StatusUnprocessableEntity, "Choose at least one day.")

	// Mon + Thu, assigned to Alfie, 10 points + 50p.
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Bins"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"weekly"},
		"days": {"1", "4"}, "starts_on": {"2026-07-01"}, "points": {"10"}, "money": {"£0.50"}, "due_time": {"18:00"},
	}), "/admin/chores")
	// A pool chore every day.
	expectRedirect(t, phone.post("/admin/chores", url.Values{
		"title": {"Unload dishwasher"}, "mode": {"pool"}, "repeat": {"daily"}, "starts_on": {"2026-07-01"},
		"points": {"5"}, "requires_approval": {"1"},
	}), "/admin/chores")

	expect(t, phone.get("/admin/chores"), http.StatusOK,
		"Bins", "Weekly: Mon, Thu", "by 18:00", "10 pts · £0.50", "Unload dishwasher", "Anyone can claim", "Every day")

	// Occurrences for the next 14 days from Wed 1 July: Thu 2, Mon 6, Thu 9, Mon 13.
	bins := h.chore("Bins")
	if got := h.dueDates(bins.ID); strings.Join(got, ",") != "2026-07-02,2026-07-06,2026-07-09,2026-07-13" {
		t.Errorf("Bins due %v", got)
	}
	if got := h.dueDates(h.chore("Unload dishwasher").ID); len(got) != 14 {
		t.Errorf("dishwasher has %d occurrences, want 14", len(got))
	}

	// Edit: move Bins to Tuesdays; upcoming open days are replaced.
	r := phone.get("/admin/chores/" + itoa(bins.ID))
	expect(t, r, http.StatusOK, `value="0.50"`, `value="18:00"`)
	expect(t, phone.post("/admin/chores/"+itoa(bins.ID), url.Values{
		"title": {"Bins"}, "mode": {"assigned"}, "assignee_id": {itoa(alfie.ID)}, "repeat": {"weekly"},
		"days": {"2"}, "starts_on": {"2026-07-01"}, "points": {"10"}, "money": {"0.50"},
	}), http.StatusOK, "Saved.")
	if got := h.dueDates(bins.ID); strings.Join(got, ",") != "2026-07-07,2026-07-14" {
		t.Errorf("after edit Bins due %v", got)
	}

	// Alfie can't be archived while Bins is assigned to him.
	expect(t, phone.post("/admin/people/"+itoa(alfie.ID)+"/archive", nil), http.StatusConflict, "still have chores")
	expectRedirect(t, phone.post("/admin/chores/"+itoa(bins.ID)+"/archive", nil), "/admin/chores")
	if got := h.dueDates(bins.ID); len(got) != 0 {
		t.Errorf("archived chore still has open occurrences %v", got)
	}
	expectRedirect(t, phone.post("/admin/people/"+itoa(alfie.ID)+"/archive", nil), "/admin/people")

	expect(t, phone.get("/admin"), http.StatusOK, "added a chore", "archived a chore")
}

func TestRewardsAdmin(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()
	expect(t, phone.post("/admin/rewards", url.Values{"title": {"Sweets"}}), http.StatusUnprocessableEntity, "Set a cost")
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Film night pick"}, "cost_points": {"50"}}), "/admin/rewards")
	expectRedirect(t, phone.post("/admin/rewards", url.Values{"title": {"Comic"}, "money": {"2.50"}}), "/admin/rewards")
	expect(t, phone.get("/admin/rewards"), http.StatusOK, "Film night pick", "50 pts", "Comic", "£2.50")

	rewards, err := h.st.Q.ListRewards(t.Context(), h.hh)
	if err != nil || len(rewards) != 2 {
		t.Fatalf("rewards = %v, %v", rewards, err)
	}
	id := itoa(rewards[0].ID)
	expect(t, phone.post("/admin/rewards/"+id, url.Values{"title": {"Comic book"}, "money": {"3"}}), http.StatusOK, "Saved.", `value="3.00"`)
	expectRedirect(t, phone.post("/admin/rewards/"+id+"/archive", nil), "/admin/rewards")
	expect(t, phone.get("/admin/rewards"), http.StatusOK, "Archived (1)")
	expectRedirect(t, phone.post("/admin/rewards/"+id+"/restore", nil), "/admin/rewards/"+id)
}

func TestSettingsAdmin(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	phone := h.phone()
	expect(t, phone.get("/admin/settings"), http.StatusOK, `value="The Smiths"`, `<option value="Europe/London" selected>`, `value="00:00"`)
	expect(t, phone.post("/admin/settings", url.Values{
		"household_name": {"The Smiths"}, "timezone": {"Mars/Olympus"}, "day_starts_at": {"04:00"}, "lock_from": {"09:00"}, "lock_until": {"15:00"}, "admin_idle_minutes": {"2"},
	}), http.StatusUnprocessableEntity, "Choose a timezone.")
	expect(t, phone.post("/admin/settings", url.Values{
		"household_name": {"Smith HQ"}, "timezone": {"Europe/London"}, "day_starts_at": {"04:00"}, "lock_from": {"09:00"}, "lock_until": {"15:00"}, "admin_idle_minutes": {"5"},
	}), http.StatusOK, "Settings saved.")
	// It's 00:30 on 1 July; with a 04:00 reset the board is still on 30 June,
	// so a new chore defaults to starting then.
	expect(t, phone.get("/admin/chores/new"), http.StatusOK, `value="2026-06-30"`)
	expect(t, phone.get("/"), http.StatusOK, "Smith HQ")
}

func TestDevicesAdmin(t *testing.T) {
	h := newHarness(t)
	wall := h.setUp()
	phone := h.phone()

	r := phone.get("/admin/devices")
	expect(t, r, http.StatusOK, "Kitchen", "Make this a household display", "This device")

	// Register a second display from another browser, then revoke it from the phone.
	hall := h.setUpDisplayLocked()
	expectRedirect(t, hall.get("/admin"), "/admin/unlock")
	devices, err := h.st.Q.ListDevices(t.Context(), h.hh)
	if err != nil || len(devices) != 2 {
		t.Fatalf("devices = %v, %v", devices, err)
	}
	var hallID int64
	for _, d := range devices {
		if d.Name == "Hallway" {
			hallID = d.ID
		}
	}
	expectRedirect(t, phone.post("/admin/devices/"+itoa(hallID)+"/revoke", nil), "/admin/devices")
	expectRedirect(t, hall.get("/"), "/signin")
	// The kitchen display is unaffected.
	expect(t, wall.get("/"), http.StatusOK)

	// Sign out another phone session.
	other := h.phone()
	expect(t, phone.post("/admin/sessions/others", nil), http.StatusOK, "Signed out everywhere else.")
	expectRedirect(t, other.get("/admin"), "/signin")
	expect(t, phone.get("/admin"), http.StatusOK)
}

func TestHTMXRedirect(t *testing.T) {
	h := newHarness(t)
	h.setUp()
	d := h.device()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.ts.URL+"/clock", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("HX-Request", "true")
	r := d.do(req)
	if r.status != http.StatusNoContent || r.header.Get("HX-Redirect") != "/signin" {
		t.Fatalf("got %d HX-Redirect=%q", r.status, r.header.Get("HX-Redirect"))
	}
}

// expectPersonCreated checks a successful "add person" (303 to their page).
func expectPersonCreated(t *testing.T, r resp) {
	t.Helper()
	const prefix = "/admin/people/"
	if r.status != http.StatusSeeOther || !strings.HasPrefix(r.location, prefix) {
		t.Fatalf("got %d → %q, want 303 → %q…\n%s", r.status, r.location, prefix, r.body)
	}
}

func (h *harness) person(name string) dbq.Person {
	h.t.Helper()
	for _, list := range [][]dbq.Person{must(h.st.Q.ListPeople(h.t.Context(), h.hh)), must(h.st.Q.ListArchivedPeople(h.t.Context(), h.hh))} {
		for _, p := range list {
			if p.Name == name {
				return p
			}
		}
	}
	h.t.Fatalf("no person %q", name)
	return dbq.Person{}
}

func (h *harness) chore(title string) dbq.Chore {
	h.t.Helper()
	for _, c := range must(h.st.Q.ListChores(h.t.Context(), h.hh)) {
		if c.Title == title {
			return c
		}
	}
	h.t.Fatalf("no chore %q", title)
	return dbq.Chore{}
}

func (h *harness) dueDates(choreID int64) []string {
	h.t.Helper()
	rows, err := h.st.DB.QueryContext(h.t.Context(),
		"SELECT due_on FROM chore_occurrences WHERE chore_id = ? AND status = 'open' ORDER BY due_on", choreID)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
