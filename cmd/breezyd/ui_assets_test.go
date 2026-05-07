// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVendorAssets(t *testing.T) {
	srv := httptest.NewServer((&Handler{}).mux())
	defer srv.Close()

	cases := []struct {
		path       string
		wantStatus int
		wantCT     string
	}{
		{"/ui/vendor/htmx-2.0.4.min.js", 200, "application/javascript; charset=utf-8"},
		{"/ui/vendor/htmx-response-targets-2.0.4.min.js", 200, "application/javascript; charset=utf-8"},
		{"/ui/vendor/missing.js", 404, ""},
		{"/ui/vendor/../etc/passwd", 404, ""},
		{"/ui/style-" + styleHash + ".css", 200, "text/css; charset=utf-8"},
		{"/ui/style-deadbeef00.css", 404, ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus == 200 {
				if got := resp.Header.Get("Content-Type"); got != tc.wantCT {
					t.Errorf("content-type: got %q, want %q", got, tc.wantCT)
				}
				if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
					t.Errorf("cache-control missing immutable: %q", got)
				}
			}
		})
	}
}
