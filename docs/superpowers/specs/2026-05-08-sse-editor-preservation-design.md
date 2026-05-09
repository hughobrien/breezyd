# SSE editor preservation across polls (#65)

## Problem

The dashboard's poll-driven SSE push currently fans out one `datastar-patch-elements` event per device with selector `.card[data-device=%q]` and mode `outer`. That patch unconditionally replaces the entire card on every poll. Any inline editor open inside the card (schedule, threshold, preset) is wiped out within `poll_interval` seconds.

This regressed during the htmx → datastar migration (#58). The pre-migration code (#56, commit `7805059`) had a "pause polls during edit" mechanism that was lost when the edit-state cookie machinery was deleted.

User-visible consequences:

- Schedule editor evaporates mid-edit.
- Threshold editor (eCO₂, VOC, RH) evaporates mid-edit.
- Preset editor's slider value snaps back to the last polled snapshot while the user is dragging.
- Playwright e2e suite races: tests have to beat a 1 s poll, which is unreliable on slower CI runs.

## Goals

- An open editor (schedule, threshold, preset) survives an arbitrary number of polls until the user saves or cancels.
- Cross-tab semantics preserved: tab A's open editor does not suppress tab B's pushes.
- Reconnect-safe: a network blip does not duplicate cards or lose the open editor.
- The fix introduces no new server-side edit-session state.
- Existing acceptance behavior unchanged: action handlers still trigger immediate push, `<details>` open state still preserved, stale styling still propagates.

## Non-goals

- Not adding new editor surfaces. The fix covers the three existing inline editors plus any future editor that follows the same `data-edit` invariant.
- Not changing the poll cadence, the snapshot cache shape, or the action-handler write paths.
- Not solving the schedule pct sync (#44) or any other UX behaviors of the editors themselves — this design preserves them, it doesn't redesign them.

## Approach: per-block content patches + per-card signal patches

The design rests on one separation of concerns:

- **HTML patches deliver content.** Each block of the card (info, energy, sensors-cells, schedule, controls) is patched independently. Each patch's selector excludes elements marked `data-edit`. If a block is in edit mode, its read-variant patch's selector misses, datastar drops the patch, the editor survives.
- **Signal patches carry reactive state.** Card-outer attributes (`stale` class, `data-speed-mode`, `data-airflow-mode`) and the conditional "X ago" stale-row are driven by per-device datastar signals via `data-class:`, `data-attr:`, `data-show`, and `data-text`. The card outer is never patched after initial render.

The unified invariant: **edit-mode wrapping elements carry `data-edit`. Read-mode poll patches target `:not([data-edit])`.**

### Why this approach over alternatives

The issue (#65) sketches three approaches: A (server-side edit-session tracking), B (custom datastar merge plugin), C (per-block patches).

- **A** does not cover the preset editor — it has no server `/edit` GET to set a flag on. To make A handle cross-tab correctly, the server must invent per-tab session IDs and tie them to the long-lived `/ui/sse` connection; this adds state that has to be torn down on every disconnect path.
- **B** couples to datastar internals and has silent failure modes.
- **C**, as elaborated here, is stateless on the server, handles all three editors uniformly, is reconnect-safe, and gives cross-tab the right semantics for free (each browser holds its own DOM and signals).

The cost of C is migration size: the push pipeline is restructured. The end state is cleaner than today.

## Components

### DOM contract

Each card's outer element renders with seed signals and reactive bindings:

```html
<div class="card"
     data-device={ v.Name }
     data-class:stale="$stale"
     data-attr:data-speed-mode="$speedMode"
     data-attr:data-airflow-mode="$airflowMode"
     data-signals={ initialCardSignals(v) }>

  @InfoBlock(...)
  @EnergyBlock(...)
  @SensorsBlock(...)
  @ScheduleBlock(...)        <!-- read variant; carries data-block="schedule" -->
  @ControlsBlock(...)        <!-- carries data-block="controls" + data-attr:data-edit="$editor !== 0" -->

  <div data-show="$stale" class="row">
    <span class="ts red">
      <span data-text="$lastPollAge ? $lastPollAge + ' ago' : 'no poll'"></span>
    </span>
  </div>
</div>
```

Each block-level wrapping element carries:

| Block | Wrapping element | Selector for poll patch | Edit-mode marker source |
|---|---|---|---|
| info | `<details data-block="info">` | `[data-device="X"][data-block="info"]:not([data-edit])` | (no editor — never marked) |
| energy | `<details data-block="energy">` | `[data-device="X"][data-block="energy"]:not([data-edit])` | (no editor) |
| sensors-cell × 12 | `<div data-sensor-cell="<key>">` | `[data-device="X"][data-sensor-cell="<key>"]:not([data-edit])` | `data-edit` set statically on threshold-edit variants of co2 / voc / humidity cells |
| schedule | `<details data-block="schedule">` | `[data-device="X"][data-block="schedule"]:not([data-edit])` | `data-edit` set statically on `ScheduleBlockEdit` |
| controls | `<details data-block="controls">` | `[data-device="X"][data-block="controls"]:not([data-edit])` | `data-attr:data-edit="$editor !== 0"` (reactive datastar binding; preset editor open ↔ marker present) |

Selectors are flat (no descendant combinator) so each per-block patch is a single attribute-set match. `data-device` is duplicated onto each block's wrapping element to keep selectors flat.

### Initial card signals

`initialCardSignals(v)` extends the existing seed (`automode`, `matchSpeeds`, `editor`, `detailsOpen`) with the four card-outer state signals:

```json
{
  "stale": false,
  "speedMode": "manual",
  "airflowMode": "ventilation",
  "lastPollAge": "",
  "automode": false,
  "matchSpeeds": true,
  "editor": 0,
  "detailsOpen": { ... }
}
```

`stale` / `speedMode` / `airflowMode` / `lastPollAge` are populated from the snapshot at first render. Subsequent updates arrive as `datastar-patch-signals` events.

### Push hub

`PushHub` gains a per-device render that produces a structured payload instead of one HTML blob:

```go
type PushEvent struct {
    DeviceName string
    Signals    map[string]any        // {stale, speedMode, airflowMode, lastPollAge}
    Blocks     []BlockPatch          // per-block content
}

type BlockPatch struct {
    Selector string                  // flat attribute selector with :not([data-edit])
    HTML     string
}
```

`Notify(name, snap)` renders each block's templ component to a string and assembles the `BlockPatch` list (info, energy, 12 sensor cells, schedule-read, controls). It also computes the four signal values from the snapshot.

Render errors on any single block drop the whole event — same defense-in-depth as the current single-render Notify.

Hub fan-out (`mu` + drop-oldest backpressure) is otherwise unchanged.

### SSE handler

`getUISSE` changes in two places:

1. **Initial-state pass** — distinguish cold load from reconnect using the `Last-Event-ID` HTTP header (EventSource auto-sends it on reconnect; absent on a fresh tab). Server emits `id:` lines on outbound events so the browser has something to send back.
   - **Cold load** (`Last-Event-ID` absent): for each device, emit one `datastar-patch-elements` event with mode `append` targeting `#device-list`, payload = full card with seed signals. Same as today.
   - **Reconnect** (`Last-Event-ID` present): for each device, emit one `datastar-patch-elements` event with mode `outer` targeting `.card[data-device="X"]`, payload = full card with re-seeded signals. Replaces existing cards in place. If a card was open in an edit variant, the outer-replace clobbers it — this is acknowledged in "Reconnect" below; it's the same UX as a manual refresh, and reconnects are rare events.
2. **Steady-state event** — drain one `PushEvent`, emit one `datastar-patch-signals` event with `Signals`, then loop `Blocks` and emit one `datastar-patch-elements` event per block with mode `outer`. Patch failures abort the connection (same as today).

The keepalive comment-line behavior is unchanged.

### PatchElementsNoTargetsFound noise

When a block is in edit mode, its poll-driven patch has no target. Datastar's default behavior surfaces this as a `PatchElementsNoTargetsFound` event the browser logs at warn level. The Go SDK's patch options include a "no required targets" mode (verify exact name during implementation; see `github.com/starfederation/datastar-go/datastar`). The implementation uses that mode for steady-state poll patches so the missing-target case is silent.

If the SDK does not expose this option cleanly, the design accepts the warn-level log on the assumption it is per-poll-per-edit, not a flood, and noted in CHANGELOG. The implementation plan records "verify SDK option" as the first step that touches the SSE handler.

## Data flow

### Steady-state poll

1. Poller calls `OnPoll(name, snap)` → `PushHub.Notify(name, snap)`.
2. Hub renders 16 templ components and computes 4 signal values; assembles one `PushEvent`.
3. Hub enqueues the event onto each subscriber's bounded channel.
4. SSE handler drains the event:
   - emits one `datastar-patch-signals` carrying the four signal values;
   - emits one `datastar-patch-elements` event per `BlockPatch`.
5. Datastar applies the signals patch (card outer's `data-class:stale` etc. update reactively); applies content patches whose selectors match; drops content patches whose selectors miss because of `data-edit`.

### Edit GET (schedule, threshold)

Unchanged. The handler emits the edit variant via the existing per-block selector (`details.block.schedule` or `[data-threshold-cell="X"]`). The edit variant carries `data-edit`, so subsequent poll patches miss.

### Save / cancel (schedule, threshold)

Unchanged shape. Handler emits the read variant (no `data-edit`). Polls resume patching the block.

### Preset editor open / close

Purely client-side. The `$editor` signal toggles 0 ↔ N. `data-attr:data-edit="$editor !== 0"` on the controls `<details>` toggles the attribute reactively. Polls' controls-block patches hit / miss in step.

### Action handler write (mode, speed, heater, power, etc.)

Unchanged. Handler calls `notifyAfterWrite(name)` which re-runs `Notify(name, snap)` and pushes through the same fan-out path.

### Reconnect

Subscriber drops; SSE handler exits. Browser EventSource auto-reconnects, sending `Last-Event-ID`. New `getUISSE` call runs the reconnect branch of the initial-state pass: for each device, the existing card in the DOM is outer-replaced (refreshing block content and re-seeding signals).

The preset editor's open state survives a reconnect: `$editor` is in datastar's signal store (not in DOM), the outer-replace re-renders the controls block which re-acquires `data-attr:data-edit="$editor !== 0"`, and that still sees `$editor !== 0`.

The schedule and threshold editors do **not** survive a reconnect: their edit variants are server-rendered DOM that the outer-replace overwrites with read variants. This is acknowledged scope: the issue's acceptance criteria is "stays open across polls", not "survives a network disconnect". A reconnect is a rare event; losing the editor on reconnect is the same UX as a tab refresh. Hardening this further would require plumbing edit state into a per-tab signal that the server reads via `Datastar-Signals` request header — out of scope for #65.

## Edge cases

- **Render error on one block**: drop the entire `PushEvent`. Next successful poll re-renders.
- **Patch failure mid-stream**: abort the SSE connection. Browser auto-reconnects. Same as current behavior.
- **Two editors open at once on the same card** (e.g., preset editor open while user clicks "edit schedule"): both their wrapping elements carry `data-edit`. Polls' patches for both blocks miss; patches for the other blocks (info, energy, sensors, controls' non-editor parts) still apply — wait, controls' wrapping element has `data-edit` while preset is open, so its block patch misses. That means the live-readings part of controls (mode chips, heater toggle) freezes while the preset editor is open. **This is acceptable**: the preset editor is a transient, deliberate user action; the wider controls block freezing is a small UX cost paid for the preserved editor state.
- **Stale device** (`v.Stale`): poll's signal patch sets `$stale = true`, `$lastPollAge = "5s"`. Card outer's `data-class:stale` flips. Stale row's `data-show` reveals the row. No HTML patch needed for the stale row itself.
- **Unreachable device** (configured but no snapshot): same as today. `unreachableCard` renders during initial-state append. No subsequent push events arrive (poller has nothing to push). Device transitions to "reachable" once a snapshot exists; the next push includes signals + blocks, and the card upgrades via outer-replace on the next push event, OR via the next initial-state pass on reconnect. Implementation detail: confirm `unreachableCard` is rendered with a structure compatible with subsequent block patches, or always replace it via outer on first successful push.

## Performance

- One push event → one signals patch + 16 content patches per device. Three devices on a 5 s poll: ~10 patch events / second. Each patch is small (one block's HTML). Negligible vs. today's one big patch.
- Channel backpressure unchanged: drop-oldest at the event level.
- Browser side: datastar applies many small selector-targeted patches efficiently; this is its intended use shape.

## Testing

Three tiers, matching the project's existing layout and #64's test-tier audit direction.

### Go templ tests

- Each block's read-variant renders without `data-edit`.
- Each block's edit-variant (schedule, threshold) renders with `data-edit`.
- Card outer renders with seed signals containing `stale`, `speedMode`, `airflowMode`, `lastPollAge`.
- Card outer carries `data-class:stale`, `data-attr:data-speed-mode`, `data-attr:data-airflow-mode`, and the `data-show`/`data-text` stale-row bindings.
- Sensor cells: each plain cell carries `data-sensor-cell="<key>"`; threshold cells (co2 / voc / humidity) read variant lacks `data-edit`, edit variant has `data-edit`.
- Controls block carries `data-attr:data-edit="$editor !== 0"`.

### Go SSE handler tests

- `Notify(name, snap)` produces a `PushEvent` containing the expected 4 signal fields and 16 block patches with the expected selectors.
- The SSE handler emits one `datastar-patch-signals` event followed by 16 `datastar-patch-elements` events for one push event. Use the existing test scaffold in `handlers_ui_sse_test.go`.
- Reconnect path: with a card already simulated in the DOM, the initial-state pass uses outer-mode targeting `.card[data-device="X"]`.

### Playwright e2e

- Acceptance test: open schedule editor, advance time past 2 × `poll_interval`, assert the form is still in the DOM with user-typed `at`/`pct` values intact.
- Same for threshold editor (each of co2 / voc / humidity).
- Same for preset editor: open editor for preset 2, drag the supply slider, wait past 2 × `poll_interval`, slider value preserved.
- Cross-tab: tab A in schedule edit mode does NOT suppress tab B's pushes. (Existing test should still pass.)
- Stale device: simulate a stop in polling via the fakedevice admin surface, assert the `stale` class appears on `.card` and the "no poll" / "X ago" row appears, all driven by signal patches without re-rendering blocks.

The threshold edit-mode flake noted at `tests/ui/dashboard.spec.ts:176` becomes deterministic — the patch is *designed* to miss when in edit mode, so the race is gone.

## Implementation notes

- The implementation must use the **datastar** skill (touching `cmd/breezyd/ui/`) and the **templ** skill (touching `.templ` / `_templ.go`). Per project memory: never edit generated `_templ.go` directly; always run `just generate` before claiming done.
- Use the **systematic-debugging** skill if any block patch unexpectedly clobbers an editor during testing.
- The implementation plan should be written via the `writing-plans` skill once this spec is approved. Plan should sequence: SDK option verification → DOM contract templ changes → push hub restructure → SSE handler restructure → tests.

## Out of scope

- The SSE-reconnect duplicate-card behavior is addressed for the steady-state per-block path (initial-state pass uses outer-then-append). The edge case where the user is actively editing a server-rendered edit variant (schedule, threshold) AND the network blips is acknowledged but not fortified — see "Reconnect" above.
- Per-tab session IDs. Not needed under this design.
- Custom datastar merge plugin. Not needed under this design.
- Refactoring the action handler write path. Unchanged.

## Acceptance (from #65)

These are conditions the implementation must satisfy; the PR closes #65 only when all five are demonstrably true.

- [ ] User opens schedule editor, leaves it idle for >> `poll_interval`, the form is still there with their edits intact.
- [ ] Same for the threshold editor.
- [ ] An e2e Playwright test pins this for schedule, threshold, and preset.
- [ ] Cross-tab: tab A in edit mode does NOT suppress tab B's pushes.
- [ ] No new flakes in `just test-ui`.
