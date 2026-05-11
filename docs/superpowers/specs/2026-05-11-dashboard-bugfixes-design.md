# Dashboard bugfix bundle — design

Date: 2026-05-11
Status: Designed.

## Goal

Four user-reported dashboard bugs, all touching the same `cmd/breezyd/ui/templates/` surface. Bundled in one PR, one commit per fix for review-clarity.

| # | Bug | Fix surface |
|---|---|---|
| 1 | a11y: power button inside `<summary>` (3 flags) | templ + CSS |
| 2 | Toggling a section flips the same section on every card (`$detailsOpen` is page-global) | templ binding rename + signal seed restructure |
| 3 | Manual slider `<span class="val">` doesn't update until release | templ adds a local signal bound to the slider |
| 4 | Schedule `enabled` checkbox not editable unless "edit schedule" entered | new endpoint + templ change + scheduler method |

All four are user-visible regressions or paper-cuts in the dashboard. None touch the protocol library, the CLI, or the `/v1` JSON surface.

## Fix 1 — power button out of `<summary>`

### Problem

`cmd/breezyd/ui/templates/device_card.templ::InfoDetails`:

```templ
<summary data-on:click="$detailsOpen.info = !$detailsOpen.info">
    <h2>{ v.Name }</h2>
    <button
        type="button"
        class="toggle toggle-inline"
        data-on:click__stop={ powerButtonExpr(v) }
        ...
    >power</button>
</summary>
```

The HTML spec disallows interactive descendants in `<summary>`. Three a11y flags = one per device card.

### Fix

Move the `<button>` to a sibling element OUTSIDE `<summary>` but INSIDE `<details class="device-info">`. CSS positions it absolutely in the top-right of the details container so the visual layout is preserved.

```templ
templ InfoDetails(v ui.DeviceView) {
    <details
        id={ "info-" + v.Name }
        class={ "device-info", templ.KV("alert", v.NeedsAttention) }
        data-block="info"
        data-attr:open={ detailsOpenBinding(v.Name, "info") }
    >
        <summary data-on:click={ detailsOpenToggle(v.Name, "info") }>
            <h2>{ v.Name }</h2>
        </summary>
        <button
            type="button"
            class="power-toggle"
            data-on:click={ powerButtonExpr(v) }
            if v.Stale { disabled }
            aria-pressed={ boolAttr(v.Power) }
        >power</button>
        @kvRow("ip", v.IP)
        ...
    </details>
}
```

(Bindings via the new `detailsOpenBinding` / `detailsOpenToggle` helpers — see Fix 2.)

`data-on:click__stop` modifier becomes unnecessary; the button is no longer a descendant of summary so clicks don't bubble through the summary's toggle handler.

CSS in `cmd/breezyd/ui/style.css`:

```css
details.device-info {
    position: relative;
}
details.device-info > summary {
    padding-right: 5rem;  /* clear the absolutely-positioned power button */
}
.power-toggle {
    position: absolute;
    top: 0.5rem;
    right: 0.5rem;
}
```

`details.device-info` already has the position-needing children pattern; if it's already `position: relative`, the rule is a no-op. The `padding-right` ensures the device name `<h2>` doesn't run under the button on narrow viewports.

### Behavior preservation

- Clicking the device name (in `<summary>`) still toggles the details panel.
- Clicking "power" still POSTs `{on: !v.Power}` to `/ui/devices/{name}/power`.
- The button stays disabled when `v.Stale`.
- `aria-pressed` still reflects current power state.

The change is purely structural; no test should change behavior. `render_test.go::TestRenderControlsBlock_StaleDisablesEveryControl` and the golden tests will re-record cleanly via `go test -update`; inspect the diff for whitespace-only changes (and the structural button-relocation).

## Fix 2 — per-card `$detailsOpen` namespace

### Problem

`device_card.templ::initialCardSignals` writes:

```go
"detailsOpen": map[string]bool{
    "info":     false,
    "sensors":  true,
    "energy":   false,
    "schedule": false,
},
```

Three cards × four sections all write to the same global keys `$detailsOpen.info` / `.sensors` / `.energy` / `.schedule`. Toggling Sensors on card A flips it on B and C.

### Fix

Namespace by device. Each card writes only its own subtree:

```go
"detailsOpen": map[string]map[string]bool{
    v.Name: {"info": false, "sensors": true, "energy": false, "schedule": false},
},
```

Datastar's deep-merge semantics means three cards seeding three sibling subtrees coexist:

```
$detailsOpen.bedroom.info, $detailsOpen.bedroom.sensors, ...
$detailsOpen.office.info, $detailsOpen.office.sensors, ...
$detailsOpen.playroom.info, $detailsOpen.playroom.sensors, ...
```

### Binding helpers

Add two helpers in `device_card.templ` (or `helpers.templ`):

```go
// detailsOpenBinding returns the datastar expression that reads the
// open-state signal for the given device's section. Per-card scoped so
// toggling one card's section doesn't flip the same section on
// sibling cards. See #25.
func detailsOpenBinding(deviceName, section string) string {
    return fmt.Sprintf("$detailsOpen.%s.%s", deviceName, section)
}

// detailsOpenToggle returns the datastar expression that flips the
// open-state signal for the given device's section.
func detailsOpenToggle(deviceName, section string) string {
    return fmt.Sprintf("$detailsOpen.%s.%s = !$detailsOpen.%s.%s",
        deviceName, section, deviceName, section)
}
```

Every existing call site:

- `device_card.templ::InfoDetails` — `data-attr:open` + `<summary data-on:click>` for "info"
- `sensors_block.templ::SensorsBlock` — same shape for "sensors"
- `energy_block.templ::EnergyBlock` — same for "energy"
- `schedule_block.templ::ScheduleBlock` — same for "schedule"

becomes:

```templ
data-attr:open={ detailsOpenBinding(v.Name, "info") }
<summary data-on:click={ detailsOpenToggle(v.Name, "info") }>
```

### Device name as signal-path segment

Datastar evaluates `$detailsOpen.<v.Name>.info` as a fixed dot-path at attribute parse time. The device name is embedded as a Go-string into the templ output before datastar sees it.

Device names must be valid JS identifier segments (alphanumeric + `_`). The config-loader at `internal/config.Load` currently only enforces collision-avoidance with reserved global verbs; it does NOT restrict characters. Names like `my-device` would produce `$detailsOpen.my-device.info`, which datastar parses as subtraction (not nested access) and silently breaks.

**Required as part of this fix**: extend the name validation in `internal/config.Load` to reject any name not matching `^[A-Za-z_][A-Za-z0-9_]*$`. Error message: `device name %q must be a valid identifier (letters / digits / underscore; starts non-digit)`. Update `CLAUDE.md`'s "Reserved global names cannot be used as device names" line to mention the character restriction.

Existing configs with restrictive names (mine — bedroom/office/playroom) keep working. Configs with `my-device` etc. would fail loading after this change — that's a deliberate breaking-of-bad-configs to prevent silent dashboard breakage. Note this in the v2.0 → v2.1 changelog when shipping.

### Render test impact

`render_test.go::TestLayout` and the golden files pin specific binding strings (`data-attr:open="$detailsOpen.info"`). After the rename, those become `$detailsOpen.bedroom.info` / `$detailsOpen.alpha.info` (whichever device name the test uses). Re-record via `go test -update`; inspect the diff to confirm only the binding strings changed.

## Fix 3 — live manual slider pct

### Problem

`controls_block.templ::manualSliderRow`:

```templ
<input type="range" ... value={ fmt.Sprintf("%d", v.ManualPct) } data-on:change__debounce.200ms={ ... } />
<span class="val">{ fmt.Sprintf("%d%%", v.ManualPct) }</span>
```

The val span renders the server-side `v.ManualPct` once and stays stale until the next SSE patch arrives (which only happens after debounce + POST + next poll).

### Fix

Bind the slider to a local datastar signal (underscore-prefixed so it doesn't ship to the server in action bodies):

```templ
<input
    type="range"
    name="manual"
    min="10"
    max="100"
    step="1"
    data-bind:_manualPct
    value={ fmt.Sprintf("%d", v.ManualPct) }
    data-on:change__debounce.200ms={ postActionExpr("/ui/devices/"+v.Name+"/speed", "{manual: evt.target.valueAsNumber}") }
/>
<span class="val" data-text="$_manualPct + '%'"></span>
```

Seed `$_manualPct` in `initialCardSignals`:

```go
s["_manualPct"] = v.ManualPct
```

### Per-card scope

Same constraint as Fix 2: each card needs its own `$_manualPct`. Use the same nested-per-name pattern that Fix 2 establishes for `$detailsOpen`. Seed:

```go
s["_manualPct"] = map[string]int{v.Name: v.ManualPct}
```

Slider binding:

```templ
data-bind={ fmt.Sprintf("_manualPct.%s", v.Name) }
```

Val span:

```templ
<span class="val" data-text={ fmt.Sprintf("$_manualPct.%s + '%%'", v.Name) }></span>
```

This depends on the identifier-safe-name validation from Fix 2 — same constraint.

### Initial paint

The seed value is `v.ManualPct` (from the server). On first paint, `$_manualPct.<name>` is the server-rendered pct; the val span shows it via `data-text`. User drags → datastar updates `$_manualPct.<name>` on every input event → val span re-renders live. On release, the `data-on:change` POST fires; next poll arrives and updates `v.ManualPct` server-side, but the val span keeps reading from the local signal regardless.

When a poll comes back with a different value (e.g. the user adjusted via the physical panel between drags), the SSE signals patch updates `$_manualPct.<name>` to match — val span updates. The slider thumb position is the trickier case: `data-bind` two-way binding means a signal change would also move the thumb. That's correct behavior — if the server says 60% and the slider was at 50% (stale), thumb moves to 60%. The original `value` attr is only used on initial paint.

### Test impact

`dashboard.spec.ts::manual slider drag posts dragged value (closes #116)` should still pass — it asserts on the POST payload, not on the val display. Adding a new Playwright test for the live val display is a stretch goal; the data-text binding is simple enough that visual confirmation via `just screenshot` is adequate.

## Fix 4 — schedule `enabled` toggle without edit mode

### Problem

In `schedule_block.templ::ScheduleBlock` (the read variant), the enabled checkbox renders with `disabled`. The toggle only works from the editor variant after clicking "edit schedule" → toggle → save.

### Fix

New endpoint `POST /ui/devices/{name}/schedule/enabled` accepting `{"enabled": bool}`. Handler calls a new `Scheduler.SetEnabled(bool)` method that flips ONLY `s.enabled` + persists; doesn't touch entries, `firedAt`, `retry`, or `lastApply`.

Scheduler method:

```go
// SetEnabled flips the schedule's enabled bit and persists. Does NOT
// touch entries, firedAt, retry, or lastApply — the toggle is
// conceptually independent of the schedule's content. See #27.
func (s *Scheduler) SetEnabled(enabled bool) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    s.enabled = enabled
    if err := s.save(); err != nil {
        return fmt.Errorf("schedule: persist: %w", err)
    }
    return nil
}
```

Handler in `handlers_ui_write.go` — uses the existing `postUIWriteJSON` envelope:

```go
// postUISchedEnabled toggles the enabled bit on a device's schedule
// without touching its entries. Lets the dashboard's inline checkbox
// flip the schedule on/off without entering edit mode.
func (h *Handler) postUISchedEnabled(w http.ResponseWriter, r *http.Request) {
    type req struct {
        Enabled *bool `json:"enabled"`
    }
    name := r.PathValue("name")
    if _, ok := h.requireDevice(w, name); !ok {
        return
    }
    var body req
    if !readBody(w, r, &body) {
        return
    }
    if body.Enabled == nil {
        h.uiValidationError(w, r, name, "missing 'enabled' field (true/false)")
        return
    }
    sch, ok := h.Schedulers[name]
    if !ok || sch == nil {
        h.uiValidationError(w, r, name, "schedule not configured for this device")
        return
    }
    if err := sch.SetEnabled(*body.Enabled); err != nil {
        h.uiServerError(w, r, name, err)
        return
    }
    notifyAfterWrite(h.PushHub, h.State, name)
    w.WriteHeader(http.StatusOK)
}
```

Route registration in `server.go::mux`:

```go
mux.HandleFunc("POST /ui/devices/{name}/schedule/enabled", h.postUISchedEnabled)
```

### Templ change

Read variant in `schedule_block.templ::ScheduleBlock`:

```templ
<input
    type="checkbox"
    if s.Enabled { checked }
    data-on:change={ postActionExpr(fmt.Sprintf("/ui/devices/%s/schedule/enabled", name), "{enabled: evt.target.checked}") }
    if stale { disabled }
/>
enabled
```

Drop the `disabled` attribute except when `stale`. Wire `data-on:change` to the new endpoint.

The edit variant's enabled checkbox (inside the form) continues to work via the existing PUT — both paths persist the same bit.

### SSE round-trip

POST returns 200 empty body. `notifyAfterWrite` triggers a PushHub notification → all subscribed dashboards re-render the schedule block with the new enabled state. The user sees their toggle confirmed within one poll cycle.

### Test impact

- Add a Go test for `Scheduler.SetEnabled` (mirror the `Replace` test shape; assert `enabled` flips, entries / firedAt / retry / lastApply unchanged).
- Add a Go test for `postUISchedEnabled` (happy path + missing-field path; mirror the existing `postUI*` test shape).
- Playwright test optional — the click-and-see-it-update is covered by visual inspection. The Go-side coverage pins behavior.

## Files

- `cmd/breezyd/ui/templates/device_card.templ` — Fix 1 + Fix 2 (`initialCardSignals`)
- `cmd/breezyd/ui/templates/sensors_block.templ` — Fix 2 (binding rename)
- `cmd/breezyd/ui/templates/energy_block.templ` — Fix 2 (binding rename)
- `cmd/breezyd/ui/templates/schedule_block.templ` — Fix 2 + Fix 4
- `cmd/breezyd/ui/templates/controls_block.templ` — Fix 3 (slider + initialCardSignals if helpers move there)
- `cmd/breezyd/ui/templates/helpers.templ` — Fix 2 helpers (or inline in device_card.templ)
- `cmd/breezyd/ui/style.css` — Fix 1 (`.power-toggle`, `.device-info` rules)
- `cmd/breezyd/ui/templates/render_test.go` — re-record affected goldens
- `cmd/breezyd/scheduler.go` — Fix 4 (`SetEnabled`)
- `cmd/breezyd/scheduler_test.go` — Fix 4 test
- `cmd/breezyd/handlers_ui_write.go` — Fix 4 (`postUISchedEnabled`)
- `cmd/breezyd/handlers_ui_write_test.go` — Fix 4 test
- `cmd/breezyd/server.go` — Fix 4 route
- `internal/config/config.go` (optional) — name validation for `$detailsOpen.<name>` safety, if not already in place

## Out of scope

- Day-of-week scheduling.
- A11y audit beyond the 3 flagged elements. (If the audit surfaces more, follow-up.)
- Slider value drag-end persistence to localStorage / signal seed. The val display is live; the POST happens on debounced change. That's adequate.
- Rate-limiting the enabled-toggle endpoint. If a user rapid-clicks the checkbox, each click POSTs; the daemon serializes via `s.mu`. No real-world abuse vector.

## Verification

- `just ci` green after each commit and after the bundle.
- Manual click-through via `just screenshot` + visual inspection (or `pnpm exec playwright test --headed`) of:
  - Sensors-section toggle on card A doesn't flip cards B/C.
  - Manual slider drag updates the % text live during drag.
  - Schedule enabled checkbox toggles inline without entering edit mode.
  - Power button keyboard-focusable as a separate tabstop from the summary toggle.

## Commit shape

Four commits on the branch, one per fix, reviewable independently:

1. `fix(ui): move power button out of <summary> (a11y)` — Fix 1
2. `fix(ui): scope $detailsOpen per device card` — Fix 2
3. `fix(ui): live pct display on manual slider drag` — Fix 3
4. `feat(ui): inline-toggle schedule enabled without edit mode` — Fix 4

Order: 1 first (smallest, no functional change), 2 second (touches every block), 3 third (controls only), 4 last (introduces a new endpoint).
