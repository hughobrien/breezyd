# SSE Editor Preservation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the single outer-card SSE push with per-block content patches plus a per-card signals patch, so an open editor (schedule, threshold, preset) survives polls.

**Architecture:** Edit-mode wrapping elements carry `data-edit`. Poll-driven patches target `:not([data-edit])` and silently miss when an editor is open. Card-outer reactive state (`stale` class, speed/airflow data-attrs, "X ago" stale-row, sensors-alert class) moves into per-card datastar signals; the card outer is never HTML-patched after initial render. Initial-state pass distinguishes cold-load (append) from reconnect (outer-replace) using the `Last-Event-ID` header.

**Tech Stack:** Go 1.x, templ v0.3.x, datastar v1.0+ (datastar-go SDK v1.2.x), Playwright (pnpm).

**Spec:** `docs/superpowers/specs/2026-05-08-sse-editor-preservation-design.md`.

**Project rules to follow during implementation:**
- Use the **datastar** skill when touching `cmd/breezyd/ui/`.
- Use the **templ** skill when touching `.templ` files; never edit `*_templ.go` directly; run `just generate` before claiming done.
- Use the **sse-events** skill when touching `handlers_ui_sse.go` / `push_hub.go`.
- Use **systematic-debugging** if a block patch unexpectedly clobbers an editor.
- Use **verification-before-completion** before claiming any task done.
- For repeatable check/lint/test combos, add a recipe to `justfile` (don't re-type).

---

## File Structure

| Path | Responsibility | Action |
|---|---|---|
| `cmd/breezyd/ui/templates/device_card.templ` | Card outer + signals seed + stale-row | **Modify** |
| `cmd/breezyd/ui/templates/sensors_block.templ` | Sensors block + cell markers | **Modify** |
| `cmd/breezyd/ui/templates/sensor_threshold.templ` | Threshold edit-variant `data-edit` | **Modify** |
| `cmd/breezyd/ui/templates/schedule_block.templ` | Schedule read/edit `data-block`+`data-edit` | **Modify** |
| `cmd/breezyd/ui/templates/controls_block.templ` | Controls `data-block`+reactive `data-attr:data-edit` | **Modify** |
| `cmd/breezyd/ui/templates/energy_block.templ` | `data-block="energy"` | **Modify** |
| `cmd/breezyd/ui/view.go` | `CardSignals` struct/helper for signal payload shape | **Modify** |
| `cmd/breezyd/ui_view.go` | Populate signals fields if any computation needed | **Modify (light)** |
| `cmd/breezyd/push_hub.go` | Structured `PushEvent` with Signals + Blocks; `renderBlocks` constructor | **Modify** |
| `cmd/breezyd/handlers_ui_sse.go` | Drain structured event, emit signal + per-block patches; reconnect detect via `Last-Event-ID` | **Modify** |
| `cmd/breezyd/main.go` | Wire `renderBlocks` into `NewPushHub` | **Modify** |
| `cmd/breezyd/handlers_ui_sse_test.go` | Update existing tests for new event shape; add reconnect test | **Modify** |
| `cmd/breezyd/push_hub_test.go` | Update tests for new render closure signature | **Modify** |
| `cmd/breezyd/ui/templates/render_test.go` | Add tests for `data-block`, `data-edit`, signals seed | **Modify** |
| `tests/ui/dashboard.spec.ts` | Editor-preservation e2e tests for schedule/threshold/preset | **Modify** |
| `CHANGELOG.md` | Note the architectural change | **Modify** |
| `justfile` | (If needed) recipe for the new test combo | **Modify (conditional)** |

---

## Task 1: CardSignals helper and seed shape

**Goal:** Define the per-card signal payload shape (`stale`, `speedMode`, `airflowMode`, `lastPollAge`, `sensorsAlert`) once, used by both initial render and runtime push.

**Files:**
- Modify: `cmd/breezyd/ui/view.go` — add `CardSignals` struct + helper.
- Test: `cmd/breezyd/ui/templates/render_test.go` — assert seed JSON shape.

**Acceptance Criteria:**
- [ ] `ui.CardSignals` struct exists with fields `Stale bool`, `SpeedMode string`, `AirflowMode string`, `LastPollAge string`, `SensorsAlert bool` (lowercase camel JSON tags: `stale`, `speedMode`, `airflowMode`, `lastPollAge`, `sensorsAlert`).
- [ ] `ui.CardSignalsFor(v DeviceView) CardSignals` returns a populated value.
- [ ] `ui.MarshalCardSignals(v DeviceView) ([]byte, error)` returns JSON bytes for SSE PatchSignals.
- [ ] Existing tests still pass.

**Verify:** `go test ./cmd/breezyd/ui/...` → all PASS.

**Steps:**

- [ ] **Step 1: Add the struct + helpers to `cmd/breezyd/ui/view.go`.**

Open `cmd/breezyd/ui/view.go` and append (place near other view types):

```go
// CardSignals is the per-device datastar signal payload that drives
// card-outer reactive state (stale class, speed-mode and airflow-mode
// data-attrs, "X ago" stale-row, sensors-block alert class). The card's
// HTML is never patched after initial render; signals are.
type CardSignals struct {
    Stale        bool   `json:"stale"`
    SpeedMode    string `json:"speedMode"`
    AirflowMode  string `json:"airflowMode"`
    LastPollAge  string `json:"lastPollAge"`
    SensorsAlert bool   `json:"sensorsAlert"`
}

// CardSignalsFor extracts CardSignals from a DeviceView.
func CardSignalsFor(v DeviceView) CardSignals {
    return CardSignals{
        Stale:        v.Stale,
        SpeedMode:    v.SpeedMode,
        AirflowMode:  v.AirflowMode,
        LastPollAge:  v.LastPollAge,
        SensorsAlert: v.Sensors.AlertActive,
    }
}

// MarshalCardSignals returns the JSON payload for a PatchSignals event.
func MarshalCardSignals(v DeviceView) ([]byte, error) {
    return json.Marshal(CardSignalsFor(v))
}
```

If `encoding/json` isn't already imported in `view.go`, add it. Run `goimports` or just edit the import block.

- [ ] **Step 2: Add a render-test assertion for the JSON shape.**

Open `cmd/breezyd/ui/templates/render_test.go` and add a test:

```go
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
    // Order-independent check: parse back, assert each field.
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
```

Add `"encoding/json"` to the test file imports if not already present.

- [ ] **Step 3: Run the test.**

```sh
go test ./cmd/breezyd/ui/templates/ -run TestCardSignalsFor_JSON -v
```

Expected: PASS.

- [ ] **Step 4: Commit.**

```sh
git add cmd/breezyd/ui/view.go cmd/breezyd/ui/templates/render_test.go
git commit -m "$(cat <<'EOF'
ui: CardSignals payload + helpers for per-card signal pushes (#65)

Defines the JSON shape used by the SSE handler's datastar-patch-signals
event. Five fields drive card-outer reactive state via data-class /
data-attr / data-show bindings.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Card-outer reactive shell

**Goal:** Wire the card outer's `class="stale"`, `data-speed-mode`, `data-airflow-mode`, the "X ago" stale row, and the seed signal payload to use the new CardSignals shape.

**Files:**
- Modify: `cmd/breezyd/ui/templates/device_card.templ`
- Test: `cmd/breezyd/ui/templates/render_test.go` (add assertions on rendered card outer).

**Acceptance Criteria:**
- [ ] Card outer's `<div class="card">` has `data-class:stale="$stale"`, `data-attr:data-speed-mode="$speedMode"`, `data-attr:data-airflow-mode="$airflowMode"`.
- [ ] Card outer's `data-signals` JSON includes the five `CardSignals` fields plus the existing `automode`, `matchSpeeds`, `editor`, `detailsOpen` seeds.
- [ ] The static stale-row block (the `if v.Stale && v.LastPollAge != "" { ... }`) is replaced by a datastar-driven row using `data-show` and `data-text`.
- [ ] `TestRenderDeviceCard_*` golden-style tests pass after running `just generate` and (if needed) `go test ./... -update`.
- [ ] Stale-class still appears on cold render when `v.Stale=true` (datastar's `data-class:` would set it on first paint via the seed signal).

**Verify:** `just generate && go test ./cmd/breezyd/ui/templates/ -v` → all PASS.

**Steps:**

- [ ] **Step 1: Modify `device_card.templ`.**

Replace the existing `<div class="card" ...>` block and the static stale-row with the reactive shell. The current block is:

```templ
<div
    class={ "card", templ.KV("stale", v.Stale) }
    data-device={ v.Name }
    data-speed-mode={ v.SpeedMode }
    data-airflow-mode={ v.AirflowMode }
    data-signals={ initialCardSignals() }
>
    ...blocks...
    if v.Stale && v.LastPollAge != "" {
        <div class="row"><span class="ts red">{ v.LastPollAge } ago</span></div>
    } else if v.Stale {
        <div class="row"><span class="ts red">no poll</span></div>
    }
    ...more blocks...
</div>
```

Replace with:

```templ
<div
    class="card"
    data-device={ v.Name }
    data-class:stale="$stale"
    data-attr:data-speed-mode="$speedMode"
    data-attr:data-airflow-mode="$airflowMode"
    data-signals={ initialCardSignals(v) }
>
    @infoDetails(v)
    <div class="row" data-show="$stale">
        <span class="ts red"><span data-text="$lastPollAge ? $lastPollAge + ' ago' : 'no poll'"></span></span>
    </div>
    @EnergyBlock(v.Name, v.Energy)
    @SensorsBlock(v.Name, v.Sensors)
    @ScheduleBlock(v.Name, v.Schedule, v.Stale)
    @controlsBlock(v)
</div>
```

(`@infoDetails(v)` will be extracted in the next step — preserves the `<details id="info-...">` block.)

- [ ] **Step 2: Extract `@infoDetails(v)` from the inlined info `<details>`.**

Add to `device_card.templ` (above `unreachableCard`):

```templ
templ infoDetails(v ui.DeviceView) {
    <details
        id={ "info-" + v.Name }
        class={ "device-info", templ.KV("alert", v.NeedsAttention) }
        data-block="info"
        data-attr:open="$detailsOpen.info"
    >
        <summary>
            <h2>{ v.Name }</h2>
            <button
                type="button"
                class="toggle toggle-inline"
                data-on:click={ powerButtonExpr(v) }
                if v.Stale { disabled }
                aria-pressed={ boolAttr(v.Power) }
            >power</button>
        </summary>
        @kvRow("ip", v.IP)
        @kvRow("serial", v.Serial)
        @kvRow("firmware version", v.FirmwareVersion)
        @kvRow("firmware date", v.FirmwareDate)
        @kvRowWithAction("filter", filterStatusStr(v), "reset filter", "/ui/devices/"+v.Name+"/reset-filter")
        @kvRow("motor", v.MotorLifetime)
        @kvRow("RTC", v.RTCBattery)
        @kvRowWithAction("faults", v.FaultLevel, "reset faults", "/ui/devices/"+v.Name+"/reset-faults")
    </details>
}
```

Note `data-block="info"` is added.

- [ ] **Step 3: Update `initialCardSignals` to accept the view and seed CardSignals.**

Replace `initialCardSignals()` (no args) with:

```go
// initialCardSignals returns the per-card datastar signals seed as JSON.
// Combines the static UI flags (automode / matchSpeeds / editor / detailsOpen)
// with the runtime CardSignals fields (stale / speedMode / airflowMode /
// lastPollAge / sensorsAlert). The seed is what datastar shows before any
// SSE arrives; subsequent pushes update only the runtime fields via
// datastar-patch-signals.
func initialCardSignals(v ui.DeviceView) string {
    s := map[string]any{
        // UI flags (client-side toggles, stable across polls).
        "automode":    false,
        "matchSpeeds": true,
        "editor":      0,
        "detailsOpen": map[string]bool{
            "info":     false,
            "sensors":  true,
            "energy":   false,
            "schedule": false,
        },
        // Runtime state from the snapshot.
        "stale":        v.Stale,
        "speedMode":    v.SpeedMode,
        "airflowMode":  v.AirflowMode,
        "lastPollAge":  v.LastPollAge,
        "sensorsAlert": v.Sensors.AlertActive,
    }
    b, _ := json.Marshal(s)
    return string(b)
}
```

- [ ] **Step 4: Run templ generation.**

```sh
just generate
```

Expected: regenerates `*_templ.go`. No errors.

- [ ] **Step 5: Update or refresh golden render tests.**

Open `cmd/breezyd/ui/templates/render_test.go`. Find the existing `TestRenderDeviceCard_*` tests (they read `testdata/*.json` and compare to `testdata/golden_*.html`). Inspect with:

```sh
ls cmd/breezyd/ui/templates/testdata/
```

Refresh goldens:

```sh
go test ./cmd/breezyd/ui/templates/ -update
```

Then visually inspect the regenerated `golden_*.html` to confirm:
- Card outer no longer has `data-speed-mode="..."` / `data-airflow-mode="..."` *literal* attribute values; it has `data-attr:data-speed-mode="$speedMode"` instead.
- Card outer's `class` is plain `"card"` (no `stale` class baked in).
- Card outer has `data-class:stale="$stale"`.
- The `data-signals` JSON contains all 5 CardSignals fields plus the 4 UI flags.
- The stale-row uses `data-show="$stale"` + `data-text` instead of static rendering.

- [ ] **Step 6: Add explicit assertions for the reactive bindings.**

Append to `render_test.go`:

```go
func TestRenderDeviceCard_ReactiveOuter(t *testing.T) {
    v := loadView(t, "settling") // any existing fixture
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
        `data-text="$lastPollAge ? $lastPollAge + &#39; ago&#39; : &#39;no poll&#39;"`,
        `&#34;sensorsAlert&#34;`,
        `&#34;speedMode&#34;`,
        `data-block=&#34;info&#34;`,
    }
    wantAbsent := []string{
        // Old static attributes must be gone.
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
```

(The `&#39;` and `&#34;` entities are how templ HTML-escapes single/double quotes inside attribute values.)

- [ ] **Step 7: Run tests.**

```sh
go test ./cmd/breezyd/ui/templates/ -v
```

Expected: PASS, including the new test.

- [ ] **Step 8: Commit.**

```sh
git add cmd/breezyd/ui/templates/device_card.templ cmd/breezyd/ui/templates/device_card_templ.go cmd/breezyd/ui/templates/render_test.go cmd/breezyd/ui/templates/testdata/
git commit -m "$(cat <<'EOF'
ui: card outer becomes reactive shell driven by CardSignals (#65)

Card class/stale, speed-mode and airflow-mode data-attrs, the "X ago"
stale row, and the sensors-alert class flag move from server-rendered
HTML to per-card datastar signals. The card's outer HTML is never
patched after initial render; signals carry the state.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Block markers (data-block, data-sensor-cell, data-edit)

**Goal:** Tag every patchable element with the markers needed for per-block selectors and edit-mode exclusion.

**Files:**
- Modify: `cmd/breezyd/ui/templates/energy_block.templ`
- Modify: `cmd/breezyd/ui/templates/sensors_block.templ`
- Modify: `cmd/breezyd/ui/templates/sensor_threshold.templ`
- Modify: `cmd/breezyd/ui/templates/schedule_block.templ`
- Modify: `cmd/breezyd/ui/templates/controls_block.templ`
- Test: `cmd/breezyd/ui/templates/render_test.go`

**Acceptance Criteria:**
- [ ] Energy block's `<details>` has `data-block="energy"`.
- [ ] Sensors block's `<details>` has `data-block="sensors"` and `data-class:alert="$sensorsAlert"` (replacing the templ-static alert class).
- [ ] Each of the 12 sensor cells has `data-sensor-cell="<key>"`. Plain cells get the marker added; threshold cells (co2/voc/humidity) keep their existing `data-threshold-cell` AND gain `data-sensor-cell` for uniform selector logic.
- [ ] Threshold edit variants (`SensorThresholdEdit`) carry `data-edit="true"` on the wrapping `<div class="sensor-cell">`.
- [ ] Schedule read variant has `data-block="schedule"`. Schedule edit variant (`ScheduleBlockEdit`) has `data-block="schedule"` AND `data-edit="true"`.
- [ ] Controls block has `data-block="controls"` AND `data-attr:data-edit="$editor !== 0"`.
- [ ] Render tests assert all of the above.

**Verify:** `just generate && go test ./cmd/breezyd/ui/templates/ -v` → all PASS.

**Steps:**

- [ ] **Step 1: Modify `energy_block.templ` — add `data-block`.**

Find the outer `<details>` and add `data-block="energy"`:

```templ
<details
    id={ "energy-" + name }
    class="block energy"
    data-block="energy"
    data-attr:open="$detailsOpen.energy"
>
    ...existing content...
</details>
```

- [ ] **Step 2: Modify `sensors_block.templ` — `data-block`, reactive alert class, per-cell markers.**

Replace the outer `<details>`:

```templ
templ SensorsBlock(name string, s ui.SensorsView) {
    <details
        id={ "sensors-" + name }
        class="block sensors"
        data-block="sensors"
        data-class:alert="$sensorsAlert"
        data-attr:open="$detailsOpen.sensors"
    >
        <summary><h3>Sensors</h3></summary>
        @sensorsGrid(name, s)
    </details>
}
```

(The static `templ.KV("alert", s.AlertActive)` is removed; the alert class now flows from the `$sensorsAlert` signal which is in the seed and updated via PatchSignals.)

Update each `plainSensorCell` call site to include the cell key. Modify `plainSensorCell` to accept a key:

```templ
templ plainSensorCell(key, label, value string) {
    <div class="sensor-cell" data-sensor-cell={ key }>
        <div class="sensor-label">{ label }</div>
        <div>{ value }</div>
    </div>
}
```

Update `sensorsGrid`:

```templ
templ sensorsGrid(name string, s ui.SensorsView) {
    <div class="sensor-grid">
        @co2Cell(name, s)
        @vocCell(name, s)
        @humidityCell(name, s)
        @plainSensorCell("recovery",      "recovery",      fmtOptPct(s.RecoveryPct))
        @plainSensorCell("supply",        "supply",        fmtTempC(s.TempOutdoorC))
        @plainSensorCell("exhaust",       "exhaust",       fmtTempC(s.TempExhaustInletC))
        @plainSensorCell("supply_regen",  "supply_regen",  fmtTempC(s.TempSupplyC))
        @plainSensorCell("exhaust_regen", "exhaust_regen", fmtTempC(s.TempExhaustOutC))
        @plainSensorCell("delta_supply",  "Δ",             tempDeltaStr(s.TempSupplyC, s.TempOutdoorC))
        @plainSensorCell("delta_exhaust", "Δ",             tempDeltaStr(s.TempExhaustOutC, s.TempExhaustInletC))
        @plainSensorCell("supply_rpm",    "supply rpm",    rpmStr(s.SupplyRPM))
        @plainSensorCell("exhaust_rpm",   "exhaust rpm",   rpmStr(s.ExtractRPM))
    </div>
}
```

(Two cells used the same label `"Δ"`; we disambiguate via the key.)

- [ ] **Step 3: Modify `sensor_threshold.templ` — add `data-sensor-cell` to read+edit variants and `data-edit` to edit variant.**

Update `SensorThresholdRead`:

```templ
templ SensorThresholdRead(name, kind, label, suffix, tooltip string, value int, alerting bool) {
    <div class="sensor-cell" data-threshold-cell={ kind } data-sensor-cell={ kind } if tooltip != "" { title={ tooltip } }>
        <div class="sensor-label">{ label }</div>
        <div
            class={ "value-clickable", templ.KV("alert-fire", alerting) }
            data-on:click={ datastar.GetSSE("/ui/devices/%s/threshold/%s/edit", name, kind) }
        >{ fmt.Sprintf("%d%s", value, suffix) }</div>
    </div>
}
```

Update `SensorThresholdEdit`:

```templ
templ SensorThresholdEdit(name, kind, label string, min, max, step, threshold int, autoFan, disabled bool) {
    <div class="sensor-cell" data-threshold-cell={ kind } data-sensor-cell={ kind } data-edit="true">
        <div class="sensor-label">{ label }</div>
        <form
            class="thresh-edit-inline"
            data-on:submit__prevent={ datastar.PutSSE("/ui/devices/%s/threshold", name) }
        >
            ...existing form...
        </form>
    </div>
}
```

- [ ] **Step 4: Modify `schedule_block.templ` — `data-block` on read variant, `data-block`+`data-edit` on edit variant.**

Update the read variant `ScheduleBlock` (find the `<details ...>` opener and replace):

```templ
<details
    id={ "schedule-" + name }
    class={ "block", "schedule", templ.KV("alert", s.Alert) }
    data-block="schedule"
    data-attr:open="$detailsOpen.schedule"
>
    ...existing content...
</details>
```

Update `ScheduleBlockEdit`:

```templ
<details
    class="block schedule"
    data-block="schedule"
    data-edit="true"
    open
>
    ...existing form content...
</details>
```

- [ ] **Step 5: Modify `controls_block.templ` — `data-block` and reactive `data-attr:data-edit`.**

Find the outer `<details>` of the controls block and add the markers:

```templ
<details
    id={ "controls-" + v.Name }
    class="block controls"
    data-block="controls"
    data-attr:open="$detailsOpen.controls"
    data-attr:data-edit="$editor !== 0 ? 'true' : null"
>
    ...existing content...
</details>
```

(The current `controls_block.templ` may not have `data-attr:open` — check the file before editing. If the controls block currently has no `<details>` open-state binding, leave that out and only add `data-block` + `data-attr:data-edit`.)

The expression `$editor !== 0 ? 'true' : null` makes datastar set the attribute when truthy and remove it when null — see datastar's `attr` plugin behavior (the JS dispatcher: `c==null?e.removeAttribute(a):...e.setAttribute(a,c)`).

- [ ] **Step 6: Run templ generation.**

```sh
just generate
```

- [ ] **Step 7: Add render tests asserting markers.**

Append to `cmd/breezyd/ui/templates/render_test.go`:

```go
func TestRenderBlocks_DataBlockMarkers(t *testing.T) {
    v := loadView(t, "settling")
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
        `data-attr:data-edit="$editor !== 0 ? &#39;true&#39; : null"`,
    } {
        if !strings.Contains(got, s) {
            t.Errorf("missing %q in card render", s)
        }
    }
    // Plain sensor cells get data-sensor-cell="...".
    for _, k := range []string{"recovery", "supply", "exhaust", "supply_regen", "exhaust_regen", "delta_supply", "delta_exhaust", "supply_rpm", "exhaust_rpm"} {
        want := `data-sensor-cell="` + k + `"`
        if !strings.Contains(got, want) {
            t.Errorf("missing plain cell marker %q", want)
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
```

- [ ] **Step 8: Run tests.**

```sh
go test ./cmd/breezyd/ui/templates/ -v
```

Expected: PASS. Goldens may need refresh:

```sh
go test ./cmd/breezyd/ui/templates/ -update
```

Visually inspect updated `testdata/golden_*.html` for the new markers.

- [ ] **Step 9: Commit.**

```sh
git add cmd/breezyd/ui/templates/
git commit -m "$(cat <<'EOF'
ui: data-block + data-edit markers on every patchable element (#65)

Each block (info/energy/sensors/schedule/controls) carries data-block
for per-block patch selectors. Edit variants (schedule, threshold)
carry data-edit so poll patches' :not([data-edit]) skip them. Controls
block uses data-attr:data-edit reactive on $editor !== 0 to preserve
the preset editor's slider value during a poll. Sensor cells get
data-sensor-cell for fine-grained per-cell patches.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: PushHub structured event (Signals + Blocks)

**Goal:** Transform `PushHub` to render and queue a structured `PushEvent` carrying signals JSON and a list of `(selector, html)` block patches, instead of one whole-card HTML string.

**Files:**
- Modify: `cmd/breezyd/push_hub.go`
- Modify: `cmd/breezyd/main.go` (wire `renderBlocks`)
- Modify: `cmd/breezyd/handlers_ui_read.go` (if it exposes `buildView` — used by renderBlocks)
- Test: `cmd/breezyd/push_hub_test.go`
- Test: `cmd/breezyd/handlers_ui_sse_test.go` (update render closure signature)

**Acceptance Criteria:**
- [ ] `PushEvent` carries `DeviceName string`, `SignalsJSON []byte`, `Blocks []BlockPatch`.
- [ ] `BlockPatch` has `Selector string`, `HTML string`.
- [ ] `NewPushHub` takes a `renderBlocks func(name string, snap Snapshot) (*PushEvent, error)` callback.
- [ ] `Notify` calls `renderBlocks`, queues the resulting `PushEvent` onto each subscriber.
- [ ] On render error, the event is dropped (current behavior).
- [ ] Existing tests updated and still pass.

**Verify:** `go test ./cmd/breezyd/... -run "TestPushHub|TestGetUISSE_Initial|TestNotify" -v` → all PASS.

**Steps:**

- [ ] **Step 1: Replace `cmd/breezyd/push_hub.go` types.**

Replace the types and `NewPushHub`/`Notify` signatures. Open `cmd/breezyd/push_hub.go` and rewrite the relevant sections:

```go
// PushEvent is one fan-out unit: one device's signal payload (JSON for
// datastar-patch-signals) plus a list of block patches (each rendered
// templ component plus the selector to target). The SSE handler emits
// one signals event followed by one elements event per block.
type PushEvent struct {
    DeviceName  string
    SignalsJSON []byte
    Blocks      []BlockPatch
}

// BlockPatch is one (selector, html) pair: a single
// datastar-patch-elements event with mode=outer.
type BlockPatch struct {
    Selector string
    HTML     string
}

// Subscriber holds a single SSE client's connection state inside the hub.
// Events is closed when the subscriber is removed.
type Subscriber struct {
    Events chan PushEvent
}

// PushNotifier is the producer-side interface — the poller and action
// handlers depend on this rather than on *PushHub so tests can swap in
// a fake.
type PushNotifier interface {
    Notify(name string, snap Snapshot)
}

// PushHub is the per-process fan-out registry.
type PushHub struct {
    renderBlocks func(name string, snap Snapshot) (*PushEvent, error)

    mu     sync.Mutex
    subs   map[*Subscriber]struct{}
    closed map[*Subscriber]struct{}
}

// NewPushHub constructs an empty hub. renderBlocks produces the
// structured per-device event payload — signals plus block patches —
// for each Notify; injection lets tests swap in a stub builder.
func NewPushHub(renderBlocks func(name string, snap Snapshot) (*PushEvent, error)) *PushHub {
    return &PushHub{
        renderBlocks: renderBlocks,
        subs:         make(map[*Subscriber]struct{}),
        closed:       make(map[*Subscriber]struct{}),
    }
}

// Notify renders the event payload for (name, snap) and enqueues the
// resulting event on every subscriber. Render errors are silently
// dropped — the next successful poll re-renders, and noisy logging
// from the poller hot path costs more than it saves.
func (h *PushHub) Notify(name string, snap Snapshot) {
    ev, err := h.renderBlocks(name, snap)
    if err != nil || ev == nil {
        return
    }

    h.mu.Lock()
    defer h.mu.Unlock()
    for sub := range h.subs {
        select {
        case sub.Events <- *ev:
        default:
            select {
            case <-sub.Events:
            default:
            }
            select {
            case sub.Events <- *ev:
            default:
            }
        }
    }
}
```

The `Subscribe`/`Unsubscribe` methods remain unchanged.

- [ ] **Step 2: Add a builder in a new file `cmd/breezyd/push_render.go`.**

Create a new file `cmd/breezyd/push_render.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// push_render.go produces the structured PushEvent payload for one
// device snapshot — the per-card signal JSON plus a list of per-block
// (selector, html) pairs. The SSE handler turns each pair into a
// datastar-patch-elements event and the signal JSON into a
// datastar-patch-signals event.
package main

import (
    "bytes"
    "context"
    "fmt"

    "github.com/hughobrien/breezyd/cmd/breezyd/ui"
    "github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
    "github.com/a-h/templ"
)

// buildPushEvent renders the templ blocks and signal JSON for a
// single device. Returns nil + error on any render failure; the
// caller drops the event in that case.
func buildPushEvent(name string, view ui.DeviceView) (*PushEvent, error) {
    sigJSON, err := ui.MarshalCardSignals(view)
    if err != nil {
        return nil, fmt.Errorf("marshal card signals: %w", err)
    }

    blocks := make([]BlockPatch, 0, 16)

    // Helper: render a templ component to a string and append to blocks.
    add := func(selector string, cmp templ.Component) error {
        var buf bytes.Buffer
        if err := cmp.Render(context.Background(), &buf); err != nil {
            return err
        }
        blocks = append(blocks, BlockPatch{Selector: selector, HTML: buf.String()})
        return nil
    }

    cardSel := fmt.Sprintf(`.card[data-device=%q]`, name)

    // Info, energy: per-block read patches (no edit variants).
    if err := add(cardSel+` [data-block="info"]:not([data-edit])`, templates.InfoDetails(view)); err != nil {
        return nil, err
    }
    if err := add(cardSel+` [data-block="energy"]:not([data-edit])`, templates.EnergyBlock(view.Name, view.Energy)); err != nil {
        return nil, err
    }

    // Schedule (read variant only — edit variant carries data-edit and is skipped by the selector).
    if err := add(cardSel+` [data-block="schedule"]:not([data-edit])`, templates.ScheduleBlock(view.Name, view.Schedule, view.Stale)); err != nil {
        return nil, err
    }

    // Controls (data-edit set reactively from $editor signal; skipped while preset editor open).
    if err := add(cardSel+` [data-block="controls"]:not([data-edit])`, templates.ControlsBlock(view)); err != nil {
        return nil, err
    }

    // Sensor cells: 12 individual patches. Threshold cells (co2/voc/humidity)
    // get edit-mode exclusion via :not([data-edit]); plain cells don't have an
    // edit variant but use the same selector shape for uniformity.
    cellPatches := sensorCellPatches(view.Name, view.Sensors)
    for _, p := range cellPatches {
        sel := fmt.Sprintf(`%s [data-sensor-cell="%s"]:not([data-edit])`, cardSel, p.Key)
        if err := add(sel, p.Component); err != nil {
            return nil, err
        }
    }

    return &PushEvent{
        DeviceName:  name,
        SignalsJSON: sigJSON,
        Blocks:      blocks,
    }, nil
}

type sensorCellPatch struct {
    Key       string
    Component templ.Component
}

// sensorCellPatches returns the 12 cells in stable order. The first
// three (co2/voc/humidity) are the editable threshold cells; the rest
// are plain readings.
func sensorCellPatches(name string, s ui.SensorsView) []sensorCellPatch {
    return []sensorCellPatch{
        {"co2", templates.CO2Cell(name, s)},
        {"voc", templates.VOCCell(name, s)},
        {"humidity", templates.HumidityCell(name, s)},
        {"recovery", templates.PlainSensorCellTpl("recovery", "recovery", ui.FmtOptPct(s.RecoveryPct))},
        {"supply", templates.PlainSensorCellTpl("supply", "supply", ui.FmtTempC(s.TempOutdoorC))},
        {"exhaust", templates.PlainSensorCellTpl("exhaust", "exhaust", ui.FmtTempC(s.TempExhaustInletC))},
        {"supply_regen", templates.PlainSensorCellTpl("supply_regen", "supply_regen", ui.FmtTempC(s.TempSupplyC))},
        {"exhaust_regen", templates.PlainSensorCellTpl("exhaust_regen", "exhaust_regen", ui.FmtTempC(s.TempExhaustOutC))},
        {"delta_supply", templates.PlainSensorCellTpl("delta_supply", "Δ", ui.TempDeltaStr(s.TempSupplyC, s.TempOutdoorC))},
        {"delta_exhaust", templates.PlainSensorCellTpl("delta_exhaust", "Δ", ui.TempDeltaStr(s.TempExhaustOutC, s.TempExhaustInletC))},
        {"supply_rpm", templates.PlainSensorCellTpl("supply_rpm", "supply rpm", ui.RPMStr(s.SupplyRPM))},
        {"exhaust_rpm", templates.PlainSensorCellTpl("exhaust_rpm", "exhaust rpm", ui.RPMStr(s.ExtractRPM))},
    }
}
```

The helpers `PlainSensorCellTpl`, `FmtOptPct`, `FmtTempC`, `TempDeltaStr`, `RPMStr` need to be exported. Move them from `sensors_block.templ` (where they're lowercase package-private functions) into `cmd/breezyd/ui/view.go` (or a new `cmd/breezyd/ui/format.go`) as exported functions, and add an exported wrapper templ `PlainSensorCellTpl`. Update `sensors_block.templ` to delegate to the new exported helpers.

(This is a small refactor; alternatively, keep the helpers unexported and move `buildPushEvent` into the `templates` package next to them. Pick whichever the implementer prefers — both are valid; the plan author leans toward exporting the helpers so push_render.go stays in `package main` next to push_hub.go.)

- [ ] **Step 3: Export `infoDetails` from device_card.templ as `InfoDetails`.**

The `@infoDetails(v)` extracted in Task 2 was lowercase; rename to `InfoDetails` (capitalize) so `push_render.go` can call `templates.InfoDetails(view)`.

Edit `device_card.templ`: rename `templ infoDetails` → `templ InfoDetails`, and update the call site `@infoDetails(v)` → `@InfoDetails(v)`.

Run `just generate`.

- [ ] **Step 4: Capitalize `controlsBlock` to `ControlsBlock`.**

`push_render.go` calls `templates.ControlsBlock(view)`. The current name is lowercase. Rename in `controls_block.templ` and update the call site in `device_card.templ`. Run `just generate`.

- [ ] **Step 5: Wire main.go.**

Find the existing `NewPushHub` call in `cmd/breezyd/main.go` (it currently passes a `func(name, snap) (templ.Component, error)`). Replace with:

```go
hub := NewPushHub(func(name string, snap Snapshot) (*PushEvent, error) {
    view := h.buildView(name, snap) // assuming a view-builder helper exists; otherwise use snapshotToView + augment with energy/schedule
    return buildPushEvent(name, view)
})
```

Inspect main.go for the exact current shape and adapt. The `buildView` may live in `handlers_ui_read.go` — read it and reuse if it already augments the snapshot with Energy and Schedule.

If no `buildView` exists, define one in `cmd/breezyd/ui_view.go`:

```go
// BuildView is the canonical Snapshot → DeviceView path used by both
// HTTP read handlers and the push hub.
func (h *Handler) BuildView(name string, snap Snapshot) ui.DeviceView {
    v := snapshotToView(name, snap)
    if t := h.EnergyTrackers[name]; t != nil {
        v.Energy = energyViewFrom(t.Snapshot())
    }
    if s := h.Schedulers[name]; s != nil {
        v.Schedule = scheduleViewFrom(s.Snapshot())
    }
    return v
}
```

Adapt to the actual handler-state types (`h.EnergyTrackers`, `h.Schedulers`) — read main.go and handlers_ui_read.go to confirm names.

- [ ] **Step 6: Update `push_hub_test.go` — render closure signature changed.**

The existing test passes `func(name, snap) (templ.Component, error)` to `NewPushHub`. Update it to the new signature:

```go
hub := NewPushHub(func(name string, _ Snapshot) (*PushEvent, error) {
    return &PushEvent{
        DeviceName:  name,
        SignalsJSON: []byte(`{"stale":false}`),
        Blocks:      []BlockPatch{{Selector: "#stub-" + name, HTML: "<div>" + name + "</div>"}},
    }, nil
})
```

Update assertions accordingly: instead of asserting on rendered HTML, assert on `event.Blocks[0].HTML` etc.

- [ ] **Step 7: Update `handlers_ui_sse_test.go` — `newSSETestHandler` builder.**

Similarly update `newSSETestHandler` to use the new signature:

```go
h.PushHub = NewPushHub(func(name string, _ Snapshot) (*PushEvent, error) {
    return &PushEvent{
        DeviceName:  name,
        SignalsJSON: []byte(`{"stale":false,"speedMode":"manual","airflowMode":"ventilation","lastPollAge":"","sensorsAlert":false}`),
        Blocks:      []BlockPatch{{Selector: `.card[data-device="` + name + `"]`, HTML: `<div class="card" data-device="` + name + `"></div>`}},
    }, nil
})
```

The existing tests in `handlers_ui_sse_test.go` will need their assertions updated when Task 5 lands (since the SSE handler will emit different events). For now, the stub here keeps them compiling.

- [ ] **Step 8: Run package tests.**

```sh
go test ./cmd/breezyd/ -run "PushHub|Notify" -v
```

Expected: PASS.

```sh
go build ./...
```

Expected: build succeeds. (The full `go test ./...` won't pass yet because the SSE handler still uses the old single-render path — Task 5 fixes that.)

- [ ] **Step 9: Commit.**

```sh
git add cmd/breezyd/push_hub.go cmd/breezyd/push_render.go cmd/breezyd/main.go cmd/breezyd/ui_view.go cmd/breezyd/push_hub_test.go cmd/breezyd/handlers_ui_sse_test.go cmd/breezyd/ui/templates/ cmd/breezyd/ui/view.go
git commit -m "$(cat <<'EOF'
push: structured PushEvent — signals JSON + per-block (selector, html) (#65)

PushHub.Notify now produces a PushEvent carrying the per-card datastar
signals payload and a list of BlockPatches. The SSE handler will turn
each into one datastar-patch-signals + N datastar-patch-elements events.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: SSE handler — signal patch + per-block patches + reconnect-aware initial state

**Goal:** Transform `getUISSE` to (a) detect cold-load vs reconnect via `Last-Event-ID`, (b) emit one `datastar-patch-signals` plus N `datastar-patch-elements` per push event.

**Files:**
- Modify: `cmd/breezyd/handlers_ui_sse.go`
- Test: `cmd/breezyd/handlers_ui_sse_test.go`

**Acceptance Criteria:**
- [ ] On a fresh connect (no `Last-Event-ID` header), each device's full card is emitted with `mode=append` against `#device-list`. (Existing behavior, preserved.)
- [ ] On reconnect (`Last-Event-ID` header present), each device's full card is emitted with `mode=outer` selector `.card[data-device="<name>"]`. Replaces existing in-place; no duplicates.
- [ ] Steady-state push: one `datastar-patch-signals` event with the device's CardSignals JSON, followed by one `datastar-patch-elements` event per `BlockPatch` (selector + mode=outer + HTML).
- [ ] Each emitted event has an `id:` field set to the device name (so `Last-Event-ID` is non-empty after the first event — that's all we need; we don't implement replay).
- [ ] Existing tests in `handlers_ui_sse_test.go` updated and PASS.
- [ ] New test asserts:
  - Cold load: `mode=append` (verified via wire bytes — `event: datastar-patch-elements` with `data: mode append`).
  - Reconnect: when request carries `Last-Event-ID: alpha`, response uses `mode=outer` for the alpha card.
  - Steady-state push: one `event: datastar-patch-signals` followed by N `event: datastar-patch-elements` per push.

**Verify:** `go test ./cmd/breezyd/ -run "GetUISSE" -v` → all PASS.

**Steps:**

- [ ] **Step 1: Read `handlers_ui_sse.go` and the datastar-go SDK to confirm option names.**

```sh
grep -n "WithSelector\|WithMode\|PatchSignals\|EventID\|WithSSEEventId" /home/hugh/go/pkg/mod/github.com/starfederation/datastar-go@v1.2.1/datastar/*.go
```

Confirm: `sse.PatchSignals(jsonBytes, opts...)` exists; `WithPatchSignalsEventID(id string)` exists; for elements, the SDK uses `WithSSEEventId` (lower-case d at end of `Id`) — verify via grep. The datastar-go-1.2.1 SDK signatures are the authoritative reference.

- [ ] **Step 2: Rewrite `getUISSE` in `cmd/breezyd/handlers_ui_sse.go`.**

Replace the existing function body with:

```go
func (h *Handler) getUISSE(w http.ResponseWriter, r *http.Request) {
    if h.PushHub == nil {
        http.Error(w, "push hub not configured", http.StatusInternalServerError)
        return
    }
    hub, ok := h.PushHub.(*PushHub)
    if !ok {
        http.Error(w, "push hub of wrong type", http.StatusInternalServerError)
        return
    }

    rc := http.NewResponseController(w)
    if err := rc.SetWriteDeadline(time.Time{}); err != nil {
        slog.Debug("sse: clear write deadline", "err", err)
    }

    sse := datastar.NewSSE(w, r)

    // Reconnect detection: EventSource auto-resends Last-Event-ID after a drop.
    // We don't implement replay — we just use the header's presence as a binary
    // cold-load-vs-reconnect signal so the initial-state pass can avoid
    // duplicating cards on reconnect.
    isReconnect := r.Header.Get("Last-Event-ID") != ""

    for _, view := range h.collectViews() {
        if err := h.emitInitialCard(sse, view, isReconnect); err != nil {
            slog.Debug("sse initial: patch failed", "err", err, "device", view.Name)
            return
        }
    }

    sub := hub.Subscribe()
    defer hub.Unsubscribe(sub)

    keepalive := time.NewTicker(keepaliveInterval)
    defer keepalive.Stop()

    for {
        select {
        case <-r.Context().Done():
            return
        case ev, ok := <-sub.Events:
            if !ok {
                return
            }
            if err := h.emitPushEvent(sse, ev); err != nil {
                slog.Debug("sse: patch failed", "err", err, "device", ev.DeviceName)
                return
            }
        case <-keepalive.C:
            if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
                return
            }
            if err := rc.Flush(); err != nil {
                return
            }
        }
    }
}

// emitInitialCard sends the full card for one device. Cold load uses
// mode=append against #device-list (the card doesn't exist yet);
// reconnect uses mode=outer against .card[data-device=...] to replace
// the existing card in-place without duplicating.
func (h *Handler) emitInitialCard(sse *datastar.ServerSentEventGenerator, view ui.DeviceView, isReconnect bool) error {
    if isReconnect {
        return sse.PatchElementTempl(
            templates.DeviceCard(view),
            datastar.WithSelectorf(`.card[data-device=%q]`, view.Name),
            datastar.WithModeOuter(),
            datastar.WithSSEEventId("device:"+view.Name),
        )
    }
    return sse.PatchElementTempl(
        templates.DeviceCard(view),
        datastar.WithSelector("#device-list"),
        datastar.WithModeAppend(),
        datastar.WithSSEEventId("device:"+view.Name),
    )
}

// emitPushEvent dispatches one PushEvent: the signals patch first
// (so card-outer reactive bindings update before any block content),
// then one elements patch per block.
func (h *Handler) emitPushEvent(sse *datastar.ServerSentEventGenerator, ev PushEvent) error {
    if len(ev.SignalsJSON) > 0 {
        if err := sse.PatchSignals(ev.SignalsJSON,
            datastar.WithPatchSignalsEventID("signals:"+ev.DeviceName),
        ); err != nil {
            return err
        }
    }
    for _, b := range ev.Blocks {
        if err := sse.PatchElements(
            b.HTML,
            datastar.WithSelector(b.Selector),
            datastar.WithModeOuter(),
            datastar.WithSSEEventId("block:"+ev.DeviceName),
        ); err != nil {
            return err
        }
    }
    return nil
}
```

(Verify the actual SDK option names with the grep from Step 1 — `WithSSEEventId` may be `WithEventID` depending on SDK version; adjust accordingly.)

- [ ] **Step 3: Update existing `TestGetUISSE_InitialStateAndPush` to match new event shape.**

The current test asserts on `data-device="bravo"` substrings in the wire bytes. With the structured event, the stub `newSSETestHandler` from Task 4 already produces a `<div class="card" data-device="X"></div>` block, so the substring assertions should still pass for cold-load.

But the test does `Notify("charlie", Snapshot{})` and expects `data-device="charlie"` in the body. Update the stub to ensure Notify's PushEvent includes that substring in `Blocks[0].HTML`.

Re-run the test:

```sh
go test ./cmd/breezyd -run TestGetUISSE_InitialStateAndPush -v
```

If it fails, inspect what wire format the new handler emits (add a `t.Logf("body=%q", body)`) and adjust the substring needles.

- [ ] **Step 4: Add a reconnect test.**

Append to `handlers_ui_sse_test.go`:

```go
func TestGetUISSE_ReconnectUsesOuterMode(t *testing.T) {
    h := newSSETestHandler(t, "alpha", "bravo")
    srv := httptest.NewServer(h.mux())
    defer srv.Close()

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
    req.Header.Set("Last-Event-ID", "device:alpha") // simulate browser auto-reconnect
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        t.Fatalf("GET /ui/sse: %v", err)
    }
    defer func() { _ = resp.Body.Close() }()

    body := readUntil(t, resp.Body, `data-device="bravo"`, 2*time.Second)

    // Reconnect path uses mode=outer on .card selector — wire format includes
    // a "selector .card[data-device=" data: line.
    if !strings.Contains(body, `selector .card[data-device=`) {
        t.Errorf("reconnect: expected selector-targeted patch; body=%q", body)
    }
    if strings.Contains(body, `selector #device-list`) {
        t.Errorf("reconnect: append-mode used unexpectedly; body=%q", body)
    }
}

func TestGetUISSE_ColdLoadUsesAppendMode(t *testing.T) {
    h := newSSETestHandler(t, "alpha")
    srv := httptest.NewServer(h.mux())
    defer srv.Close()

    ctx, cancel := context.WithCancel(context.Background())
    defer cancel()

    req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
    // No Last-Event-ID — cold load.
    resp, err := http.DefaultClient.Do(req)
    if err != nil {
        t.Fatalf("GET /ui/sse: %v", err)
    }
    defer func() { _ = resp.Body.Close() }()

    body := readUntil(t, resp.Body, `data-device="alpha"`, 2*time.Second)
    if !strings.Contains(body, `selector #device-list`) {
        t.Errorf("cold load: expected append against #device-list; body=%q", body)
    }
}
```

Note: the wire format datastar-go produces for selector and mode is `data: selector <value>` and `data: mode <value>` lines. Verify by reading SDK source `sse.go::elements.go` if needed.

- [ ] **Step 5: Run all SSE tests.**

```sh
go test ./cmd/breezyd -run "GetUISSE" -v
```

Expected: PASS.

```sh
go test ./...
```

Expected: full suite PASS.

- [ ] **Step 6: Hand-test in browser.**

```sh
just build
./breezyd --config <test-config>
# In another terminal:
curl http://localhost:<port>/  # should serve the layout
```

Open `/` in a browser. DevTools → Network → /ui/sse → Response. Confirm:
- The first wire frames include `event: datastar-patch-elements` with `data: mode append`.
- After a poll fires, see one `event: datastar-patch-signals` followed by 16 `event: datastar-patch-elements`.
- Console: no `PatchElementsNoTargetsFound` errors during steady-state polling (when no editor open).
- Open the schedule editor; wait past one poll. Form remains.
- Close DevTools network tab and reopen — confirm Last-Event-ID is sent in the new request.

- [ ] **Step 7: Commit.**

```sh
git add cmd/breezyd/handlers_ui_sse.go cmd/breezyd/handlers_ui_sse_test.go
git commit -m "$(cat <<'EOF'
sse: signal patch + per-block patches; reconnect detect via Last-Event-ID (#65)

Drains the structured PushEvent and emits one datastar-patch-signals
followed by N datastar-patch-elements per push. Initial-state pass
distinguishes cold load (append) from reconnect (outer-replace) using
the Last-Event-ID header so reconnects don't duplicate cards.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: Playwright e2e — editor-preservation tests

**Goal:** Pin the acceptance criteria with browser-driven tests.

**Files:**
- Modify: `tests/ui/dashboard.spec.ts`

**Acceptance Criteria:**
- [ ] Test: open schedule editor → wait 2× `poll_interval` → form is still in DOM with the user's typed values intact.
- [ ] Test: open threshold editor (eCO₂ at minimum) → wait 2× `poll_interval` → input still present with typed value.
- [ ] Test: open preset editor (preset 2) → drag supply slider to a known value → wait 2× `poll_interval` → slider value unchanged.
- [ ] Test: cross-tab — tab A in schedule edit, tab B receives a poll-driven update for any non-edit block. (The existing cross-tab test should still pass.)
- [ ] Test: stale device — when polling stops, `.card.stale` class appears via signal patch (no card-outer HTML re-render).
- [ ] `just test-ui` PASSES with no new flakes.

**Verify:** `just test-ui` → all PASS.

**Steps:**

- [ ] **Step 1: Read existing dashboard.spec.ts patterns for editor open + waits.**

```sh
grep -n "schedule\|threshold\|preset\|edit" /home/hugh/twinfresh/tests/ui/dashboard.spec.ts | head -30
```

Adapt to the project's conventions (timeout helpers, fakedevice admin helpers).

- [ ] **Step 2: Add the schedule editor preservation test.**

Append (or edit existing) in `tests/ui/dashboard.spec.ts`:

```typescript
test("schedule editor survives multiple polls", async ({ page, fakedevice }) => {
  // poll_interval is 1s in the test config — wait through 3 polls.
  await page.goto("/");
  const card = page.locator('.card[data-device="attic"]');
  // Open editor.
  await card.locator('button:has-text("edit schedule")').click();
  const editForm = card.locator('details.schedule[data-edit="true"] form');
  await expect(editForm).toBeVisible();

  // Type a recognizable value into a row's pct input.
  const pctInput = editForm.locator('input[name="pct"]').first();
  await pctInput.fill("77");

  // Wait through 3 poll intervals (3s).
  await page.waitForTimeout(3000);

  // Form must still be present with the typed value.
  await expect(editForm).toBeVisible();
  await expect(pctInput).toHaveValue("77");
});
```

- [ ] **Step 3: Add threshold editor preservation test.**

```typescript
test("threshold editor survives multiple polls", async ({ page, fakedevice }) => {
  await page.goto("/");
  const card = page.locator('.card[data-device="attic"]');
  await card.locator('[data-threshold-cell="co2"] .value-clickable').click();
  const editCell = card.locator('[data-threshold-cell="co2"][data-edit="true"]');
  await expect(editCell).toBeVisible();
  const valueInput = editCell.locator('input[name="value"]');
  await valueInput.fill("999");

  await page.waitForTimeout(3000);

  await expect(editCell).toBeVisible();
  await expect(valueInput).toHaveValue("999");
});
```

- [ ] **Step 4: Add preset editor preservation test.**

```typescript
test("preset editor slider survives multiple polls", async ({ page, fakedevice }) => {
  await page.goto("/");
  const card = page.locator('.card[data-device="attic"]');
  // Click preset 2 chip to open the editor.
  await card.locator('button[aria-pressed]').filter({ hasText: /\/.*\// }).nth(1).click();
  const editor = card.locator('[data-preset-editor="2"]');
  await expect(editor).toBeVisible();

  // Set a recognizable supply slider value via dispatchEvent (the slider has data-on:change__debounce.200ms).
  const supplySlider = editor.locator('input[type="range"]').first();
  await supplySlider.evaluate((el: HTMLInputElement) => {
    el.value = "85";
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

  await page.waitForTimeout(3000);

  await expect(editor).toBeVisible();
  await expect(supplySlider).toHaveValue("85");
});
```

(The exact selector for the preset-2 chip depends on the controls block markup; verify with the rendered DOM during a smoke run.)

- [ ] **Step 5: Add stale-via-signal test.**

```typescript
test("stale class is applied via signal patch (no card re-render)", async ({ page, fakedevice }) => {
  await page.goto("/");
  const card = page.locator('.card[data-device="attic"]');
  await expect(card).not.toHaveClass(/stale/);

  // Tag the card so we can observe DOM identity later.
  await card.evaluate((el) => { (el as HTMLElement).dataset.testTag = "marker-1"; });

  // Stop polling on the fakedevice — daemon will mark stale after >90s.
  // For the test, we use the admin endpoint to fast-forward stale.
  await fakedevice.setStale(true); // assumes admin helper exists; otherwise simulate by dropping device

  // Wait long enough for the next poll cycle to flag stale.
  await expect(card).toHaveClass(/stale/, { timeout: 5000 });

  // The marker tag must still be there — the card was NOT re-rendered.
  const stillTagged = await card.evaluate((el) => (el as HTMLElement).dataset.testTag);
  expect(stillTagged).toBe("marker-1");
});
```

(If `fakedevice.setStale` doesn't exist, define it via the admin surface or simulate by killing the fake device and waiting through the daemon's 90s threshold — likely too slow for a test, so you'll want a shorter test-only stale threshold or an admin endpoint. Inspect `cmd/fakedevice` and `tests/ui/global-setup.ts` for the available helpers.)

- [ ] **Step 6: Run Playwright.**

```sh
just test-ui
```

Expected: all tests PASS, including the new ones.

If a test fails, debug with:
```sh
cd tests/ui && pnpm exec playwright test --debug --grep "schedule editor survives"
```

- [ ] **Step 7: Commit.**

```sh
git add tests/ui/dashboard.spec.ts
git commit -m "$(cat <<'EOF'
tests: editor preservation across polls (schedule/threshold/preset) (#65)

Pins the acceptance criteria for #65: opens each inline editor, waits
through 3 poll intervals, asserts the editor is still in the DOM with
user-typed values intact. Adds a stale-via-signal test that asserts
the card's identity is preserved (no re-render) when the stale class
toggles.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: CHANGELOG + final verification

**Goal:** Document the architectural change in CHANGELOG and run the full pre-push gate to confirm nothing regressed.

**Files:**
- Modify: `CHANGELOG.md`

**Acceptance Criteria:**
- [ ] CHANGELOG entry added under the unreleased section describing the change in user-visible terms ("editors now survive polls") with a note on the architectural shift (per-block patches).
- [ ] `just check-all` PASSES (lint + tests + race + Playwright + templ-drift).
- [ ] No console warnings about `PatchElementsNoTargetsFound` during normal poll cycles (only when an editor is open, which is the design).

**Verify:** `just check-all` → all green.

**Steps:**

- [ ] **Step 1: Update CHANGELOG.md.**

Open `CHANGELOG.md` and add under the unreleased / next-version section:

```markdown
### Fixed
- Inline editors (schedule, threshold, preset slider) no longer get clobbered by poll-driven SSE pushes (#65). Each open editor is preserved across polls until the user saves or cancels. Cross-tab editing semantics preserved: tab A's open editor does not suppress tab B's pushes.

### Changed
- The dashboard's SSE push pipeline now emits one `datastar-patch-signals` event plus per-block `datastar-patch-elements` events instead of one full-card outer patch. Card-outer reactive state (`stale` class, speed/airflow data-attrs, "X ago" stale row, sensors-block alert class) flows through datastar signals; the card outer is never HTML-patched after initial render. SSE reconnects use the `Last-Event-ID` header to detect cold load vs reconnect and avoid duplicating cards.
```

- [ ] **Step 2: Run the full pre-push gate.**

```sh
just check-all
```

Expected: PASS. Investigate any failure before claiming done.

- [ ] **Step 3: Manual smoke test in browser.**

```sh
just build
./breezyd --config <test-config>
```

Open `/` in a browser. DevTools console open. Verify:
- No errors during normal polling.
- Open schedule editor → wait through several polls → form survives. Confirmed.
- Open threshold editor → same. Confirmed.
- Open preset editor → drag a slider mid-edit → value preserved. Confirmed.
- Stale a device (kill the fakedevice or wait past the threshold) → `stale` class appears on the card without re-rendering it (use DevTools "Break on subtree modifications" on the card to verify).

- [ ] **Step 4: Commit and push.**

```sh
git add CHANGELOG.md
git commit -m "$(cat <<'EOF'
changelog: SSE editor preservation + per-block patches (#65)

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
git push
```

- [ ] **Step 5: Open PR.**

```sh
gh pr create --title "fix(ui): preserve inline editors across SSE polls (closes #65)" --body "$(cat <<'EOF'
## Summary
- Per-block SSE patches replace the single outer-card push so an open editor (schedule, threshold, preset) survives polls.
- Card-outer reactive state moves into per-card datastar signals; the card outer is never HTML-patched after initial render.
- SSE reconnect uses Last-Event-ID to avoid duplicating cards.

Closes #65.

## Test plan
- [x] `just check-all` (lint + tests + race + Playwright + templ-drift)
- [x] Manual: schedule editor survives 5+ polls
- [x] Manual: threshold editor survives 5+ polls
- [x] Manual: preset editor slider value survives 5+ polls
- [x] Manual: cross-tab editing — tab A edit doesn't suppress tab B updates
- [x] Manual: stale device → stale class via signal (no re-render)

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

Then auto-merge per project convention:

```sh
gh pr merge <PR-number> --squash --auto
```

---

## Self-Review Checklist (run before handoff)

- [ ] **Spec coverage:** Each spec section has a task — DOM contract (Task 2 & 3), components (Task 4), data flow (Task 5), edge cases (covered across Tasks 4–5), testing (Tasks 1, 3, 5, 6).
- [ ] **Placeholder scan:** No "TBD", "TODO", "implement later". The "verify SDK option name" in Task 5 Step 1 is a *concrete grep step*, not a placeholder.
- [ ] **Type consistency:** `BlockPatch{Selector, HTML}` and `PushEvent{DeviceName, SignalsJSON, Blocks}` are used identically in tasks 4 and 5. `CardSignals` field names are stable across Tasks 1, 2, and 3 (json tags `stale`, `speedMode`, `airflowMode`, `lastPollAge`, `sensorsAlert`).
- [ ] **Render closure signature change** propagates through both `push_hub_test.go` (Task 4 Step 6) and `handlers_ui_sse_test.go` (Task 4 Step 7).
- [ ] **Templ regeneration:** every templ edit task ends with `just generate`.
- [ ] **Project rules:** datastar / templ / sse-events skills referenced; pnpm not npm; just recipe added if a check combo is repeated.
