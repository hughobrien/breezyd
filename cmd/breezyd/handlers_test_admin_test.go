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
	h, mc := newMemHandler(t)

	// Write 0x42 to param 0x01 (power).
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/params/01",
		map[string]string{"value": "42"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the MemClient returns the new value.
	got, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	if err != nil {
		t.Fatalf("ReadParams: %v", err)
	}
	if len(got[0x01]) != 1 || got[0x01][0] != 0x42 {
		t.Errorf("param 0x01 = %x, want [42]", got[0x01])
	}
}

// TestAdminSetParam_HexPrefix verifies the 0x-prefixed form of the param ID.
func TestAdminSetParam_HexPrefix(t *testing.T) {
	h, mc := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/params/0x01",
		map[string]string{"value": "FF"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	if err != nil {
		t.Fatalf("ReadParams: %v", err)
	}
	if len(got[0x01]) != 1 || got[0x01][0] != 0xFF {
		t.Errorf("param 0x01 = %x, want [FF]", got[0x01])
	}
}

// TestAdminSetParam_BadHex verifies that a non-hex value body returns 400.
func TestAdminSetParam_BadHex(t *testing.T) {
	h, _ := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/params/01",
		map[string]string{"value": "not-hex!"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 got %d", rec.Code)
	}
}

// TestAdminSetParam_MultiByteValue verifies that multi-byte hex values are accepted.
func TestAdminSetParam_MultiByteValue(t *testing.T) {
	h, mc := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/params/01",
		map[string]string{"value": "AABB"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	if err != nil {
		t.Fatalf("ReadParams: %v", err)
	}
	if len(got[0x01]) != 2 || got[0x01][0] != 0xAA || got[0x01][1] != 0xBB {
		t.Errorf("param 0x01 = %x, want [AA BB]", got[0x01])
	}
}

// TestAdminInjectError_Auth verifies that inject-error kind=auth causes the
// next MemClient read to return ErrAuth.
func TestAdminInjectError_Auth(t *testing.T) {
	h, mc := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "auth"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 got %d: %s", rec.Code, rec.Body.String())
	}

	_, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	if !errors.Is(err, breezy.ErrAuth) {
		t.Errorf("expected ErrAuth, got %v", err)
	}
}

// TestAdminInjectError_Timeout verifies that inject-error kind=timeout causes
// the next MemClient write to return ErrTimeout.
func TestAdminInjectError_Timeout(t *testing.T) {
	h, mc := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "timeout"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 got %d: %s", rec.Code, rec.Body.String())
	}

	err := mc.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x01, Value: []byte{0x00}}})
	if !errors.Is(err, breezy.ErrTimeout) {
		t.Errorf("expected ErrTimeout, got %v", err)
	}
}

// TestAdminInjectError_None verifies that kind=none clears previously armed
// fault injection.
func TestAdminInjectError_None(t *testing.T) {
	h, mc := newMemHandler(t)

	// Arm auth failure.
	mc.SetAuthFailureMode(true)

	// Clear it via the endpoint.
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "none"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 got %d: %s", rec.Code, rec.Body.String())
	}

	// Subsequent reads should succeed.
	_, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	if err != nil {
		t.Errorf("unexpected error after clearing fault: %v", err)
	}
}

// TestAdminInjectError_AuthClearsTimeout verifies the mutual-exclusion contract:
// arming auth clears a previously armed timeout.
func TestAdminInjectError_AuthClearsTimeout(t *testing.T) {
	h, mc := newMemHandler(t)

	// First arm timeout, then arm auth.
	doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "timeout"})
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "auth"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 got %d", rec.Code)
	}

	_, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	if !errors.Is(err, breezy.ErrAuth) {
		t.Errorf("expected ErrAuth (timeout was cleared), got %v", err)
	}
}

// TestAdminInjectError_UnknownKind verifies that an unrecognised kind returns 400.
func TestAdminInjectError_UnknownKind(t *testing.T) {
	h, _ := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/inject-error",
		map[string]string{"kind": "banana"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 got %d", rec.Code)
	}
}

// TestAdminReset verifies that POST /test/devices/{name}/reset restores the
// seed params and clears injected fault state.
func TestAdminReset(t *testing.T) {
	h, mc := newMemHandler(t)

	// Read the seed value of param 0x01 for comparison later.
	before, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	if err != nil {
		t.Fatalf("ReadParams seed: %v", err)
	}

	// Mutate the param and arm a fault.
	doAdminRequest(t, h, "POST", "/test/devices/playroom/params/01",
		map[string]string{"value": "FF"})
	mc.SetAuthFailureMode(true)

	// Reset.
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/reset", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 got %d: %s", rec.Code, rec.Body.String())
	}

	// Param should be back to seed value.
	after, err := mc.ReadParams(context.Background(), []breezy.ParamID{0x01})
	if err != nil {
		t.Fatalf("ReadParams after reset: %v (fault not cleared?)", err)
	}
	if string(before[0x01]) != string(after[0x01]) {
		t.Errorf("param 0x01 after reset = %x, want seed %x", after[0x01], before[0x01])
	}
}

// TestAdminNotFound verifies that a request for an unknown device returns an error,
// not a panic.
func TestAdminNotFound(t *testing.T) {
	h, _ := newMemHandler(t)

	rec := doAdminRequest(t, h, "POST", "/test/devices/nosuchdevice/reset", nil)
	// The ClientFactory returns an error for unknown names; dial() propagates
	// this as a 500 from memClientFor.
	if rec.Code == http.StatusOK {
		t.Errorf("expected non-200 for unknown device, got 200")
	}
}

// TestAdminUDPBackend verifies that the endpoints return 400 when the
// ClientFactory provides a non-MemClient (i.e. a UDP-backed client).
func TestAdminUDPBackend(t *testing.T) {
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
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400 for UDP backend, got %d: %s",
				target, rec.Code, rec.Body.String())
		}
	}
}

// TestAdminNoRouteWithoutTag verifies that without the build tag, the routes
// are not registered and return 404. This test can't really test "without tag"
// (since this file requires the tag), but it at least exercises that the
// endpoints exist with the tag.
func TestAdminRoutesRegistered(t *testing.T) {
	h, _ := newMemHandler(t)

	// Verify the routes exist by checking we get non-404 responses for valid calls.
	rec := doAdminRequest(t, h, "POST", "/test/devices/playroom/reset", nil)
	if rec.Code == http.StatusNotFound {
		t.Error("POST /test/devices/playroom/reset returned 404; route not registered")
	}
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
