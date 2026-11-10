package photos

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// memBlobs is a Blobs kept in memory, standing in for object storage.
type memBlobs struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (m *memBlobs) Put(_ context.Context, name string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[name] = bytes.Clone(data)
	return nil
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

func (m *memBlobs) Open(_ context.Context, name string) (io.ReadSeekCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.files[name]
	if !ok {
		return nil, fmt.Errorf("%s: %w", name, fs.ErrNotExist)
	}
	return nopCloser{bytes.NewReader(b)}, nil
}

func (m *memBlobs) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.files, name)
	return nil
}

func TestUploadsGoToTheBlobStore(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	mem := &memBlobs{files: map[string][]byte{}}
	f.lib.SetBlobs(mem)

	p, err := f.lib.Upload(ctx, f.hh, f.mum.ID, bytes.NewReader(jpegBytes(t, 800, 600)))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if len(mem.files) != 2 || mem.files[p.File+".jpg"] == nil || mem.files[p.File+"_t.jpg"] == nil {
		t.Fatalf("blob store has %d files, want the photo and its thumbnail", len(mem.files))
	}
	if entries, _ := os.ReadDir(filepath.Join(f.data, "photos")); len(entries) != 0 {
		t.Errorf("%d files written to disk too", len(entries))
	}
	thumb, err := f.lib.OpenUpload(ctx, f.hh, p.File, true)
	if err != nil {
		t.Fatal(err)
	}
	if c := decodeFile(t, thumb); c.Width != ThumbSize {
		t.Errorf("thumb width = %d", c.Width)
	}

	if err := f.lib.Delete(ctx, f.hh, f.mum.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if len(mem.files) != 0 {
		t.Errorf("%d files left after delete", len(mem.files))
	}
}

func TestDiskBlobsRefuseOtherNames(t *testing.T) {
	d, err := NewDiskBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../ingle.db", "x.jpg", ""} {
		if err := d.Put(t.Context(), name, []byte("x")); err == nil {
			t.Errorf("Put(%q) succeeded", name)
		}
	}
}
