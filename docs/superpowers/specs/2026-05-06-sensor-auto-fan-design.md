# Sensor auto-fan toggle + threshold input width (issue #34)

## Problem

Issue #34 reports two related defects in the inline threshold editor shipped with #28:

1. **Editable field is too small to hold the full number.** The input is `width: 3rem` which fits ~3 characters. CO₂ thresholds go up to 2000 (4 digits), so values like `1500` or `2000` overflow visually. RH (40–80) and VOC index (50–250) also push the limit on 3-digit values.
2. **No "auto fan" checkbox per sensor.** The firmware has three independent sensor-enable flags (one per sensor) that decide whether crossing the threshold actually triggers a fan boost. The dashboard exposes thresholds but not the enable flags, so the user has to use `breezy <device> set <param-id> <val>` (or the Twinfresh app) to toggle them.

Background:
- The three firmware flags are param IDs `0x000F` (humidity), `0x0011` (CO₂), `0x0315` (VOC), each `uint8` (0/1).
- The poller already reads them and emits Prometheus gauges (`breezy_*_sensor_enabled`), but they're absent from the JSON snapshot's `configured` block and from the CLI surface.
- The firmware's alert byte (`0x0084`) only sets a sensor's alert bit when the sensor is enabled AND the threshold is exceeded — so disabling auto-fan also clears the dashboard's red `alert-fire` colour for that cell on the next poll. No special UI suppression needed.

## Goal

Surface the three sensor-enable flags throughout the stack:

- **Backend**: in the JSON snapshot's `configured` block; in a `SetSensorEnabled`-like write path; in the HTTP endpoint that the dashboard calls.
- **Frontend**: as an `auto fan` checkbox inside the inline threshold editor (the place the user already reaches when changing per-sensor settings).
- **CLI**: as two new per-device verbs (`threshold` and `auto-fan`).

Plus the trivial CSS bump on the inline-editor input width.

## Decisions (locked in during brainstorming)

- **Single HTTP endpoint.** `POST /v1/devices/<name>/threshold` body becomes `{kind, value?, enabled?}`. At least one of `value` and `enabled` must be supplied (else 400). Both, neither field-validated independently. One `WriteParams` call writes 1 or 2 params atomically. Reasoning: the dashboard always edits both at once, and the daemon's UDP mutex serializes anyway.
- **Checkbox lives inside the inline editor** (not a separate always-visible affordance). Click cell value → editor opens with `[input] [☐ auto-fan] ✓ ✕`. ✓ commits whichever fields changed; ✕ cancels both.
- **CLI has two distinct verbs**, not one overloaded one:
  - `breezy <device> threshold <kind> <value>`
  - `breezy <device> auto-fan <kind> on|off`
  
  Cleaner semantics than `threshold humidity 60` vs `threshold humidity off`.
- **Verb name `auto-fan`** matches the dashboard checkbox label and the user's issue text.
- **No alert-color suppression code path.** The firmware's `0x0084` alert byte naturally clears bits when the sensor is disabled; existing `alert-fire` logic continues to work.
- **No status-line render for thresholds or auto-fan.** Out of scope for this issue. `breezy <device> get humidity_threshold` / `get humidity_sensor_enabled` already work via the generic verb.
- **Three-task plan, one PR.** Backend → frontend → CLI, each independently committable.

## Design

### Task 1 — Backend

#### `pkg/breezy/status.go` — surface the three enable flags

In the existing `Configured` population block (around lines 72–80, where the threshold values are read), add:

```go
if b, ok := Uint8At(values, 0x000F); ok {
    resp.Configured["humidity_sensor_enabled"] = b == 1
}
if b, ok := Uint8At(values, 0x0011); ok {
    resp.Configured["co2_sensor_enabled"] = b == 1
}
if b, ok := Uint8At(values, 0x0315); ok {
    resp.Configured["voc_sensor_enabled"] = b == 1
}
```

Place them adjacent to the existing thresholds so `Configured` reads as `humidity_threshold_pct, humidity_sensor_enabled, co2_threshold_ppm, co2_sensor_enabled, voc_threshold_index, voc_sensor_enabled` (i.e. interleaved by sensor, not segregated). Or keep them grouped — implementation detail; the JSON keys are the contract.

#### `pkg/breezy/ops.go` — extend `SetThreshold` to take an optional `enabled`

Current signature: `SetThreshold(ctx, c, kind string, value int) error`.

Two reasonable shapes:
- **(a)** Add a new function `SetSensorEnabled(ctx, c, kind, enabled)`; keep `SetThreshold` unchanged. Two write paths.
- **(b)** Replace `SetThreshold` with a unified function `SetThresholdConfig(ctx, c, kind string, value *int, enabled *bool) error`. Single write path; one `WriteParams` call writing 1–2 params.

I'll take **(b)** because the dashboard always edits both. The unified function:

```go
// SetThresholdConfig writes one or both of: the per-sensor over-threshold
// setpoint and the per-sensor enable flag. At least one of value/enabled
// must be non-nil; otherwise ErrInvalidArg. Both writes (when supplied)
// land in a single WriteParams call so the device sees them atomically.
//
// Kinds (case-insensitive):
//   - "humidity": value 40..80 RH%, enable flag at 0x000F
//   - "co2":      value 400..2000 ppm step 10, enable flag at 0x0011
//   - "voc":      value 50..250 index, enable flag at 0x0315
//
// Out-of-range values and unknown kinds return ErrInvalidArg with no write.
func SetThresholdConfig(ctx context.Context, c DeviceClient, kind string, value *int, enabled *bool) error {
    // … see plan for full body
}
```

Old `SetThreshold(ctx, c, kind, value)` becomes a 1-line wrapper around `SetThresholdConfig` for callers that only set the value (CLI's `set` generic, existing tests). Keeps backward compatibility cheap.

CLI's new `threshold` verb calls `SetThresholdConfig(ctx, c, kind, &value, nil)`. CLI's new `auto-fan` verb calls `SetThresholdConfig(ctx, c, kind, nil, &enabled)`. Dashboard calls it via the HTTP endpoint with both pointers as appropriate.

#### `cmd/breezyd/handlers_device.go` — extend the `/threshold` POST body

Current body: `{kind, value}` with `value` required.

New body: `{kind, value?, enabled?}`. At least one of `value` and `enabled` must be non-null; else 400 with `error: "must supply at least one of value or enabled"`. The handler decodes both into `*int` and `*bool`, calls `SetThresholdConfig`, and on success updates the per-device cache to reflect both writes (parallel to how the existing handler updates the cache for the value-only case).

In daemon mode the dashboard fires this single endpoint; in standalone mode the CLI calls `SetThresholdConfig` directly via `pkg/breezy/ops`.

#### Tests

- `pkg/breezy/ops_test.go`: add tests for `SetThresholdConfig` covering value-only, enabled-only, both, neither (error), out-of-range value, unknown kind.
- `cmd/breezyd/server_test.go`: extend the `/threshold` handler tests to cover enabled-only and value+enabled bodies; add the 400-on-missing-both case.
- `pkg/breezy/status_test.go` (or wherever `BuildStatus` is exercised): assert the three new `*_sensor_enabled` keys surface in `Configured`.

### Task 2 — Frontend

#### CSS

In `cmd/breezyd/ui/index.html`, change `.thresh-edit-inline .thresh-input { width: 3rem }` to `width: 4.5rem`. Restores the legacy editor's input width, fits 4-digit `1500`/`2000` comfortably.

#### Inline editor markup

`thresholdCell` (or wherever the inline editor is rendered) currently emits:

```js
<span class="thresh-edit-inline">
  <input type="number" class="thresh-input" min=… max=… step=… value=… data-name=… data-kind=…>
  <button data-action="threshold-save" data-name=… data-kind=… ${dis}>✓</button>
  <button data-action="threshold-cancel" data-name=… data-kind=… ${dis}>✕</button>
</span>
```

Add a checkbox before the buttons:

```js
<label class="thresh-auto-fan">
  <input type="checkbox" class="thresh-auto-fan-input"
         data-name=… data-kind=… ${enabledNow ? "checked" : ""} ${dis}>
  auto fan
</label>
```

Where `enabledNow` is read from `snap.configured.humidity_sensor_enabled` etc. by the cell renderer.

CSS: a small `.thresh-auto-fan` rule for label spacing (a single `margin-left: 0.4rem; font-size: 0.85rem` or similar — keep it minimal).

#### Save semantics

The existing `saveThreshold(name, kind)` reads the input value and POSTs `{kind, value}`. Extend it to:

1. Compare the input value to `snap.configured.<kind>_threshold_*` — include `value` in the body only when it changed.
2. Compare the checkbox state to `snap.configured.<kind>_sensor_enabled` — include `enabled` in the body only when it changed.
3. If both unchanged — close the editor without POSTing.
4. Otherwise POST the change set and update `editingThreshold[name]` / re-render on success per the existing pattern.

#### Tests (`tests/ui/dashboard.spec.ts`)

Three new tests parallel to the existing threshold-save / threshold-cancel pattern:

1. **Inline editor renders the checkbox; state reflects `configured.<kind>_sensor_enabled`** — given `humidity_sensor_enabled: true`, the checkbox is checked; given `false`, unchecked.
2. **Toggling checkbox + ✓ POSTs `{kind, enabled}` (no `value`)** — when only the enable flag changed.
3. **Editing value + toggling checkbox + ✓ POSTs `{kind, value, enabled}`** — when both changed.

The existing "save POSTs `{kind, value}`" test continues to assert that when only the value changed, the request body has no `enabled` key.

`baseSnapshot` in the test fixture needs the three new `*_sensor_enabled` fields populated (default `true` matches the firmware default and keeps existing tests passing without per-test overrides).

### Task 3 — CLI

#### Two new verbs

In `cmd/breezy/main.go`, add two `case` arms parallel to the existing `"speed"`, `"mode"`, etc.:

```go
case "threshold":
    err = doThreshold(client, args)
case "auto-fan":
    err = doAutoFan(client, args)
```

Implementations in `cmd/breezy/commands.go`:

```go
// breezy <device> threshold <kind> <value>
func doThreshold(client breezy.DeviceClient, args []string) error {
    if len(args) != 2 { return usageErrf("threshold: usage: threshold <kind> <value>") }
    kind := args[0]
    value, err := strconv.Atoi(args[1])
    if err != nil { return usageErrf("threshold: value must be an integer, got %q", args[1]) }
    return breezy.SetThresholdConfig(ctx, client, kind, &value, nil)
}

// breezy <device> auto-fan <kind> on|off
func doAutoFan(client breezy.DeviceClient, args []string) error {
    if len(args) != 2 { return usageErrf("auto-fan: usage: auto-fan <kind> on|off") }
    kind := args[0]
    var enabled bool
    switch strings.ToLower(args[1]) {
    case "on": enabled = true
    case "off": enabled = false
    default: return usageErrf("auto-fan: state must be on or off, got %q", args[1])
    }
    return breezy.SetThresholdConfig(ctx, client, kind, nil, &enabled)
}
```

Both verbs work in standalone mode (the `client` here is a `pkg/breezy.Client` over UDP) and daemon mode (the `client` is a daemon-mode adapter that POSTs `/threshold` on the daemon URL — same pattern as existing write verbs).

#### Tests

`cmd/breezy/main_test.go` gains:

- `TestCLI_Threshold_*` — humidity/co2/voc happy paths; out-of-range value rejected; bad kind rejected.
- `TestCLI_AutoFan_*` — humidity/co2/voc on/off; bad state ("yes") rejected; bad kind rejected.

Both use the same in-process daemon harness as existing CLI tests.

#### Help text

`breezy --help` (or whatever generates the per-verb help) lists the two new verbs alongside the existing per-device verbs. Two lines added.

## Out of scope

- No CHANGELOG entry / version bump (deferred to next release tag).
- No status-line render of thresholds or auto-fan in `breezy <device> status` (separate cleanup).
- No bulk "disable all sensors" toggle.
- No HomeKit exposure.
- No alert-color suppression code (firmware handles this naturally).
- Nothing in `pkg/homekit`, `internal/config`, or the `cmd/breezyd/state.go` storage layer — only the param-snapshot pipeline gains three new keys.

## Rollout order

Three-task plan, executed in order:

1. **Task 1 — backend.** `BuildStatus` exposes 3 enable fields, `SetThresholdConfig` replaces `SetThreshold` (with a 1-liner wrapper for backward compatibility), `/threshold` HTTP body extended, tests.
2. **Frontend** — inline editor checkbox + width fix + tests. Depends on Task 1's JSON fields.
3. **CLI** — two new verbs + tests. Depends on Task 1's `SetThresholdConfig`.

Each task produces one commit and is independently testable and reviewable. Tasks 2 and 3 are independent of each other; either can land first after Task 1 is merged.
