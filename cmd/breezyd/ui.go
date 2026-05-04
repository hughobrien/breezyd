// SPDX-License-Identifier: GPL-3.0-or-later

// HTTP handler for the embedded single-page dashboard. The UI is a single
// self-contained HTML file (CSS + JS inlined) that lives at
// cmd/breezyd/ui/index.html and ships baked into the binary via go:embed.
//
// The handler is intentionally minimal: one route (GET /), one byte slice,
// no caching, no asset tree. If the UI grows past one file, this is the
// place to switch to embed.FS + http.FileServerFS.
package main

import (
	_ "embed"
	"net/http"
)

//go:embed ui/index.html
var indexHTML []byte

// getIndex serves the embedded dashboard HTML. We send Cache-Control:
// no-store so an upgraded daemon's UI is picked up on the next page load
// rather than after a hard refresh.
func (h *Handler) getIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}
