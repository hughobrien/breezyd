// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/matryer/is"
)

func TestVendorAssets(t *testing.T) {
	srv := httptest.NewServer((&Handler{}).mux())
	defer srv.Close()

	cases := []struct {
		path       string
		wantStatus int
		wantCT     string
	}{
		{"/ui/vendor/datastar-1.0.1.min.js", 200, "application/javascript; charset=utf-8"},
		{"/ui/vendor/missing.js", 404, ""},
		{"/ui/vendor/../etc/passwd", 404, ""},
		{"/ui/style-" + styleHash + ".css", 200, "text/css; charset=utf-8"},
		{"/ui/style-deadbeef00.css", 404, ""},
		{"/favicon.svg", 200, "image/svg+xml"},
		{"/favicon.ico", 200, "image/svg+xml"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			is := is.New(t)
			resp, err := http.Get(srv.URL + tc.path)
			is.NoErr(err)
			defer func() { _ = resp.Body.Close() }()
			is.Equal(resp.StatusCode, tc.wantStatus) // HTTP status
			if tc.wantStatus == 200 {
				is.Equal(resp.Header.Get("Content-Type"), tc.wantCT) // Content-Type
				// Versioned/hashed assets are immutable; favicon is short-cached.
				if strings.HasPrefix(tc.path, "/ui/") {
					is.True(strings.Contains(resp.Header.Get("Cache-Control"), "immutable")) // Cache-Control must include immutable
				}
			}
		})
	}
}
