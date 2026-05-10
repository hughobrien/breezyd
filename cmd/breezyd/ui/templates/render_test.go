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
		`data-init="@get('/ui/sse')"`,
		`<summary><h1>breezy</h1></summary>`,
		`data-theme-set="light"`,
		`data-theme-set="dark"`,
		`data-theme-set="auto"`,
		// Page-shell skeleton markers (G-web-5 from #131): the action-error
		// sink and the empty initial-state device-list container.
		`id="global-error-banner"`,
		`aria-live="polite"`,
		`id="device-list"`,
		`class="grid"`,
		// Theme-picker IIFE markers (G-web-6 from #131): bfcache-restore
		// guard, the theme-write/clear pair, and the data-theme-set hook
		// the IIFE listens for. Keeps the inline IIFE from silently
		// disappearing during a layout refactor.
		`picker.open = false`,
		`localStorage.setItem('theme'`,
		`localStorage.removeItem('theme')`,
		`data-theme-set`,
	}
	wantAbsent := []string{
		`htmx`,
		`legacy.js`,
		`dashboard.js`,
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
//   - #66 (last-edited restore): the pct <input> carries a data-on:change
//     handler that updates data-orig-pct on every commit, so off→on
//     restores the user's last edit rather than the server-render value.
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

	// wantPctChangeExpr is the static literal data-on:change on the pct
	// <input> itself. No single-quote escaping needed (the expression
	// uses none); pinned as a literal substring.
	const wantPctChangeExpr = `data-on:change="evt.target.dataset.origPct = evt.target.value"`

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
			if !strings.Contains(got, wantPctChangeExpr) {
				t.Errorf("pct input missing data-on:change literal (issue #66 last-edited restore regression)\nwant: %s\n--- got ---\n%s", wantPctChangeExpr, got)
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

// TestInfoDetails_ActiveFault pins the rendering of a device with an
// active fault (FaultLevel != "none") and the soiled-filter / fault path
// that flips NeedsAttention. The golden snapshot_schedule_alert fixture
// happens to set both, but a focused contract is more useful when the
// fault path itself changes — failures point at the right thing instead
// of "golden mismatch."
//
// Behaviors pinned (catalog B-04):
//   - The faults kvRow shows the FaultLevel string verbatim.
//   - The reset-faults action button is wired to /ui/devices/<name>/reset-faults.
//   - When NeedsAttention is true, the InfoDetails block carries the
//     "alert" class (CSS uses .device-info.alert > summary > h2 to
//     tint the device name red).
//   - When NeedsAttention is false, the "alert" class is absent.
func TestInfoDetails_ActiveFault(t *testing.T) {
	cases := []struct {
		name           string
		faultLevel     string
		needsAttention bool
		wantAlertClass bool
	}{
		{"alarm + needs attention", "alarm", true, true},
		{"warning + needs attention", "warning", true, true},
		{"none + clear", "none", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := ui.DeviceView{
				Name:           "alpha",
				FaultLevel:     c.faultLevel,
				NeedsAttention: c.needsAttention,
				FilterStatus:   "clean",
			}
			var sb strings.Builder
			if err := InfoDetails(v).Render(context.Background(), &sb); err != nil {
				t.Fatal(err)
			}
			got := sb.String()

			// faults kvRow shows the level verbatim. kvRowWithAction
			// emits a space between the key and value spans (templ
			// inserts whitespace between sibling lines in the .templ
			// source), so we match the space-separated form.
			wantRow := `<span class="k">faults</span> <span>` + c.faultLevel + `</span>`
			if !strings.Contains(got, wantRow) {
				t.Errorf("missing faults row %q\n%s", wantRow, got)
			}

			// reset-faults button wired to the right endpoint.
			wantPost := `/ui/devices/alpha/reset-faults`
			if !strings.Contains(got, wantPost) {
				t.Errorf("missing reset-faults POST URL %q\n%s", wantPost, got)
			}

			// alert class presence tracks NeedsAttention.
			hasAlert := strings.Contains(got, ` class="device-info alert"`)
			if hasAlert != c.wantAlertClass {
				t.Errorf("alert class presence = %v, want %v\n%s", hasAlert, c.wantAlertClass, got)
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

// TestRenderBlocks_DetailsOpenBinding pins that every collapsible
// <details> block in the card pairs `data-attr:open="$detailsOpen.<key>"`
// on the <details> with `data-on:click="$detailsOpen.<key> = !$detailsOpen.<key>"`
// on its <summary>. The pair makes user-toggled and signal-driven open state
// consistent: the click flips the signal, the browser also toggles the open
// attribute, and data-attr:open re-applies the (now-matching) signal so
// nothing reverts. A `data-on:toggle` writeback would not work because
// datastar's MutationObserver-driven re-evaluation runs before the toggle
// event fires (see #118).
//
// Catalog B-28 (#36) — regressed once during the htmx → datastar
// migration; pinning here keeps it caught at unit-test speed.
func TestRenderBlocks_DetailsOpenBinding(t *testing.T) {
	v := loadView(t, "snapshot_settling")
	cases := []struct {
		block      string
		wantAttr   string
		wantToggle string
		render     func() (string, error)
	}{
		{
			block:      "info",
			wantAttr:   `data-attr:open="$detailsOpen.info"`,
			wantToggle: `data-on:click="$detailsOpen.info = !$detailsOpen.info"`,
			render: func() (string, error) {
				var sb strings.Builder
				err := InfoDetails(v).Render(context.Background(), &sb)
				return sb.String(), err
			},
		},
		{
			block:      "sensors",
			wantAttr:   `data-attr:open="$detailsOpen.sensors"`,
			wantToggle: `data-on:click="$detailsOpen.sensors = !$detailsOpen.sensors"`,
			render: func() (string, error) {
				var sb strings.Builder
				err := SensorsBlock(v.Name, v.Sensors).Render(context.Background(), &sb)
				return sb.String(), err
			},
		},
		{
			block:      "energy",
			wantAttr:   `data-attr:open="$detailsOpen.energy"`,
			wantToggle: `data-on:click="$detailsOpen.energy = !$detailsOpen.energy"`,
			render: func() (string, error) {
				var sb strings.Builder
				err := EnergyBlock(v.Name, v.Energy).Render(context.Background(), &sb)
				return sb.String(), err
			},
		},
		{
			block:      "schedule",
			wantAttr:   `data-attr:open="$detailsOpen.schedule"`,
			wantToggle: `data-on:click="$detailsOpen.schedule = !$detailsOpen.schedule"`,
			render: func() (string, error) {
				var sb strings.Builder
				err := ScheduleBlock(v.Name, v.Schedule, v.Stale).Render(context.Background(), &sb)
				return sb.String(), err
			},
		},
	}
	for _, c := range cases {
		t.Run(c.block, func(t *testing.T) {
			got, err := c.render()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(got, c.wantAttr) {
				t.Errorf("%s block missing %q\n%s", c.block, c.wantAttr, got)
			}
			if !strings.Contains(got, c.wantToggle) {
				t.Errorf("%s block missing %q\n%s", c.block, c.wantToggle, got)
			}
		})
	}
}

// TestRenderSensorsBlock_FormattedValues pins that SensorsBlock formats
// HumidityPct as `<n>%` and CO2PPM as `<n> ppm` for a fixture view. The
// equivalent Playwright assertion previously lived in dashboard.spec.ts
// ("sensor block surfaces live values") but that test was a static-render
// check dressed up as an integration test — moved here per #64. The
// live-from-daemon path is still covered by the @smoke test and by the
// SSE-push tests.
// TestRenderUnreachableCard pins the placeholder card rendered when a
// device is configured but has never polled successfully. It must not
// emit any of the four collapsible block markers (info / energy /
// sensors / schedule) or the controls block — the unreachable card is
// header + IP/serial + a warn line, nothing more.
func TestRenderUnreachableCard(t *testing.T) {
	v := ui.DeviceView{
		Name:        "office",
		IP:          "192.168.1.42:4000",
		Serial:      "BREEZY9876",
		Unreachable: true,
	}
	var sb strings.Builder
	if err := DeviceCard(v).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	wantContains := []string{
		`class="card unreachable"`,
		`data-device="office"`,
		`192.168.1.42:4000`, // IP rendered in kvRow
		`BREEZY9876`,        // serial rendered in kvRow
		`unreachable`,       // badge text
	}
	wantAbsent := []string{
		`data-block="info"`,
		`data-block="energy"`,
		`data-block="sensors"`,
		`data-block="schedule"`,
		`data-block="controls"`,
		`<details`, // no collapsible blocks at all
	}
	for _, s := range wantContains {
		if !strings.Contains(got, s) {
			t.Errorf("missing %q in unreachable card render", s)
		}
	}
	for _, s := range wantAbsent {
		if strings.Contains(got, s) {
			t.Errorf("unexpected %q in unreachable card render", s)
		}
	}
}

// TestRenderControlsBlock_StaleDisablesEveryControl pins that when
// v.Stale is true, every interactive control (button + input) in the
// controls block carries the disabled attribute. A regression here lets
// the user fire actions against a stale device, generating spurious
// 502s. We compare against the Stale=false baseline to avoid coupling
// to the exact button count.
func TestRenderControlsBlock_StaleDisablesEveryControl(t *testing.T) {
	// Construct a view with every conditional branch in ControlsBlock
	// active: speed_mode=manual + special_mode=off renders the MODE
	// chips and manual slider.
	mk := func(stale bool) ui.DeviceView {
		return ui.DeviceView{
			Name:        "alpha",
			SpeedMode:   "manual",
			SpecialMode: "off",
			AirflowMode: "regeneration",
			ManualPct:   30,
			Preset1:     ui.PresetView{Supply: 30, Extract: 30},
			Preset2:     ui.PresetView{Supply: 50, Extract: 50},
			Preset3:     ui.PresetView{Supply: 70, Extract: 70},
			Stale:       stale,
		}
	}

	render := func(v ui.DeviceView) string {
		var sb strings.Builder
		if err := ControlsBlock(v).Render(context.Background(), &sb); err != nil {
			t.Fatal(err)
		}
		return sb.String()
	}

	stale := render(mk(true))

	// Walk every <button> open-tag and assert it carries `disabled`.
	// findOpenTags returns the slice of substrings between each `<TAG`
	// and the matching `>`, i.e. the literal attribute soup the browser
	// will parse.
	openTags := func(html, tag string) []string {
		needle := "<" + tag
		var out []string
		idx := 0
		for {
			start := strings.Index(html[idx:], needle)
			if start < 0 {
				break
			}
			start += idx
			end := strings.Index(html[start:], ">")
			if end < 0 {
				break
			}
			end += start
			out = append(out, html[start:end+1])
			idx = end + 1
		}
		return out
	}

	buttonTags := openTags(stale, "button")
	if len(buttonTags) < 5 {
		t.Fatalf("test view should produce ≥5 buttons (preset×3 + mode×4 + timer×2 + heater + manual + ...); got %d", len(buttonTags))
	}
	for i, tag := range buttonTags {
		if !strings.Contains(tag, "disabled") {
			t.Errorf("stale button #%d missing disabled: %s", i, tag)
		}
	}

	// And the healthy render must not emit disabled at all.
	healthy := render(mk(false))
	if strings.Contains(healthy, " disabled") {
		t.Errorf("healthy controls block must not emit disabled; got: %s", healthy)
	}
}

func TestRenderSensorsBlock_FormattedValues(t *testing.T) {
	s := ui.SensorsView{HumidityPct: 54, CO2PPM: 1175}
	var sb strings.Builder
	if err := SensorsBlock("alpha", s).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	for _, want := range []string{"54%", "1175 ppm"} {
		if !strings.Contains(got, want) {
			t.Errorf("sensors render missing %q\n%s", want, got)
		}
	}
}

// TestRenderControls_NoColonFormDataBind pins the controls block against
// the lowercase trap of colon-keyed `data-bind:<camelCaseKey>`: HTML
// attribute names are lowercased by the parser, so `data-bind:manualPct`
// is parsed as `data-bind:manualpct` — a key that doesn't auto-camelCase
// (no hyphens to flip) — and datastar autocreates a separate
// `$manualpct` signal. Expressions that read the camelCase signal see
// the stale initial seed. See #116.
//
// The boolean checkboxes (automode, matchSpeeds) use value-form
// `data-bind="<key>"`, which preserves casing in the value position.
// The manual slider doesn't bind to a signal at all — its @post reads
// evt.target.valueAsNumber directly, sidestepping the value-form
// "signal-wins overrides server-rendered value" issue when a poll
// re-renders the card with a fresh value attribute.
func TestRenderControls_NoColonFormDataBind(t *testing.T) {
	v := loadView(t, "snapshot_manual")
	var sb strings.Builder
	if err := ControlsBlock(v).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	wantContains := []string{
		// Booleans use value-form data-bind.
		`data-bind="automode"`,
		`data-bind="matchSpeeds"`,
		// Manual slider reads input value directly via evt.target — no bind.
		`{manual: evt.target.valueAsNumber}`,
	}
	wantAbsent := []string{
		// Colon-form for any camelCase signal silently lowercases the key.
		`data-bind:manualPct`,
		`data-bind:matchSpeeds`,
		`data-bind:automode`,
		// Manual slider must not bind to $manualPct — that would re-introduce
		// the signal-wins clobber on outer re-renders.
		`data-bind="manualPct"`,
		`{manual: $manualPct}`,
	}
	for _, s := range wantContains {
		if !strings.Contains(got, s) {
			t.Errorf("controls render missing %q", s)
		}
	}
	for _, s := range wantAbsent {
		if strings.Contains(got, s) {
			t.Errorf("controls render has forbidden form %q", s)
		}
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
	// Edit variant must force <details ... open>: collapsing the editor
	// after a click would dump the user's typed input back to a closed
	// state on the next render.
	if !strings.Contains(got, `<details class="block schedule" data-block="schedule" data-edit="true" open>`) {
		t.Errorf("ScheduleBlockEdit must force-open details; got=%q", got)
	}
}

// TestRenderScheduleBlock_AlertWarnFooter pins that the read variant
// renders a `<div class="warn">` footer when the schedule's last fire
// failed (Alert=true plus a LastApply with the error). Without it,
// daemon-reported scheduler failures would be invisible to the user.
func TestRenderScheduleBlock_AlertWarnFooter(t *testing.T) {
	var sb strings.Builder
	sv := ui.ScheduleView{
		Present:   true,
		Enabled:   true,
		Alert:     true,
		LastApply: &ui.LastApplyView{At: "08:00", Err: "device_unreachable"},
	}
	if err := ScheduleBlock("alpha", sv, false).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	if !strings.Contains(got, `class="warn"`) {
		t.Errorf("ScheduleBlock missing warn footer when Alert=true; got=%q", got)
	}
	if !strings.Contains(got, "device_unreachable") {
		t.Errorf("ScheduleBlock warn footer must include LastApply.Err; got=%q", got)
	}

	// Negative: with Alert=false the warn footer must not render.
	var sb2 strings.Builder
	sv2 := ui.ScheduleView{Present: true, Enabled: true, Alert: false}
	if err := ScheduleBlock("alpha", sv2, false).Render(context.Background(), &sb2); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sb2.String(), `class="warn"`) {
		t.Errorf("ScheduleBlock must not render warn footer when Alert=false; got=%q", sb2.String())
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
	// Cancel button (`✕`) must point at the read URL (no /edit suffix)
	// so clicking it swaps the edit cell back to its read variant. A
	// typo that targets the edit URL again would leave the user stuck
	// in the editor with no way out except form submit. templ escapes
	// the single quotes to &#39; in dynamic-attribute output.
	if !strings.Contains(got, `@get(&#39;/ui/devices/alpha/threshold/co2&#39;)`) {
		t.Errorf("SensorThresholdEdit cancel button must @get the read URL; got=%q", got)
	}
	// Negative: must not contain a cancel pointing at the edit URL.
	if strings.Contains(got, `@get(&#39;/ui/devices/alpha/threshold/co2/edit&#39;)`) {
		t.Errorf("SensorThresholdEdit must not @get the /edit URL on cancel; got=%q", got)
	}
}

// TestPresetChipExpr pins the strict-equality toggle on the per-card
// $editor signal: clicking chip n flips $editor between 0 and n.
// Strict equality (===) matters because $editor is the same JS engine
// across stringified-vs-numeric edge cases — `==` would coerce and
// match unexpectedly when the seed type drifted (see G-web-8 and the
// preset numeric-typing test).
func TestPresetChipExpr(t *testing.T) {
	got := presetChipExpr("alpha", 2)
	want := "$editor = $editor === 2 ? 0 : 2; @post('/ui/devices/alpha/speed', {payload: {preset: 2}})"
	if got != want {
		t.Errorf("presetChipExpr(alpha, 2):\n  got: %s\n want: %s", got, want)
	}
	// Negative: must use strict equality, not loose.
	if strings.Contains(got, "$editor == 2") {
		t.Errorf("presetChipExpr must use strict equality (===), got: %s", got)
	}
}

// TestPresetSliderExpr pins the full data-on:change expression for one
// (name, n, side) tuple. Locks the four implied-mode branches without
// a browser:
//
//   - $automode → 'ventilation'
//   - both ≥ 10 → 'regeneration'
//   - sup=0, ext≥10 → 'extract'
//   - sup≥10, ext=0 → 'supply'
//
// Plus the 1..9 → 0 snap, the matchSpeeds mirror, and the per-preset
// scoping ($preset[n].{supply,extract} reads).
func TestPresetSliderExpr_SupplyN2(t *testing.T) {
	got := presetSliderExpr("alpha", 2, "supply")
	want := "let raw = parseInt(evt.target.value, 10); " +
		"let v = (raw > 0 && raw < 10) ? 0 : raw; " +
		"$preset[2].supply = v; if ($matchSpeeds) $preset[2].extract = v; " +
		"let sup = $preset[2].supply, ext = $preset[2].extract; " +
		"if (sup >= 10 && ext >= 10) @post('/ui/devices/alpha/preset', {payload: {preset: 2, supply: sup, extract: ext}}); " +
		"let implied = null; " +
		"if ($automode) implied = 'ventilation'; " +
		"else if (sup >= 10 && ext >= 10) implied = 'regeneration'; " +
		"else if (sup === 0 && ext >= 10) implied = 'extract'; " +
		"else if (sup >= 10 && ext === 0) implied = 'supply'; " +
		"if (implied && $speedMode === 'preset2' && $airflowMode !== implied) " +
		"@post('/ui/devices/alpha/mode', {payload: {mode: implied}});"
	if got != want {
		t.Errorf("presetSliderExpr(alpha, 2, supply):\n  got: %s\n want: %s", got, want)
	}
}

// TestPresetSliderExpr_ExtractMirrorsCorrectly pins that the extract
// side mirrors to supply (not the other way around). Without this,
// dragging the extract slider with $matchSpeeds=true would silently
// fail to update the supply side.
func TestPresetSliderExpr_ExtractMirrorsCorrectly(t *testing.T) {
	got := presetSliderExpr("alpha", 1, "extract")
	if !strings.Contains(got, "$preset[1].extract = v;") {
		t.Errorf("extract slider must self-update extract; got: %s", got)
	}
	if !strings.Contains(got, "if ($matchSpeeds) $preset[1].supply = v;") {
		t.Errorf("extract slider must mirror to supply when matchSpeeds; got: %s", got)
	}
}

// TestInitialCardSignals_StaticFlagsAndDetailsOpen pins the static UI
// flags and detailsOpen defaults baked into every card's data-signals
// seed. The runtime fields (stale / speedMode / etc.) are covered by
// TestCardSignalsFor_JSON below; this pins the static half.
func TestInitialCardSignals_StaticFlagsAndDetailsOpen(t *testing.T) {
	v := ui.DeviceView{
		// Doesn't matter: only static fields under test.
		Name: "alpha",
	}
	raw := initialCardSignals(v)
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v\nseed: %s", err, raw)
	}

	if got["automode"] != false {
		t.Errorf("automode: want false, got %v", got["automode"])
	}
	if got["matchSpeeds"] != true {
		t.Errorf("matchSpeeds: want true, got %v", got["matchSpeeds"])
	}
	// json.Unmarshal lands ints as float64.
	if got["editor"] != float64(0) {
		t.Errorf("editor: want 0, got %v", got["editor"])
	}

	details, ok := got["detailsOpen"].(map[string]any)
	if !ok {
		t.Fatalf("detailsOpen: want object, got %T", got["detailsOpen"])
	}
	wantOpen := map[string]bool{
		"info":     false,
		"sensors":  true, // intentional: sensors block defaults open
		"energy":   false,
		"schedule": false,
	}
	for k, w := range wantOpen {
		if details[k] != w {
			t.Errorf("detailsOpen.%s: want %v, got %v", k, w, details[k])
		}
	}
}

// TestInitialCardSignals_PresetSeedTypedAsNumber pins that preset.<n>.{
// supply,extract} is seeded as a JSON number, not a string. If the seed
// ever drifted to strings, datastar's data-bind on the preset sliders
// would silently flip type; mid-drag reseeds would clobber the user's
// in-progress value with a string. Spec calls this out explicitly.
func TestInitialCardSignals_PresetSeedTypedAsNumber(t *testing.T) {
	v := ui.DeviceView{
		Preset1: ui.PresetView{Supply: 30, Extract: 40},
		Preset2: ui.PresetView{Supply: 50, Extract: 60},
		Preset3: ui.PresetView{Supply: -1, Extract: 70}, // -1 sentinel maps to 50
	}
	raw := initialCardSignals(v)
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("unmarshal: %v\nseed: %s", err, raw)
	}
	preset, ok := got["preset"].(map[string]any)
	if !ok {
		t.Fatalf("preset: want object, got %T", got["preset"])
	}

	for _, n := range []string{"1", "2", "3"} {
		entry, ok := preset[n].(map[string]any)
		if !ok {
			t.Errorf("preset.%s: want object, got %T", n, preset[n])
			continue
		}
		for _, side := range []string{"supply", "extract"} {
			v := entry[side]
			if _, ok := v.(float64); !ok {
				t.Errorf("preset.%s.%s: want JSON number, got %T (%v)", n, side, v, v)
			}
		}
	}

	// Spot-check: -1 sentinel mapped to 50.
	if preset["3"].(map[string]any)["supply"] != float64(50) {
		t.Errorf("preset.3.supply: want 50 (sentinel-mapped), got %v", preset["3"].(map[string]any)["supply"])
	}
	// Spot-check: real value passed through.
	if preset["1"].(map[string]any)["supply"] != float64(30) {
		t.Errorf("preset.1.supply: want 30, got %v", preset["1"].(map[string]any)["supply"])
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
