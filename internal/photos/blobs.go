package photos

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// Blobs stores the files of uploaded photos, each a JPEG under a name such
// as "3f9c…a1.jpg" or its thumbnail "3f9c…a1_t.jpg". The default keeps them
// on disk beside the database; another program can keep them elsewhere
// (e.g. object storage) with [Library.SetBlobs].
type Blobs interface {
	// Put stores data under name, replacing any file already there.
	Put(ctx context.Context, name string, data []byte) error
	// Open returns the file stored under name. A missing name is an
	// error matching fs.ErrNotExist.
	Open(ctx context.Context, name string) (io.ReadSeekCloser, error)
	// Delete removes the file under name. A missing name is not an error.
	Delete(ctx context.Context, name string) error
}

// blobNameRe is the names the library stores: an upload or its thumbnail.
var blobNameRe = regexp.MustCompile(`^[0-9a-f]{32}(_t)?\.jpg$`)

// DiskBlobs keeps photo files in a directory.
type DiskBlobs struct{ dir string }

// NewDiskBlobs keeps photo files in dir, creating it if needed.
func NewDiskBlobs(dir string) (*DiskBlobs, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	return &DiskBlobs{dir: dir}, nil
}

func (d *DiskBlobs) path(name string) (string, error) {
	if !blobNameRe.MatchString(name) {
		return "", fmt.Errorf("photo file %q: %w", name, fs.ErrNotExist)
	}
	return filepath.Join(d.dir, name), nil
}

// Put writes data to a temporary file and renames it into place, so a
// reader never sees half a photo.
func (d *DiskBlobs) Put(_ context.Context, name string, data []byte) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.dir, ".upload-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write photo: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("store photo: %w", err)
	}
	return nil
}

// Open opens the file under name.
func (d *DiskBlobs) Open(_ context.Context, name string) (io.ReadSeekCloser, error) {
	p, err := d.path(name)
	if err != nil {
		return nil, err
	}
	return os.Open(p) //nolint:gosec // name matched blobNameRe
}

// Delete removes the file under name.
func (d *DiskBlobs) Delete(_ context.Context, name string) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func encodeJPEG(img image.Image, quality int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}
