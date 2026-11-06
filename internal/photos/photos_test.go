package photos

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

type fixture struct {
	t     *testing.T
	st    *store.Store
	lib   *Library
	hh    int64
	mum   dbq.Person
	data  string
	photo string // the read-only folder
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := t.Context()
	data := t.TempDir()
	db, err := store.Open(ctx, filepath.Join(data, "ingle.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.New(db)
	h, err := st.EnsureHousehold(ctx, "Home", time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	mum, err := st.Q.CreatePerson(ctx, dbq.CreatePersonParams{HouseholdID: h.ID, Name: "Mum", Kind: "adult", Colour: "#4A90D9", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	lib, err := New(st, data, folder, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, st: st, lib: lib, hh: h.ID, mum: mum, data: data, photo: folder}
}

func jpegBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		for y := range h {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withOrientation inserts an EXIF APP1 segment saying "rotate 90° clockwise
// to display" (orientation 6), as phone cameras write.
func withOrientation(t *testing.T, j []byte) []byte {
	t.Helper()
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a")
	_ = binary.Write(&tiff, binary.BigEndian, uint32(8))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(1))      // one entry
	_ = binary.Write(&tiff, binary.BigEndian, uint16(0x0112)) // Orientation
	_ = binary.Write(&tiff, binary.BigEndian, uint16(3))      // SHORT
	_ = binary.Write(&tiff, binary.BigEndian, uint32(1))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(6))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(0))
	_ = binary.Write(&tiff, binary.BigEndian, uint32(0)) // no next IFD
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	_ = binary.Write(&out, binary.BigEndian, uint16(len(payload)+2))
	out.Write(payload)
	out.Write(j[2:]) // the rest of the original JPEG, after its SOI
	return out.Bytes()
}

func decodeFile(t *testing.T, f *os.File) image.Config {
	t.Helper()
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestUploadAndDelete(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	p, err := f.lib.Upload(ctx, f.hh, f.mum.ID, bytes.NewReader(withOrientation(t, jpegBytes(t, 3000, 1000))))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	// Rotated upright (portrait now), then scaled to fit FullSize.
	if p.Width != 682 || p.Height != FullSize {
		t.Errorf("size = %dx%d, want 682x%d", p.Width, p.Height, FullSize)
	}
	full, err := f.lib.OpenUpload(ctx, f.hh, p.File, false)
	if err != nil {
		t.Fatal(err)
	}
	if c := decodeFile(t, full); c.Width != 682 || c.Height != FullSize {
		t.Errorf("stored = %dx%d", c.Width, c.Height)
	}
	thumb, err := f.lib.OpenUpload(ctx, f.hh, p.File, true)
	if err != nil {
		t.Fatal(err)
	}
	if c := decodeFile(t, thumb); c.Width != ThumbSize || c.Height != ThumbSize {
		t.Errorf("thumb = %dx%d", c.Width, c.Height)
	}

	slides, err := f.lib.Slides(ctx, f.hh)
	if err != nil || len(slides) != 1 || slides[0] != "/photos/u/"+p.File {
		t.Errorf("Slides = %v, %v", slides, err)
	}
	if _, err := f.lib.OpenUpload(ctx, f.hh+1, p.File, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("other household = %v, want ErrNotFound", err)
	}
	if _, err := f.lib.OpenUpload(ctx, f.hh, "../ingle.db", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("traversal = %v, want ErrNotFound", err)
	}

	if err := f.lib.Delete(ctx, f.hh, f.mum.ID, p.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(f.data, "photos", "*")); len(left) != 0 {
		t.Errorf("files left after delete: %v", left)
	}
	if err := f.lib.Delete(ctx, f.hh, f.mum.ID, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
	var n int
	if err := f.st.DB.QueryRowContext(ctx, `SELECT count(*) FROM activity WHERE verb IN ('photo.added', 'photo.removed')`).Scan(&n); err != nil || n != 2 {
		t.Errorf("activity rows = %d, %v; want 2", n, err)
	}
}

func TestUploadRejects(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	if _, err := f.lib.Upload(ctx, f.hh, f.mum.ID, strings.NewReader("not a photo")); !errors.Is(err, ErrNotAnImage) {
		t.Errorf("text = %v, want ErrNotAnImage", err)
	}
	big := io.MultiReader(bytes.NewReader(jpegBytes(t, 10, 10)), bytes.NewReader(make([]byte, MaxUpload)))
	if _, err := f.lib.Upload(ctx, f.hh, f.mum.ID, big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("huge = %v, want ErrTooLarge", err)
	}
	if ps, _ := f.lib.List(ctx, f.hh); len(ps) != 0 {
		t.Errorf("%d photos stored after rejects", len(ps))
	}
}

func write(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFolder(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 50, 40))); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.photo, "Holiday", "beach.JPG"), jpegBytes(t, 4000, 3000))
	write(t, filepath.Join(f.photo, "garden.png"), pngBuf.Bytes())
	write(t, filepath.Join(f.photo, "broken.jpg"), []byte("nope"))
	write(t, filepath.Join(f.photo, "notes.txt"), []byte("hi"))
	write(t, filepath.Join(f.photo, ".hidden.jpg"), jpegBytes(t, 10, 10))
	write(t, filepath.Join(f.photo, ".thumbs", "x.jpg"), jpegBytes(t, 10, 10))
	if err := os.Symlink(filepath.Join(f.data, "ingle.db"), filepath.Join(f.photo, "escape.jpg")); err != nil {
		t.Fatal(err)
	}

	if err := f.lib.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.lib.FolderCount(); n != 3 { // beach, garden, broken
		t.Fatalf("FolderCount = %d, want 3", n)
	}
	beach := folderID(filepath.Join("Holiday", "beach.JPG"))
	if beach != folderID(filepath.Join("Holiday", "beach.JPG")) || len(beach) != 16 {
		t.Fatalf("unstable id %q", beach)
	}

	fh, err := f.lib.OpenFolder(ctx, beach)
	if err != nil {
		t.Fatalf("OpenFolder: %v", err)
	}
	if c := decodeFile(t, fh); c.Width != FullSize || c.Height != 1536 {
		t.Errorf("resized = %dx%d, want %dx1536", c.Width, c.Height, FullSize)
	}
	cached, _ := filepath.Glob(filepath.Join(f.data, "photo-cache", beach+"-*.jpg"))
	if len(cached) != 1 {
		t.Fatalf("cache = %v", cached)
	}
	// Served from the cache the second time.
	before, _ := os.Stat(cached[0])
	fh, err = f.lib.OpenFolder(ctx, beach)
	if err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()
	if after, _ := os.Stat(cached[0]); !after.ModTime().Equal(before.ModTime()) {
		t.Error("cached copy was regenerated")
	}

	// The broken file drops out of the slideshow once it fails.
	if _, err := f.lib.OpenFolder(ctx, folderID("broken.jpg")); !errors.Is(err, ErrNotFound) {
		t.Errorf("broken = %v, want ErrNotFound", err)
	}
	slides, _ := f.lib.Slides(ctx, f.hh)
	if len(slides) != 2 {
		t.Errorf("Slides = %v, want beach and garden", slides)
	}
	for _, bad := range []string{"0000000000000000", "../../ingle.db", folderID("notes.txt")} {
		if _, err := f.lib.OpenFolder(ctx, bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("OpenFolder(%q) = %v, want ErrNotFound", bad, err)
		}
	}

	// Changing the file makes a fresh copy and drops the stale one.
	write(t, filepath.Join(f.photo, "Holiday", "beach.JPG"), jpegBytes(t, 300, 200))
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(f.photo, "Holiday", "beach.JPG"), later, later); err != nil {
		t.Fatal(err)
	}
	if err := f.lib.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	fh, err = f.lib.OpenFolder(ctx, beach)
	if err != nil {
		t.Fatal(err)
	}
	if c := decodeFile(t, fh); c.Width != 300 {
		t.Errorf("after change width = %d, want 300 (never upscaled)", c.Width)
	}
	if cached, _ := filepath.Glob(filepath.Join(f.data, "photo-cache", beach+"-*.jpg")); len(cached) != 1 {
		t.Errorf("cache after change = %v", cached)
	}

	// Removing the file prunes its cached copy.
	if err := os.RemoveAll(filepath.Join(f.photo, "Holiday")); err != nil {
		t.Fatal(err)
	}
	if err := f.lib.Rescan(ctx); err != nil {
		t.Fatal(err)
	}
	if cached, _ := filepath.Glob(filepath.Join(f.data, "photo-cache", beach+"-*.jpg")); len(cached) != 0 {
		t.Errorf("cache after removal = %v", cached)
	}
}
