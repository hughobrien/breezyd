// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// newUITestHandler builds a Handler with the supplied device names, each
// backed by a seeded Snapshot so the UI handlers have data to render.
func newUITestHandler(t *testing.T, names ...string) *Handler {
	t.Helper()
	state := NewState()
	devices := make(map[string]DeviceConfig, len(names))
	for _, name := range names {
		devices[name] = DeviceConfig{ID: "TESTID00000000" + name[:2], Password: "1111", IP: "127.0.0.1:4000"}
		state.Set(name, Snapshot{
			IP:       "127.0.0.1:4000",
			LastPoll: time.Now(),
			Values: map[breezy.ParamID][]byte{
				0x0001: {0x01}, // power on
				0x0002: {0xFF}, // manual mode
				0x0044: {0x32}, // manual 50%
				0x00B7: {0x01}, // regeneration
				0x0068: {0x00}, // heater off
				0x0088: {0x00}, // filter clean
				0x0083: {0x00}, // fault none
			},
		})
	}
	h := &Handler{
		State:      state,
		Devices:    NewDeviceRegistry(devices),
		Pollers:    map[string]*Poller{},
		Schedulers: map[string]*Scheduler{},
	}
	return h
}

func TestUIReadDeviceList(t *testing.T) {
	h := newUITestHandler(t, "alpha", "bravo")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content-type: %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control: %q, want no-store", got)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, name := range []string{"alpha", "bravo"} {
		if !strings.Contains(string(body), name) {
			t.Errorf("body missing device %q", name)
		}
	}
}

func TestUIReadDeviceCard_Happy(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/alpha/card")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content-type: %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control: %q, want no-store", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "alpha") {
		t.Errorf("body missing device name %q", "alpha")
	}
}

func TestUIReadDeviceCard_NotFound(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices/nope/card")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 404 {
		t.Fatalf("status: %d, want 404", resp.StatusCode)
	}
}

func TestUIReadIndex_ServesHTML(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content-type: %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control: %q, want no-store", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<!doctype html") {
		t.Errorf("body does not look like HTML: %q", string(body)[:min(100, len(body))])
	}
	// Layout must reference the htmx vendor script.
	if !strings.Contains(string(body), "htmx") {
		t.Errorf("body missing htmx script reference")
	}
	// Layout must reference legacy.js.
	if !strings.Contains(string(body), "legacy.js") {
		t.Errorf("body missing legacy.js reference")
	}
}

func TestUIReadLegacyJS(t *testing.T) {
	h := &Handler{}
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/legacy.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/javascript") {
		t.Errorf("content-type: %q", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control: %q, want no-store", got)
	}
}
