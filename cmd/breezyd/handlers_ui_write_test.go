// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// newUIWriteTestHandler builds a Handler for write-path UI tests. It seeds a
// Snapshot so viewFor works, and wires a real ClientFactory that dials the
// fakedevice so actual UDP writes succeed.
func newUIWriteTestHandler(t *testing.T) *Handler {
	t.Helper()
	addr := newServerFakeDevice(t)

	h := newUITestHandler(t, "alpha")
	// Replace the device config with one pointing at the real fakedevice.
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: srvPassword,
		IP:       addr,
	})
	h.ClientFactory = func(name string) (HandlerClient, error) {
		d, ok := h.Devices.Get(name)
		if !ok {
			return nil, fmt.Errorf("unknown device %q", name)
		}
		return breezy.NewClient(d.IP, d.ID, d.Password,
			breezy.WithRetries(0), breezy.WithTimeout(500*time.Millisecond))
	}
	return h
}

func TestUIWritePower_Happy(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/power", url.Values{"on": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `data-device="alpha"`) {
		t.Errorf("body missing card markup: %s", string(body))
	}
}

func TestUIWritePower_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/nope/power", url.Values{"on": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
}

func TestUIWritePower_BadForm(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Missing 'on' field — form value is absent, so onStr == "".
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/power", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `data-device="alpha"`) {
		t.Errorf("body missing card markup")
	}
	if !strings.Contains(string(body), "missing or invalid") {
		t.Errorf("body missing error message: %s", string(body))
	}
}

func TestUIWritePower_BackendError(t *testing.T) {
	h := newUIWriteTestHandler(t)
	// 192.0.2.0/24 is the TEST-NET-1 range — guaranteed unreachable.
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: srvPassword,
		IP:       "192.0.2.1:4000",
	})
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/power", url.Values{"on": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 502 {
		t.Fatalf("status: %d, want 502", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "err-banner") {
		t.Errorf("body missing error banner: %s", string(body))
	}
}

func TestUIWritePower_AuthError(t *testing.T) {
	h := newUIWriteTestHandler(t)
	// Keep the real device address but use the wrong password.
	addr, _ := h.Devices.Get("alpha")
	h.Devices.Set("alpha", DeviceConfig{
		ID:       srvDeviceID,
		Password: "WRONG",
		IP:       addr.IP,
	})
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/power", url.Values{"on": {"true"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 401 {
		t.Fatalf("status: %d, want 401", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "auth") {
		t.Errorf("body missing auth error: %s", string(body))
	}
}

// ---------- postUIMode tests ----------

func TestUIWriteMode_Happy(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, mode := range []string{"ventilation", "regeneration", "supply", "extract"} {
		resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/mode", url.Values{"mode": {mode}})
		if err != nil {
			t.Fatalf("mode=%s: %v", mode, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("mode=%s: status=%d, want 200", mode, resp.StatusCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `data-device="alpha"`) {
			t.Errorf("mode=%s: body missing card markup: %s", mode, string(body))
		}
	}
}

func TestUIWriteMode_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/nope/mode", url.Values{"mode": {"regeneration"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIWriteMode_BadForm(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Invalid mode value.
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/mode", url.Values{"mode": {"auto"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `data-device="alpha"`) {
		t.Errorf("body missing card markup")
	}
	if !strings.Contains(string(body), "ventilation/regeneration/supply/extract") {
		t.Errorf("body missing error message: %s", string(body))
	}
}

// ---------- postUISpeed tests ----------

func TestUIWriteSpeed_HappyManual(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"manual": {"50"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `data-device="alpha"`) {
		t.Errorf("body missing card markup: %s", string(body))
	}
}

func TestUIWriteSpeed_HappyPreset(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	for _, preset := range []string{"1", "2", "3"} {
		resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"preset": {preset}})
		if err != nil {
			t.Fatalf("preset=%s: %v", preset, err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != 200 {
			t.Fatalf("preset=%s: status=%d, want 200", preset, resp.StatusCode)
		}
	}
}

func TestUIWriteSpeed_NotFound(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/nope/speed", url.Values{"manual": {"50"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIWriteSpeed_BadForm_NeitherField(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "exactly one") {
		t.Errorf("body missing error message: %s", string(body))
	}
}

func TestUIWriteSpeed_BadForm_BothFields(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"manual": {"50"}, "preset": {"2"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
}

func TestUIWriteSpeed_BadForm_InvalidManual(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Out of range (5 < 10).
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"manual": {"5"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
}

func TestUIWriteSpeed_BadForm_InvalidPreset(t *testing.T) {
	h := newUIWriteTestHandler(t)
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	// Out of range (4 > 3).
	resp, err := http.PostForm(srv.URL+"/ui/devices/alpha/speed", url.Values{"preset": {"4"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 422 {
		t.Fatalf("status: %d, want 422", resp.StatusCode)
	}
}
