package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lukeskelhorn/ingle/internal/calendar"
	"github.com/lukeskelhorn/ingle/internal/photos"
	"github.com/lukeskelhorn/ingle/internal/store/dbq"
)

// maxScreensaverEvents is how many calendar events the screensaver lists.
const maxScreensaverEvents = 6

// screensaverView is the overlay's information panel.
type screensaverView struct {
	Clock    clockView
	Today    []eventView
	Tomorrow []eventView
	People   []progressView
	// HasCalendars hides the "nothing on" line when no feeds are set up.
	HasCalendars bool
}

type eventView struct {
	Title, When, Colour string
}

type progressView struct {
	Person      dbq.Person
	Done, Total int
	Overdue     int
}

func (s *Server) handleScreensaver(w http.ResponseWriter, r *http.Request) {
	v, err := s.screensaver(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPartial(w, r, "screensaver", v)
}

func (s *Server) screensaver(r *http.Request) (screensaverView, error) {
	ctx := r.Context()
	v := screensaverView{Clock: s.clock(ctx)}

	b, err := s.Chores.Board(ctx, hhOf(r), s.boardDay(r))
	if err != nil {
		return v, err
	}
	for _, c := range b.Columns {
		if c.TotalToday == 0 {
			continue
		}
		p := progressView{Person: c.Person, Done: c.DoneToday, Total: c.TotalToday}
		for _, card := range c.Cards {
			if card.Overdue && !card.Done {
				p.Overdue++
			}
		}
		v.People = append(v.People, p)
	}

	if s.Calendars == nil || !s.moduleOn(ctx, modCalendar) {
		return v, nil
	}
	feeds, err := s.Calendars.Feeds(ctx, hhOf(r))
	if err != nil {
		return v, err
	}
	v.HasCalendars = len(feeds) > 0
	loc := s.Locale
	loc.Location = s.Settings.Location(ctx, hhOf(r))
	now := s.now().In(loc.Location)
	y, m, d := now.Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, loc.Location)
	tomorrow := midnight.AddDate(0, 0, 1)
	after := midnight.AddDate(0, 0, 2)

	n := 0
	for _, e := range s.Calendars.Upcoming(hhOf(r), now, after) {
		if n == maxScreensaverEvents {
			break
		}
		ev := eventView{Title: e.Title, Colour: e.Colour}
		switch onToday := e.Start.Before(tomorrow); {
		case e.AllDay:
			ev.When = "All day"
			if onToday {
				v.Today = append(v.Today, ev)
			} else {
				v.Tomorrow = append(v.Tomorrow, ev)
			}
		case onToday:
			if e.Start.Before(now) { // already under way (Upcoming drops finished ones)
				ev.When = "until " + loc.FormatTime(e.End.In(loc.Location))
			} else {
				ev.When = loc.FormatTime(e.Start.In(loc.Location))
			}
			v.Today = append(v.Today, ev)
		default:
			ev.When = loc.FormatTime(e.Start.In(loc.Location))
			v.Tomorrow = append(v.Tomorrow, ev)
		}
		n++
	}
	return v, nil
}

// handleSlides lists the slideshow photos as JSON; the page shuffles them.
func (s *Server) handleSlides(w http.ResponseWriter, r *http.Request) {
	urls := []string{}
	if s.Photos != nil {
		var err error
		if urls, err = s.Photos.Slides(r.Context(), hhOf(r)); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string][]string{"photos": urls})
}

// handlePhoto serves an uploaded photo (/photos/u/{name}, ?s=thumb for the
// admin thumbnail) or a folder photo (/photos/f/{name}).
func (s *Server) handlePhoto(w http.ResponseWriter, r *http.Request) {
	if s.Photos == nil {
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	var (
		f   io.ReadSeekCloser
		err error
	)
	switch r.PathValue("kind") {
	case "u":
		f, err = s.Photos.OpenUpload(r.Context(), hhOf(r), name, r.URL.Query().Get("s") == "thumb")
	case "f":
		f, err = s.Photos.OpenFolder(r.Context(), name)
	default:
		err = photos.ErrNotFound
	}
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	// A blob store other than disk may not know when a file was written;
	// uploads don't need it, as their names are never reused.
	var modTime time.Time
	if st, ok := f.(interface{ Stat() (os.FileInfo, error) }); ok {
		if info, err := st.Stat(); err == nil {
			modTime = info.ModTime()
			if r.PathValue("kind") != "u" {
				// Folder photos can change on disk; revalidate by ETag.
				w.Header().Set("ETag", fmt.Sprintf(`"%x-%x"`, info.ModTime().UnixNano(), info.Size()))
			}
		}
	}
	if r.PathValue("kind") == "u" {
		// Upload names are random and never reused.
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "private, no-cache")
	}
	w.Header().Set("Content-Type", "image/jpeg")
	http.ServeContent(w, r, "", modTime, f)
}

// Admin › Photos.

// maxPhotosPerUpload caps one multi-file upload.
const maxPhotosPerUpload = 20

type photosPage struct {
	Photos      []photoView
	HasFolder   bool
	FolderCount int
	Problems    []string
}

type photoView struct {
	ID       int64
	Thumb    string
	Full     string
	Uploaded int64
}

func (s *Server) handlePhotos(w http.ResponseWriter, r *http.Request) {
	s.renderPhotos(w, r, http.StatusOK, "", nil)
}

func (s *Server) renderPhotos(w http.ResponseWriter, r *http.Request, status int, flash string, problems []string) {
	ps, err := s.Photos.List(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p := photosPage{HasFolder: s.Photos.HasFolder(), FolderCount: s.Photos.FolderCount(), Problems: problems}
	for _, ph := range ps {
		p.Photos = append(p.Photos, photoView{
			ID: ph.ID, Thumb: photos.UploadURL(ph.File, true), Full: photos.UploadURL(ph.File, false), Uploaded: ph.CreatedAt,
		})
	}
	v := s.view(r, "Photos", p)
	v.Nav = "photos"
	v.Flash = flash
	s.render(w, r, status, "admin_photos", v)
}

// handlePhotoUpload streams a multi-file upload part by part, so large
// batches never sit in memory or temp files all at once.
func (s *Server) handlePhotoUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotosPerUpload*(photos.MaxUpload+maxFormBytes))
	mr, err := r.MultipartReader()
	if err != nil {
		s.renderPhotos(w, r, http.StatusUnprocessableEntity, "", []string{"Choose one or more photos to upload."})
		return
	}
	added := 0
	var problems []string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			problems = append(problems, "The upload was interrupted or too large. Try fewer photos at a time.")
			break
		}
		if part.FormName() != "photos" || part.FileName() == "" {
			_ = part.Close()
			continue
		}
		name := part.FileName()
		if added+len(problems) >= maxPhotosPerUpload {
			problems = append(problems, fmt.Sprintf("Only %d photos can be uploaded at a time; the rest were skipped.", maxPhotosPerUpload))
			_ = part.Close()
			break
		}
		_, uerr := s.Photos.Upload(r.Context(), hhOf(r), actor(r).Person.ID, part)
		_ = part.Close()
		switch {
		case errors.Is(uerr, photos.ErrTooLarge), errors.Is(uerr, photos.ErrNotAnImage):
			problems = append(problems, name+": "+uerr.Error()+".")
		case uerr != nil:
			s.serverError(w, r, uerr)
			return
		default:
			added++
		}
	}
	flash := ""
	switch added {
	case 0:
	case 1:
		flash = "Added 1 photo."
	default:
		flash = fmt.Sprintf("Added %d photos.", added)
	}
	status := http.StatusOK
	if added == 0 {
		status = http.StatusUnprocessableEntity
		if len(problems) == 0 {
			problems = []string{"Choose one or more photos to upload."}
		}
	}
	s.renderPhotos(w, r, status, flash, problems)
}

func (s *Server) handlePhotoDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := s.Photos.Delete(r.Context(), hhOf(r), actor(r).Person.ID, id)
	if errors.Is(err, photos.ErrNotFound) {
		s.renderPhotos(w, r, http.StatusNotFound, "That photo has already gone.", nil)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderPhotos(w, r, http.StatusOK, "Photo removed.", nil)
}

// Admin › Calendars.

type calendarsPage struct {
	Feeds []feedView
	Name  string
	URL   string
}

type feedView struct {
	calendar.Feed
	Updated string // "Updated 3 min ago", or "" before the first fetch
}

func (s *Server) handleCalendars(w http.ResponseWriter, r *http.Request) {
	s.renderCalendars(w, r, http.StatusOK, calendarsPage{}, nil, "")
}

func (s *Server) renderCalendars(w http.ResponseWriter, r *http.Request, status int, p calendarsPage, errs map[string]string, flash string) {
	feeds, err := s.Calendars.Feeds(r.Context(), hhOf(r))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	now := s.now()
	p.Feeds = p.Feeds[:0]
	for _, f := range feeds {
		fv := feedView{Feed: f}
		if !f.FetchedAt.IsZero() {
			fv.Updated = "Updated " + ago(now.Sub(f.FetchedAt))
		}
		p.Feeds = append(p.Feeds, fv)
	}
	v := s.view(r, "Calendars", p)
	v.Nav = "calendars"
	v.Errors = errs
	v.Flash = flash
	s.render(w, r, status, "admin_calendars", v)
}

func (s *Server) handleCalendarAdd(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(w, r); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f := newForm(r)
	p := calendarsPage{Name: f.str("name"), URL: f.str("url")}
	_, err := s.Calendars.AddFeed(r.Context(), hhOf(r), actor(r).Person.ID, p.Name, p.URL)
	if errs, ok := validationErrors(err); ok {
		s.renderCalendars(w, r, http.StatusUnprocessableEntity, p, errs, "")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderCalendars(w, r, http.StatusOK, calendarsPage{}, nil, p.Name+" added.")
}

func (s *Server) handleCalendarDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	err := s.Calendars.RemoveFeed(r.Context(), hhOf(r), actor(r).Person.ID, id)
	if errors.Is(err, calendar.ErrNotFound) {
		s.renderCalendars(w, r, http.StatusNotFound, calendarsPage{}, nil, "That calendar has already gone.")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderCalendars(w, r, http.StatusOK, calendarsPage{}, nil, "Calendar removed.")
}

func (s *Server) handleCalendarRefresh(w http.ResponseWriter, r *http.Request) {
	if err := s.Calendars.RefreshHousehold(r.Context(), hhOf(r)); err != nil {
		s.Log.ErrorContext(r.Context(), "refresh calendars", slog.Any("err", err))
		s.serverError(w, r, err)
		return
	}
	s.renderCalendars(w, r, http.StatusOK, calendarsPage{}, nil, "Calendars refreshed.")
}

// ago is a short, friendly duration for status lines.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		h := int(d.Hours())
		return fmt.Sprintf("%d hour%s ago", h, strings.Repeat("s", min(h-1, 1)))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
