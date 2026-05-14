# Timer duration editor

## Motivation

The night and turbo timer chips in the dashboard's CONTROLS block are
toggle widgets: first click activates, second click deactivates. The
configured per-mode duration (params `0x0302` night, `0x0303` turbo)
is read-only from the dashboard — the user has no way to change it
short of editing the device via its panel or via the raw `breezy set`
CLI verb.

Preset chips already use a different idiom: first click activates, a
re-click on the active chip opens a sub-editor that edits the chip's
configured value. This change brings the timer chips into that idiom
so the dashboard's CONTROLS block has one consistent "click the
active thing to configure it" gesture.

## User-facing behaviour

A timer chip (`night` / `turbo`) responds to clicks as follows:

| Click state | Outcome |
|-------------|---------|
| chip was inactive | Activate the timer (current behaviour preserved). The chip lights up, the live countdown line appears, the duration editor stays closed. |
| chip was active, editor closed for this mode | Open the duration editor for this mode below the countdown line. No POST. |
| chip was active, editor open for this mode | Close the duration editor. No POST. The timer keeps running. |

The chip itself no longer has an in-place deactivate gesture. To stop
an active timer the user clicks any speed-mode chip (preset/manual),
which routes through the existing `speedMode` cascade that clears
`$specialMode`, or powers the device off. Clicking the *other* timer
chip while one is active still switches modes the same way it does
today.

The editor is rendered centred under the countdown line:

```
[ TIMER ]
[ night* ][ turbo ]
night timer active
       [ 1 ] h  [ 30 ] m
```

Two `<input type="number">` elements ("h" and "m" suffix text inline)
sit centred in the timer column.

## Firmware behaviour (verified)

Writing `0x0302` or `0x0303` while the timer is running causes the
firmware to restart the running countdown to the new duration on its
own. Confirmed against the office unit at firmware 0.11:

```
night active, 0x000B = 07:59:48
write 0x0302 = 0500   (5 minutes)
                       ↓  (no other write)
night active, 0x000B = 00:04:46    (counting down normally)
```

The handler therefore does not need to re-write `0x0007` to re-arm
the timer after a duration change. A single param write is enough.

## Validation

| Field | Range |
|-------|-------|
| `mode` | `"night"` or `"turbo"` |
| `hours` | 0–23 |
| `minutes` | 0–59 |
| sum | ≥ 1 minute |

`hours == 0 && minutes == 0` is rejected (snap-to-1m on the client
before posting; server returns 400 if a hand-crafted request slips
through). Anything else out of range returns 400 with the SSE error
envelope into `#global-error-banner`.

## Signals

One new per-device signal, three new per-device client-only signals:

| Signal | Type | Purpose |
|--------|------|---------|
| `$durationEditor.<name>` | `'off' \| 'night' \| 'turbo'` | Which mode's editor is open. Seeded `'off'` on initial render. |
| `$_durationEdit.<name>.night.hours` | int | Editor input value for night-hours. Seeded from `$nightDurationSeconds`. |
| `$_durationEdit.<name>.night.minutes` | int | Editor input value for night-minutes. |
| `$_durationEdit.<name>.turbo.hours` | int | Editor input value for turbo-hours. |
| `$_durationEdit.<name>.turbo.minutes` | int | Editor input value for turbo-minutes. |

The underscore prefix on `$_durationEdit` follows the established
client-only convention (matches `$_manualPct`, `$_attention`): these
keys are not transmitted in `@post` payloads. The `$durationEditor`
signal is server-visible state so a `data-show` binding can react
to it.

All signals are scoped per device (nested under `<name>`) to keep
the multi-card layout's editors independent — same pattern as
`$editor` / `$preset`.

Initial values are seeded in `device_card.templ::initialSignals`
next to `$specialMode`. A `data-effect` re-seeds the input signals
whenever the server-pushed `$nightDurationSeconds` or
`$turboDurationSeconds` changes, so a duration change made elsewhere
(another tab, the device panel) flows into the editor inputs the
next time it opens. The effect skips re-seeding while the editor for
that mode is open so it doesn't fight live editing.

## Editor closure rules

The duration editor stays open only while `$durationEditor.<name>`
matches the active special mode. Implemented as a single
`data-effect` on the timer-group element:

```
data-effect="if ($durationEditor.<name> !== $specialMode.<name>) $durationEditor.<name> = 'off'"
```

This one rule covers every closure path:

- Cascade-driven deactivation (preset/manual/mode click clears
  `$specialMode` through the existing speedMode cascade).
- Power-off click (existing power-off path clears `$specialMode`).
- Server-pushed deactivation (scheduler fires power-off; firmware
  ages out the timer; another client deactivates).
- Server-pushed mode change (another client switches night→turbo;
  the night editor closes because it no longer matches).

The first-click branch of `timerClickExpr` also explicitly sets
`$durationEditor.<name> = 'off'` so the editor for the *other* timer
mode closes synchronously when switching night↔turbo locally, before
the effect runs. No other edits to click handlers or cascades.

## Templ changes

In `cmd/breezyd/ui/templates/controls_block.templ`:

- `timerClickExpr` is rewritten so the re-click branch toggles
  `$durationEditor.<name>` between `<mode>` and `'off'` instead of
  emitting an `@post('/timer', {mode: 'off'})`. The first-click branch
  preserves the activation flow exactly (seed
  `$specialModeRemainingSeconds`, set `$durationEditor.<name> = 'off'`,
  delegate to `clickAction` for the POST).
- A new `timerDurationEditor(v, mode)` component is rendered as a
  sibling of `timerRemaining`, once per mode (night, turbo), both
  inside `.ctrl-group-timer`. `data-show` keys on
  `$durationEditor.<name> === '<mode>'`. Pre-paint style is
  `display:none` so the editor never flashes.
- A new `timerDurationChangeExpr(name, mode)` Go helper builds the
  `data-on:change__debounce.300ms` body for the hour and minute
  inputs: clamps the value, snaps `(0,0)` to `(0,1)`, mirrors into
  `$specialModeRemainingSeconds` and `$<mode>DurationSeconds`, then
  posts `/ui/devices/<name>/timer-duration`.

In `cmd/breezyd/ui/templates/layout.templ` — add the
`.timer-duration-editor` rule to the inline `<style>` block: flex,
`justify-content: center`, small gap, inputs sized to two digits
each, "h" / "m" suffix spans inline. Matches the visual weight of
the `timer-remaining` line above.

## Server changes

New route in `cmd/breezyd/server.go`:

```
mux.HandleFunc("POST /ui/devices/{name}/timer-duration", h.postUITimerDuration)
```

New handler in `cmd/breezyd/handlers_ui_write.go`:

```go
func (h *Handler) postUITimerDuration(w http.ResponseWriter, r *http.Request) {
    type req struct {
        Mode    string `json:"mode"`
        Hours   int    `json:"hours"`
        Minutes int    `json:"minutes"`
    }
    shape := func(q *req) bool {
        if q.Mode != "night" && q.Mode != "turbo" {
            h.uiValidationError(w, r, "", "mode must be 'night' or 'turbo'")
            return false
        }
        if q.Hours < 0 || q.Hours > 23 {
            h.uiValidationError(w, r, "", "hours must be 0..23")
            return false
        }
        if q.Minutes < 0 || q.Minutes > 59 {
            h.uiValidationError(w, r, "", "minutes must be 0..59")
            return false
        }
        if q.Hours == 0 && q.Minutes == 0 {
            h.uiValidationError(w, r, "", "duration must be at least 1 minute")
            return false
        }
        return true
    }
    postUIWriteJSON(h, w, r, shape, func(ctx context.Context, rc *recordingClient, q *req) error {
        return breezy.SetTimerDuration(ctx, rc, q.Mode, q.Hours, q.Minutes)
    })
}
```

New op in `pkg/breezy/ops.go`:

```go
// SetTimerDuration writes the configured duration for one of the
// special-mode timers: param 0x0302 (night) or 0x0303 (turbo). When
// the matching timer is currently active, the firmware restarts the
// running countdown to the new total on its own (verified against
// firmware 0.11). Mode is case-insensitive.
func SetTimerDuration(ctx context.Context, c DeviceClient, mode string, hours, minutes int) error {
    if hours < 0 || hours > 23 || minutes < 0 || minutes > 59 {
        return fmt.Errorf("%w: hours 0..23, minutes 0..59", ErrInvalidArg)
    }
    if hours == 0 && minutes == 0 {
        return fmt.Errorf("%w: duration must be at least 1 minute", ErrInvalidArg)
    }
    var id ParamID
    switch strings.ToLower(mode) {
    case "night":
        id = 0x0302
    case "turbo":
        id = 0x0303
    default:
        return fmt.Errorf("%w: mode must be night or turbo, got %q", ErrInvalidArg, mode)
    }
    return c.WriteParams(ctx, []ParamWrite{{ID: id, Value: []byte{byte(minutes), byte(hours)}}})
}
```

The handler returns 200 + empty body on success (per the
existing `/ui/...` convention; the SSE push refreshes the view).

## Testing

### Go unit tests

- `pkg/breezy/ops_test.go::TestSetTimerDuration_*`
  - Night happy path: emits one `ParamWrite` to `0x0302` with `[min,
    hr]` bytes.
  - Turbo happy path: same but `0x0303`.
  - `hours` out of range, `minutes` out of range, both zero,
    unknown mode: each returns `ErrInvalidArg` and emits no writes.

### Templ render tests

In `cmd/breezyd/ui/templates/render_test.go`:

- The existing `TestTimerBtn_NightClickExpr` (and its turbo sibling)
  has its golden expectation rewritten to match the new click-expr
  shape: same `$specialModeRemainingSeconds` seed, plus the
  `$durationEditor.<name> = 'off'` reset, plus the
  `clickAction` POST. The activation-from-inactive path is still
  what this test exercises.
- A new `TestTimerBtn_NightClickExpr_TogglesEditor_WhenActive` (and
  turbo sibling) covers the re-click-when-active branch: the output
  contains the `$durationEditor` toggle ternary and does NOT contain
  any `@post('/timer'` substring. Implementation note: the timer-btn
  click expression is a single string compiled from both branches
  (a wasActive ternary); these tests assert on substrings of the
  same single output.
- A new `TestTimerDurationEditor_ChangeExpr` covers the editor input
  handler: output contains the clamp+snap arithmetic, the
  optimistic seeds for `$specialModeRemainingSeconds` and
  `$<mode>DurationSeconds`, and the
  `@post('/ui/devices/<name>/timer-duration'` POST.

### Handler tests

In `cmd/breezyd/handlers_ui_write_test.go`:

- Happy path: `POST /ui/devices/{name}/timer-duration` with valid
  body returns 200 and emits one `WriteParams` to the recording
  client with the right param ID and bytes.
- Each validation failure path returns the 400+SSE error envelope.
- Unknown device name → 404 (rides the existing
  `postUIWriteJSON` plumbing).

### Playwright

A new `tests/ui/timer-duration.spec.ts` driving the dashboard
against the memory backend:

1. Click night → chip active, countdown line visible, editor hidden.
2. Click night again → editor opens centred; the two number inputs
   read the device's configured duration.
3. Type a new value → after debounce, a POST to `/timer-duration`
   fires with the right body; the countdown snaps to the new total.
4. Click night a third time → editor closes; chip stays active.
5. Click preset1 while editor open → timer deactivates AND editor
   closes.
6. Click power-off while editor open → editor closes.

## What's deliberately out of scope

- No CLI verb for setting timer duration (the existing `breezy <name>
  set night_duration <hex>` works for shell users; adding a typed
  verb is independent scope).
- No HomeKit exposure for the duration value (the bridge already
  doesn't model timer modes; this stays consistent).
- No quick-preset chips inside the editor (e.g. 15m / 1h / 8h).
  YAGNI — the two-input form covers all common cases and a free-typed
  value is one tap away.
