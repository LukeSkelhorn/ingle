// Package web holds the embedded HTML templates and static assets
// (htmx, PWA manifest, service worker, icons).
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sync"
)

// Templates contains the html/template files.
//
//go:embed templates
var Templates embed.FS

// Static contains assets served under /static/ (plus manifest and sw.js at root).
//
//go:embed static
var Static embed.FS

// StaticVersion is a short hash of every static asset. Asset URLs carry it
// (/static/app.css?v=…) and the service worker names its cache after it, so
// a new build is never served old CSS or JS from a cache.
var StaticVersion = sync.OnceValue(func() string {
	h := sha256.New()
	_ = fs.WalkDir(Static, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(Static, path)
		if err != nil {
			return err
		}
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write(b)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
})
