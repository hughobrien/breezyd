// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"net/http"
	"strings"
	"testing"
)

// TestUI_GetIndex confirms GET / returns the embedded dashboard with the
// correct Content-Type and a non-empty body that includes the page title.
func TestUI_GetIndex(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	body := rec.Body.String()
	if len(body) == 0 {
		t.Fatal("body is empty; go:embed likely not wired")
	}
	if !strings.Contains(body, "<title>breezyd</title>") {
		t.Errorf("body missing <title>breezyd</title>; got prefix %q", body[:min(200, len(body))])
	}
}

// TestUI_DoesNotInterceptAPI is a regression: the new GET /{$} pattern
// must not catch other API paths. /v1/devices should still return JSON.
func TestUI_DoesNotInterceptAPI(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json...", got)
	}
}

// TestUI_UnknownPath confirms the index pattern does NOT catch arbitrary
// unmatched paths. A typo on the API surface should 404, not return HTML.
func TestUI_UnknownPath(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/asdf", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
