# Optimistic UI updates: cascades + derived signals

## Problem

Every action button in the dashboard suffers the same lag: click → POST → server writes device → snapshot updates → SSE patch arrives → UI snaps. The roundtrip is 200–800ms (worse on UDP retries). During that window the UI shows stale state, and most of that staleness is *cross-signal*: clicking a preset chip while night mode is active correctly clears the timer on the device, but the night-mode chip stays visually pressed until the patch lands.

We've fixed this ad-hoc in two places — `timerClickExpr` optimistically flips `$specialMode` / `$specialModeRemainingSeconds`, and `presetChipExpr` (recently) flips `$editor`. Other click handlers don't optimistically update anything. Each ad-hoc fix is a new place to forget. We want one pattern that catches everything.

## Constraints discovered by probing the office device

A 5-minute probe script (`/tmp/firmware_invariants_test.sh`, run 2026-05-11 against the office Twinfresh Elite 160) established which cross-signal effects are firmware-driven vs. handler-driven. Every test wrote a single param, waited 20s, then read related params:

| Trigger | Effect | Source |
| --- | --- | --- |
| `speed_mode` write (any value) | `timer` clears | **Firmware** |
| `airflow_mode` write | `timer` unchanged | (No cascade) |
| `timer` activate | `power` unchanged (firmware runs fans regardless) | (Handler writes `power=on`) |
| `power` off | `timer` unchanged by firmware | (Handler writes `timer=0`) |

Firmware cascades fire on the device-side write regardless of who sent it (CLI, panel button, IR remote). Handler cascades fire only when the action goes through `breezyd`'s HTTP handler; an external actor like the wall panel can leave the daemon's cache temporarily incoherent (e.g. `timer=turbo, power=off`).

## Design

Two layers. Layer A handles state derivable from a pure function of other signals. Layer B handles state where a write *causes* another signal to change but the new value isn't pure-function-derivable — these need to fire on user click, not on signal-change (a `data-on-signal-patch` watcher would race against server-pushed patches that correctly reflect legitimate states like `speed=preset1, timer=night`).

### Layer A — Derived signals

A small set of JS helpers next to `fmtRemaining` in `layout.templ`. Pure functions; UI bindings call them where the "effective" view of a signal differs from its raw value.

```js
// effPower bridges externally-induced state: panel-button / IR-remote can
// start a timer while $power stays 0 (firmware doesn't auto-power-on and
// only the /timer HTTP handler does). Poll reflects this honestly; effPower
// makes the dashboard show "on" anyway.
function effPower(power, special) { return special !== 'off' || power; }
```

Only one entry today. Add more here as derivations surface.

### Layer B — Single click-action helper + cascade table

Click handlers stop hand-rolling multi-signal logic. Each becomes a one-liner naming its primary intent; cascades come from one table. The cascade map is the single place to forget something, and is covered by a unit test enumerating writable signals.

```go
// cmd/breezyd/ui/templates/cascades.go (new file)

// cascadeFunc returns the JS string for implied signal updates that
// follow a write to its primary signal. The cascade fires AFTER the
// primary write, so it can read $signal.name directly to see the new
// value — no jsValue argument needed, and no risk of operator-precedence
// surprises with ternary toggle expressions.
type cascadeFunc func(name string) string

var cascades = map[string]cascadeFunc{
    // Firmware: any speed_mode write clears the timer (probe 2026-05-11).
    "speedMode": func(name string) string {
        return fmt.Sprintf(
            "$specialMode.%s = 'off'; $specialModeRemainingSeconds.%s = 0;",
            name, name)
    },

    // Handler: activating a timer (non-off) writes power=on. Reads the
    // signal directly so the ternary toggle expression (timer click) is
    // evaluated exactly once — by the primary write — not re-evaluated
    // here.
    "specialMode": func(name string) string {
        return fmt.Sprintf(
            "if ($specialMode.%s !== 'off') { $power.%s = true; }",
            name, name)
    },

    // Handler: power=off explicitly clears the timer. Same direct-read
    // approach.
    "power": func(name string) string {
        return fmt.Sprintf(
            "if (!$power.%s) { $specialMode.%s = 'off'; $specialModeRemainingSeconds.%s = 0; }",
            name, name, name)
    },

    // No cascade (firmware leaves timer alone). Explicit nil so the
    // coverage test recognizes deliberate emptiness vs. omission.
    "airflowMode": nil,
    "heater":      nil,
}

// clickAction builds the data-on:click expression for a button whose
// primary intent is a single optimistic signal write plus a POST. The
// cascade table contributes implied signal updates between the primary
// write and the POST. Order is critical: jsValue is evaluated ONCE into
// __next; primary write uses __next; cascade reads the signal (now
// holding __next); POST payload uses __next directly.
//
// The __next intermediate exists because toggle-style jsValues like
// `!$power.alpha` and `$specialMode.alpha === 'night' ? 'off' : 'night'`
// read the same signal they're about to write. Without __next, the
// payload expression would re-read the just-mutated signal and send
// the wrong value to the server. Callers reference __next in payload
// expressions wherever the payload should equal the new signal value.
//
//   name    — device name (e.g. "alpha")
//   signal  — primary signal key, must appear in cascades
//   jsValue — JS expression for the new value (e.g. "'preset2'", "!$power.alpha")
//   url     — POST endpoint
//   payload — JS object-literal expression for the POST body (or "")
//             May reference `__next` for the just-computed new value.
func clickAction(name, signal, jsValue, url, payload string) string {
    cascade, ok := cascades[signal]
    if !ok {
        panic(fmt.Sprintf("clickAction: unknown signal %q — register in cascades map", signal))
    }
    parts := []string{
        fmt.Sprintf("const __next = %s;", jsValue),
        fmt.Sprintf("$%s.%s = __next;", signal, name),
    }
    if cascade != nil {
        parts = append(parts, cascade(name))
    }
    parts = append(parts, postActionExpr(url, payload))
    return strings.Join(parts, " ")
}
```

The `__next` intermediate is the load-bearing piece for toggle handlers. With it, the timer button's ternary evaluates once (deciding between `'off'` and `'<value>'`), the primary write stores that result, and the POST sends the same value — no risk of the ternary's predicate flipping mid-expression.

Callers whose payload doesn't depend on the new signal value (preset chips: `{preset: 2}`, mode buttons: `{mode: 'ventilation'}`) ignore `__next`. Callers whose payload mirrors the new value (power, heater, timer) use `__next` directly.

### Click-handler call sites

Each rewrite replaces existing multi-step expression building:

```go
// presetBtn: click expression becomes the editor-toggle logic plus clickAction.
return fmt.Sprintf(
    "const wasActive = ($specialMode.%s === 'off' && $speedMode.%s === 'preset%d'); "+
    "$editor.%s = wasActive ? ($editor.%s === %d ? 0 : %d) : 0; "+
    "%s",
    name, name, n, name, name, n, n,
    clickAction(name, "speedMode", fmt.Sprintf("'preset%d'", n),
        fmt.Sprintf("/ui/devices/%s/speed", name),
        fmt.Sprintf("{preset: %d}", n)),
)

// manualBtn:
clickAction(v.Name, "speedMode", "'manual'",
    fmt.Sprintf("/ui/devices/%s/speed", v.Name),
    fmt.Sprintf("{manual: %d}", manualBtnPct(v)))

// modeBtn:
clickAction(v.Name, "airflowMode", fmt.Sprintf("'%s'", value),
    fmt.Sprintf("/ui/devices/%s/mode", v.Name),
    fmt.Sprintf("{mode: '%s'}", value))

// timerBtn: toggle logic moves into the jsValue expression.
clickAction(v.Name, "specialMode",
    fmt.Sprintf("$specialMode.%s === '%s' ? 'off' : '%s'", v.Name, value, value),
    fmt.Sprintf("/ui/devices/%s/timer", v.Name),
    fmt.Sprintf("{mode: $specialMode.%s === '%s' ? 'off' : '%s'}", v.Name, value, value))

// power button (in ControlsBlock):
clickAction(v.Name, "power", fmt.Sprintf("!$power.%s", v.Name),
    fmt.Sprintf("/ui/devices/%s/power", v.Name),
    fmt.Sprintf("{on: !$power.%s}", v.Name))

// heaterBtn:
clickAction(v.Name, "heater", fmt.Sprintf("!$heater.%s", v.Name),
    fmt.Sprintf("/ui/devices/%s/heater", v.Name),
    fmt.Sprintf("{on: !$heater.%s}", v.Name))
```

The `$specialModeRemainingSeconds.X = $<night|turbo>DurationSeconds.X` seeding from today's `timerClickExpr` stays in the timer click handler — it's not a cascade (specialMode change implies it), it's an explicit additional optimistic seed for the countdown display. Keep it inline alongside `clickAction()`.

### Bindings to update

Only one binding switches from a raw signal to a Layer-A derivation:

| Element | Old binding | New binding |
| --- | --- | --- |
| Power button icon / aria-pressed | `$power.X ? 'true' : 'false'` | `effPower($power.X, $specialMode.X) ? 'true' : 'false'` |

Everything else stays on raw signals — Layer B keeps them coherent on user click; SSE patches keep them coherent on poll.

## Why this is "once and for all"

Three signal-write paths exist into the page:

1. **User click** → click handler runs Layer-B `clickAction()` → primary signal + cascades + POST. Cascades fire before any roundtrip.
2. **SSE patch** (data-signals replacement via card morph) → server-authoritative state replaces client state. Always coherent because the server-side cache is updated atomically by `WriteThrough` before the push.
3. **External actor** (panel button, daemon-restart, IR remote) → poller eventually catches up. Layer A handles the only state transition this can leave incoherent (timer-active + power-off → render as "on" via `effPower`).

A new writable signal added to the dashboard MUST be registered in `cascades` (even as `nil`). The coverage test enforces this:

```go
// TestCascadeTable_AllWritableSignalsCovered: every signal name that
// appears as the second argument to a clickAction(...) call must have an
// entry in the cascades map. Today this whitelist is hand-maintained;
// future work could grep call sites via ast.
func TestCascadeTable_AllWritableSignalsCovered(t *testing.T) {
    writable := []string{"speedMode", "specialMode", "power", "heater", "airflowMode"}
    for _, sig := range writable {
        if _, ok := cascades[sig]; !ok {
            t.Errorf("signal %q is written by a click handler but not in cascades — register it (use nil for no cascade)", sig)
        }
    }
}
```

Add a per-cascade behavior test:

```go
// TestCascades_SpeedModeClearsTimer locks in the firmware invariant.
// If a future firmware revision changes this and we remove the cascade,
// this test ensures the rationale is documented in the diff.
func TestCascades_SpeedModeClearsTimer(t *testing.T) {
    got := cascades["speedMode"]("alpha")
    want := "$specialMode.alpha = 'off'; $specialModeRemainingSeconds.alpha = 0;"
    if got != want { t.Errorf("got: %s\nwant: %s", got, want) }
}
// Similar for specialMode (activation → power on) and power (off →
// clear timer). The cascade reads signals post-primary-write, so the
// guard checks the actual signal value rather than a re-evaluated
// jsValue expression.
```

## Migration touchpoints

| File | Change |
| --- | --- |
| `cmd/breezyd/ui/templates/cascades.go` (new) | `cascades` map + `clickAction()` helper. |
| `cmd/breezyd/ui/templates/controls_block.templ` | Rewrite six click handlers (preset, manual, mode, timer, power, heater) to use `clickAction()`. Switch power button `data-attr:aria-pressed` to `effPower(...)`. Remove `presetChipExpr` / `timerClickExpr` / `heaterClickExpr` / `attentionIfOff`'s wrapping of bespoke expression-building (the attention-glow wrapper itself stays). |
| `cmd/breezyd/ui/templates/layout.templ` | Add `effPower` JS helper next to `fmtRemaining`. |
| `cmd/breezyd/ui/templates/render_test.go` | Add `TestCascadeTable_AllWritableSignalsCovered` + `TestCascades_*` per-cascade tests. Replace `TestPresetChipExpr` / similar with a `TestClickAction_*` table-driven test. Regenerate goldens after templ edits. |
| `tests/ui/dashboard.spec.ts` | Add the headline regression test: with night-mode active, click a preset chip; within ~50ms the night chip's `aria-pressed` reads `false`. Locks in that the cascade fires optimistically and isn't gated on the SSE roundtrip. |

## Out of scope

- `$editor` and `$specialModeRemainingSeconds` are client-only / countdown signals, not device-backed. Their seeding stays inline in the click handler that owns them (preset chip owns `$editor`; timer button seeds `$specialModeRemainingSeconds`). They aren't cascades — they're additional optimistic seeds, separate from the cascade table.
- Server-side flow is unchanged: `notifyAfterWrite` + card morph stays as-is. The cache write-through via `breezy.Client` already ensures push payloads are coherent.
- HomeKit accessory, `/v1/devices` JSON, Prometheus metrics: all read from the daemon's cache, not the dashboard's optimistic signals. No change.
- Threshold editor, schedule editor: those have their own client-state lifecycles (`data-edit` attribute on the cell / form) which already work correctly. Not in scope.
- A future generalization where `cascades` is auto-derived from a static analysis of `clickAction` call sites: noted, not built. The hand-maintained whitelist suffices at our six-handler scale.
