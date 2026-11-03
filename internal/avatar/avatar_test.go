package avatar

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSavePhoto(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "avatars"))
	if err != nil {
		t.Fatal(err)
	}
	name, err := s.SavePhoto(bytes.NewReader(pngBytes(t, 800, 300)))
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != Size || b.Dy() != Size {
		t.Errorf("stored %v, want %dx%d", b, Size, Size)
	}

	if err := s.Remove(name); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(name); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open after Remove: %v", err)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Join(dir, "avatars"))
	if len(entries) != 0 {
		t.Errorf("leftover files %v", entries)
	}
}

func TestSavePhotoRejects(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SavePhoto(bytes.NewReader([]byte("hello"))); !errors.Is(err, ErrNotAnImage) {
		t.Errorf("text: %v", err)
	}
	if _, err := s.SavePhoto(bytes.NewReader(make([]byte, MaxUpload+1))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize: %v", err)
	}
	// A small file that claims to be enormous.
	if _, err := s.SavePhoto(bytes.NewReader(pngBytes(t, 10000, 10000))); !errors.Is(err, ErrTooLarge) {
		t.Errorf("bomb: %v", err)
	}
}

func TestOpenRejectsBadNames(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"../domus.db", "x.jpg", "", "0123456789abcdef0123456789abcdef.png", ".upload-1"} {
		if _, err := s.Open(n); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("Open(%q) = %v", n, err)
		}
	}
}

func TestParseAndValues(t *testing.T) {
	v, ok := EmojiValue("🦊")
	if !ok {
		t.Fatal("fox not in picker")
	}
	if k, e := Parse(&v); k != Emoji || e != "🦊" {
		t.Errorf("Parse(%q) = %v %q", v, k, e)
	}
	if _, ok := EmojiValue("<script>"); ok {
		t.Error("arbitrary emoji accepted")
	}
	p := PhotoValue("abc.jpg")
	if k, n := Parse(&p); k != Photo || n != "abc.jpg" {
		t.Errorf("Parse(%q) = %v %q", p, k, n)
	}
	if k, _ := Parse(nil); k != None {
		t.Error("nil not None")
	}
	legacy := "🐶"
	if k, _ := Parse(&legacy); k != None {
		t.Error("unprefixed value not None")
	}
}
