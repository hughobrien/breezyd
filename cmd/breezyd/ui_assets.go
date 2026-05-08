// SPDX-License-Identifier: GPL-3.0-or-later

// HTTP handlers for the dashboard's static assets: the page shell, the
// extracted stylesheet, and the vendored datastar JS bundle. Templates
// that render device data live in handlers_ui_read.go and
// handlers_ui_write.go.
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

// datastarVersion is the vendored datastar JS bundle version. The file
// is the unmodified `bundles/datastar.js` from the datastar v1.0.1
// release (already minified despite the lack of `.min` suffix upstream;
// we keep the `.min.js` filename so versioned URLs stay cache-busting).
//
// SHA-256 of cmd/breezyd/ui/vendor/datastar-1.0.1.min.js:
//
//	54768cf34985be0229c7229f1df9469fbd32e2a0c09b4a3f1e81ad8c4d6840da
//
// Bumping this version is a deliberate act: download the new bundle,
// recompute the digest, and update both the constant and the comment.
const datastarVersion = "1.0.1"

//go:embed ui/vendor
var vendorFS embed.FS

// vendorRoot is vendorFS rooted at "ui/vendor" so URL paths can map directly.
var vendorRoot fs.FS

// styleHash is the short SHA-256 prefix of the embedded style.css. Computed
// at startup; baked into the page shell so the URL is stable per binary.
var styleHash string

//go:embed ui/style.css
var styleCSS []byte

//go:embed ui/favicon.svg
var faviconSVG []byte

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

// getIndex serves the templ-rendered page shell (Layout). The Layout
// template includes the style hash, datastar version, and theme-picker
// JS.
func (h *Handler) getIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	d := templates.LayoutData{StyleHash: styleHash, DatastarVersion: datastarVersion}
	if err := templates.Layout(d).Render(r.Context(), w); err != nil {
		slog.Error("render Layout", "err", err)
	}
}

// getFavicon serves the SVG favicon. Browsers fetch /favicon.ico
// aggressively even with <link rel="icon"> set; serving the same SVG
// at both paths silences the 404.
func (h *Handler) getFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(faviconSVG)
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
