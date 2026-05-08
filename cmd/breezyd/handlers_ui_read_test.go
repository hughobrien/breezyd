// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/internal/uistate"
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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	// Serial must be populated from the device registry (regression: was always empty).
	// newUITestHandler assigns ID = "TESTID00000000" + name[:2], so "alpha" -> "TESTID00000000al".
	if !strings.Contains(string(body), "TESTID00000000al") {
		t.Errorf("body missing serial; got: %s", string(body))
	}
}

// TestUIReadDeviceList_Unreachable is the issue #50 regression: a device
// configured in config.toml but with no successful poll yet must show up
// in /ui/devices as a placeholder, not be silently hidden.
func TestUIReadDeviceList_Unreachable(t *testing.T) {
	// Hand-build a Handler with one configured device but NO Snapshot in State.
	devices := map[string]DeviceConfig{
		"ghost": {ID: "BREEZYGHOST00001", Password: "1111", IP: "10.0.0.99:4000"},
	}
	h := &Handler{
		State:      NewState(),
		Devices:    NewDeviceRegistry(devices),
		Pollers:    map[string]*Poller{},
		Schedulers: map[string]*Scheduler{},
	}
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	got := string(body)
	if !strings.Contains(got, "ghost") {
		t.Errorf("body missing unreachable device name 'ghost'; got: %s", got)
	}
	if !strings.Contains(got, "unreachable") {
		t.Errorf("body missing 'unreachable' badge; got: %s", got)
	}
	if !strings.Contains(got, "10.0.0.99") {
		t.Errorf("body missing configured IP; got: %s", got)
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
	defer func() { _ = resp.Body.Close() }()

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
	defer func() { _ = resp.Body.Close() }()

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
	// Layout must NOT reference legacy.js (deleted in Task 21).
	if strings.Contains(string(body), "legacy.js") {
		t.Errorf("layout unexpectedly contains legacy.js reference")
	}
}

// ---------- buildView / cookie unit tests ----------

// testSnap returns a minimal Snapshot for use in buildView tests.
func testSnap(name string) Snapshot {
	return Snapshot{
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
	}
}

// TestBuildView_CookieSensorsClosed verifies that when the cookie says
// sensors-<name>=false and there is no alert, sensors block is closed.
func TestBuildView_CookieSensorsClosed(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	snap := testSnap("alpha")
	state := uistate.State{
		Details: map[string]bool{"sensors-alpha": false},
	}
	v := h.buildView("alpha", snap, state)
	if v.DetailsOpen["sensors"] {
		t.Error("cookie says sensors-alpha=false with no alert; want DetailsOpen[sensors]=false")
	}
}

// TestBuildView_CookieOpensSensors verifies that when the cookie says
// sensors-<name>=true, sensors block is open even by default rules.
func TestBuildView_CookieOpensSensors(t *testing.T) {
	h := newUITestHandler(t, "bedroom")
	snap := testSnap("bedroom")
	state := uistate.State{
		Details: map[string]bool{"sensors-bedroom": true},
	}
	v := h.buildView("bedroom", snap, state)
	if !v.DetailsOpen["sensors"] {
		t.Error("cookie says sensors-bedroom=true; want DetailsOpen[sensors]=true")
	}
}

// TestBuildView_AlertForcesSensorsOpen verifies that AlertActive forces
// sensors open even when the cookie says closed.
func TestBuildView_AlertForcesSensorsOpen(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	snap := testSnap("alpha")
	// Inject an alert via the 0x0084 bitmap.
	snap.Values[0x0084] = []byte{0x01, 0x00, 0x00, 0x00, 0x00} // humidity alert
	state := uistate.State{
		Details: map[string]bool{"sensors-alpha": false}, // cookie says closed
	}
	v := h.buildView("alpha", snap, state)
	if !v.DetailsOpen["sensors"] {
		t.Error("AlertActive should force sensors open regardless of cookie")
	}
}

// TestBuildView_NoCookieDefaults verifies section defaults when no cookie entry exists.
// sensors=true (default-open), info/energy/schedule=false.
func TestBuildView_NoCookieDefaults(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	snap := testSnap("alpha")
	state := uistate.State{} // empty cookie
	v := h.buildView("alpha", snap, state)

	if !v.DetailsOpen["sensors"] {
		t.Error("sensors should default to open")
	}
	for _, section := range []string{"info", "energy", "schedule"} {
		if v.DetailsOpen[section] {
			t.Errorf("%s should default to closed; got open", section)
		}
	}
}

// TestBuildView_MissingDeviceEntryPresetDefaults verifies that when a device
// has no entry in state.Preset, preset defaults are applied:
// EditingPreset=0, Automode=false, MatchSpeeds=true.
func TestBuildView_MissingDeviceEntryPresetDefaults(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	snap := testSnap("alpha")
	state := uistate.State{} // no preset entry for "alpha"
	v := h.buildView("alpha", snap, state)

	if v.EditingPreset != 0 {
		t.Errorf("EditingPreset: got %d, want 0", v.EditingPreset)
	}
	if v.Automode {
		t.Error("Automode: got true, want false")
	}
	if !v.MatchSpeeds {
		t.Error("MatchSpeeds: got false, want true")
	}
}

// TestBuildView_CookiePresetOverrides verifies that when a device entry exists
// in state.Preset, the values override defaults.
func TestBuildView_CookiePresetOverrides(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	snap := testSnap("alpha")
	state := uistate.State{
		Preset: map[string]uistate.PresetState{
			"alpha": {Open: 2, Automode: true, Match: false},
		},
	}
	v := h.buildView("alpha", snap, state)

	if v.EditingPreset != 2 {
		t.Errorf("EditingPreset: got %d, want 2", v.EditingPreset)
	}
	if !v.Automode {
		t.Error("Automode: got false, want true")
	}
	if v.MatchSpeeds {
		t.Error("MatchSpeeds: got true, want false")
	}
}

// TestBuildView_NeedsAttentionForcesInfoOpen verifies that NeedsAttention
// forces info open even when the cookie says closed.
func TestBuildView_NeedsAttentionForcesInfoOpen(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	snap := testSnap("alpha")
	// Inject fault level to trigger NeedsAttention.
	snap.Values[0x0083] = []byte{0x01} // fault warning
	state := uistate.State{
		Details: map[string]bool{"info-alpha": false}, // cookie says closed
	}
	v := h.buildView("alpha", snap, state)
	if !v.DetailsOpen["info"] {
		t.Error("NeedsAttention should force info open regardless of cookie")
	}
}
