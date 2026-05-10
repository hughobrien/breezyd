//go:build !breezyd_test_admin

// SPDX-License-Identifier: GPL-3.0-or-later

// Tests for the off-side stub of mountTestAdmin (production builds).
// Compiled when the breezyd_test_admin build tag is NOT set, this asserts
// the stub registers no routes so a stray production binary cannot expose
// /test/... admin endpoints.
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/matryer/is"
)

func TestMountTestAdmin_OffStub_RegistersNothing(t *testing.T) {
	is := is.New(t)

	mux := http.NewServeMux()
	mountTestAdmin(mux, nil)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	for _, path := range []string{
		"/test/devices/playroom/params/0x0001",
		"/test/devices/playroom/inject-error",
		"/test/devices/playroom/reset",
	} {
		req, err := http.NewRequest(http.MethodPost, srv.URL+path, nil)
		is.NoErr(err)
		resp, err := http.DefaultClient.Do(req)
		is.NoErr(err)
		_ = resp.Body.Close()
		is.Equal(resp.StatusCode, http.StatusNotFound) // production builds must return 404 on /test/... paths
	}
}
