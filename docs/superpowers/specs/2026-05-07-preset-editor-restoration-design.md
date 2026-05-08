# Preset editor restoration + automode fix + cookie-driven UI state — design

Date: 2026-05-07
Issues: #53 (preset radio buttons no longer expand), #46 (automode behaviour bugs).
Retrofit: #49 (`<details>` open-state — currently a JS-shim that flickers
on every poll; re-implement with the same cookie mechanism).

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

Issue #46 records two behaviour bugs in the legacy editor still
relevant to the restored version:

1. The `automode` checkbox should default **unchecked**; legacy
   defaulted checked.
2. Toggling `automode` from checked → unchecked while both fans are
   ≥ 10% should fire a `regeneration` mode write immediately. Legacy
   only stored the preference.

Issue #49 (`<details>` open-state) shipped in PR #56 with a JS shim
that re-applies open state in `htmx:afterSettle`. The shim works but
the server emits the default state first and JS swaps it after the
HTML lands — the user sees a one-frame flicker on every 5-second
poll. Same flicker would affect any client-only restoration of the
new preset-editor open state.

We replace the flickering JS-restoration pattern with a cookie that
carries UI state from the browser to the server, so every render
(initial page load, htmx poll, htmx action response) emits the right
markup the first time.

## Goals

- Restore the inline preset editor for SPEED presets 1/2/3.
- Fix #46.1 (automode default off) and #46.2 (uncheck → regen write).
- Eliminate the flicker by making the server authoritative for UI
  state on every render. The browser sends state via cookie; the
  server reads it before rendering. No client-side re-apply pass.
- Retrofit #49 onto the same cookie mechanism so `<details>` open
  state stops flickering on every poll.

## Non-goals

- Daemon-side persistence of UI state. The cookie lives in the
  browser; multiple browsers don't share state. (Considered and
  rejected: shared state means two people in the same household
  fight over which panels are open.)
- Optimistic live overlay (legacy `optimisticLive` Map). After a
  write, the daemon's 12-second fan-settle window keeps `live.fan_*_rpm`
  at stale or 0 values. We accept the visual lag; restoring the
  overlay is a separate decision.
- Cross-device "close other editors on different-card interaction"
  beyond a small best-effort cookie-mutation on click.
- CLI surface for preset editing. The `/v1/devices/{name}/preset`
  endpoint is already exposed; nothing else changes.

## Architecture

### Cookie format

A single cookie `breezy-ui` carries all UI state for the dashboard.
Set by JS on every interaction; read by the server on every render.

```
Name:     breezy-ui
Path:     /
SameSite: Lax
Max-Age:  31536000   (1 year)
HttpOnly: false      (JS needs to write)
Value:    URL-encoded JSON
```

JSON shape:

```jsonc
{
  // <details id> -> open?
  // ids: "info-<device>", "sensors-<device>", "energy-<device>",
  //      "schedule-<device>". Cookie keys are the literal element ids.
  "details": {
    "info-bedroom":     true,
    "sensors-bedroom":  false,
    "energy-bedroom":   true
  },
  // Preset-editor state, keyed by device name.
  "preset": {
    "bedroom": { "open": 2, "automode": false, "match": true }
    // open: 0 means closed; 1|2|3 is which preset is open
    // automode default false; match default true
  }
}
```

Sized for 3 devices × 4 details + 3 devices × preset entry: well
under 1 KB. Cookie limit is 4 KB so we have ample headroom.

Encoding: `encodeURIComponent(JSON.stringify(obj))`. Parsing on the
server uses `encoding/json` after `url.QueryUnescape`.

### Server-side: cookie parsing into the view

A new `internal/uistate` package exposes:

```go
type State struct {
    Details map[string]bool          // element id -> open
    Preset  map[string]PresetState   // device name -> preset state
}

type PresetState struct {
    Open     int  // 0 closed, 1|2|3 open
    Automode bool
    Match    bool // defaults to true; cookie stores explicit
}

// Parse reads the breezy-ui cookie from the request. Missing or
// malformed cookies return a zero State; callers apply defaults.
func Parse(r *http.Request) State

// Defaults returns a per-device PresetState with documented defaults
// (Match=true, others zero). Used when the cookie has no entry.
func DefaultsForDevice(name string) PresetState
```

`cmd/breezyd/handlers_ui_read.go::buildView` calls
`uistate.Parse(r)` once per request and threads the result through
each device's view. The `DeviceView` struct gains:

```go
DetailsOpen   map[string]bool   // "info"/"sensors"/"energy"/"schedule" -> open?
EditingPreset int               // 0 closed, 1|2|3
Automode      bool
MatchSpeeds   bool
```

### `<details>` open-state retrofit (#49)

Each `<details>` template reads its open value from `v.DetailsOpen`:

```go
// device_card.templ:17
<details id={ "info-" + v.Name }
         class="device-info"
         if forceOpen("info", v) { open }>
```

```go
// helper, in helpers.templ
func forceOpen(section string, v ui.DeviceView) bool {
    // NeedsAttention forces device-info open regardless of cookie.
    if section == "info" && v.NeedsAttention {
        return true
    }
    if open, ok := v.DetailsOpen[section]; ok {
        return open
    }
    // Per-section default when the cookie has no entry.
    return defaultsBySection[section]
}
```

`defaultsBySection`:

| section   | default | force-open trigger        |
|---|---|---|
| info      | closed  | `v.NeedsAttention`        |
| sensors   | open    | (none)                    |
| energy    | closed  | (none)                    |
| schedule  | closed  | (none)                    |

The existing JS shim in `layout.templ:72-101` is **deleted**. Its
job is now done by the cookie + server render. The click handler
that *writes* the cookie on `<summary>` clicks is added in its
place (a few lines).

### Render shape — preset editor

`cmd/breezyd/ui/templates/controls_block.templ` always emits three
editor panels per card. Server decides which is visible based on
`v.EditingPreset`:

```html
<div class="ctrl">
  <span class="ctrl-label">SPEED</span>
  <div class="seg">
    <button data-action="preset" data-value="1" ...>22/23</button>
    <button data-action="preset" data-value="2" ...>48/49</button>
    <button data-action="preset" data-value="3" ...>100/100</button>
    <button data-action="manual-speed" ...>manual</button>
  </div>
  <div class="preset-editor"
       data-preset-editor="1"
       data-name="bedroom"
       if v.EditingPreset != 1 { hidden }>
    <label class="match-speeds">
      <input type="checkbox" data-action="automode-toggle"
             data-name="bedroom"
             if v.Automode { checked }>
      automode
    </label>
    <label class="match-speeds">
      <input type="checkbox" data-action="match-speeds-toggle"
             data-name="bedroom"
             if v.MatchSpeeds { checked }>
      match speeds
    </label>
    <div class="slider-row">
      <span class="val-label">supply</span>
      <input type="range" min="0" max="100" step="1"
             value={ fmt.Sprintf("%d", v.Preset1.Supply) }
             data-action="preset-supply-slider"
             data-name="bedroom" data-preset="1">
      <span class="val">{ fmt.Sprintf("%d%%", v.Preset1.Supply) }</span>
    </div>
    <div class="slider-row">
      <span class="val-label">exhaust</span>
      <input type="range" min="0" max="100" step="1"
             value={ fmt.Sprintf("%d", v.Preset1.Extract) }
             data-action="preset-extract-slider"
             data-name="bedroom" data-preset="1">
      <span class="val">{ fmt.Sprintf("%d%%", v.Preset1.Extract) }</span>
    </div>
  </div>
  <div class="preset-editor" data-preset-editor="2" ...
       if v.EditingPreset != 2 { hidden }>...</div>
  <div class="preset-editor" data-preset-editor="3" ...
       if v.EditingPreset != 3 { hidden }>...</div>
  ...existing manual-only MODE seg + manualSliderRow stay as-is...
</div>
```

The server is the single source of truth for `hidden`. JS never
toggles it directly — JS writes the cookie and lets the htmx swap
re-render with the right state.

### Client-side state mutation

A small JS module in `layout.templ`'s inline `<script>` exposes a
single helper:

```js
function setUIState(mut) {
  const cur = readCookie() || {};
  mut(cur);
  document.cookie =
    "breezy-ui=" + encodeURIComponent(JSON.stringify(cur)) +
    "; path=/; max-age=31536000; samesite=lax";
}
```

Delegated event listeners call `setUIState` on user interaction:

| Event target | Mutation |
|---|---|
| `<summary>` click inside a `<details id="...">` | toggle `details[id]`. The browser's native `<details>` toggle handles the visual state for *this* click; the cookie write makes it stick across the next swap. |
| `[data-action="preset"]` click | If current `preset[name].open === N` → set 0; else set N AND clear other devices' open editors. |
| `[data-action="manual-speed"]` click | Set `preset[name].open = 0`. |
| `[data-action="mode"]` click | Set `preset[name].open = 0`. |
| `[data-action="manual-slider"]` change | Set `preset[name].open = 0`. |
| `[data-action="automode-toggle"]` change | Set `preset[name].automode = el.checked`. |
| `[data-action="match-speeds-toggle"]` change | Set `preset[name].match = el.checked`. |

The cookie write happens **synchronously on click**, before htmx
fires its request, so the next render reads the new state.

### Slider drag flow

Each preset-slider has `hx-trigger="change delay:200ms"` and
`hx-post="/ui/devices/{name}/preset"`, with `hx-include` pulling
both supply and extract sliders into the same payload, and
`hx-vals` providing `preset=N`.

In `htmx:configRequest`:

1. **Snap-to-zero:** any slider value in `1..9` → `0`. Update the
   slider DOM and the request payload.
2. **Match-speeds sync:** if cookie's `preset[name].match !== false`
   and only one slider was the change source, mirror its value to
   the sibling. Update both the sibling DOM and the payload.

Skip the actual `/preset` POST when either side is `< 10` — the
firmware register accepts 10..100 only and the daemon-side
`SetPresetSpeed` validates this. The user's "off this side" intent
still drives the implied-mode write below.

### Implied-mode write

After a slider moves (whether the `/preset` POST fires or is skipped):

| `automode` | supply | extract | implied mode |
|---|---|---|---|
| on  | any | any | `ventilation` |
| off | ≥ 10 | ≥ 10 | `regeneration` |
| off | 0 | ≥ 10 | `extract` |
| off | ≥ 10 | 0 | `supply` |
| off | 0 | 0 | (no write — illegal state) |

Two guards before the mode POST fires:

- The card's `data-speed-mode` must equal `preset{N}` for the open
  panel. Editing an inactive preset stores register values without
  changing the running mode.
- The implied mode must differ from the card's `data-airflow-mode`.

`data-speed-mode` and `data-airflow-mode` are new attributes on the
card root (`<div class="card" data-device="...">` in
`device_card.templ:13`), populated from `v.SpeedMode` and
`v.AirflowMode`. Reading them off the card root is simpler than
walking `aria-pressed` on the chips.

### Automode toggle flow (#46.2)

`change` listener on `[data-action="automode-toggle"]`:

1. Update the cookie (`setUIState`).
2. If transition was checked → unchecked AND
   `data-speed-mode === "preset{N}"` for the open editor's `N` AND
   both sliders show ≥ 10 → fire `POST /ui/devices/{name}/mode` with
   `{"mode":"regeneration"}`.
3. Otherwise no write — `off → on` and `on` while editing an
   inactive preset are pure preference changes.

### Match-speeds toggle flow

`change` listener on `[data-action="match-speeds-toggle"]`:

1. Update the cookie.
2. No write. Next slider drag respects the new state.

## Endpoints

All HTTP plumbing already exists:

- `POST /v1/devices/{name}/preset` — `{"preset":1|2|3,"supply":N,"extract":N}` →
  `breezy.SetPresetSpeed`. Handler at `cmd/breezyd/handlers_device.go:237`.
- `POST /v1/devices/{name}/mode` — `{"mode":"regeneration|ventilation|supply|extract"}`.
- `POST /v1/devices/{name}/speed` — `{"preset":N}` (preset activation).

The `/ui/devices/{name}/...` shims that re-render the card on success
already exist for `/speed` and `/mode`; we add a
`/ui/devices/{name}/preset` shim mirroring the same shape (decode →
call backend → re-render the card via `templ` → write fragment).

The new shim and existing shims must read the cookie and pass the
parsed state to `buildView`, so the re-rendered fragment reflects
current UI state (the action might have changed `editingPreset` —
e.g., a manual-slider POST closes the open editor by virtue of the
JS cookie write that fired before the POST).

## Files touched

- `internal/uistate/state.go` (new) — cookie type, `Parse`,
  `DefaultsForDevice`. Plus `state_test.go` (cookie round-trip,
  malformed-cookie tolerance, missing-cookie defaults).
- `cmd/breezyd/ui/view.go` — extend `DeviceView` with `DetailsOpen`,
  `EditingPreset`, `Automode`, `MatchSpeeds`.
- `cmd/breezyd/ui_view.go` and/or `handlers_ui_read.go::buildView` —
  call `uistate.Parse(r)` and populate the new view fields.
- `cmd/breezyd/ui/templates/device_card.templ` — replace inline
  `if v.NeedsAttention { open }` with `if forceOpen("info", v) { open }`,
  add `data-speed-mode` / `data-airflow-mode` to the card root.
- `cmd/breezyd/ui/templates/sensors_block.templ` /
  `energy_block.templ` / `schedule_block.templ` — same `forceOpen`
  swap on each `<details>`.
- `cmd/breezyd/ui/templates/helpers.templ` — `forceOpen` helper +
  `defaultsBySection` map.
- `cmd/breezyd/ui/templates/controls_block.templ` — emit 3 preset
  panels with `hidden` driven by `v.EditingPreset`, automode and
  match-speeds checkboxes driven by view fields.
- `cmd/breezyd/ui/templates/layout.templ` — **delete** the
  `htmx:afterSettle` re-apply pass for `<details>` (#49 shim);
  add the new `setUIState` helper, delegated `click`/`change`
  listeners that write the cookie, and the `htmx:configRequest`
  snap-to-zero + match-speeds sync.
- `cmd/breezyd/handlers_ui_write.go` (or wherever the existing
  `/ui/...` shims live) — add `/ui/devices/{name}/preset` shim;
  ensure all shims read the cookie via `buildView`.
- `cmd/breezyd/ui/style.css` — `.preset-editor` and `.match-speeds`
  rules already exist (lines 284-291). No CSS additions expected.

## Testing

Unit tests:

- `internal/uistate/state_test.go` — cookie round-trip
  (encode/decode), malformed JSON tolerance (returns zero `State`,
  no error to the caller), missing cookie returns zero `State`,
  oversized cookie value (>4 KB) returns zero `State`. The
  philosophy: bad UI-state cookies never produce a 5xx and never
  partially apply — they fall through to defaults.
- `cmd/breezyd/ui/templates/render_test.go` — extend with golden
  HTML for: cookie says `info-bedroom: true` but `NeedsAttention =
  false` → `<details ... open>`; cookie says
  `info-bedroom: false` but `NeedsAttention = true` → still `open`
  (force wins); cookie says `preset[bedroom].open = 2` →
  `data-preset-editor="2"` panel renders without `hidden` and the
  others render with `hidden`.

Playwright tests in `tests/ui/dashboard.spec.ts`, un-`fixme`d and
updated for the cookie-driven world:

| Line | Test | Action |
|---|---|---|
| 893 | `preset editor open: no fan slider rows` | Re-enable. |
| 913 | `preset editor: automode default ON; dragging POSTs ventilation` | **Rewrite:** automode default OFF, dragging both ≥ 10 POSTs `regeneration`. |
| 919 | `preset editor: dragging a slider into 1-9 snaps to 0` | Re-enable. |
| 925 | `preset editor: automode off + supply→0 implies extract mode` | Re-enable. |
| 930 | `preset editor: automode off + extract→0 implies supply mode` | Re-enable. |
| 935 | `preset editor: automode off + both > 0 implies regeneration` | Re-enable. |
| 940 | `preset activation (automode on): clicks ventilation alongside the preset` | **Rewrite:** "with automode on, dragging in editor POSTs ventilation". |
| 946 | `speed preset: editor opens after activating` | Re-enable. |
| 951 | `speed preset: clicking same active preset twice closes the editor` | Re-enable. |
| 956 | `speed preset editor: match-speeds default true → moving supply POSTs both` | Re-enable. |
| 961 | `speed preset editor: match-speeds off → moving extract preserves cached supply` | Re-enable. |

Out of scope, leave `.fixme`:
- `mode click in manual: carries the higher fan pct as new manual_pct` (900)
- `mode click in manual: optimistic overlay flips Sensors rpms immediately` (906)

New tests:
- `automode default: unchecked when editor opens (no cookie)`
- `automode off→toggle while in preset, both fans ≥ 10: POSTs regeneration mode`
- `preset editor: open state survives 5s poll (no flicker)` — assert
  the panel's `hidden` attribute is *never* present after the swap
  by listening for `htmx:beforeSwap` and `htmx:afterSwap` and
  asserting visibility across both events.
- `<details>: open state survives 5s poll (no flicker)` — same, on
  the Sensors / Energy / device-info `<details>`. Replaces the
  three #49 tests at lines 783, 798, 812 (their assertions become
  about the cookie + flicker-free swap rather than the JS
  re-apply).
- `cookie: malformed value falls back to defaults without 5xx` —
  set the cookie via `page.context().addCookies([{...}])`, hit
  `/`, expect 200 and default panel state.

Gate: `just test-ui`. After tests pass, `just screenshot`
regenerates `dashboard-3col.png` showing one card with its preset
editor open so the README reflects the restored UI.

## Risks

- **Cookie size growth.** Each new device adds ~80 bytes. With
  ~50 configured devices we'd still be under 4 KB but the JSON
  parsing on every request grows linearly. Mitigation: log the
  cookie size in a debug-only path; if it ever crosses 2 KB, drop
  to a compact text encoding.
- **Cookie tampering / oversize from outside the dashboard.** The
  cookie is unsigned because nothing security-sensitive lives in
  it. A user pasting `breezy-ui=garbage` into devtools should not
  500 the daemon; `Parse` must defensively recover (json.Unmarshal
  error → return zero `State`). Covered by the malformed-cookie
  test.
- **Server-side default drift.** The cookie carries explicit
  values; a section default change (e.g., flipping schedule from
  closed-default to open-default) only takes effect for users
  whose cookie has no entry for that section. Mitigation: any
  default change includes a one-line note in CHANGELOG.md so we
  remember the cookie-shadowing behaviour.
- **Schedule-edit pause interaction (#56).** The schedule block
  pauses polling during edit. The cookie change for an opened
  preset editor does NOT pause polling; the server simply renders
  the open editor on the next poll. No interaction issue, but if
  we ever need the same pause-on-edit semantics for the preset
  editor, the cookie-driven render keeps state correct *during*
  pauses too — the pause is purely about not re-rendering, and
  when polling resumes the cookie still says "preset 2 open."
- **#46.2 transition tracking.** Detecting checked → unchecked
  needs the prior state. The cookie carries it; the change event
  on the checkbox sees the new value. Compute `prev = !el.checked`
  before updating cookie. Robust against poll swaps because the
  server emits the cookie's value, not the DOM's last-known state.
