# Collapsable Sensors block (issue #29)

## Problem

The dashboard's per-card Sensors block (`cmd/breezyd/ui/index.html`) is a plain `<div class="block">` containing a `<h3>Sensors</h3>` and the 2 × 6 grid of values. It cannot be collapsed. On a card with no current attention demand, it occupies vertical real estate that the user would rather skip past.

Issue #29 asks: make it collapsable, expanded by default, and force-expanded when a sensor value crosses its alert threshold.

## Goal

Sensors block becomes a `<details>` element with the same chevron-on-the-left treatment as the existing Energy block.

- **Default state:** expanded.
- **User can collapse** by clicking the summary; collapse persists across the 5 s polling re-render.
- **Auto-expand when alerting:** if any of `snap.live.sensor_alerts.{humidity, co2, voc}` is true, render `<details open>` regardless of the user's prior collapse state — and the user's collapse intent is preserved (it takes effect again as soon as the alert clears).

Mirrors the existing Service / Device-Info pattern (`deviceInfoOpen[name]` + `needsAttention` override), with default polarity inverted.

## Design

### DOM

Today (`index.html:471–474`):

```html
<div class="block">
  <h3>Sensors</h3>
  ${sensorsGrid(name, snap)}
</div>
```

After:

```html
<details class="block sensors"${expanded ? " open" : ""}>
  <summary><h3>Sensors</h3></summary>
  ${sensorsGrid(name, snap)}
</details>
```

`sensorsGrid()` is unchanged. The threshold-editor inline-edit behavior (just shipped for #28) keeps working — it lives inside `sensorsGrid()`'s output, which is now nested in `<details>` content. Toggling the disclosure does not destroy the inline editor's DOM, but a 5 s render re-emits the whole card so `editingThreshold[name]` continues to drive editor state.

### State

New module-scoped map (sibling to `deviceInfoOpen`, `energyOpen`):

```js
// Per-card Sensors <details> collapsed state. Default is expanded, so we track
// only when the user has explicitly collapsed it. Survives the 5 s grid
// re-render via the toggle listener at the bottom of this file. Auto-expand
// on active alert wins over user collapse — when the alert clears, the
// collapse intent is preserved and re-applies.
const sensorsCollapsed = {}; // name -> true (absent = default expanded)
```

The polarity is inverted from `deviceInfoOpen` because the default is the opposite. Same shape (presence-as-truth, delete-on-clear) so the toggle handler stays symmetric.

### Render computation

In the card render (`renderCard`, currently around `index.html:471`), before emitting the block:

```js
const sensorAlerts = snap.live?.sensor_alerts || {};
const sensorAlerting = sensorAlerts.humidity === true
                    || sensorAlerts.co2 === true
                    || sensorAlerts.voc === true;
const sensorsExpanded = sensorAlerting || !sensorsCollapsed[name];
```

`sensorAlerting` is also already used implicitly inside `thresholdCell()` (`alerting = snap.live?.sensor_alerts?.[cfg.alertKey] === true`) — that's per-cell colouring. The block-level `sensorAlerting` is the OR of the three. Compute it once at the card scope.

### Toggle listener

Extend `index.html:1045–1056` with a `.sensors` branch:

```js
} else if (el.classList?.contains("sensors")) {
  if (el.open) delete sensorsCollapsed[name];
  else sensorsCollapsed[name] = true;
}
```

Inverted relative to the `device-info`/`energy` branches because the state map tracks "collapsed", not "open".

### CSS

The existing `.block.energy` rule (`index.html:79–…`) already styles a left-side chevron on its `<summary>` and aligns the `<h3>` with the rest of the card. Add a parallel rule for `.block.sensors` — mirrors the energy block exactly. Likely just:

```css
details.block.sensors > summary { /* same as .block.energy > summary */ }
details.block.sensors > summary > h3 { /* same */ }
```

If the existing rules can be combined via a shared selector (`.block.energy, .block.sensors`), prefer that — minor DRYing that costs nothing.

The `<summary>` cursor should stay `pointer` (existing default for `<details>` summaries — verify it's not overridden).

## Behavior corners (decided, not open)

- **Alert during user-collapse:** alert force-expands; the user's `sensorsCollapsed[name] = true` is preserved. When the alert clears, the next render returns to the collapsed state. Same precedent as the Service block.
- **First load with active alert:** block opens. `sensorsCollapsed[name]` is empty so it would open anyway; alert just makes it explicit.
- **Mid-edit collapse:** if the user has the threshold inline editor open and clicks the Sensors summary to collapse, the editor's DOM is now hidden but `editingThreshold[name]` state is intact. On re-expand the editor still shows. Acceptable; matches how existing `<details>` collapse handles nested form state elsewhere.

## Tests (Playwright, `tests/ui/dashboard.spec.ts`)

Four cases covering the state machine, all using `page.locator('details.sensors')`:

1. **Default expanded.** No alerts; assert `details.sensors` has `open` attribute.
2. **User collapse persists across re-render.** Click the summary; assert no `open`; trigger a re-render (call into the page's `render()` via `page.evaluate` — already used elsewhere in the spec) and assert still no `open`.
3. **Alert force-expands over user collapse.** Collapse first, then load a snapshot with `sensor_alerts.co2 === true`; assert `open`.
4. **Alert clears returns to user-collapsed state.** Continuation of #3 — load a snapshot with `sensor_alerts.co2 === false`; assert no `open`.

Add a smoke assertion to the existing default-render test (around line 183 of the spec) confirming the new wrapper element exists. The existing tests that locate `.card .block` with `hasText: "Sensors"` should still match because the `<h3>Sensors</h3>` is still inside.

Re-run `just screenshot` afterwards. The screenshot script's `playroom` device fires `co2: true, voc: true` alerts so its Sensors block will render expanded — visually identical to today. The `bedroom` and `office` cards have no alerts and are also rendered expanded by default, so they're also visually identical to today.

## Files touched

- `cmd/breezyd/ui/index.html` — add CSS rule, change `<div class="block">` wrapper to `<details class="block sensors">`, add `sensorsCollapsed` state map, extend toggle listener.
- `tests/ui/dashboard.spec.ts` — four new tests.
- `tests/ui/screenshots/dashboard-1col.png`, `tests/ui/screenshots/dashboard-3col.png` — regenerate (likely byte-identical given alerts force expansion in the screenshot data).

## Out of scope

- Changing which sensors live in the block.
- Nested collapsing (no per-row collapse).
- Persisting collapse across browser reloads (no localStorage).
- "Acknowledge alert by collapsing" UX — alert always wins.
- The threshold inline editor (#28) — unchanged.
