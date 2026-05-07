//go:build fakedevice_admin

// SPDX-License-Identifier: GPL-3.0-or-later

package fakedevice

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// newAdminTestServer creates a Server + AdminServer for a test, registering
// cleanup for both. Relies on snapshotPath, testDeviceID, and testPassword
// defined in fake_test.go (same package, no build tag).
func newAdminTestServer(t *testing.T) (*Server, *AdminServer) {
	t.Helper()
	s, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a, err := s.StartAdmin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return s, a
}

func mustNewRequest(t *testing.T, method, url string, body []byte) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func TestAdminPutState(t *testing.T) {
	s, a := newAdminTestServer(t)

	body := map[string]any{
		"params": map[string]string{
			"0001": "01", // power on
			"0044": "32", // 50%
		},
	}
	buf, _ := json.Marshal(body)
	resp, err := http.DefaultClient.Do(mustNewRequest(t, "PUT", "http://"+a.Addr()+"/state", buf))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT /state status: %d", resp.StatusCode)
	}

	s.mu.Lock()
	v0001 := s.values[breezy.ParamID(0x0001)]
	v0044 := s.values[breezy.ParamID(0x0044)]
	s.mu.Unlock()

	if !bytes.Equal(v0001, []byte{0x01}) {
		t.Errorf("param 0001: got %x, want 01", v0001)
	}
	if !bytes.Equal(v0044, []byte{0x32}) {
		t.Errorf("param 0044: got %x, want 32", v0044)
	}
}

func TestAdminPutState_BadParamID(t *testing.T) {
	_, a := newAdminTestServer(t)

	body := map[string]any{
		"params": map[string]string{
			"ZZZZ": "01",
		},
	}
	buf, _ := json.Marshal(body)
	resp, err := http.DefaultClient.Do(mustNewRequest(t, "PUT", "http://"+a.Addr()+"/state", buf))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for bad param id, got %d", resp.StatusCode)
	}
}

func TestAdminSimulateAuthFailure(t *testing.T) {
	s, a := newAdminTestServer(t)

	resp, err := http.Post("http://"+a.Addr()+"/simulate/auth-failure", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	s.mu.Lock()
	af := s.forceAuthFailure
	s.mu.Unlock()
	if !af {
		t.Error("forceAuthFailure should be true after POST /simulate/auth-failure")
	}
}

func TestAdminSimulateAuthFailure_Off(t *testing.T) {
	s, a := newAdminTestServer(t)
	s.SetAuthFailureMode(true)

	resp, err := http.Post("http://"+a.Addr()+"/simulate/auth-failure?on=false", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	s.mu.Lock()
	af := s.forceAuthFailure
	s.mu.Unlock()
	if af {
		t.Error("forceAuthFailure should be false after ?on=false")
	}
}

func TestAdminSimulateUDPTimeout(t *testing.T) {
	s, a := newAdminTestServer(t)

	resp, err := http.Post("http://"+a.Addr()+"/simulate/udp-timeout", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	s.mu.Lock()
	silent := s.silentMode
	s.mu.Unlock()
	if !silent {
		t.Error("silentMode should be true after POST /simulate/udp-timeout")
	}
}

func TestAdminSimulateFanSettle(t *testing.T) {
	s, a := newAdminTestServer(t)

	resp, err := http.Post("http://"+a.Addr()+"/simulate/fan-settle?ms=500", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	s.mu.Lock()
	d := s.replyDelay
	s.mu.Unlock()
	if d.Milliseconds() != 500 {
		t.Errorf("replyDelay: got %v, want 500ms", d)
	}
}

func TestAdminSimulateFanSettle_BadParam(t *testing.T) {
	_, a := newAdminTestServer(t)

	resp, err := http.Post("http://"+a.Addr()+"/simulate/fan-settle?ms=abc", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestAdminReset(t *testing.T) {
	s, a := newAdminTestServer(t)

	// Put server into a modified state.
	s.SetAuthFailureMode(true)
	s.SetSilentMode(true)
	s.SetReplyDelay(200)
	s.SetParamValue(breezy.ParamID(0x0001), []byte{0x00})

	resp, err := http.Post("http://"+a.Addr()+"/reset", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}

	s.mu.Lock()
	af := s.forceAuthFailure
	silent := s.silentMode
	delay := s.replyDelay
	s.mu.Unlock()

	if af {
		t.Error("forceAuthFailure should be cleared after reset")
	}
	if silent {
		t.Error("silentMode should be cleared after reset")
	}
	if delay != 0 {
		t.Errorf("replyDelay should be 0 after reset, got %v", delay)
	}
}

func TestAdminAddr(t *testing.T) {
	_, a := newAdminTestServer(t)
	addr := a.Addr()
	if addr == "" {
		t.Fatal("Addr() returned empty string")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("bad addr %q: %v", addr, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("host: got %q, want 127.0.0.1", host)
	}
	if port == "0" {
		t.Errorf("port should not be 0 after bind")
	}
}
