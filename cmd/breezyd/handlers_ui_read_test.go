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
	"github.com/matryer/is"
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
	is := is.New(t)
	h := &Handler{}
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	is.Equal(resp.StatusCode, 200)
	is.True(strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html")) // content-type must be text/html
	is.Equal(resp.Header.Get("Cache-Control"), "no-store")
	body, _ := io.ReadAll(resp.Body)
	bs := string(body)
	is.True(strings.Contains(bs, "<!doctype html"))              // body should look like HTML
	is.True(strings.Contains(bs, "datastar-1.0.1.min.js"))       // layout must reference datastar script
	is.True(strings.Contains(bs, `data-init="@get('/ui/sse')"`)) // layout must wire data-init to /ui/sse
	is.True(!strings.Contains(bs, "htmx"))                       // layout must not contain htmx leftovers
}

func TestCollectViews_HappyPath(t *testing.T) {
	is := is.New(t)
	h := newUITestHandler(t, "alpha", "bravo")
	views := h.collectViews()
	is.Equal(len(views), 2)
	for i, want := range []string{"alpha", "bravo"} {
		is.Equal(views[i].Name, want)  // view name must match registry order
		is.True(!views[i].Unreachable) // view must not be Unreachable
		is.True(views[i].Serial != "") // serial must come from registry
	}
}

// TestCollectViews_SortedByName pins that collectViews returns devices
// in lexicographic name order regardless of registry insertion order.
// The SSE handler relies on this for stable initial-state cold-load
// ordering on the dashboard. Without the sort, Go's randomised map
// iteration would jitter device tile order across page loads.
func TestCollectViews_SortedByName(t *testing.T) {
	is := is.New(t)
	// Insert in non-alphabetical order to defeat any "happens to be sorted"
	// false positive from a single map walk.
	devices := map[string]DeviceConfig{
		"zulu":  {ID: "BREEZYZULU000001", Password: "1111", IP: "10.0.0.1:4000"},
		"alpha": {ID: "BREEZYALPHA00001", Password: "1111", IP: "10.0.0.2:4000"},
		"mike":  {ID: "BREEZYMIKE000001", Password: "1111", IP: "10.0.0.3:4000"},
		"bravo": {ID: "BREEZYBRAVO00001", Password: "1111", IP: "10.0.0.4:4000"},
	}
	h := &Handler{
		State:      NewState(),
		Devices:    NewDeviceRegistry(devices),
		Pollers:    map[string]*Poller{},
		Schedulers: map[string]*Scheduler{},
	}
	views := h.collectViews()
	is.Equal(len(views), 4)
	want := []string{"alpha", "bravo", "mike", "zulu"}
	for i, w := range want {
		is.Equal(views[i].Name, w)
	}
}

// TestCollectViews_Unreachable is the issue #50 regression: a device
// configured in config.toml but with no successful poll yet must show
// up in the SSE initial-state pass as a placeholder, not be silently
// hidden.
func TestCollectViews_Unreachable(t *testing.T) {
	is := is.New(t)
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
	is.Equal(len(views), 1)
	v := views[0]
	is.True(v.Unreachable) // ghost device should be Unreachable=true
	is.Equal(v.Name, "ghost")
	is.Equal(v.IP, "10.0.0.99:4000")
}

func TestViewFor_PopulatesSerial(t *testing.T) {
	is := is.New(t)
	h := newUITestHandler(t, "alpha")
	v, ok := h.viewFor("alpha")
	is.True(ok) // viewFor must return ok=true
	// newUITestHandler assigns ID = "TESTID00000000" + name[:2], so
	// "alpha" → "TESTID00000000al".
	is.Equal(v.Serial, "TESTID00000000al")
}

func TestBuildView_DefaultsAreClean(t *testing.T) {
	is := is.New(t)
	h := newUITestHandler(t, "alpha")
	v := h.buildView("alpha", testSnap())
	is.Equal(v.Name, "alpha")
	is.True(v.Power)  // Power must be true
	is.True(!v.Stale) // Stale must be false
}
