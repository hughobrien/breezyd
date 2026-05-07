# Preset editor restoration + automode fix — design

Date: 2026-05-07
Issues: #53 (preset radio buttons no longer expand), #46 (automode behaviour bugs).
Related: #49 (`<details>` open-state shim — same JS pattern).

## Background

Pre-htmx (commit `3a1f83d` and earlier), clicking a SPEED preset chip on a
device card opened an inline editor underneath the chip row. The editor
let the user adjust the per-preset supply/extract speeds (firmware
registers `0x14`/`0x15`/`0x16` for presets 1/2/3) and decide how the
preset's airflow mode is derived. The htmx migration (PR #14/#48) kept
the `.preset-editor` CSS in `cmd/breezyd/ui/style.css` but dropped the
template that renders it; clicking a preset chip now only POSTs
`{"preset":N}` and that's it. Users have lost the ability to edit a
preset's stored speeds from the dashboard.

Issue #46 records two behaviour bugs in the legacy editor that are still
relevant to whatever we restore:

1. The `automode` checkbox should default **unchecked**; legacy
   defaulted checked.
2. Toggling `automode` from checked → unchecked while both fans are
   ≥ 10% should fire a `regeneration` mode write immediately. The legacy
   code only stored the preference and waited for the next slider edit.

We restore the editor and fix both #46 behaviours in one change rather
than shipping a known-buggy editor and immediately re-touching it.

## Goals

- Bring back the inline preset editor for SPEED presets 1/2/3.
- Editor state (which preset is open, automode, match-speeds) survives
  the 5-second htmx poll without a server round-trip.
- `automode` defaults **off** (#46.1); unchecking it with both fans ≥ 10
  fires a `regeneration` mode write (#46.2).
- Match the existing JS-shim pattern from #49 (`<details>` open-state).
  No new heavy client code, no JS-rendered subtree, no server-side UI
  state.

## Non-goals

- Optimistic live overlay (legacy `optimisticLive` Map). After a write,
  the daemon's 12-second fan-settle window keeps `live.fan_*_rpm` at
  stale or 0 values. We accept the visual lag; restoring the overlay is
  a separate decision.
- Closing other cards' editors when the user interacts with a different
  card. Legacy did this. Included as best-effort below; if the JS gets
  awkward we drop it.
- Any CLI surface for preset editing. The `/v1/devices/{name}/preset`
  endpoint is already exposed for external consumers; nothing else
  changes.

## Architecture

### Render shape

`cmd/breezyd/ui/templates/controls_block.templ` always emits three
editor panels per card, hidden by default:

```html
<div class="ctrl">
  <span class="ctrl-label">SPEED</span>
  <div class="seg">
    <button data-action="preset" data-value="1" ...>22/23</button>
    <button data-action="preset" data-value="2" ...>48/49</button>
    <button data-action="preset" data-value="3" ...>100/100</button>
    <button data-action="manual-speed" ...>manual</button>
  </div>
  <div class="preset-editor" hidden data-preset-editor="1" data-name="bedroom">
    <label class="match-speeds">
      <input type="checkbox" data-action="automode-toggle" data-name="bedroom">
      automode
    </label>
    <label class="match-speeds">
      <input type="checkbox" data-action="match-speeds-toggle" data-name="bedroom" checked>
      match speeds
    </label>
    <div class="slider-row">
      <span class="val-label">supply</span>
      <input type="range" min="0" max="100" step="1" value="22"
             data-action="preset-supply-slider" data-name="bedroom" data-preset="1">
      <span class="val">22%</span>
    </div>
    <div class="slider-row">
      <span class="val-label">exhaust</span>
      <input type="range" min="0" max="100" step="1" value="23"
             data-action="preset-extract-slider" data-name="bedroom" data-preset="1">
      <span class="val">23%</span>
    </div>
  </div>
  <div class="preset-editor" hidden data-preset-editor="2" ...>...</div>
  <div class="preset-editor" hidden data-preset-editor="3" ...>...</div>
  ...existing manual-only MODE seg + manualSliderRow stay as-is...
</div>
```

The slider initial values come from `v.Preset1/2/3` on the existing
`DeviceView` — no new view fields. The automode checkbox is rendered
**unchecked** by default (#46.1); match-speeds rendered **checked**.

### Client state

Three `Map`s live on `window` (so they survive htmx swaps; the swap
replaces DOM nodes but not JS module state). They sit in
`cmd/breezyd/ui/templates/layout.templ`'s inline `<script>` alongside
the #49 details-state code.

```js
window.editingPreset = window.editingPreset || new Map();  // name -> 1|2|3 (set means open)
window.automode      = window.automode      || new Map();  // name -> bool (default false)
window.matchSpeeds   = window.matchSpeeds   || new Map();  // name -> bool (default true)
```

All three reset on full page reload. None are persisted to
`localStorage` — these are ephemeral session-scoped UI state, same as
legacy.

### Re-apply pass after htmx swap

A single `htmx:afterSettle` listener walks the Maps and reconciles the
DOM:

- For each `[data-preset-editor]` panel: set `hidden` based on
  `editingPreset.get(name) === preset`.
- For each `[data-action="automode-toggle"]`: set `.checked` from
  `automode.get(name)`.
- For each `[data-action="match-speeds-toggle"]`: set `.checked` from
  `matchSpeeds.get(name)` (default `true` if Map has no entry).

This is the same pattern as the #49 shim and lives in the same script
block.

### Click flow

A single delegated `click` listener on `document.body`:

| Selector | Action |
|---|---|
| `[data-action="preset"]` | If `editingPreset.get(name) === N` → `delete`; else set Map to N and clear other devices' entries. Don't `preventDefault` — htmx still POSTs `/ui/devices/{name}/speed` with `{"preset":N}` as today. |
| `[data-action="manual-speed"]` | `editingPreset.delete(name)`. Editor closes on next settle. |
| `[data-action="mode"]` | `editingPreset.delete(name)`. |
| `[data-action="manual-slider"]` (`change` event) | `editingPreset.delete(name)`. |

### Slider drag flow

Each preset-slider has `hx-trigger="change delay:200ms"` and
`hx-post="/ui/devices/{name}/preset"` with `hx-include="[data-preset-editor='{N}'] input[type=range]"`
(both supply and extract sliders inside the same panel) and
`hx-vals='{"preset":{N}}'`.

Two pre-flight transformations happen in `htmx:configRequest`:

1. **Snap-to-zero:** any slider value in `1..9` → `0`. Update both the
   slider's `value` attribute (so the thumb moves) and the request
   payload.
2. **Match-speeds sync:** if `matchSpeeds.get(name) !== false` and the
   user touched only one slider, mirror that value to the sibling.
   Update the sibling's slider DOM and the payload.

After the request: if either side is `< 10`, the daemon-side
`SetPresetSpeed` will refuse (firmware register accepts 10..100 only),
so we **skip** the `/preset` POST in that case — checked client-side
before letting htmx fire. The user's "off this side" intent is still
captured by the implied-mode write below.

### Implied-mode write

After a slider moves (whether the `/preset` POST fires or is skipped),
JS computes the airflow mode that this preset edit *implies* and, if
needed, fires a second POST to `/ui/devices/{name}/mode`:

| `automode` | supply | extract | implied mode |
|---|---|---|---|
| on  | any | any | `ventilation` |
| off | ≥ 10 | ≥ 10 | `regeneration` |
| off | 0 | ≥ 10 | `extract` |
| off | ≥ 10 | 0 | `supply` |
| off | 0 | 0 | (no write — illegal state) |

Two guards before the mode POST fires:

- The device's current `configured.speed_mode` must equal `preset{N}`
  for the panel being edited. Editing an inactive preset stores
  register values without changing the running mode.
- The implied mode must differ from the device's current
  `configured.airflow_mode` (no-op writes wasted).

The current snapshot for these guards is read from two new `data-*`
attributes on the card root (`<div class="card" data-device="bedroom">`
in `device_card.templ:13`): `data-speed-mode` and `data-airflow-mode`,
populated from `v.SpeedMode` and `v.AirflowMode`. Reading these off the
card root is simpler than walking aria-pressed values on the chips.

### Automode toggle flow (#46.2)

`change` listener on `[data-action="automode-toggle"]`:

1. Update `automode` Map.
2. If transition was checked → unchecked AND device's
   `configured.speed_mode === "preset{N}"` for the open editor's `N`
   AND both sliders show ≥ 10 → fire `POST /ui/devices/{name}/mode` with
   `{"mode":"regeneration"}` (mirrors what a future slider drag would
   compute, but fires immediately so the user sees the change without
   needing to bump a slider).
3. Otherwise no write — automode `off → on` and `on` while editing an
   inactive preset are pure preference changes.

### Match-speeds toggle flow

`change` listener on `[data-action="match-speeds-toggle"]`:

1. Update `matchSpeeds` Map.
2. No write. Next slider drag respects the new state.

## Endpoints

All HTTP plumbing already exists:

- `POST /v1/devices/{name}/preset` — `{"preset":1|2|3,"supply":N,"extract":N}` →
  `breezy.SetPresetSpeed`. Handler at `cmd/breezyd/handlers_device.go:237`.
- `POST /v1/devices/{name}/mode` — `{"mode":"regeneration|ventilation|supply|extract"}`.
- `POST /v1/devices/{name}/speed` — `{"preset":N}` (preset activation, already wired).

The `/ui/devices/{name}/...` shims that re-render the card on success
already exist for `/speed` and `/mode`; we add a `/ui/devices/{name}/preset`
shim mirroring the same shape (decode → call backend → re-render the
card via `templ` → write fragment).

No daemon logic changes.

## Files touched

- `cmd/breezyd/ui/templates/controls_block.templ` — emit 3 hidden
  `.preset-editor` panels with their initial slider values from
  `v.Preset1/2/3`.
- `cmd/breezyd/ui/templates/layout.templ` — extend the inline `<script>`
  with the three Maps, the delegated `click`/`change` handlers, and the
  `htmx:afterSettle` re-apply pass.
- `cmd/breezyd/handlers_ui_write.go` (or wherever the existing `/ui/...`
  shims live) — add `/ui/devices/{name}/preset` shim.
- `cmd/breezyd/ui/templates/device_card.templ` — add `data-speed-mode`
  and `data-airflow-mode` to the card root (`v.SpeedMode`,
  `v.AirflowMode` already on `DeviceView`). No `view.go` changes.
- `cmd/breezyd/ui/style.css` — `.preset-editor` and `.match-speeds`
  rules already exist (lines 284-291). No CSS additions expected.

## Testing

`tests/ui/dashboard.spec.ts` un-`fixme`d and updated for the htmx world:

| Line | Test | Action |
|---|---|---|
| 893 | `preset editor open: no fan slider rows` | Re-enable. Assert manual slider row hidden when a preset is the active speed_mode. |
| 913 | `preset editor: automode default ON; dragging POSTs ventilation` | **Rewrite:** automode default OFF, dragging both ≥ 10 POSTs `regeneration`. |
| 919 | `preset editor: dragging a slider into 1-9 snaps to 0` | Re-enable. |
| 925 | `preset editor: automode off + supply→0 implies extract mode` | Re-enable. |
| 930 | `preset editor: automode off + extract→0 implies supply mode` | Re-enable. |
| 935 | `preset editor: automode off + both > 0 implies regeneration` | Re-enable. |
| 940 | `preset activation (automode on): clicks ventilation alongside the preset` | **Rewrite:** "with automode on, dragging in editor POSTs ventilation" — activation itself doesn't decide mode; the editor does. |
| 946 | `speed preset: editor opens after activating` | Re-enable. |
| 951 | `speed preset: clicking same active preset twice closes the editor` | Re-enable. |
| 956 | `speed preset editor: match-speeds default true → moving supply POSTs both` | Re-enable. |
| 961 | `speed preset editor: match-speeds off → moving extract preserves cached supply` | Re-enable. |

Out of scope, leave `.fixme`:
- `mode click in manual: carries the higher fan pct as new manual_pct` (900)
- `mode click in manual: optimistic overlay flips Sensors rpms immediately` (906)

New tests (#46 + #49 parity):
- `automode default: unchecked when editor opens`
- `automode off→toggle while in preset, both fans ≥ 10: POSTs regeneration mode`
- `preset editor: open state survives 5s poll`
- `preset editor: match-speeds checkbox state survives 5s poll`
- `preset editor: automode checkbox state survives 5s poll`

Gate: `just test-ui` (Playwright against real `breezyd` + `fakedevice`).
After tests pass, `just screenshot` regenerates `dashboard-3col.png`
showing one card with its editor open so the README reflects the
restored UI.

## Risks

- **JS shim complexity creep.** The #49 shim is ~20 lines; this one is
  larger (3 Maps, multiple delegated listeners, `htmx:configRequest`
  hook). Mitigation: keep all of it in `layout.templ`'s inline script
  rather than splitting into a new file; if it grows past ~150 lines
  total (including #49 + theme picker), promote it to a vendored
  `cmd/breezyd/ui/dashboard.js` file in a follow-up.
- **`htmx:configRequest` interception.** The match-speeds sibling-sync
  has to mutate both the DOM (so the visible thumb moves) and the
  payload. If we get it wrong, the user's slider drag could POST
  inconsistent values. Mitigation: a Playwright test per match-speeds
  case (test 956 + 961).
- **#46.2 transition tracking.** "checked → unchecked" needs the
  previous state. We rely on the Map (default `false` if absent) and
  the `change` event's new value to compute the transition. If a poll
  swap re-renders the checkbox between the user's check and uncheck,
  we'd lose the prior state — but the re-apply pass writes the Map's
  value back into the DOM, so the user sees what we believe and the
  transition is consistent.

## Out of scope (recap)

- Optimistic live overlay.
- Cross-device "close other editors on different card interaction"
  beyond a best-effort attempt.
- Schedule-edit integration (#56's poll-pause approach is reserved for
  rare/long edits; preset edits are short and tolerate the 5s poll
  flicker via the re-apply pass).
