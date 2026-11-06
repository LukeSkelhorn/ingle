package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/lukeskelhorn/ingle/internal/locale"
	"github.com/lukeskelhorn/ingle/internal/valid"
)

// maxFormBytes caps url-encoded form bodies; multipart (photo) uploads get
// avatar.MaxUpload on top.
const maxFormBytes = 64 << 10

func parseForm(w http.ResponseWriter, r *http.Request) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	return r.ParseForm()
}

// form wraps submitted values with helpers that collect field errors.
type form struct {
	r    *http.Request
	errs valid.Errors
}

func newForm(r *http.Request) *form { return &form{r: r, errs: valid.Errors{}} }

func (f *form) str(name string) string { return strings.TrimSpace(f.r.FormValue(name)) }

// raw returns the value untrimmed (passwords).
func (f *form) raw(name string) string { return f.r.FormValue(name) }

func (f *form) optStr(name string) *string {
	if v := f.str(name); v != "" {
		return &v
	}
	return nil
}

func (f *form) checked(name string) bool { return f.r.FormValue(name) != "" }

func (f *form) int(name, msg string) int64 {
	v := f.str(name)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		f.errs.Add(name, msg)
	}
	return n
}

func (f *form) optID(name string) *int64 {
	n, err := strconv.ParseInt(f.str(name), 10, 64)
	if err != nil || n <= 0 {
		return nil
	}
	return &n
}

// ok reports whether the form parsed without field errors. Handlers check it
// before calling the domain, so a value that failed to parse (and read as
// zero) is never saved.
func (f *form) ok() bool { return len(f.errs) == 0 }

// validationErrors returns err's field errors if it is a validation error.
func validationErrors(err error) (valid.Errors, bool) {
	var ve valid.Errors
	if errors.As(err, &ve) {
		return ve, true
	}
	return nil, false
}

func pathID(r *http.Request) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return n, err == nil && n > 0
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func formatMoneyInput(pence int64) string { return locale.FormatMoneyInput(pence) }
