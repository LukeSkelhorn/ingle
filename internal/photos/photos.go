// Package photos is the screensaver's slideshow library: photos adults
// upload in admin (stored next to the database) plus, optionally, a
// read-only folder such as a NAS share (INGLE_PHOTOS_DIR) that Ingle indexes
// but never writes to.
package photos

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"

	"github.com/lukeskelhorn/ingle/internal/picture"
	"github.com/lukeskelhorn/ingle/internal/store"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// Sizes of stored and served photos, in pixels.
const (
	// FullSize is the longest side of a slideshow photo: sharp on a tablet,
	// small enough to load quickly over Wi-Fi.
	FullSize = 2048
	// ThumbSize is the side of the square thumbnails in admin.
	ThumbSize = 400
)

// MaxUpload is the largest accepted upload, in bytes.
const MaxUpload = 20 << 20

// maxFolderFile is the largest folder photo Ingle will decode.
const maxFolderFile = 64 << 20

// MaxFolderPhotos caps how many folder photos are indexed.
const MaxFolderPhotos = 5000

// Errors.
var (
	ErrNotFound   = errors.New("photo not found")
	ErrTooLarge   = errors.New("that photo is too large (20 MB maximum)")
	ErrNotAnImage = errors.New("that file isn't a JPEG, PNG or GIF image")
)

var (
	uploadRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	folderRe = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// folderFile is one indexed photo in the folder.
type folderFile struct {
	rel   string
	mtime time.Time
	size  int64
}

// Library stores uploads and indexes the folder.
type Library struct {
	st     *store.Store
	blobs  Blobs  // uploaded photos: <name>.jpg and <name>_t.jpg
	cache  string // resized folder photos
	folder string // read-only folder, or "" for none
	log    *slog.Logger
	now    func() time.Time

	mu    sync.RWMutex
	index map[string]folderFile
	ids   []string        // sorted, for a stable order
	bad   map[string]bool // ids that failed to decode, logged once

	genMu sync.Mutex // serialises resizing folder photos
}

// New returns a Library storing uploads under dataDir/photos and resized
// folder photos under dataDir/photo-cache. folder may be "".
func New(st *store.Store, dataDir, folder string, log *slog.Logger) (*Library, error) {
	l := &Library{
		st:     st,
		cache:  filepath.Join(dataDir, "photo-cache"),
		folder: folder,
		log:    log,
		now:    time.Now,
		index:  map[string]folderFile{},
		bad:    map[string]bool{},
	}
	if err := os.MkdirAll(l.cache, 0o750); err != nil {
		return nil, fmt.Errorf("create %s: %w", l.cache, err)
	}
	blobs, err := NewDiskBlobs(filepath.Join(dataDir, "photos"))
	if err != nil {
		return nil, err
	}
	l.blobs = blobs
	return l, nil
}

// HasFolder reports whether a photos folder is configured.
func (l *Library) HasFolder() bool { return l.folder != "" }

// FolderCount is how many folder photos are indexed.
func (l *Library) FolderCount() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.ids)
}

// Upload decodes an uploaded image, stores a slideshow-sized copy and a
// thumbnail, and records it.
func (l *Library) Upload(ctx context.Context, householdID, actorID int64, r io.Reader) (dbq.Photo, error) {
	img, err := picture.Decode(r, MaxUpload)
	switch {
	case errors.Is(err, picture.ErrTooLarge):
		return dbq.Photo{}, ErrTooLarge
	case errors.Is(err, picture.ErrNotAnImage):
		return dbq.Photo{}, ErrNotAnImage
	case err != nil:
		return dbq.Photo{}, err
	}
	full := picture.Fit(img, FullSize)
	thumb := imaging.Fill(img, ThumbSize, ThumbSize, imaging.Center, imaging.Lanczos)

	fullJPEG, err := encodeJPEG(full, 85)
	if err != nil {
		return dbq.Photo{}, err
	}
	thumbJPEG, err := encodeJPEG(thumb, 80)
	if err != nil {
		return dbq.Photo{}, err
	}
	name := randomName()
	fullName, thumbName := blobNames(name)
	if err := l.blobs.Put(ctx, fullName, fullJPEG); err != nil {
		return dbq.Photo{}, err
	}
	if err := l.blobs.Put(ctx, thumbName, thumbJPEG); err != nil {
		l.removeBlobs(ctx, name)
		return dbq.Photo{}, err
	}

	var p dbq.Photo
	now := l.now()
	err = l.st.WithTx(ctx, func(q *dbq.Queries) error {
		var err error
		p, err = q.CreatePhoto(ctx, dbq.CreatePhotoParams{
			HouseholdID: householdID, File: name,
			Width: int64(full.Bounds().Dx()), Height: int64(full.Bounds().Dy()),
			CreatedBy: &actorID, CreatedAt: now.Unix(),
		})
		if err != nil {
			return fmt.Errorf("create photo: %w", err)
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &actorID, Verb: "photo.added",
			SubjectType: "photo", SubjectID: p.ID, At: now,
		})
	})
	if err != nil {
		l.removeBlobs(ctx, name)
		return dbq.Photo{}, err
	}
	return p, nil
}

// Delete removes an uploaded photo and its files.
func (l *Library) Delete(ctx context.Context, householdID, actorID, photoID int64) error {
	var file string
	err := l.st.WithTx(ctx, func(q *dbq.Queries) error {
		p, err := q.GetPhoto(ctx, dbq.GetPhotoParams{HouseholdID: householdID, ID: photoID})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get photo: %w", err)
		}
		file = p.File
		if _, err := q.DeletePhoto(ctx, dbq.DeletePhotoParams{HouseholdID: householdID, ID: photoID}); err != nil {
			return fmt.Errorf("delete photo: %w", err)
		}
		return store.LogActivity(ctx, q, store.Activity{
			HouseholdID: householdID, ActorID: &actorID, Verb: "photo.removed",
			SubjectType: "photo", SubjectID: photoID, At: l.now(),
		})
	})
	if err != nil {
		return err
	}
	l.removeBlobs(ctx, file)
	return nil
}

// List returns the household's uploaded photos, newest first.
func (l *Library) List(ctx context.Context, householdID int64) ([]dbq.Photo, error) {
	ps, err := l.st.Q.ListPhotos(ctx, householdID)
	if err != nil {
		return nil, fmt.Errorf("list photos: %w", err)
	}
	return ps, nil
}

// Slides returns the URL of every photo for the slideshow: uploads, then
// the folder. The page shuffles them.
func (l *Library) Slides(ctx context.Context, householdID int64) ([]string, error) {
	ps, err := l.List(ctx, householdID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ps)+l.FolderCount())
	for _, p := range ps {
		out = append(out, UploadURL(p.File, false))
	}
	l.mu.RLock()
	for _, id := range l.ids {
		if !l.bad[id] {
			out = append(out, "/photos/f/"+id)
		}
	}
	l.mu.RUnlock()
	return out, nil
}

// UploadURL is where an uploaded photo (or its thumbnail) is served.
func UploadURL(file string, thumb bool) string {
	if thumb {
		return "/photos/u/" + file + "?s=thumb"
	}
	return "/photos/u/" + file
}

// OpenUpload opens a household's uploaded photo for serving. Names that
// aren't the household's photos return ErrNotFound, so paths can't escape.
func (l *Library) OpenUpload(ctx context.Context, householdID int64, file string, thumb bool) (io.ReadSeekCloser, error) {
	if !uploadRe.MatchString(file) {
		return nil, ErrNotFound
	}
	if _, err := l.st.Q.GetPhotoByFile(ctx, dbq.GetPhotoByFileParams{HouseholdID: householdID, File: file}); err != nil {
		return nil, ErrNotFound
	}
	fullName, thumbName := blobNames(file)
	n := fullName
	if thumb {
		n = thumbName
	}
	f, err := l.blobs.Open(ctx, n)
	if err != nil {
		return nil, ErrNotFound
	}
	return f, nil
}

// OpenFolder opens a slideshow-sized copy of an indexed folder photo,
// resizing it on first use. Only ids from the index are accepted; client
// input never becomes a path.
func (l *Library) OpenFolder(ctx context.Context, id string) (*os.File, error) {
	if !folderRe.MatchString(id) {
		return nil, ErrNotFound
	}
	l.mu.RLock()
	ff, ok := l.index[id]
	bad := l.bad[id]
	l.mu.RUnlock()
	if !ok || bad {
		return nil, ErrNotFound
	}
	cached := filepath.Join(l.cache, fmt.Sprintf("%s-%d-%d.jpg", id, ff.mtime.UnixNano(), ff.size))
	if f, err := os.Open(cached); err == nil { //nolint:gosec // built from an indexed id
		return f, nil
	}

	l.genMu.Lock()
	defer l.genMu.Unlock()
	if f, err := os.Open(cached); err == nil { //nolint:gosec // another request made it meanwhile
		return f, nil
	}
	if err := l.resize(ff, cached); err != nil {
		l.mu.Lock()
		if !l.bad[id] {
			l.bad[id] = true
			l.log.WarnContext(ctx, "skipping unreadable photo", slog.String("file", ff.rel), slog.Any("err", err))
		}
		l.mu.Unlock()
		return nil, ErrNotFound
	}
	// Drop copies made from an older version of the file.
	if old, err := filepath.Glob(filepath.Join(l.cache, id+"-*.jpg")); err == nil {
		for _, o := range old {
			if o != cached {
				_ = os.Remove(o)
			}
		}
	}
	return os.Open(cached) //nolint:gosec // built from an indexed id
}

func (l *Library) resize(ff folderFile, dst string) error {
	src, err := os.Open(filepath.Join(l.folder, ff.rel))
	if err != nil {
		return err
	}
	defer src.Close()
	img, err := picture.Decode(src, maxFolderFile)
	if err != nil {
		return err
	}
	return picture.WriteJPEG(dst, picture.Fit(img, FullSize), 85)
}

// Rescan re-indexes the folder: regular .jpg/.jpeg/.png/.gif files,
// skipping hidden files and directories and symlinks, up to
// MaxFolderPhotos. Cached copies of photos that have gone are removed.
func (l *Library) Rescan(ctx context.Context) error {
	if l.folder == "" {
		return nil
	}
	index := map[string]folderFile{}
	truncated := false
	err := filepath.WalkDir(l.folder, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == l.folder {
				return err
			}
			return nil // unreadable subdirectory: skip it
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		name := d.Name()
		if path != l.folder && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() || !isPhotoExt(name) {
			return nil
		}
		if len(index) >= MaxFolderPhotos {
			truncated = true
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(l.folder, path)
		if err != nil {
			return nil
		}
		index[folderID(rel)] = folderFile{rel: rel, mtime: info.ModTime(), size: info.Size()}
		return nil
	})
	if err != nil {
		return fmt.Errorf("scan photos folder: %w", err)
	}
	if truncated {
		l.log.WarnContext(ctx, "photos folder has too many photos; showing the first ones", slog.Int("max", MaxFolderPhotos))
	}
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	l.mu.Lock()
	for id, ff := range l.index {
		// Forget a failure if the file changed, so a fixed photo comes back.
		if nf, ok := index[id]; !ok || !nf.mtime.Equal(ff.mtime) || nf.size != ff.size {
			delete(l.bad, id)
		}
	}
	l.index, l.ids = index, ids
	l.mu.Unlock()

	l.pruneCache(index)
	return nil
}

func (l *Library) pruneCache(index map[string]folderFile) {
	entries, err := os.ReadDir(l.cache)
	if err != nil {
		return
	}
	for _, e := range entries {
		id, _, ok := strings.Cut(e.Name(), "-")
		if !ok {
			continue
		}
		if _, keep := index[id]; !keep {
			_ = os.Remove(filepath.Join(l.cache, e.Name()))
		}
	}
}

// SetBlobs stores uploaded photos in b instead of on disk. Call it before
// serving; photos already uploaded stay where they were.
func (l *Library) SetBlobs(b Blobs) { l.blobs = b }

// blobNames are the stored files of the upload name.
func blobNames(name string) (full, thumb string) {
	return name + ".jpg", name + "_t.jpg"
}

// removeBlobs deletes both files of the upload name, logging failures: the
// photo's row is already gone, or was never written.
func (l *Library) removeBlobs(ctx context.Context, name string) {
	full, thumb := blobNames(name)
	for _, n := range []string{full, thumb} {
		if err := l.blobs.Delete(ctx, n); err != nil {
			l.log.WarnContext(ctx, "remove photo file", slog.Any("err", err))
		}
	}
}

func isPhotoExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	}
	return false
}

func folderID(rel string) string {
	sum := sha256.Sum256([]byte(filepath.ToSlash(rel)))
	return hex.EncodeToString(sum[:8])
}

func randomName() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
