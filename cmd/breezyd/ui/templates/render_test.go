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
	d := LayoutData{StyleHash: "abc123def0", DatastarVersion: "1.0.1"}
	if err := Layout(d).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	// FOUC script must appear before the stylesheet link.
	scriptIdx := strings.Index(got, "localStorage.getItem")
	linkIdx := strings.Index(got, `<link rel="stylesheet"`)
	if scriptIdx < 0 || linkIdx < 0 || scriptIdx > linkIdx {
		t.Fatalf("FOUC script not before stylesheet link\n%s", got)
	}
	wantContains := []string{
		`/ui/style-abc123def0.css`,
		`/ui/vendor/datastar-1.0.1.min.js`,
		`/ui/vendor/dashboard.js`,
		`data-init="@get('/ui/sse')"`,
		`<summary><h1>breezy</h1></summary>`,
		`data-theme-set="light"`,
		`data-theme-set="dark"`,
		`data-theme-set="auto"`,
	}
	wantAbsent := []string{
		`htmx`,
		`legacy.js`,
		`every 5s`,
		`hx-ext`,
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

// TestScheduleEditRow pins behaviors that were issue regressions:
//
//   - #42: the 'at' input is a native timepicker (type="time"), not a
//     free text field.
//   - #44 (handler): when the action is "off", the pct input has no
//     value (an empty fan percent is the truthful read for an off row,
//     and the handler accepts empty pct iff action=="off").
//   - #44 (editor sync): the action <select> carries an inline
//     data-on:change handler that mirrors value/readonly/class on the
//     pct <input> when the user toggles action, and every pct input
//     stashes a sane fallback in data-orig-pct so toggling back from
//     "off" restores a valid value.
//
// Note on attribute escaping: templ HTML-escapes single quotes in
// dynamic attribute values to &#39; (string-interpolated values via
// `data-on:change={ expr }`). Static literal attributes like
// `data-on:click="evt.target.closest('tr').remove()"` are emitted
// verbatim. We pin the &#39;-escaped form for the change handler.
func TestScheduleEditRow(t *testing.T) {
	// wantChangeExpr is the exact escaped JS string templ emits for the
	// data-on:change attribute on the action <select>. Pinning the
	// literal guards against accidental edits to scheduleActionChangeExpr
	// or to templ's escaping behavior on dynamic attribute interpolation.
	const wantChangeExpr = `data-on:change="const pct = evt.target.closest(&#39;tr&#39;).querySelector(&#39;input[name=pct]&#39;); if (evt.target.value === &#39;off&#39;) { pct.value = &#39;&#39;; pct.setAttribute(&#39;readonly&#39;, &#39;&#39;); pct.classList.add(&#39;pct-disabled&#39;); } else { pct.value = pct.dataset.origPct; pct.removeAttribute(&#39;readonly&#39;); pct.classList.remove(&#39;pct-disabled&#39;); }"`

	cases := []struct {
		name        string
		entry       ui.ScheduleEntryView
		wantValueP  string
		wantOrigPct string // expected data-orig-pct attribute value
		notWant     []string
	}{
		{
			name:        "regen row keeps pct value",
			entry:       ui.ScheduleEntryView{At: "08:00", Action: "regeneration", Pct: 60},
			wantValueP:  `value="60"`,
			wantOrigPct: `data-orig-pct="60"`,
		},
		{
			name:        "off row has empty pct value but stashes 50 fallback",
			entry:       ui.ScheduleEntryView{At: "23:00", Action: "off", Pct: 0},
			wantValueP:  `value=""`,
			wantOrigPct: `data-orig-pct="50"`,
		},
		{
			name:        "min-range pct rendered as orig-pct",
			entry:       ui.ScheduleEntryView{At: "06:00", Action: "ventilation", Pct: 10},
			wantValueP:  `value="10"`,
			wantOrigPct: `data-orig-pct="10"`,
		},
		{
			name:        "max-range pct rendered as orig-pct",
			entry:       ui.ScheduleEntryView{At: "12:00", Action: "supply", Pct: 100},
			wantValueP:  `value="100"`,
			wantOrigPct: `data-orig-pct="100"`,
		},
		{
			name:        "out-of-range pct falls back to 50 in orig-pct",
			entry:       ui.ScheduleEntryView{At: "15:00", Action: "regeneration", Pct: 5},
			wantValueP:  `value="5"`,
			wantOrigPct: `data-orig-pct="50"`,
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
			if !strings.Contains(got, c.wantOrigPct) {
				t.Errorf("pct input missing %s (issue #44 editor-sync regression)\n%s", c.wantOrigPct, got)
			}
			if !strings.Contains(got, wantChangeExpr) {
				t.Errorf("action select missing data-on:change literal (issue #44 editor-sync regression)\nwant: %s\n--- got ---\n%s", wantChangeExpr, got)
			}
			for _, n := range c.notWant {
				if strings.Contains(got, n) {
					t.Errorf("pct input unexpectedly contains %s (issue #44 regression)\n%s", n, got)
				}
			}
		})
	}
}

// TestSchedulePctOrigValue pins the fallback logic feeding data-orig-pct
// directly. Redundant with TestScheduleEditRow's wantOrigPct cases, but
// fast and useful when the helper itself changes.
func TestSchedulePctOrigValue(t *testing.T) {
	cases := []struct {
		name string
		pct  int
		want string
	}{
		{"zero (off-row sentinel)", 0, "50"},
		{"below min", 5, "50"},
		{"min boundary", 10, "10"},
		{"mid range", 60, "60"},
		{"max boundary", 100, "100"},
		{"above max", 150, "50"},
		{"negative", -1, "50"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := schedulePctOrigValue(ui.ScheduleEntryView{Pct: c.pct})
			if got != c.want {
				t.Errorf("schedulePctOrigValue(Pct=%d) = %q; want %q", c.pct, got, c.want)
			}
		})
	}
}

func TestDeviceCardGolden(t *testing.T) {
	cases := []string{
		"snapshot_regen", "snapshot_manual", "snapshot_settling",
		"snapshot_sensor_alert", "snapshot_schedule_alert",
		"snapshot_energy_error", "snapshot_no_energy",
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

func TestRenderDeviceCard_ReactiveOuter(t *testing.T) {
	v := loadView(t, "snapshot_settling")
	var sb strings.Builder
	if err := DeviceCard(v).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	wantContains := []string{
		`data-class:stale="$stale"`,
		`data-attr:data-speed-mode="$speedMode"`,
		`data-attr:data-airflow-mode="$airflowMode"`,
		`data-show="$stale"`,
		`&#34;sensorsAlert&#34;`,
		`&#34;speedMode&#34;`,
		`data-block="info"`,
	}
	wantAbsent := []string{
		`data-speed-mode="manual"`,
		`data-speed-mode="preset1"`,
		`class="card stale"`,
	}
	for _, s := range wantContains {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in card render", s)
		}
	}
	for _, s := range wantAbsent {
		if strings.Contains(got, s) {
			t.Errorf("unexpected %q in card render", s)
		}
	}
}

func TestRenderBlocks_DataBlockMarkers(t *testing.T) {
	v := loadView(t, "snapshot_settling")
	var sb strings.Builder
	if err := DeviceCard(v).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	for _, s := range []string{
		`data-block="info"`,
		`data-block="energy"`,
		`data-block="sensors"`,
		`data-block="schedule"`,
		`data-block="controls"`,
		`data-class:alert="$sensorsAlert"`,
	} {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in card render", s)
		}
	}
	// Plain sensor cells get data-sensor-cell="<key>".
	for _, k := range []string{"recovery", "supply", "exhaust", "supply_regen", "exhaust_regen", "delta_supply", "delta_exhaust", "supply_rpm", "exhaust_rpm"} {
		want := `data-sensor-cell="` + k + `"`
		if !strings.Contains(got, want) {
			t.Errorf("missing plain cell marker %q", want)
		}
	}
	// Controls block reactive data-edit binding. The attribute is a static
	// literal in the templ file so single quotes are not HTML-escaped.
	if !strings.Contains(got, `data-attr:data-edit="$editor !== 0 ? 'true' : null"`) {
		t.Errorf("controls reactive data-edit binding missing")
	}
}

func TestRenderScheduleEdit_HasDataEdit(t *testing.T) {
	var sb strings.Builder
	sv := ui.ScheduleView{Present: true}
	if err := ScheduleBlockEdit("alpha", sv, false, "").Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	if !strings.Contains(got, `data-edit="true"`) {
		t.Errorf("ScheduleBlockEdit missing data-edit; got=%q", got)
	}
	if !strings.Contains(got, `data-block="schedule"`) {
		t.Errorf("ScheduleBlockEdit missing data-block=schedule; got=%q", got)
	}
}

func TestRenderScheduleEdit_EnabledCheckboxReflectsState(t *testing.T) {
	cases := []struct {
		enabled     bool
		wantChecked bool
	}{
		{enabled: true, wantChecked: true},
		{enabled: false, wantChecked: false},
	}
	for _, tc := range cases {
		var sb strings.Builder
		sv := ui.ScheduleView{Present: true, Enabled: tc.enabled}
		if err := ScheduleBlockEdit("alpha", sv, false, "").Render(context.Background(), &sb); err != nil {
			t.Fatalf("enabled=%v: render: %v", tc.enabled, err)
		}
		got := sb.String()
		// The enabled checkbox is the only `name="enabled"` input of type checkbox;
		// the sibling hidden field has the same name but type=hidden.
		hasChecked := strings.Contains(got, `<input type="checkbox" name="enabled" value="true" checked>`)
		if hasChecked != tc.wantChecked {
			t.Errorf("enabled=%v: got checked=%v, want %v; html=%q", tc.enabled, hasChecked, tc.wantChecked, got)
		}
	}
}

func TestRenderThresholdEdit_HasDataEdit(t *testing.T) {
	var sb strings.Builder
	if err := SensorThresholdEdit("alpha", "co2", "eCO₂", 400, 2000, 10, 800, true, false).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	if !strings.Contains(got, `data-edit="true"`) {
		t.Errorf("SensorThresholdEdit missing data-edit; got=%q", got)
	}
	if !strings.Contains(got, `data-sensor-cell="co2"`) {
		t.Errorf("SensorThresholdEdit missing data-sensor-cell; got=%q", got)
	}
}

func TestCardSignalsFor_JSON(t *testing.T) {
	v := ui.DeviceView{
		Stale:       true,
		SpeedMode:   "manual",
		AirflowMode: "regeneration",
		LastPollAge: "12s",
		Sensors:     ui.SensorsView{AlertActive: true},
	}
	got, err := ui.MarshalCardSignals(v)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"stale":        true,
		"speedMode":    "manual",
		"airflowMode":  "regeneration",
		"lastPollAge":  "12s",
		"sensorsAlert": true,
	}
	for k, w := range want {
		if back[k] != w {
			t.Errorf("field %q: got %v, want %v", k, back[k], w)
		}
	}
}
