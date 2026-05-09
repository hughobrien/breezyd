// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/matryer/is"
)

// TestUI_GetIndex confirms GET / returns the embedded dashboard with the
// correct Content-Type and a non-empty body that includes the page title.
func TestUI_GetIndex(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/", nil)

	is.Equal(rec.Code, http.StatusOK)                                      // status
	is.Equal(rec.Header().Get("Content-Type"), "text/html; charset=utf-8") // Content-Type
	is.Equal(rec.Header().Get("Cache-Control"), "no-store")                // Cache-Control
	body := rec.Body.String()
	is.True(len(body) > 0) // body must be non-empty; go:embed wired
	// Layout template renders <title>breezy</title> (index.html had "breezyd").
	is.True(strings.Contains(body, "<title>breezy</title>")) // body must contain page title
}

// TestUI_DoesNotInterceptAPI is a regression: the new GET /{$} pattern
// must not catch other API paths. /v1/devices should still return JSON.
func TestUI_DoesNotInterceptAPI(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices", nil)

	is.Equal(rec.Code, http.StatusOK)                                                // status
	is.True(strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json")) // Content-Type must be JSON
}

// TestUI_UnknownPath confirms the index pattern does NOT catch arbitrary
// unmatched paths. A typo on the API surface should 404, not return HTML.
func TestUI_UnknownPath(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/asdf", nil)

	is.Equal(rec.Code, http.StatusNotFound) // status
}
