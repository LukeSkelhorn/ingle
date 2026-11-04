// Package picture decodes untrusted uploaded or on-disk images safely
// (format allow-list, decompression-bomb guard, EXIF auto-orientation) and
// writes JPEGs atomically. Avatars and the screensaver photos share it.
package picture

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // register decoders for DecodeConfig
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/disintegration/imaging"
)

// MaxPixels guards against decompression bombs (a tiny file that decodes to
// a huge image).
const MaxPixels = 50_000_000

// Errors returned by Decode.
var (
	ErrTooLarge   = errors.New("image is too large")
	ErrNotAnImage = errors.New("not a JPEG, PNG or GIF image")
)

// Decode reads at most maxBytes from r and decodes a JPEG, PNG or GIF,
// rotated upright according to its EXIF orientation.
func Decode(r io.Reader, maxBytes int64) (image.Image, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png" && format != "gif") {
		return nil, ErrNotAnImage
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxPixels {
		return nil, ErrTooLarge
	}
	img, err := imaging.Decode(bytes.NewReader(data), imaging.AutoOrientation(true))
	if err != nil {
		return nil, ErrNotAnImage
	}
	return img, nil
}

// Fit scales img down (never up) to fit within max×max.
func Fit(img image.Image, maxSide int) image.Image {
	b := img.Bounds()
	if b.Dx() <= maxSide && b.Dy() <= maxSide {
		return img
	}
	return imaging.Fit(img, maxSide, maxSide, imaging.Lanczos)
}

// WriteJPEG encodes img to path via a temp file in the same directory and a
// rename, so readers never see a half-written file.
func WriteJPEG(path string, img image.Image, quality int) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := jpeg.Encode(tmp, img, &jpeg.Options{Quality: quality}); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("encode jpeg: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("store image: %w", err)
	}
	return nil
}
