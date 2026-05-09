//go:build breezyd_test_admin

// SPDX-License-Identifier: GPL-3.0-or-later

// Tests for the build-tagged /test/devices/{name}/... admin surface.
// These run only when -tags breezyd_test_admin is set; the default
// test run (go test ./cmd/breezyd/...) does not include them.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// adminSnapshotPath returns the absolute path to snapshot_148.json.
func adminSnapshotPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../pkg/breezy/fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

// newMemHandler builds a Handler backed by a single MemClient seeded from
// snapshot_148.json. The Handler has no real Poller, which is fine — the
// test admin endpoints only need ClientFactory and Devices.
//
// Returns the handler and the underlying MemClient so tests can inspect state.
func newMemHandler(t *testing.T) (*Handler, *breezy.MemClient) {
	t.Helper()
	mc, err := breezy.NewMemClientFromFile(adminSnapshotPath(t))
	if err != nil {
		t.Fatalf("NewMemClientFromFile: %v", err)
	}

	h := &Handler{
		State: NewState(),
		Devices: NewDeviceRegistry(map[string]DeviceConfig{
			"playroom": {ID: srvDeviceID, Password: srvPassword, IP: "127.0.0.1:0"},
		}),
		// Pollers is empty so lockDevice returns a no-op unlock — fine for
		// MemClient which doesn't need the UDP serialisation mutex.
		Pollers: map[string]*Poller{},
	}
	h.ClientFactory = func(name string) (HandlerClient, error) {
		if name == "playroom" {
			return mc, nil
		}
		return nil, fmt.Errorf("unknown device %q", name)
	}
	return h, mc
}

// doAdminRequest issues an HTTP request against h and returns the recorder.
// body may be nil for bodyless requests.
func doAdminRequest(t *testing.T, h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		buf = bytes.NewReader(b)
	} else {
		buf = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestAdminSetParam verifies that POST /test/devices/{name}/params/{id}
// updates the MemClient's param value.
func TestAdminSetParam(t *testing.T) {
	is := is.New(t)
	h, mc := newMemHandler(t)

	// Write 0x42 to param 0x01 (power).
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/params/01",
		map[string]string{"value": "42"})
	is.Equal(rec.Code, http.StatusNoContent) // POST set-param must return 204

	// Verify the MemClient returns the new value.
	got, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	is.NoErr(err)
	is.Equal(len(got[0x01]), 1)        // single byte written
	is.Equal(got[0x01][0], byte(0x42)) // value reflects POST body
}

// TestAdminSetParam_HexPrefix verifies the 0x-prefixed form of the param ID.
func TestAdminSetParam_HexPrefix(t *testing.T) {
	is := is.New(t)
	h, mc := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/params/0x01",
		map[string]string{"value": "FF"})
	is.Equal(rec.Code, http.StatusNoContent) // 0x-prefixed param IDs accepted

	got, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	is.NoErr(err)
	is.Equal(len(got[0x01]), 1)
	is.Equal(got[0x01][0], byte(0xFF))
}

// TestAdminSetParam_BadHex verifies that a non-hex value body returns 400.
func TestAdminSetParam_BadHex(t *testing.T) {
	is := is.New(t)
	h, _ := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/params/01",
		map[string]string{"value": "not-hex!"})
	is.Equal(rec.Code, http.StatusBadRequest) // non-hex value is rejected
}

// TestAdminSetParam_MultiByteValue verifies that multi-byte hex values are accepted.
func TestAdminSetParam_MultiByteValue(t *testing.T) {
	is := is.New(t)
	h, mc := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/params/01",
		map[string]string{"value": "AABB"})
	is.Equal(rec.Code, http.StatusNoContent)

	got, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	is.NoErr(err)
	is.Equal(len(got[0x01]), 2) // multi-byte hex value preserved
	is.Equal(got[0x01][0], byte(0xAA))
	is.Equal(got[0x01][1], byte(0xBB))
}

// TestAdminInjectError_Auth verifies that inject-error kind=auth causes the
// next MemClient read to return ErrAuth.
func TestAdminInjectError_Auth(t *testing.T) {
	is := is.New(t)
	h, mc := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "auth"})
	is.Equal(rec.Code, http.StatusNoContent)

	_, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	is.True(errors.Is(err, breezy.ErrAuth)) // injected auth fault surfaces as ErrAuth
}

// TestAdminInjectError_Timeout verifies that inject-error kind=timeout causes
// the next MemClient write to return ErrTimeout.
func TestAdminInjectError_Timeout(t *testing.T) {
	is := is.New(t)
	h, mc := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "timeout"})
	is.Equal(rec.Code, http.StatusNoContent)

	err := mc.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x01, Value: []byte{0x00}}})
	is.True(errors.Is(err, breezy.ErrTimeout)) // injected timeout fault surfaces as ErrTimeout
}

// TestAdminInjectError_None verifies that kind=none clears previously armed
// fault injection.
func TestAdminInjectError_None(t *testing.T) {
	is := is.New(t)
	h, mc := newMemHandler(t)

	// Arm auth failure.
	mc.SetAuthFailureMode(true)

	// Clear it via the endpoint.
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "none"})
	is.Equal(rec.Code, http.StatusNoContent)

	// Subsequent reads should succeed.
	_, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	is.NoErr(err) // kind=none clears the previously armed fault
}

// TestAdminInjectError_AuthClearsTimeout verifies the mutual-exclusion contract:
// arming auth clears a previously armed timeout.
func TestAdminInjectError_AuthClearsTimeout(t *testing.T) {
	is := is.New(t)
	h, mc := newMemHandler(t)

	// First arm timeout, then arm auth.
	doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "timeout"})
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "auth"})
	is.Equal(rec.Code, http.StatusNoContent)

	_, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	is.True(errors.Is(err, breezy.ErrAuth)) // arming auth must clear prior timeout
}

// TestAdminInjectError_UnknownKind verifies that an unrecognised kind returns 400.
func TestAdminInjectError_UnknownKind(t *testing.T) {
	is := is.New(t)
	h, _ := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "banana"})
	is.Equal(rec.Code, http.StatusBadRequest) // unrecognised kind rejected
}

// TestAdminReset verifies that POST /test/devices/{name}/reset restores the
// seed params and clears injected fault state.
func TestAdminReset(t *testing.T) {
	is := is.New(t)
	h, mc := newMemHandler(t)

	// Read the seed value of param 0x01 for comparison later.
	before, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	is.NoErr(err)

	// Mutate the param and arm a fault.
	doAdminRequest(t, h, "POST", "/test/devices/playroom/params/01",
		map[string]string{"value": "FF"})
	mc.SetAuthFailureMode(true)

	// Reset.
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/reset", nil)
	is.Equal(rec.Code, http.StatusNoContent)

	// Param should be back to seed value.
	after, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	is.NoErr(err)                                       // reset must clear the injected auth fault
	is.Equal(string(before[0x01]), string(after[0x01])) // reset restores seed value
}

// TestAdminNotFound verifies that a request for an unknown device returns an error,
// not a panic.
func TestAdminNotFound(t *testing.T) {
	is := is.New(t)
	h, _ := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/nosuchdevice/reset", nil)
	// The ClientFactory returns an error for unknown names; dial() propagates
	// this as a 500 from memClientFor.
	is.True(rec.Code != http.StatusOK) // unknown device must not return 200
}

// TestAdminUDPBackend verifies that the endpoints return 400 when the
// ClientFactory provides a non-MemClient (i.e. a UDP-backed client).
func TestAdminUDPBackend(t *testing.T) {
	is := is.New(t)
	// Build a handler whose ClientFactory returns a stub that is NOT a
	// *breezy.MemClient, simulating a UDP backend.
	h := &Handler{
		State: NewState(),
		Devices: NewDeviceRegistry(map[string]DeviceConfig{
			"playroom": {ID: srvDeviceID, Password: srvPassword, IP: "127.0.0.1:0"},
		}),
		Pollers: map[string]*Poller{},
	}
	h.ClientFactory = func(name string) (HandlerClient, error) {
		return &udpStubClient{}, nil
	}

	endpoints := []string{
		"/test/devices/playroom/params/01",
		"/test/devices/playroom/inject-error",
		"/test/devices/playroom/reset",
	}
	bodies := []any{
		map[string]string{"value": "FF"},
		map[string]string{"kind": "auth"},
		nil,
	}

	for i, target := range endpoints {
		rec := doAdminRequest(t, h, "POST", target, bodies[i])
		is.Equal(rec.Code, http.StatusBadRequest) // non-MemClient backend rejected per endpoint
	}
}

// TestAdminNoRouteWithoutTag verifies that without the build tag, the routes
// are not registered and return 404. This test can't really test "without tag"
// (since this file requires the tag), but it at least exercises that the
// endpoints exist with the tag.
func TestAdminRoutesRegistered(t *testing.T) {
	is := is.New(t)
	h, _ := newMemHandler(t)

	// Verify the routes exist by checking we get non-404 responses for valid calls.
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/reset", nil)
	is.True(rec.Code != http.StatusNotFound) // route must be registered under build tag
}

// TestAdminConcurrency verifies that concurrent admin calls on the same device
// don't race (the MemClient uses internal locking).
func TestAdminConcurrency(t *testing.T) {
	h, _ := newMemHandler(t)

	done := make(chan struct{})
	for i := 0; i < 20; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			val := fmt.Sprintf("%02x", n%256)
			doAdminRequest(t, h, "POST", "/test/devices/playroom/params/01",
				map[string]string{"value": val})
		}(i)
	}
	timeout := time.After(5 * time.Second)
	for i := 0; i < 20; i++ {
		select {
		case <-done:
		case <-timeout:
			t.Fatal("concurrent admin calls timed out")
		}
	}
}

// udpStubClient is a HandlerClient that is NOT a *breezy.MemClient, simulating
// a UDP backend for the TestAdminUDPBackend test.
type udpStubClient struct{}

func (u *udpStubClient) ReadParams(_ context.Context, _ []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
	return nil, nil
}
func (u *udpStubClient) WriteParams(_ context.Context, _ []breezy.ParamWrite) error { return nil }
func (u *udpStubClient) IsLocal() bool                                              { return false }
func (u *udpStubClient) Close() error                                               { return nil }
