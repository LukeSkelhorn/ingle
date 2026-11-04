// Package avatar handles how people are pictured on the board: an emoji
// from a curated set, or an uploaded photo that is auto-oriented, centre
// cropped and stored as a small JPEG next to the database.
package avatar

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/disintegration/imaging"

	"github.com/lukeskelhorn/domus/internal/picture"
)

// Size is the width and height of stored photos, in pixels.
const Size = 256

// MaxUpload is the largest accepted upload, in bytes.
const MaxUpload = 8 << 20

// Value prefixes stored in people.avatar.
const (
	emojiPrefix = "emoji:"
	photoPrefix = "photo:"
)

// Errors returned by SavePhoto.
var (
	ErrTooLarge   = errors.New("photo is too large (8 MB maximum)")
	ErrNotAnImage = errors.New("that file isn't a JPEG, PNG or GIF image")
)

// Emojis is the curated picker. Big, friendly and easy to tell apart from
// across a room.
var Emojis = []string{
	"🐶", "🐱", "🐭", "🐹", "🐰", "🦊", "🐻", "🐼", "🐨", "🐯",
	"🦁", "🐮", "🐷", "🐸", "🐵", "🐔", "🐧", "🐦", "🦄", "🐝",
	"🦋", "🐢", "🐙", "🦖", "🐳", "🐬", "🦈", "🌸", "🌻", "🌈",
	"⭐", "🌙", "☀️", "⚽", "🏀", "🎸", "🎨", "🚀", "🚂", "🍕",
	"🍓", "🍩", "👑", "🤖", "👻", "🧙", "🦸", "🧜",
}

var nameRe = regexp.MustCompile(`^[0-9a-f]{32}\.jpg$`)

// Kind is how an avatar value is shown.
type Kind int

// Avatar kinds.
const (
	None Kind = iota
	Emoji
	Photo
)

// Parse splits a stored people.avatar value.
func Parse(v *string) (Kind, string) {
	if v == nil {
		return None, ""
	}
	switch {
	case strings.HasPrefix(*v, emojiPrefix):
		return Emoji, strings.TrimPrefix(*v, emojiPrefix)
	case strings.HasPrefix(*v, photoPrefix):
		return Photo, strings.TrimPrefix(*v, photoPrefix)
	}
	return None, ""
}

// EmojiValue returns the stored value for emoji e, or false if e isn't in
// the picker.
func EmojiValue(e string) (string, bool) {
	if !slices.Contains(Emojis, e) {
		return "", false
	}
	return emojiPrefix + e, true
}

// PhotoValue returns the stored value for a saved photo name.
func PhotoValue(name string) string { return photoPrefix + name }

// Store saves and serves photos from a directory.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir, creating it if needed.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create avatar dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

// SavePhoto reads an uploaded image, auto-orients it from EXIF, centre crops
// it to Size×Size and stores it as JPEG. It returns the stored file name.
func (s *Store) SavePhoto(r io.Reader) (string, error) {
	img, err := picture.Decode(r, MaxUpload)
	switch {
	case errors.Is(err, picture.ErrTooLarge):
		return "", ErrTooLarge
	case errors.Is(err, picture.ErrNotAnImage):
		return "", ErrNotAnImage
	case err != nil:
		return "", err
	}
	thumb := imaging.Fill(img, Size, Size, imaging.Center, imaging.Lanczos)
	name := randomName()
	if err := picture.WriteJPEG(filepath.Join(s.dir, name), thumb, 85); err != nil {
		return "", err
	}
	return name, nil
}

// Remove deletes a stored photo. Unknown or invalid names are ignored.
func (s *Store) Remove(name string) error {
	if !nameRe.MatchString(name) {
		return nil
	}
	err := os.Remove(filepath.Join(s.dir, name))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Open opens a stored photo for serving. Names that aren't ones SavePhoto
// produced return os.ErrNotExist, so paths can't escape the directory.
func (s *Store) Open(name string) (*os.File, error) {
	if !nameRe.MatchString(name) {
		return nil, os.ErrNotExist
	}
	return os.Open(filepath.Join(s.dir, name)) //nolint:gosec // name matched nameRe above, so it can't leave s.dir
}

func randomName() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) + ".jpg"
}
