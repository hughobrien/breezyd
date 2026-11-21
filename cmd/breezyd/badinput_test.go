// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/matryer/is"
)

// Reserved param IDs and oversize values reaching the raw param endpoints
// must produce 400 bad_request, not a panic that drops the connection (#260).
func TestHandler_RawParam_BadInput_400(t *testing.T) {
	long := strings.Repeat("ab", 300)
	cases := []struct {
		name, method, path string
		body               any
	}{
		{"get reserved FF", http.MethodGet, "/v1/devices/playroom/params/00FF", nil},
		{"get reserved page 1", http.MethodGet, "/v1/devices/playroom/params/0x01FC", nil},
		{"post reserved FE", http.MethodPost, "/v1/devices/playroom/params/0x00FE", map[string]any{"hex": "01"}},
		{"post oversize", http.MethodPost, "/v1/devices/playroom/params/0x0077", map[string]any{"hex": long}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, tc.method, tc.path, tc.body)
			is.Equal(rec.Code, http.StatusBadRequest)
			var env errEnvelope
			_ = json.Unmarshal(rec.Body.Bytes(), &env)
			is.Equal(env.Code, "bad_request")
		})
	}
}

// A too-long [daemon].password must make discovery fail with an error
// (logged as a warning by the caller), not crash the daemon.
func TestRunDiscovery_LongPassword_ReturnsError(t *testing.T) {
	is := is.New(t)
	err := runDiscovery(context.Background(), NewDeviceRegistry(nil), "123456789")
	is.True(err != nil)
}
