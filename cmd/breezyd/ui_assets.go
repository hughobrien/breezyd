// SPDX-License-Identifier: GPL-3.0-or-later

// HTTP handlers for the dashboard's static assets: the page shell, the
// extracted stylesheet, and the vendored htmx libraries. Templates that
// render device data live in handlers_ui_read.go and handlers_ui_write.go.
package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
)

// htmxVersion is the vendored htmx version embedded under ui/vendor/.
// The Layout template references /ui/vendor/htmx-<version>.min.js.
const htmxVersion = "2.0.4"

//go:embed ui/index.html
var indexHTML []byte

//go:embed ui/legacy.js
var legacyJS []byte

//go:embed ui/vendor
var vendorFS embed.FS

// vendorRoot is vendorFS rooted at "ui/vendor" so URL paths can map directly.
var vendorRoot fs.FS

// styleHash is the short SHA-256 prefix of the embedded style.css. Computed
// at startup; baked into the page shell so the URL is stable per binary.
var styleHash string

//go:embed ui/style.css
var styleCSS []byte

func init() {
	root, err := fs.Sub(vendorFS, "ui/vendor")
	if err != nil {
		panic(err)
	}
	vendorRoot = root

	// MUST match tests/ui/screenshot.ts and tests/ui/dashboard.spec.ts:
	// sha256(style.css) → hex → first 10 chars. Drift = 404 on the stylesheet.
	sum := sha256.Sum256(styleCSS)
	styleHash = hex.EncodeToString(sum[:])[:10]
}

// getIndex serves the templ-rendered page shell (Layout). The Layout template
// includes the style hash, htmx version, theme-picker JS, and legacy.js.
func (h *Handler) getIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	d := templates.LayoutData{StyleHash: styleHash, HTMXVersion: htmxVersion}
	if err := templates.Layout(d).Render(r.Context(), w); err != nil {
		slog.Error("render Layout", "err", err)
	}
}

// getLegacyJS serves the extracted write-side JS from index.html. This script
// handles all POST interactions (power, mode, speed, heater, timer, threshold,
// schedule). No-store: it is regenerated per binary and must not be cached.
func (h *Handler) getLegacyJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(legacyJS)
}

// getStyle serves the extracted stylesheet at /ui/style-<hash>.css.
// Hash is short SHA-256 prefix; stable per binary, so immutable caching is safe.
func (h *Handler) getStyle(w http.ResponseWriter, r *http.Request) {
	want := "/ui/style-" + styleHash + ".css"
	if r.URL.Path != want {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(styleCSS)
}

// getVendor serves files from cmd/breezyd/ui/vendor under /ui/vendor/<filename>.
// Filenames carry version suffixes for cache-busting.
func (h *Handler) getVendor(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/ui/vendor/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(vendorRoot, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}
