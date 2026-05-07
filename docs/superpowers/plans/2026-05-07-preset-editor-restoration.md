# Preset Editor Restoration + Cookie-Driven UI State Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore the inline SPEED preset editor lost in the htmx migration (#53), apply #46's automode-default-off + uncheck-fires-regen behaviours, and replace the JS-shim re-apply pattern (#49) with a cookie-driven server render so `<details>` open state and the new preset-editor state stop flickering on every poll.

**Architecture:** A new `internal/uistate` package owns a `breezy-ui` JSON cookie that carries `<details>` open state and per-device preset-editor state (open preset, automode, match-speeds). The server reads the cookie on every `/`, `/ui/devices`, and `/ui/devices/{name}/...` render and emits the right `open`/`hidden`/`checked` markup directly. Client JS writes the cookie on every interaction; htmx swaps then render with no reapply pass.

**Tech Stack:** Go 1.23, templ (codegen), htmx 2.0.4, Playwright. Same surfaces as the rest of the dashboard.

**Spec:** `docs/superpowers/specs/2026-05-07-preset-editor-restoration-design.md`.

---

## File Structure

**New files:**
- `internal/uistate/state.go` — cookie type, `Parse(r)`, `DefaultsForDevice(name)`, `Cookie(state)`.
- `internal/uistate/state_test.go` — round-trip, malformed/missing/oversize cookie tolerance.

**Modified files:**
- `cmd/breezyd/ui/view.go` — extend `DeviceView` with `DetailsOpen`, `EditingPreset`, `Automode`, `MatchSpeeds`. Drop `SensorsView.Expanded` (superseded).
- `cmd/breezyd/ui_view.go` — drop the `s.Expanded = true` line in `sensorsViewFrom`. The default-open rule moves into `buildView`.
- `cmd/breezyd/handlers_ui_read.go` — `buildView` reads the cookie via `uistate.Parse(r)`, populates `DetailsOpen`/`EditingPreset`/`Automode`/`MatchSpeeds` per the rules in the spec. `viewFor`/`collectViews` accept the request so they can pass state through.
- `cmd/breezyd/handlers_ui_write.go` — every `uiRenderCard`/`uiValidationError` call already runs through `viewFor`, so they pick up cookie state for free once `viewFor` reads the request. New `postUIPreset` handler.
- `cmd/breezyd/server.go` — register `POST /ui/devices/{name}/preset → h.postUIPreset`.
- `cmd/breezyd/ui/templates/device_card.templ` — `<details>` open conditions read `v.DetailsOpen[…]`; card root gains `data-speed-mode` / `data-airflow-mode`.
- `cmd/breezyd/ui/templates/sensors_block.templ` — replace `if s.Expanded { open }` with `v.DetailsOpen` lookup (signature change: pass full DeviceView or pre-computed bool).
- `cmd/breezyd/ui/templates/energy_block.templ` — same.
- `cmd/breezyd/ui/templates/schedule_block.templ` — same; `s.Alert` keeps its force-open role.
- `cmd/breezyd/ui/templates/controls_block.templ` — emit three `<div class="preset-editor" data-preset-editor="N">` panels with `hidden` driven by `v.EditingPreset`, automode and match-speeds checkboxes driven by view fields, sliders sourced from `v.PresetN`.
- `cmd/breezyd/ui/templates/layout.templ` — delete the `htmx:afterSettle` re-apply pass; add cookie helper + delegated click/change handlers + `htmx:configRequest` slider hook + post-slider implied-mode write.
- `cmd/breezyd/ui/templates/render_test.go` + `testdata/` — extend goldens for cookie scenarios.
- `tests/ui/dashboard.spec.ts` — un-`fixme` listed tests, rewrite two, add new no-flicker + #46 tests.
- `tests/ui/screenshots/` — regenerated `dashboard-3col.png`.

---

## Task 1: `internal/uistate` package

**Goal:** Cookie type + parser + encoder, with defensive parsing (malformed/oversize cookies fall through to the zero state) and unit tests.

**Files:**
- Create: `internal/uistate/state.go`
- Create: `internal/uistate/state_test.go`

**Acceptance Criteria:**
- [ ] `Parse(r *http.Request) State` reads cookie `breezy-ui`, URL-decodes, JSON-decodes, returns zero `State` on any error.
- [ ] `Cookie(s State) *http.Cookie` returns a `*http.Cookie` ready for `http.SetCookie`. (Used by tests to round-trip; not required by handlers since the JS owns writes.)
- [ ] Cookie value above 4096 bytes returns zero `State` (defensive: untrusted browser input).
- [ ] `DefaultsForDevice(name string) PresetState` returns `{Open: 0, Automode: false, Match: true}`.
- [ ] All public funcs have doc comments.

**Verify:** `go test ./internal/uistate/...` → all pass.

**Steps:**

- [ ] **Step 1: Create `internal/uistate/state.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// Package uistate carries dashboard UI state via the breezy-ui cookie.
//
// The browser owns the writes (in layout.templ's inline JS); the server
// reads the cookie on every render and emits the right <details open>
// and <div hidden> markup directly. This keeps server output authoritative
// and avoids the JS-restore flicker pattern from #49.
package uistate

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// CookieName is the name of the cookie carrying UI state.
const CookieName = "breezy-ui"

// maxCookieBytes is the largest cookie value we trust before falling back
// to defaults. Real cookies for the supported device count (~50) sit well
// under 1 KB; anything above 4 KB is either tampered or pathological.
const maxCookieBytes = 4096

// State is the parsed contents of the breezy-ui cookie.
type State struct {
	// Details maps a <details id> (e.g. "info-bedroom", "sensors-bedroom")
	// to its user-toggled open state. Absence means "use the section's
	// default plus force-open rules"; the server applies those.
	Details map[string]bool `json:"details,omitempty"`

	// Preset maps a device name to its preset-editor UI state.
	Preset map[string]PresetState `json:"preset,omitempty"`
}

// PresetState is the per-device preset-editor UI state.
type PresetState struct {
	// Open is which numbered preset's editor panel is visible.
	// 0 means closed; 1, 2, or 3 means that preset's editor is shown.
	Open int `json:"open,omitempty"`

	// Automode is the editor's automode-checkbox state. Default false.
	Automode bool `json:"automode,omitempty"`

	// Match is the editor's match-speeds-checkbox state. Default true,
	// stored explicitly so the cookie survives a flag flip in code.
	Match bool `json:"match,omitempty"`
}

// DefaultsForDevice returns the documented per-device defaults for a
// device with no cookie entry: editor closed, automode off, match-speeds on.
func DefaultsForDevice(name string) PresetState {
	return PresetState{Open: 0, Automode: false, Match: true}
}

// Parse reads the breezy-ui cookie from r and returns its parsed contents.
// On any error (missing, malformed, oversize) returns the zero State —
// callers apply defaults from there. Parse never returns an error: the
// philosophy is that bad UI-state cookies must never produce a 5xx and
// never partially apply.
func Parse(r *http.Request) State {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return State{}
	}
	if len(c.Value) > maxCookieBytes {
		return State{}
	}
	raw, err := url.QueryUnescape(c.Value)
	if err != nil {
		return State{}
	}
	var s State
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return State{}
	}
	return s
}

// Cookie returns an *http.Cookie for s, primarily for tests and any
// future server-initiated write path. The JS in layout.templ owns the
// production writes via document.cookie.
func Cookie(s State) *http.Cookie {
	b, _ := json.Marshal(s) // State is a plain struct; Marshal cannot fail.
	return &http.Cookie{
		Name:     CookieName,
		Value:    url.QueryEscape(string(b)),
		Path:     "/",
		MaxAge:   31536000, // 1 year
		SameSite: http.SameSiteLaxMode,
	}
}
```

- [ ] **Step 2: Create `internal/uistate/state_test.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package uistate

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParse_MissingCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	got := Parse(r)
	if got.Details != nil || got.Preset != nil {
		t.Fatalf("missing cookie should yield zero State, got %+v", got)
	}
}

func TestParse_RoundTrip(t *testing.T) {
	want := State{
		Details: map[string]bool{
			"info-bedroom":    true,
			"sensors-bedroom": false,
		},
		Preset: map[string]PresetState{
			"bedroom": {Open: 2, Automode: false, Match: true},
		},
	}
	c := Cookie(want)

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(c)
	got := Parse(r)

	if got.Details["info-bedroom"] != true {
		t.Errorf("info-bedroom: got %v, want true", got.Details["info-bedroom"])
	}
	if got.Details["sensors-bedroom"] != false {
		t.Errorf("sensors-bedroom: got %v, want false (explicit)", got.Details["sensors-bedroom"])
	}
	if _, ok := got.Details["sensors-bedroom"]; !ok {
		t.Errorf("sensors-bedroom: should be present in map even when false")
	}
	if got.Preset["bedroom"].Open != 2 {
		t.Errorf("preset.bedroom.Open: got %d, want 2", got.Preset["bedroom"].Open)
	}
	if got.Preset["bedroom"].Match != true {
		t.Errorf("preset.bedroom.Match: got %v, want true", got.Preset["bedroom"].Match)
	}
}

func TestParse_MalformedJSON(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "%7Bnot-json"})
	got := Parse(r)
	if got.Details != nil || got.Preset != nil {
		t.Fatalf("malformed cookie should yield zero State, got %+v", got)
	}
}

func TestParse_BadURLEncoding(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: "%ZZ"})
	got := Parse(r)
	if got.Details != nil || got.Preset != nil {
		t.Fatalf("bad URL-encoding should yield zero State, got %+v", got)
	}
}

func TestParse_OversizeCookie(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: CookieName, Value: strings.Repeat("a", maxCookieBytes+1)})
	got := Parse(r)
	if got.Details != nil || got.Preset != nil {
		t.Fatalf("oversize cookie should yield zero State, got %+v", got)
	}
}

func TestDefaultsForDevice(t *testing.T) {
	d := DefaultsForDevice("anything")
	if d.Open != 0 || d.Automode != false || d.Match != true {
		t.Errorf("defaults: got %+v, want {Open:0 Automode:false Match:true}", d)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./internal/uistate/...`
Expected: all pass, no warnings.

- [ ] **Step 4: Commit**

```bash
git add internal/uistate/
git commit -m "uistate: cookie type + parser for dashboard UI state (#53)"
```

---

## Task 2: Extend DeviceView and wire cookie into buildView

**Goal:** Add new `DeviceView` fields, drop superseded `SensorsView.Expanded`, populate the new fields from the parsed cookie inside `buildView`. Templates and tests are NOT yet updated — that happens in Tasks 3 & 4 (this task is the data-plumbing wedge).

**Files:**
- Modify: `cmd/breezyd/ui/view.go` — DeviceView additions, SensorsView.Expanded removal.
- Modify: `cmd/breezyd/ui_view.go` — `sensorsViewFrom` no longer sets `Expanded`.
- Modify: `cmd/breezyd/handlers_ui_read.go` — `buildView`/`viewFor`/`collectViews` accept `*http.Request`, parse cookie via `uistate.Parse`, populate new fields with cookie + force-open rules.
- Modify: `cmd/breezyd/handlers_ui_write.go` — call sites for `viewFor` updated to pass `r`.
- Test: `cmd/breezyd/handlers_ui_read_test.go` — add unit tests covering cookie + force-open precedence in `buildView`.

**Acceptance Criteria:**
- [ ] `DeviceView` has `DetailsOpen map[string]bool`, `EditingPreset int`, `Automode bool`, `MatchSpeeds bool`.
- [ ] `SensorsView.Expanded` removed; `sensorsViewFrom` no longer references it.
- [ ] `buildView` populates `DetailsOpen` keys for `"info"`, `"sensors"`, `"energy"`, `"schedule"` per the spec's defaults table, with cookie overriding defaults but `NeedsAttention` and `Sensors.AlertActive` and `Schedule.Alert` forcing open regardless of cookie.
- [ ] `EditingPreset`/`Automode`/`MatchSpeeds` populated from cookie's per-device entry; `Match` defaults to `true` when the device entry is absent.
- [ ] Compile clean; templates still build (they read no new fields yet).
- [ ] Unit tests cover: cookie says open + no force → open; cookie says closed + force-open → open; missing cookie → defaults from table; missing device entry → preset defaults.

**Verify:** `just generate && go test ./cmd/breezyd/... ./internal/uistate/... ./cmd/breezyd/ui/...` → all pass.

**Steps:**

- [ ] **Step 1: Update `cmd/breezyd/ui/view.go`**

Replace the SensorsView.Expanded block (currently lines 65-69) with:

```go
// SensorsView carries all sensor readings plus threshold/alert state.
type SensorsView struct {
	AlertActive bool // any of humidity/CO2/VOC alerting

	HumidityPct int
	CO2PPM      int
	VOCPPM      int // VOC index (Sensirion 0-500)
	// ... rest unchanged ...
```

(Drop the `Expanded bool` field and its doc comment. The default-open
rule moves into `buildView`.)

Add to `DeviceView` (after the existing `Sensors` / `Energy` / `Schedule`
fields):

```go
	// DetailsOpen carries the cookie-derived + force-open <details>
	// state, keyed by section name: "info", "sensors", "energy",
	// "schedule". Templates read this to decide the `open` attribute.
	DetailsOpen map[string]bool

	// EditingPreset is which preset-editor panel is visible (1, 2, 3)
	// or 0 when no editor is open. Server-rendered from the cookie;
	// the JS in layout.templ writes the cookie on chip click.
	EditingPreset int

	// Automode is the editor's automode-checkbox state. Default false.
	Automode bool

	// MatchSpeeds is the editor's match-speeds-checkbox state. Default true.
	MatchSpeeds bool
```

- [ ] **Step 2: Update `cmd/breezyd/ui_view.go::sensorsViewFrom`**

Find the line `s.Expanded = true` (around line 209) and the surrounding
comment, and delete the line.

- [ ] **Step 3: Update `cmd/breezyd/handlers_ui_read.go` — buildView/viewFor/collectViews threading**

Change signatures to accept `*http.Request`. Replace the existing
`buildView`/`viewFor`/`collectViews` with:

```go
func (h *Handler) collectViews(r *http.Request) []ui.DeviceView {
	if h.Devices == nil {
		return nil
	}
	state := uistate.Parse(r)
	registry := h.Devices.Snapshot()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)

	views := make([]ui.DeviceView, 0, len(names))
	for _, name := range names {
		if h.State != nil {
			if snap, ok := h.State.Get(name); ok && len(snap.Values) > 0 {
				views = append(views, h.buildView(name, snap, state))
				continue
			}
		}
		views = append(views, h.unreachableView(name, registry[name]))
	}
	return views
}

func (h *Handler) viewFor(r *http.Request, name string) (ui.DeviceView, bool) {
	if h.State == nil {
		return ui.DeviceView{}, false
	}
	snap, ok := h.State.Get(name)
	if !ok {
		return ui.DeviceView{}, false
	}
	return h.buildView(name, snap, uistate.Parse(r)), true
}

func (h *Handler) buildView(name string, snap Snapshot, state uistate.State) ui.DeviceView {
	v := snapshotToView(name, snap)

	if h.Devices != nil {
		if cfg, ok := h.Devices.Get(name); ok {
			v.Serial = cfg.ID
		}
	}
	if h.Pollers != nil {
		if p, ok := h.Pollers[name]; ok && p != nil && p.Energy != nil {
			ev := p.Energy.Snapshot()
			v.Energy = energyViewFrom(ev)
		}
	}
	if h.Schedulers != nil {
		if sch, ok := h.Schedulers[name]; ok && sch != nil {
			v.Schedule = scheduleViewFrom(sch.Snapshot())
		}
	}

	v.DetailsOpen = computeDetailsOpen(name, v, state)
	if ps, ok := state.Preset[name]; ok {
		v.EditingPreset = ps.Open
		v.Automode = ps.Automode
		v.MatchSpeeds = ps.Match
	} else {
		def := uistate.DefaultsForDevice(name)
		v.EditingPreset = def.Open
		v.Automode = def.Automode
		v.MatchSpeeds = def.Match
	}
	return v
}

// computeDetailsOpen returns the per-section open state for a device,
// applying cookie state, force-open rules, and section defaults in that
// order. Force-open always wins (NeedsAttention for info; AlertActive
// for sensors; Schedule.Alert for schedule).
func computeDetailsOpen(name string, v ui.DeviceView, state uistate.State) map[string]bool {
	open := map[string]bool{
		"info":     defaultOpen("info"),
		"sensors":  defaultOpen("sensors"),
		"energy":   defaultOpen("energy"),
		"schedule": defaultOpen("schedule"),
	}
	for section := range open {
		id := section + "-" + name
		if val, ok := state.Details[id]; ok {
			open[section] = val
		}
	}
	if v.NeedsAttention {
		open["info"] = true
	}
	if v.Sensors.AlertActive {
		open["sensors"] = true
	}
	if v.Schedule.Alert {
		open["schedule"] = true
	}
	return open
}

// defaultOpen returns the per-section default when the cookie has no
// entry for that section. See the spec's defaultsBySection table.
func defaultOpen(section string) bool {
	switch section {
	case "sensors":
		return true
	default:
		return false
	}
}
```

Update the imports at the top of `handlers_ui_read.go` to include
`"github.com/hughobrien/breezyd/internal/uistate"`.

Update the two existing `getUIDeviceList` / `getUIDeviceCard` handlers
to pass `r`:

```go
func (h *Handler) getUIDeviceList(w http.ResponseWriter, r *http.Request) {
	views := h.collectViews(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := templates.DeviceList(views).Render(r.Context(), w); err != nil {
		slog.Error("render DeviceList", "err", err)
	}
}

func (h *Handler) getUIDeviceCard(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	view, ok := h.viewFor(r, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := templates.DeviceCard(view).Render(r.Context(), w); err != nil {
		slog.Error("render DeviceCard", "device", name, "err", err)
	}
}
```

- [ ] **Step 4: Update all `viewFor`/`collectViews`/`buildView` call sites in `handlers_ui_write.go`**

Find each `h.viewFor(name)` and change to `h.viewFor(r, name)`. There
are several inside `scheduleReadFrag`, `scheduleEditFrag`,
`uiRenderCard`, `uiValidationError`, `getUIThresholdRead`,
`getUIThresholdEdit`, `putUIThreshold`. Each function already has `r`
in scope. Compile to confirm.

- [ ] **Step 5: Update `cmd/breezyd/main.go` and any other internal callers if present**

Run: `grep -rn 'h.buildView\|h.viewFor\|h.collectViews' cmd/breezyd/`
Update each call site to pass `r`. If a callsite has no `r` available,
fall back to `state := uistate.State{}` and pass an empty state via a
new `buildViewWith` helper. (No such site is expected; if grep finds
none, skip.)

- [ ] **Step 6: Add unit tests in `cmd/breezyd/handlers_ui_read_test.go`**

```go
func TestBuildView_CookieOpensSensors(t *testing.T) {
	h := newTestHandler(t) // existing helper; uses fakedevice
	snap := h.State.MustGet("bedroom")  // existing snapshot fixture

	state := uistate.State{Details: map[string]bool{"sensors-bedroom": true}}
	v := h.buildView("bedroom", snap, state)

	if !v.DetailsOpen["sensors"] {
		t.Errorf("cookie says sensors-bedroom: true; got DetailsOpen[sensors]=false")
	}
}

func TestBuildView_CookieClosedButAlertForcesOpen(t *testing.T) {
	h := newTestHandler(t)
	snap := h.State.MustGet("bedroom-alert") // snapshot with humidity > threshold
	state := uistate.State{Details: map[string]bool{"sensors-bedroom-alert": false}}
	v := h.buildView("bedroom-alert", snap, state)

	if !v.Sensors.AlertActive {
		t.Fatalf("test fixture missing alert; can't verify force-open")
	}
	if !v.DetailsOpen["sensors"] {
		t.Errorf("alert should force sensors open even with cookie=false")
	}
}

func TestBuildView_NoCookiePresetDefaults(t *testing.T) {
	h := newTestHandler(t)
	snap := h.State.MustGet("bedroom")
	v := h.buildView("bedroom", snap, uistate.State{})

	if v.EditingPreset != 0 {
		t.Errorf("EditingPreset: got %d, want 0", v.EditingPreset)
	}
	if v.Automode != false {
		t.Errorf("Automode: got %v, want false (default)", v.Automode)
	}
	if v.MatchSpeeds != true {
		t.Errorf("MatchSpeeds: got %v, want true (default)", v.MatchSpeeds)
	}
}

func TestBuildView_CookiePresetOverrides(t *testing.T) {
	h := newTestHandler(t)
	snap := h.State.MustGet("bedroom")
	state := uistate.State{
		Preset: map[string]uistate.PresetState{
			"bedroom": {Open: 2, Automode: true, Match: false},
		},
	}
	v := h.buildView("bedroom", snap, state)

	if v.EditingPreset != 2 {
		t.Errorf("EditingPreset: got %d, want 2", v.EditingPreset)
	}
	if v.Automode != true {
		t.Errorf("Automode: got %v, want true", v.Automode)
	}
	if v.MatchSpeeds != false {
		t.Errorf("MatchSpeeds: got %v, want false", v.MatchSpeeds)
	}
}
```

If `newTestHandler` / `MustGet` helpers don't exist with these exact
shapes, mirror the patterns already in the file. Adjust fixture device
names to match what's in `cmd/breezyd/handlers_ui_read_test.go`.

- [ ] **Step 7: Run tests**

Run: `just generate && go test ./cmd/breezyd/... ./internal/uistate/...`
Expected: all pass; templates re-build cleanly because the new fields
on `DeviceView` are not yet referenced by any template.

- [ ] **Step 8: Commit**

```bash
git add cmd/breezyd/ internal/uistate/
git commit -m "ui: thread breezy-ui cookie state into DeviceView (#53)"
```

---

## Task 3: Update `<details>` templates + add card data attributes

**Goal:** All four `<details>` blocks read their `open` attribute from `v.DetailsOpen[…]`. Card root gains `data-speed-mode` / `data-airflow-mode`. Render goldens regenerated.

**Files:**
- Modify: `cmd/breezyd/ui/templates/device_card.templ`
- Modify: `cmd/breezyd/ui/templates/sensors_block.templ`
- Modify: `cmd/breezyd/ui/templates/energy_block.templ`
- Modify: `cmd/breezyd/ui/templates/schedule_block.templ`
- Modify: `cmd/breezyd/ui/templates/render_test.go` (or `testdata/`) — update goldens.

**Acceptance Criteria:**
- [ ] `<details id="info-...">` open condition is `v.DetailsOpen["info"]`.
- [ ] `<details id="sensors-...">` open condition is the new `open` parameter passed from `device_card.templ`.
- [ ] `<details id="energy-...">` open condition is the existing `open` param, now sourced from `v.DetailsOpen["energy"]` at the call site.
- [ ] `<details id="schedule-...">` open condition is `v.DetailsOpen["schedule"]`.
- [ ] Card root has `data-speed-mode={ v.SpeedMode }` and `data-airflow-mode={ v.AirflowMode }`.
- [ ] All `golden_*.html` files updated to match new attributes.

**Verify:** `just generate && just test` → all pass (render_test.go's golden comparisons hold).

**Steps:**

- [ ] **Step 1: Update `device_card.templ`**

Around line 13, change:

```go
<div class={ "card", templ.KV("stale", v.Stale) } data-device={ v.Name }>
```

to:

```go
<div class={ "card", templ.KV("stale", v.Stale) }
     data-device={ v.Name }
     data-speed-mode={ v.SpeedMode }
     data-airflow-mode={ v.AirflowMode }>
```

Around line 17, change:

```go
<details id={ "info-" + v.Name } class="device-info" if v.NeedsAttention { open }>
```

to:

```go
<details id={ "info-" + v.Name } class="device-info" if v.DetailsOpen["info"] { open }>
```

(Force-open rule for `NeedsAttention` is already merged into `DetailsOpen` by `computeDetailsOpen`.)

Around lines 46-48, change the block-call lines:

```go
@EnergyBlock(v.Name, v.Energy, false)
@SensorsBlock(v.Name, v.Sensors)
@ScheduleBlock(v.Name, v.Schedule, v.Stale)
```

to:

```go
@EnergyBlock(v.Name, v.Energy, v.DetailsOpen["energy"])
@SensorsBlock(v.Name, v.Sensors, v.DetailsOpen["sensors"])
@ScheduleBlock(v.Name, v.Schedule, v.Stale, v.DetailsOpen["schedule"])
```

- [ ] **Step 2: Update `sensors_block.templ`**

Change the function signature and the open condition:

```go
templ SensorsBlock(name string, s ui.SensorsView, open bool) {
	<details id={ "sensors-" + name } class="block sensors" if open { open }>
```

(Replace `if s.Expanded { open }` with `if open { open }`.)

- [ ] **Step 3: Update `schedule_block.templ`**

Change the signature and open condition:

```go
templ ScheduleBlock(name string, s ui.ScheduleView, stale bool, open bool) {
	if s.Present {
		<details id={ "schedule-" + name } class="block schedule" if open { open }>
```

(Replace `if s.Alert { open }` — `Alert` is already merged into the
caller's `open` value via `computeDetailsOpen`, so the template
becomes a thin reflector.)

- [ ] **Step 4: Update `energy_block.templ`**

The signature already has `open bool` — no change needed beyond the
call-site update in Step 1. Verify by reading the file.

- [ ] **Step 5: Update `handlers_ui_write.go` for `scheduleReadFrag`/`scheduleEditFrag` callers of `ScheduleBlock`**

Around line 39, the existing call is:

```go
templates.ScheduleBlock(name, view.Schedule, view.Stale).Render(...)
```

Update to:

```go
templates.ScheduleBlock(name, view.Schedule, view.Stale, view.DetailsOpen["schedule"]).Render(...)
```

- [ ] **Step 6: Regenerate templ-generated Go**

Run: `just generate`
Expected: `cmd/breezyd/ui/templates/*_templ.go` files updated. Compile clean.

- [ ] **Step 7: Update render goldens**

Run: `go test ./cmd/breezyd/ui/templates/ -run TestRenderGoldens -update`
(If `-update` flag isn't supported, manually inspect each `golden_*.html`
in `cmd/breezyd/ui/templates/testdata/` and add the new
`data-speed-mode="…" data-airflow-mode="…"` attributes to the card div.
The test's failure message will print the diff; copy the actual into
the golden.)

- [ ] **Step 8: Run all tests**

Run: `just check`
Expected: all pass. If render goldens fail, repeat Step 7 inspection.

- [ ] **Step 9: Commit**

```bash
git add cmd/breezyd/ui/templates/ cmd/breezyd/handlers_ui_write.go
git commit -m "ui: <details> open state and card data-attrs read DeviceView (#53)"
```

---

## Task 4: Render the preset editor

**Goal:** `controlsBlock` emits 3 `<div class="preset-editor">` panels per card, hidden by default, with sliders + automode/match-speeds checkboxes wired for htmx slider POSTs.

**Files:**
- Modify: `cmd/breezyd/ui/templates/controls_block.templ`
- Modify: `cmd/breezyd/ui/templates/render_test.go` / `testdata/` — add a golden for editor-open state.

**Acceptance Criteria:**
- [ ] After the SPEED `<div class="seg">` row, three `<div class="preset-editor" data-preset-editor="N" data-name="…">` panels are emitted.
- [ ] Each panel has `hidden` unless `v.EditingPreset == N`.
- [ ] Each panel renders `<input type="checkbox" data-action="automode-toggle">` (checked iff `v.Automode`) and `<input type="checkbox" data-action="match-speeds-toggle">` (checked iff `v.MatchSpeeds`).
- [ ] Each panel renders two `<input type="range">` sliders (`data-action="preset-supply-slider"` and `…-extract-slider`) with `value={v.PresetN.Supply}` / `…Extract`, `min=0`, `max=100`, `step=1`.
- [ ] Sliders have `hx-post="/ui/devices/{name}/preset"`, `hx-trigger="change delay:200ms"`, `hx-target="closest .card"`, `hx-swap="outerHTML"`, `hx-vals='{"preset":N}'`, `hx-include="[data-preset-editor='N'] input[type=range]"`, `hx-disabled-elt="this"`, `disabled` when `v.Stale`.
- [ ] A new render-test golden `golden_editor_open_preset2.html` covers the editor-open case.

**Verify:** `just generate && go test ./cmd/breezyd/ui/templates/...` → all pass.

**Steps:**

- [ ] **Step 1: Edit `cmd/breezyd/ui/templates/controls_block.templ`**

After the existing closing `</div>` of the SPEED `seg` (around current
line 18), and before the manual-only MODE block (line 19), insert:

```go
				@presetEditor(v, 1, v.Preset1)
				@presetEditor(v, 2, v.Preset2)
				@presetEditor(v, 3, v.Preset3)
```

Add the new templ component at the bottom of the file:

```go
templ presetEditor(v ui.DeviceView, n int, p ui.PresetView) {
	<div
		class="preset-editor"
		data-preset-editor={ fmt.Sprintf("%d", n) }
		data-name={ v.Name }
		if v.EditingPreset != n { hidden }
	>
		<label class="match-speeds">
			<input
				type="checkbox"
				data-action="automode-toggle"
				data-name={ v.Name }
				if v.Automode { checked }
				if v.Stale { disabled }
			/>
			automode
		</label>
		<label class="match-speeds">
			<input
				type="checkbox"
				data-action="match-speeds-toggle"
				data-name={ v.Name }
				if v.MatchSpeeds { checked }
				if v.Stale { disabled }
			/>
			match speeds
		</label>
		<div class="slider-row">
			<span class="val-label">supply</span>
			<input
				type="range"
				name="supply"
				min="0"
				max="100"
				step="1"
				value={ presetSliderValue(p.Supply) }
				data-action="preset-supply-slider"
				data-name={ v.Name }
				data-preset={ fmt.Sprintf("%d", n) }
				hx-post={ "/ui/devices/" + v.Name + "/preset" }
				hx-trigger="change delay:200ms"
				hx-target="closest .card"
				hx-swap="outerHTML"
				hx-vals={ fmt.Sprintf(`{"preset":%d}`, n) }
				hx-include={ fmt.Sprintf(`[data-preset-editor="%d"] input[type="range"]`, n) }
				hx-disabled-elt="this"
				if v.Stale { disabled }
			/>
			<span class="val">{ fmt.Sprintf("%d%%", clampPresetDisplay(p.Supply)) }</span>
		</div>
		<div class="slider-row">
			<span class="val-label">exhaust</span>
			<input
				type="range"
				name="extract"
				min="0"
				max="100"
				step="1"
				value={ presetSliderValue(p.Extract) }
				data-action="preset-extract-slider"
				data-name={ v.Name }
				data-preset={ fmt.Sprintf("%d", n) }
				hx-post={ "/ui/devices/" + v.Name + "/preset" }
				hx-trigger="change delay:200ms"
				hx-target="closest .card"
				hx-swap="outerHTML"
				hx-vals={ fmt.Sprintf(`{"preset":%d}`, n) }
				hx-include={ fmt.Sprintf(`[data-preset-editor="%d"] input[type="range"]`, n) }
				hx-disabled-elt="this"
				if v.Stale { disabled }
			/>
			<span class="val">{ fmt.Sprintf("%d%%", clampPresetDisplay(p.Extract)) }</span>
		</div>
	</div>
}

// presetSliderValue returns the slider's `value` attribute, treating the
// "-1 = unknown" sentinel as 50 (mid-range fallback) so the thumb has
// somewhere to sit before the first poll lands.
func presetSliderValue(pct int) string {
	if pct < 0 {
		return "50"
	}
	return fmt.Sprintf("%d", pct)
}

// clampPresetDisplay returns the user-visible %% readout. Negative
// (unknown) renders as a dash via fmt's normal handling — but here we
// always have a numeric label, so just clamp to 0 floor for display.
func clampPresetDisplay(pct int) int {
	if pct < 0 {
		return 0
	}
	return pct
}
```

- [ ] **Step 2: Add a golden for editor-open**

Create `cmd/breezyd/ui/templates/testdata/golden_editor_open_preset2.html`
by extending the test corpus in `render_test.go`:

```go
// Add to the test cases slice in TestRenderGoldens:
{
	name: "editor_open_preset2",
	view: func() ui.DeviceView {
		v := loadGoldenSnapshot(t, "regen") // reuse an existing snapshot fixture
		v.EditingPreset = 2
		v.Automode = false
		v.MatchSpeeds = true
		v.DetailsOpen = map[string]bool{
			"info": false, "sensors": true, "energy": false, "schedule": false,
		}
		return v
	}(),
},
```

If the existing render_test fixture loader yields a `Snapshot`-shaped
input (not a `DeviceView`), add a path that lets the test inject
post-`buildView` state. Mirror the patterns already in the file.

Run the test once with `-update` (or manually copy the actual output)
to populate `golden_editor_open_preset2.html`.

- [ ] **Step 3: Run tests**

Run: `just generate && go test ./cmd/breezyd/ui/templates/`
Expected: all goldens match.

- [ ] **Step 4: Commit**

```bash
git add cmd/breezyd/ui/templates/
git commit -m "ui: render preset-editor panels in controlsBlock (#53)"
```

---

## Task 5: `POST /ui/devices/{name}/preset` shim handler

**Goal:** New htmx-shim endpoint that decodes form-encoded `preset=N&supply=N&extract=N`, calls `breezy.SetPresetSpeed`, re-renders the card. Validation errors render `DeviceCard` with `PostError` set.

**Files:**
- Modify: `cmd/breezyd/handlers_ui_write.go` — new `postUIPreset`.
- Modify: `cmd/breezyd/server.go` — register the route.
- Modify: `cmd/breezyd/handlers_ui_write_test.go` — handler tests (success, validation error, ErrAuth).

**Acceptance Criteria:**
- [ ] `POST /ui/devices/{name}/preset` with valid form returns 200 + DeviceCard fragment.
- [ ] Missing or out-of-range fields return 422 + DeviceCard with PostError.
- [ ] `breezy.ErrAuth` returns 401 + error_banner.
- [ ] Other backend errors return 502 + error_banner.

**Verify:** `go test ./cmd/breezyd/... -run TestPostUIPreset` → all pass.

**Steps:**

- [ ] **Step 1: Write the failing test**

Add to `cmd/breezyd/handlers_ui_write_test.go`:

```go
func TestPostUIPreset_Success(t *testing.T) {
	h := newTestHandlerWithFakeDevice(t) // existing helper pattern
	body := "preset=2&supply=40&extract=45"
	req := httptest.NewRequest("POST", "/ui/devices/bedroom/preset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "bedroom")
	w := httptest.NewRecorder()

	h.postUIPreset(w, req)

	if w.Code != 200 {
		t.Errorf("status: got %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `data-device="bedroom"`) {
		t.Errorf("response should contain a DeviceCard fragment; got %s", w.Body.String())
	}
}

func TestPostUIPreset_BadPreset(t *testing.T) {
	h := newTestHandlerWithFakeDevice(t)
	body := "preset=4&supply=40&extract=45"
	req := httptest.NewRequest("POST", "/ui/devices/bedroom/preset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "bedroom")
	w := httptest.NewRecorder()

	h.postUIPreset(w, req)

	if w.Code != 422 {
		t.Errorf("status: got %d, want 422", w.Code)
	}
}

func TestPostUIPreset_BadSupply(t *testing.T) {
	h := newTestHandlerWithFakeDevice(t)
	body := "preset=1&supply=5&extract=45" // 5 < 10 protocol minimum
	req := httptest.NewRequest("POST", "/ui/devices/bedroom/preset", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("name", "bedroom")
	w := httptest.NewRecorder()

	h.postUIPreset(w, req)

	if w.Code != 422 {
		t.Errorf("status: got %d, want 422", w.Code)
	}
}
```

If `newTestHandlerWithFakeDevice` doesn't exist, mirror the pattern in
`handlers_ui_write_test.go` (the existing tests for `postUISpeed`
already wire up a fakedevice — copy that).

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./cmd/breezyd/ -run TestPostUIPreset -v`
Expected: FAIL — `postUIPreset` undefined.

- [ ] **Step 3: Implement `postUIPreset` in `handlers_ui_write.go`**

Add this function (after `postUISpeed` is a natural location):

```go
// postUIPreset writes the per-preset supply/extract percentages.
//
// Form: preset=1|2|3, supply=10..100, extract=10..100
func (h *Handler) postUIPreset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	preset, err := strconv.Atoi(r.FormValue("preset"))
	if err != nil || preset < 1 || preset > 3 {
		h.uiValidationError(w, r, name, "preset must be 1, 2, or 3")
		return
	}
	supply, err := strconv.Atoi(r.FormValue("supply"))
	if err != nil || supply < 10 || supply > 100 {
		h.uiValidationError(w, r, name, "supply must be 10..100")
		return
	}
	extract, err := strconv.Atoi(r.FormValue("extract"))
	if err != nil || extract < 10 || extract > 100 {
		h.uiValidationError(w, r, name, "extract must be 10..100")
		return
	}

	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetPresetSpeed(ctx, rc, preset, supply, extract); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}
```

- [ ] **Step 4: Register the route in `server.go`**

After the `mux.HandleFunc("POST /ui/devices/{name}/speed", h.postUISpeed)` line (around line 228), add:

```go
mux.HandleFunc("POST /ui/devices/{name}/preset", h.postUIPreset)
```

- [ ] **Step 5: Run tests**

Run: `just generate && go test ./cmd/breezyd/ -run TestPostUIPreset -v`
Expected: all pass.

- [ ] **Step 6: Run full daemon test suite**

Run: `just check`
Expected: all pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/breezyd/
git commit -m "ui: add POST /ui/devices/{name}/preset shim (#53)"
```

---

## Task 6: layout.templ — cookie helper + #49 retrofit

**Goal:** Replace the `htmx:afterSettle` re-apply pass with a `setUIState` cookie helper and a summary-click handler that writes the cookie. After this task, `<details>` open state survives the 5s poll without flicker because the server emits `<details open>` directly.

**Files:**
- Modify: `cmd/breezyd/ui/templates/layout.templ`
- Modify: `tests/ui/dashboard.spec.ts` — update the three open-state-survives-polls tests (lines 783, 798, 812) to assert no `[hidden]`/`open` flicker mid-swap.

**Acceptance Criteria:**
- [ ] The IIFE at lines 83-101 (the `htmx:afterSettle` re-apply pass) is gone.
- [ ] A new IIFE provides a `setUIState(mut)` helper that reads the cookie, mutates via callback, writes back as URL-encoded JSON.
- [ ] A summary click (any `<details id="…">`) writes `details[id] = !el.open` into the cookie before the htmx swap fires.
- [ ] Three Playwright tests still pass; one new `flicker` test passes.

**Verify:** `just test-ui` (only the relevant tests) → all pass.

**Steps:**

- [ ] **Step 1: Replace the inline script in `layout.templ`**

In the `<script>` block (lines 44-102), keep the theme-picker IIFE
(lines 45-70) and replace the `<details>` shim (lines 72-101) with:

```js
// breezy-ui cookie helpers. Source of truth for dashboard UI state
// (which <details> are open, which preset editor is visible, automode
// + match-speeds toggles). The server reads this cookie on every render
// and emits the right markup directly — no JS reapply pass, no flicker.
//
// The cookie is JSON, URL-encoded, path=/, samesite=lax, max-age=1y.
// See internal/uistate for the schema.
function readUIState() {
  var raw = document.cookie.split('; ')
    .find(function(c) { return c.indexOf('breezy-ui=') === 0; });
  if (!raw) return {};
  try {
    return JSON.parse(decodeURIComponent(raw.slice('breezy-ui='.length)));
  } catch (e) {
    return {};
  }
}

function writeUIState(state) {
  document.cookie = 'breezy-ui=' +
    encodeURIComponent(JSON.stringify(state)) +
    '; path=/; max-age=31536000; samesite=lax';
}

function setUIState(mut) {
  var s = readUIState();
  if (!s.details) s.details = {};
  if (!s.preset) s.preset = {};
  mut(s);
  writeUIState(s);
}

// <summary> click → toggle details[id] in the cookie. The browser's
// native <details> click also flips el.open; we record the new value
// (which is `!el.open` *before* the click is processed because click
// listeners on <summary> fire before the toggle). Reading el.open after
// the next animation frame would be cleaner but is not necessary —
// the browser-native toggle and our cookie write must agree on the
// same logical state, and `!el.open` (pre-toggle) === el.open (post)
// once the click has propagated.
document.body.addEventListener('click', function(ev) {
  var summary = ev.target instanceof Element ? ev.target.closest('summary') : null;
  if (!summary) return;
  var d = summary.parentElement;
  if (!(d instanceof HTMLDetailsElement) || !d.id) return;
  setUIState(function(s) {
    s.details[d.id] = !d.open; // pre-toggle: invert to get the new state
  });
});
```

- [ ] **Step 2: Update Playwright tests for #49**

Find the three tests at lines 783, 798, 812 in `tests/ui/dashboard.spec.ts`.
For each, replace the existing assertion that the `<details>` is open
after a poll with a stricter no-flicker assertion. Pattern:

```typescript
test("sensors block: open state survives polls (no flicker)", async ({ page }) => {
  await page.goto("/");
  await page.waitForSelector('details[id^="sensors-"]');

  // Open the first card's sensors details.
  const details = page.locator('details[id^="sensors-"]').first();
  await details.locator('summary').click();
  await expect(details).toHaveAttribute("open", "");

  // Wait for a swap to complete and assert the open attr never leaves.
  let everClosed = false;
  const observer = await page.evaluateHandle(() => {
    const el = document.querySelector('details[id^="sensors-"]');
    let closed = false;
    const obs = new MutationObserver(() => {
      if (el && !el.hasAttribute('open')) closed = true;
    });
    obs.observe(el, { attributes: true, attributeFilter: ['open'], subtree: false });
    return { stop: () => { obs.disconnect(); return closed; } };
  });

  await page.waitForTimeout(6000); // > one 5s poll
  everClosed = await observer.evaluate((h) => h.stop());
  expect(everClosed).toBe(false);
  await expect(details).toHaveAttribute("open", "");
});
```

Apply the same pattern to the ENERGY and device-info tests at the
other two line numbers. Adjust the selectors accordingly.

- [ ] **Step 3: Regenerate templ + run UI tests**

```bash
just generate
just build  # rebuild breezyd binary so the next test-ui invocation uses new layout.templ
just test-ui -- --grep "open state survives polls"
```

Expected: the three #49 tests pass.

- [ ] **Step 4: Run full UI test suite**

Run: `just test-ui`
Expected: no regressions in the 66 active tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/breezyd/ui/templates/layout.templ tests/ui/dashboard.spec.ts
git commit -m "ui: replace #49 details-state shim with cookie writes (#53)"
```

---

## Task 7: layout.templ — preset chip + checkbox + slider JS

**Goal:** Wire the preset editor's interactions: chip click toggles cookie's `preset[name].open`; manual/mode/manual-slider clicks close the editor; automode/match-speeds checkboxes update cookie + automode-uncheck-while-active fires implied-mode write; slider drags get a snap-to-zero + match-speeds sibling sync via `htmx:configRequest`; post-slider implied-mode write fires when applicable.

**Files:**
- Modify: `cmd/breezyd/ui/templates/layout.templ` — extend the inline script with preset-editor handlers.
- Modify: `tests/ui/dashboard.spec.ts` — un-`fixme` and update tests at lines 893, 913, 919, 925, 930, 935, 940, 946, 951, 956, 961.

**Acceptance Criteria:**
- [ ] Click on `[data-action="preset"]` updates cookie's `preset[name].open` (toggle if same N, else set N and clear other devices).
- [ ] Click on `[data-action="manual-speed"]`, `[data-action="mode"]`, or change on `[data-action="manual-slider"]` sets `preset[name].open = 0`.
- [ ] `[data-action="automode-toggle"]` change → cookie write; if transition was checked → unchecked AND `data-speed-mode === "preset{N}"` for the open editor's N AND both sliders show ≥ 10 → fires `POST /ui/devices/{name}/mode` with `{"mode":"regeneration"}`.
- [ ] `[data-action="match-speeds-toggle"]` change → cookie write only.
- [ ] Slider snap rule: rawValue 1..9 → 0, slider DOM value updated, request payload mutated.
- [ ] Match-speeds sibling sync: when active, sibling slider DOM and payload mirror the changed value.
- [ ] After the slider POST, an implied-mode write fires when `data-speed-mode === "preset{N}"`, the implied mode differs from `data-airflow-mode`, and the rules in the spec table apply.
- [ ] The 11 listed Playwright tests + the new automode-default + uncheck-fires-regen tests pass.

**Verify:** `just test-ui` (full suite) → all pass.

**Steps:**

- [ ] **Step 1: Append preset-editor JS to `layout.templ`**

After the summary-click handler from Task 6, append inside the same
`<script>` block:

```js
// Preset chip click: toggle the per-device preset[name].open state.
// The htmx POST to /ui/devices/{name}/speed (preset=N activation) still
// fires; our listener only updates the cookie so the next swap renders
// the editor.
document.body.addEventListener('click', function(ev) {
  var btn = ev.target instanceof Element ? ev.target.closest('[data-action="preset"]') : null;
  if (!btn) return;
  var name = btn.getAttribute('data-name');
  var n = parseInt(btn.getAttribute('data-value'), 10);
  setUIState(function(s) {
    if (!s.preset[name]) s.preset[name] = { open: 0, automode: false, match: true };
    if (s.preset[name].open === n) {
      s.preset[name].open = 0; // re-click same preset closes editor
    } else {
      s.preset[name].open = n;
      // One editor open across the grid: clear other devices' open editors.
      Object.keys(s.preset).forEach(function(other) {
        if (other !== name && s.preset[other]) s.preset[other].open = 0;
      });
    }
  });
});

// Manual / mode / manual-slider clicks all close the editor.
function closePresetEditor(name) {
  setUIState(function(s) {
    if (s.preset[name]) s.preset[name].open = 0;
  });
}
document.body.addEventListener('click', function(ev) {
  var t = ev.target instanceof Element ? ev.target : null;
  if (!t) return;
  var manual = t.closest('[data-action="manual-speed"]');
  var mode = t.closest('[data-action="mode"]');
  if (manual) closePresetEditor(manual.getAttribute('data-name'));
  if (mode) closePresetEditor(mode.getAttribute('data-name'));
});
document.body.addEventListener('change', function(ev) {
  var t = ev.target instanceof Element ? ev.target : null;
  if (!t) return;
  if (t.matches('[data-action="manual-slider"]')) {
    closePresetEditor(t.getAttribute('data-name'));
  }
});

// Automode checkbox change: update cookie. If transition was
// checked → unchecked AND device's speed_mode is the open preset AND
// both sliders show ≥ 10, fire a regen mode write so the user sees
// the change without bumping a slider (#46.2).
document.body.addEventListener('change', function(ev) {
  var t = ev.target instanceof Element ? ev.target : null;
  if (!t || !t.matches('[data-action="automode-toggle"]')) return;
  var name = t.getAttribute('data-name');
  var nowOn = t.checked;
  setUIState(function(s) {
    if (!s.preset[name]) s.preset[name] = { open: 0, automode: false, match: true };
    s.preset[name].automode = nowOn;
  });
  if (nowOn) return; // only the on→off transition triggers a write

  var card = t.closest('.card');
  if (!card) return;
  var openPreset = parseInt(card.querySelector('[data-preset-editor]:not([hidden])')
    ?.getAttribute('data-preset-editor') || '0', 10);
  if (!openPreset) return;
  if (card.getAttribute('data-speed-mode') !== 'preset' + openPreset) return;
  var sup = parseInt(card.querySelector(
    '[data-action="preset-supply-slider"][data-preset="' + openPreset + '"]')?.value || '0', 10);
  var ext = parseInt(card.querySelector(
    '[data-action="preset-extract-slider"][data-preset="' + openPreset + '"]')?.value || '0', 10);
  if (sup < 10 || ext < 10) return;
  if (card.getAttribute('data-airflow-mode') === 'regeneration') return;
  var fd = new FormData();
  fd.append('mode', 'regeneration');
  htmx.ajax('POST', '/ui/devices/' + name + '/mode',
            { values: { mode: 'regeneration' }, target: card, swap: 'outerHTML' });
});

// Match-speeds checkbox change: cookie only.
document.body.addEventListener('change', function(ev) {
  var t = ev.target instanceof Element ? ev.target : null;
  if (!t || !t.matches('[data-action="match-speeds-toggle"]')) return;
  var name = t.getAttribute('data-name');
  setUIState(function(s) {
    if (!s.preset[name]) s.preset[name] = { open: 0, automode: false, match: true };
    s.preset[name].match = t.checked;
  });
});

// Slider drag pre-flight: snap 1..9 to 0, sync sibling when match-speeds
// is on. Mutates both DOM and outgoing request parameters.
document.body.addEventListener('htmx:configRequest', function(ev) {
  var el = ev.detail.elt;
  if (!(el instanceof HTMLInputElement)) return;
  if (!el.matches('[data-action="preset-supply-slider"], [data-action="preset-extract-slider"]')) return;

  var name = el.getAttribute('data-name');
  var preset = el.getAttribute('data-preset');
  var isSupply = el.getAttribute('data-action') === 'preset-supply-slider';

  var raw = parseInt(el.value, 10);
  var snapped = (raw > 0 && raw < 10) ? 0 : raw;
  if (snapped !== raw) el.value = String(snapped);

  var siblingAction = isSupply ? 'preset-extract-slider' : 'preset-supply-slider';
  var sibling = document.querySelector(
    '[data-action="' + siblingAction + '"][data-name="' + name + '"][data-preset="' + preset + '"]');

  var s = readUIState();
  var matchOn = !s.preset || !s.preset[name] || s.preset[name].match !== false;

  var supply, extract;
  if (matchOn) {
    if (sibling) sibling.value = String(snapped);
    supply = snapped;
    extract = snapped;
  } else if (isSupply) {
    supply = snapped;
    extract = sibling ? parseInt(sibling.value, 10) : snapped;
  } else {
    supply = sibling ? parseInt(sibling.value, 10) : snapped;
    extract = snapped;
  }

  ev.detail.parameters['supply'] = String(supply);
  ev.detail.parameters['extract'] = String(extract);
  ev.detail.parameters['preset'] = preset;

  // If either side is < 10, the firmware register can't store it; cancel
  // this POST and let the implied-mode write below carry the user intent.
  if (supply < 10 || extract < 10) {
    ev.preventDefault();
  }

  // Stash the post-snap values for the after-request implied-mode write.
  el.dataset.snapSupply = String(supply);
  el.dataset.snapExtract = String(extract);
});

// After the slider's /preset POST (or its cancellation), maybe fire an
// implied-mode write. Runs on htmx:afterRequest because it works for both
// the success and the preventDefault'd "skip POST" path.
document.body.addEventListener('htmx:afterRequest', function(ev) {
  var el = ev.detail.elt;
  if (!(el instanceof HTMLInputElement)) return;
  if (!el.matches('[data-action="preset-supply-slider"], [data-action="preset-extract-slider"]')) return;
  var name = el.getAttribute('data-name');
  var preset = parseInt(el.getAttribute('data-preset'), 10);
  var supply = parseInt(el.dataset.snapSupply || '0', 10);
  var extract = parseInt(el.dataset.snapExtract || '0', 10);

  var card = el.closest('.card');
  if (!card) return;
  if (card.getAttribute('data-speed-mode') !== 'preset' + preset) return;

  var s = readUIState();
  var automode = s.preset && s.preset[name] && s.preset[name].automode === true;

  var implied;
  if (automode) {
    implied = 'ventilation';
  } else if (supply >= 10 && extract >= 10) {
    implied = 'regeneration';
  } else if (supply === 0 && extract >= 10) {
    implied = 'extract';
  } else if (supply >= 10 && extract === 0) {
    implied = 'supply';
  } else {
    return; // both 0: no write
  }
  if (card.getAttribute('data-airflow-mode') === implied) return;

  htmx.ajax('POST', '/ui/devices/' + name + '/mode',
            { values: { mode: implied }, target: card, swap: 'outerHTML' });
});
```

- [ ] **Step 2: Un-fixme the listed Playwright tests**

For each line below in `tests/ui/dashboard.spec.ts`, change `test.fixme(`
to `test(`. Update the body for the two **rewrite** entries:

| Line | Action |
|---|---|
| 893 | un-fixme |
| 913 | rewrite to assert: automode default OFF, drag both sliders to ≥10, expect POST /ui/devices/{name}/mode with mode=regeneration |
| 919 | un-fixme |
| 925 | un-fixme |
| 930 | un-fixme |
| 935 | un-fixme |
| 940 | rewrite to assert: with cookie automode=true, drag in editor sends POST /mode with mode=ventilation |
| 946 | un-fixme |
| 951 | un-fixme |
| 956 | un-fixme |
| 961 | un-fixme |

For the rewritten test at line 913:

```typescript
test("preset editor: automode default OFF; dragging both ≥10 POSTs regeneration", async ({ page }) => {
  await page.goto("/");
  // Open preset 2's editor.
  const card = page.locator('.card[data-device="bedroom"]');
  await card.locator('[data-action="preset"][data-value="2"]').click();
  const editor = card.locator('[data-preset-editor="2"]');
  await expect(editor).toBeVisible();

  // Verify automode default is unchecked.
  const automode = editor.locator('[data-action="automode-toggle"]');
  await expect(automode).not.toBeChecked();

  // Watch for the mode POST.
  const modeReq = page.waitForRequest(req =>
    req.method() === "POST" && req.url().includes("/ui/devices/bedroom/mode"));

  // Drag the supply slider (and match-speeds will mirror it).
  const supply = editor.locator('[data-action="preset-supply-slider"]');
  await supply.fill("50");
  await supply.dispatchEvent("change");

  const req = await modeReq;
  expect(req.postData()).toContain("mode=regeneration");
});
```

For the rewritten test at line 940:

```typescript
test("preset editor: with automode on, dragging POSTs ventilation", async ({ page }) => {
  await page.context().addCookies([{
    name: "breezy-ui",
    value: encodeURIComponent(JSON.stringify({
      preset: { bedroom: { open: 2, automode: true, match: true } }
    })),
    url: page.url() || "http://localhost:8000",
    path: "/",
    sameSite: "Lax",
  }]);
  await page.goto("/");
  const card = page.locator('.card[data-device="bedroom"]');
  const editor = card.locator('[data-preset-editor="2"]');
  await expect(editor).toBeVisible();

  const modeReq = page.waitForRequest(req =>
    req.method() === "POST" && req.url().includes("/ui/devices/bedroom/mode"));

  const supply = editor.locator('[data-action="preset-supply-slider"]');
  await supply.fill("50");
  await supply.dispatchEvent("change");

  const req = await modeReq;
  expect(req.postData()).toContain("mode=ventilation");
});
```

- [ ] **Step 3: Add the new tests**

Add at the end of the un-fixme'd block:

```typescript
test("automode default: unchecked when editor opens (no cookie)", async ({ page, context }) => {
  await context.clearCookies();
  await page.goto("/");
  await page.locator('.card[data-device="bedroom"] [data-action="preset"][data-value="1"]').click();
  const automode = page.locator(
    '.card[data-device="bedroom"] [data-preset-editor="1"] [data-action="automode-toggle"]');
  await expect(automode).not.toBeChecked();
});

test("automode off→toggle while in preset, both fans ≥10: POSTs regeneration", async ({ page, context }) => {
  await context.clearCookies();
  await page.goto("/");
  // Activate preset 1; slider defaults are 22/23 in the fixture so we drag both up to ≥10 (already there).
  await page.locator('.card[data-device="bedroom"] [data-action="preset"][data-value="1"]').click();
  // After the htmx swap, the card root reflects speed_mode=preset1.
  await expect(page.locator('.card[data-device="bedroom"]'))
    .toHaveAttribute("data-speed-mode", "preset1");

  const automode = page.locator(
    '.card[data-device="bedroom"] [data-preset-editor="1"] [data-action="automode-toggle"]');
  await automode.check();   // off→on, pure preference
  const modeReq = page.waitForRequest(req =>
    req.method() === "POST" && req.url().includes("/ui/devices/bedroom/mode"));
  await automode.uncheck(); // on→off → fires regen
  const req = await modeReq;
  expect(req.postData()).toContain("mode=regeneration");
});

test("preset editor: open state survives 5s poll (no flicker)", async ({ page }) => {
  await page.goto("/");
  await page.locator('.card[data-device="bedroom"] [data-action="preset"][data-value="2"]').click();
  const editor = page.locator('.card[data-device="bedroom"] [data-preset-editor="2"]');
  await expect(editor).toBeVisible();

  const everHidden = await page.evaluate(() => {
    const el = document.querySelector(
      '.card[data-device="bedroom"] [data-preset-editor="2"]');
    let hidden = false;
    const obs = new MutationObserver(() => {
      if (el && el.hasAttribute('hidden')) hidden = true;
    });
    obs.observe(el, { attributes: true, attributeFilter: ['hidden'] });
    return new Promise(resolve => {
      setTimeout(() => { obs.disconnect(); resolve(hidden); }, 6000);
    });
  });
  expect(everHidden).toBe(false);
});

test("cookie: malformed value falls back to defaults without 5xx", async ({ page, context }) => {
  await context.addCookies([{
    name: "breezy-ui",
    value: "%7Bnot-json",
    url: "http://localhost:8000",
    path: "/",
    sameSite: "Lax",
  }]);
  const resp = await page.goto("/");
  expect(resp?.status()).toBe(200);
});
```

- [ ] **Step 4: Run UI tests**

```bash
just generate
just build
just test-ui
```

Expected: all 66+ active tests pass, including the un-fixme'd and new ones.

- [ ] **Step 5: Commit**

```bash
git add cmd/breezyd/ui/templates/layout.templ tests/ui/dashboard.spec.ts
git commit -m "ui: preset chip + checkbox + slider JS via cookie state (#53, #46)"
```

---

## Task 8: Final Playwright sweep + screenshot

**Goal:** Confirm the full Playwright suite is green; regenerate the README screenshot to show the restored editor.

**Files:**
- Modify: `tests/ui/screenshot.ts` — open preset 2's editor on the bedroom card before the 3-col screenshot so the README image shows the restored UI.
- Regenerate: `tests/ui/screenshots/dashboard-3col.png`.

**Acceptance Criteria:**
- [ ] `just check-all` passes.
- [ ] `just screenshot` regenerates `dashboard-3col.png` with the bedroom card's preset-2 editor visible.
- [ ] `tests/ui/screenshots/dashboard-3col.png` is updated (committed binary diff).

**Verify:** `just check-all` → all pass. Visual: open `tests/ui/screenshots/dashboard-3col.png` and confirm preset 2's editor shows on the bedroom card.

**Steps:**

- [ ] **Step 1: Update `tests/ui/screenshot.ts` to open the editor**

Find the section that takes the 3-col screenshot. Before the screenshot
call, add:

```typescript
await page.locator(
  '.card[data-device="bedroom"] [data-action="preset"][data-value="2"]'
).click();
await page.waitForSelector(
  '.card[data-device="bedroom"] [data-preset-editor="2"]:not([hidden])');
```

- [ ] **Step 2: Run the full check**

```bash
just check-all
```

Expected: all pass.

- [ ] **Step 3: Regenerate screenshots**

```bash
just screenshot
```

Expected: `tests/ui/screenshots/dashboard-3col.png` updated; the new
image shows preset 2's editor open on the bedroom card.

- [ ] **Step 4: Visual confirm**

Open `tests/ui/screenshots/dashboard-3col.png` in any image viewer.
Verify: bedroom card has the preset-2 editor visible with the
automode + match-speeds checkboxes and supply/exhaust sliders.

- [ ] **Step 5: Commit**

```bash
git add tests/ui/screenshot.ts tests/ui/screenshots/dashboard-3col.png
git commit -m "ui: README screenshot shows restored preset editor (#53)"
```

- [ ] **Step 6: Close GitHub issues**

Run:

```bash
gh issue close 49 --comment "Fixed in #56 (initial JS-shim) and re-fixed via cookie-driven render in this branch — no flicker now."
gh issue close 51 --comment "Already addressed in #54."
gh issue close 53 --comment "Restored editor (cookie-driven) and folded in #46.1/#46.2 in this branch."
gh issue close 46 --comment "Fixed alongside #53."
```

(Run only after the PR for this branch is merged.)

---

## Self-Review Notes

- **Spec coverage:** ✓ uistate package (Task 1), DeviceView wiring (Task 2), template details retrofit + card data attrs (Task 3), preset-editor markup (Task 4), preset shim handler (Task 5), cookie helper + #49 retrofit (Task 6), preset interactions + #46 (Task 7), screenshot + close-issues (Task 8). Risks called out in the spec are all addressed by either tests or guards in the implementation.
- **Type/symbol consistency:** `setUIState` / `readUIState` / `writeUIState` named consistently. `data-preset-editor` / `data-action` / `data-name` / `data-preset` form a stable set. `DetailsOpen`/`EditingPreset`/`Automode`/`MatchSpeeds` match across spec, view, buildView, templates, and tests.
- **Out-of-scope, not lost:** optimistic live overlay and cross-device editor closing acknowledged as intentional gaps in the spec; not scheduled here.
