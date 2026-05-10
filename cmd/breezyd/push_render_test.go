// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
	"github.com/matryer/is"
)

// TestBuildPushEvent_SelectorsAndPatchCount pins the per-device push
// event shape: exactly 4 block patches + 12 sensor-cell patches, every
// selector ends with `:not([data-edit])` so an editor-open card doesn't
// have its block silently overwritten by the next poll's push, and the
// three editable threshold cells (co2 / voc / humidity) are present.
//
// A regression on the `:not([data-edit])` clause is the #65 class —
// editor work silently lost mid-poll. Pinning the literal selectors
// keeps the regression caught at unit-test speed.
func TestBuildPushEvent_SelectorsAndPatchCount(t *testing.T) {
	is := is.New(t)

	view := ui.DeviceView{
		Name:        "alpha",
		Serial:      "TESTID0000000001",
		IP:          "127.0.0.1:4000",
		SpeedMode:   "manual",
		AirflowMode: "regeneration",
		SpecialMode: "off",
	}

	ev, err := buildPushEvent("alpha", view)
	is.NoErr(err)
	is.True(ev != nil)
	is.Equal(ev.DeviceName, "alpha")

	// 4 blocks (info, energy, schedule, controls) + 12 sensor cells = 16.
	is.Equal(len(ev.Blocks), 16)

	// Every selector must end with :not([data-edit]).
	const editorSkip = `:not([data-edit])`
	seen := make(map[string]bool, len(ev.Blocks))
	for _, b := range ev.Blocks {
		if !strings.HasSuffix(b.Selector, editorSkip) {
			t.Errorf("selector %q does not end with %q (#65 regression — editor-open block would get overwritten)", b.Selector, editorSkip)
		}
		if seen[b.Selector] {
			t.Errorf("duplicate selector %q", b.Selector)
		}
		seen[b.Selector] = true
	}

	// Each of the four blocks must appear by its data-block attribute.
	for _, key := range []string{"info", "energy", "schedule", "controls"} {
		want := `[data-block="` + key + `"]:not([data-edit])`
		found := false
		for _, b := range ev.Blocks {
			if strings.Contains(b.Selector, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing block selector for %q (substring %q)", key, want)
		}
	}

	// All twelve sensor cells must appear, with the three editable ones
	// (co2 / voc / humidity) included — these are the cells the
	// threshold editor mounts in, and are the highest-stakes
	// :not([data-edit]) targets.
	wantCells := []string{
		"co2", "voc", "humidity",
		"recovery",
		"supply", "exhaust", "supply_regen", "exhaust_regen",
		"delta_supply", "delta_exhaust",
		"supply_rpm", "exhaust_rpm",
	}
	for _, key := range wantCells {
		want := `[data-sensor-cell="` + key + `"]:not([data-edit])`
		found := false
		for _, b := range ev.Blocks {
			if strings.Contains(b.Selector, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing sensor-cell selector for %q (substring %q)", key, want)
		}
	}

	// Every selector must scope to the device's card; otherwise a
	// multi-device dashboard would patch the wrong tile.
	cardScope := `.card[data-device="alpha"]`
	for _, b := range ev.Blocks {
		if !strings.HasPrefix(b.Selector, cardScope) {
			t.Errorf("selector %q does not start with %q (would cross-device scope)", b.Selector, cardScope)
		}
	}
}
