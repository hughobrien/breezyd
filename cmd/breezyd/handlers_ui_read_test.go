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

// newUITestHandler builds a Handler with the supplied device names,
// each backed by a seeded Snapshot so the read-side helpers have data.
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

// testSnap returns a minimal Snapshot for use in buildView tests.
func testSnap() Snapshot {
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

func TestUIReadIndex_ServesDatastar(t *testing.T) {
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
	bs := string(body)
	if !strings.Contains(bs, "<!doctype html") {
		t.Errorf("body does not look like HTML: %q", bs[:min(100, len(bs))])
	}
	if !strings.Contains(bs, "datastar-1.0.1.min.js") {
		t.Errorf("layout missing datastar script reference; got: %s", bs)
	}
	if !strings.Contains(bs, `data-init="@get('/ui/sse')"`) {
		t.Errorf("layout missing data-init to /ui/sse; got: %s", bs)
	}
	if strings.Contains(bs, "htmx") {
		t.Errorf("layout unexpectedly contains htmx reference; got: %s", bs)
	}
}

func TestCollectViews_HappyPath(t *testing.T) {
	h := newUITestHandler(t, "alpha", "bravo")
	views := h.collectViews()
	if len(views) != 2 {
		t.Fatalf("got %d views, want 2", len(views))
	}
	for i, want := range []string{"alpha", "bravo"} {
		if views[i].Name != want {
			t.Errorf("view %d name: got %q, want %q", i, views[i].Name, want)
		}
		if views[i].Unreachable {
			t.Errorf("view %d unexpectedly unreachable", i)
		}
		if views[i].Serial == "" {
			t.Errorf("view %d serial empty (must come from registry)", i)
		}
	}
}

// TestCollectViews_Unreachable is the issue #50 regression: a device
// configured in config.toml but with no successful poll yet must show
// up in the SSE initial-state pass as a placeholder, not be silently
// hidden.
func TestCollectViews_Unreachable(t *testing.T) {
	devices := map[string]DeviceConfig{
		"ghost": {ID: "BREEZYGHOST00001", Password: "1111", IP: "10.0.0.99:4000"},
	}
	h := &Handler{
		State:      NewState(),
		Devices:    NewDeviceRegistry(devices),
		Pollers:    map[string]*Poller{},
		Schedulers: map[string]*Scheduler{},
	}
	views := h.collectViews()
	if len(views) != 1 {
		t.Fatalf("got %d views, want 1", len(views))
	}
	v := views[0]
	if !v.Unreachable {
		t.Error("ghost should be Unreachable=true")
	}
	if v.Name != "ghost" {
		t.Errorf("name: got %q, want ghost", v.Name)
	}
	if v.IP != "10.0.0.99:4000" {
		t.Errorf("IP: got %q, want 10.0.0.99:4000", v.IP)
	}
}

func TestViewFor_PopulatesSerial(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	v, ok := h.viewFor("alpha")
	if !ok {
		t.Fatal("viewFor returned ok=false")
	}
	// newUITestHandler assigns ID = "TESTID00000000" + name[:2], so
	// "alpha" → "TESTID00000000al".
	if v.Serial != "TESTID00000000al" {
		t.Errorf("Serial: got %q, want TESTID00000000al", v.Serial)
	}
}

func TestBuildView_DefaultsAreClean(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	v := h.buildView("alpha", testSnap())
	if v.Name != "alpha" {
		t.Errorf("Name: got %q, want alpha", v.Name)
	}
	if !v.Power {
		t.Error("Power: got false, want true")
	}
	if v.Stale {
		t.Error("Stale: got true, want false")
	}
}
