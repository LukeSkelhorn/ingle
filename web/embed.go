// Package web holds the embedded HTML templates and static assets
// (htmx, PWA manifest, service worker, icons).
package web

import "embed"

// Templates contains the html/template files.
//
//go:embed templates
var Templates embed.FS

// Static contains assets served under /static/ (plus manifest and sw.js at root).
//
//go:embed static
var Static embed.FS
