// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hughobrien/breezyd/internal/config"
	"github.com/matryer/is"
)

// Bad param IDs / values / passwords are local usage errors: exit 2 and a
// message, never a panic, in both standalone and daemon mode (#260).

func TestStandalone_BadInput_ExitsTwo(t *testing.T) {
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	long := strings.Repeat("ab", 300)
	cases := []struct {
		name string
		args []string
	}{
		{"get reserved", []string{"playroom", "get", "0xFF"}},
		{"get reserved page 1", []string{"playroom", "get", "0x01FE"}},
		{"set reserved", []string{"playroom", "set", "0xFC", "01"}},
		{"set oversize", []string{"playroom", "set", "0x77", long}},
		{"set empty", []string{"playroom", "set", "0x77", ""}},
		{"set empty 0x", []string{"playroom", "set", "0x77", "0x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			code, _, stderr := runStandalone(t, devices, tc.args...)
			is.Equal(code, 2) // local usage error
			is.True(stderr != "")
		})
	}
}

func TestDaemonMode_BadInput_ExitsTwo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("CLI must reject locally, but daemon was called: %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	long := strings.Repeat("ab", 300)
	for _, args := range [][]string{
		{"playroom", "get", "0xFF"},
		{"playroom", "set", "0xFE", "01"},
		{"playroom", "set", "0x77", long},
		{"playroom", "set", "0x77", ""},
	} {
		code, _, stderr := runCLI(t, srv, args...)
		if code != 2 || stderr == "" {
			t.Errorf("%v: code=%d stderr=%q, want 2 + message", args, code, stderr)
		}
	}
}

func TestDiscover_LongPassword_ExitsTwo(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"discover", "-p", "123456789"},
		{"discover", "--password=123456789", "127.0.0.1:1"},
	} {
		var stdout, stderr strings.Builder
		code := run(args, &stdout, &stderr, nil)
		is.Equal(code, 2) // usage error, not panic
		is.True(strings.Contains(stderr.String(), "password"))
	}
}
