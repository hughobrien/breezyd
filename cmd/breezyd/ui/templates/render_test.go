// SPDX-License-Identifier: GPL-3.0-or-later

package templates

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
)

var update = flag.Bool("update", false, "update golden files")

func loadView(t *testing.T, name string) ui.DeviceView {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var v ui.DeviceView
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestLayout(t *testing.T) {
	var sb strings.Builder
	d := LayoutData{StyleHash: "abc123def0", HTMXVersion: "2.0.4"}
	if err := Layout(d).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	// FOUC script must appear before the stylesheet link
	scriptIdx := strings.Index(got, "localStorage.getItem")
	linkIdx := strings.Index(got, `<link rel="stylesheet"`)
	if scriptIdx < 0 || linkIdx < 0 || scriptIdx > linkIdx {
		t.Fatalf("FOUC script not before stylesheet link\n%s", got)
	}
	wantContains := []string{
		`/ui/style-abc123def0.css`,
		`/ui/vendor/htmx-2.0.4.min.js`,
		`/ui/vendor/htmx-response-targets-2.0.4.min.js`,
		`hx-ext="response-targets"`,
		`hx-target-422="closest .card"`,
		`every 5s`,
		`<summary><h1>breezy</h1></summary>`,
		`data-theme-set="light"`,
		`data-theme-set="dark"`,
		`data-theme-set="auto"`,
	}
	wantAbsent := []string{
		`legacy.js`,
	}
	for _, w := range wantContains {
		if !strings.Contains(got, w) {
			t.Errorf("layout missing %q", w)
		}
	}
	for _, w := range wantAbsent {
		if strings.Contains(got, w) {
			t.Errorf("layout unexpectedly contains %q", w)
		}
	}
}

// TestScheduleEditRow pins two behaviors that were issue regressions:
//
//   - #42: the 'at' input is a native timepicker (type="time"), not a free
//     text field.
//   - #44: when the action is "off", the pct input has no value (an empty
//     fan percent is the truthful read for an off row, and the handler
//     accepts empty pct iff action=="off").
func TestScheduleEditRow(t *testing.T) {
	cases := []struct {
		name       string
		entry      ui.ScheduleEntryView
		wantValueP string // pct input value attribute literal we expect to find
		notWant    []string
	}{
		{
			name:       "regen row keeps pct value",
			entry:      ui.ScheduleEntryView{At: "08:00", Action: "regeneration", Pct: 60},
			wantValueP: `value="60"`,
		},
		{
			name:       "off row has empty pct value",
			entry:      ui.ScheduleEntryView{At: "23:00", Action: "off", Pct: 60},
			wantValueP: `value=""`,
			notWant:    []string{`value="60"`},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sb strings.Builder
			if err := ScheduleEditRow(c.entry).Render(context.Background(), &sb); err != nil {
				t.Fatal(err)
			}
			got := sb.String()
			if !strings.Contains(got, `type="time"`) || !strings.Contains(got, `name="at"`) {
				t.Errorf("at input is not a timepicker (issue #42 regression)\n%s", got)
			}
			if !strings.Contains(got, c.wantValueP) {
				t.Errorf("pct input missing %s\n%s", c.wantValueP, got)
			}
			for _, n := range c.notWant {
				if strings.Contains(got, n) {
					t.Errorf("pct input unexpectedly contains %s (issue #44 regression)\n%s", n, got)
				}
			}
		})
	}
}

func TestDeviceCardGolden(t *testing.T) {
	cases := []string{
		"snapshot_regen", "snapshot_manual", "snapshot_settling",
		"snapshot_sensor_alert", "snapshot_schedule_alert",
		"snapshot_energy_error", "snapshot_no_energy",
		"snapshot_editor_open_preset2",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			view := loadView(t, name)
			var sb strings.Builder
			if err := DeviceCard(view).Render(context.Background(), &sb); err != nil {
				t.Fatal(err)
			}
			got := sb.String()
			goldenPath := filepath.Join("testdata", "golden_"+strings.TrimPrefix(name, "snapshot_")+".html")
			if *update {
				if err := os.WriteFile(goldenPath, []byte(got), 0644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != got {
				t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, string(want))
			}
		})
	}
}
