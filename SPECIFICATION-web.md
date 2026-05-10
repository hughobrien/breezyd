# SPECIFICATION-web

The breezyd web dashboard: a single-page templ + datastar UI served by `cmd/breezyd`. One card per configured Breezy ERV. State arrives via a long-lived Server-Sent Events stream; user actions POST/PUT JSON or form bodies to action endpoints under `/ui/*` and the resulting card updates ride back over the same SSE stream. The dashboard has no client-side polling.

This specification covers what the dashboard does and how the pieces wire up. Daemon internals (UDP polling, fan-settle, energy tracking, scheduler retry) are in `SPECIFICATION-daemon.md`. The HomeKit accessory model is in `SPECIFICATION-hap.md`. The CLI is in `SPECIFICATION-cli.md`. Wire-protocol details live in `pkg/breezy/frame.go`.

## Audience and intent

A frontend or full-stack engineer who needs to know: what the dashboard renders, what's reactive vs. server-rendered, how the SSE stream works, what the action-handler contracts are, and what state survives a poll. Voice is prescriptive — what the dashboard does, not why it got this way.

## Routes

The dashboard's HTTP surface, registered in `cmd/breezyd/server.go::routes`:

| Method | Path | Returns | Purpose |
|---|---|---|---|
| `GET` | `/{$}` | `text/html` | Page shell (Layout). The `{$}` anchor is load-bearing — see "Page shell" below. |
| `GET` | `/ui/sse` | `text/event-stream` | Long-lived push channel. Initial-state cards on connect, then per-device updates. |
| `GET` | `/ui/style-{hash}.css` | `text/css` | Versioned stylesheet, immutable cache. Hash is short SHA-256 of `cmd/breezyd/ui/style.css`. |
| `GET` | `/ui/vendor/{file}` | per type | Vendored datastar bundle: `datastar-1.0.1.min.js`. |
| `GET` | `/favicon.svg`, `/favicon.ico` | `image/svg+xml` | Same SVG at both paths. |
| `POST` | `/ui/devices/{name}/power` | 200 + empty | `{"on": bool}`. |
| `POST` | `/ui/devices/{name}/mode` | 200 + empty | `{"mode": "ventilation"|"regeneration"|"supply"|"extract"}`. |
| `POST` | `/ui/devices/{name}/speed` | 200 + empty | `{"manual": 10..100}` XOR `{"preset": 1..3}`. |
| `POST` | `/ui/devices/{name}/preset` | 200 + empty | `{"preset": 1..3, "supply": 10..100, "extract": 10..100}`. |
| `POST` | `/ui/devices/{name}/heater` | 200 + empty | `{"on": bool}`. |
| `POST` | `/ui/devices/{name}/timer` | 200 + empty | `{"mode": "off"|"night"|"turbo"}`. |
| `POST` | `/ui/devices/{name}/reset-filter` | 200 + empty | No body. |
| `POST` | `/ui/devices/{name}/reset-faults` | 200 + empty | No body. |
| `GET` | `/ui/devices/{name}/threshold/{kind}` | SSE patch | Read-variant cell for `humidity`/`co2`/`voc`. |
| `GET` | `/ui/devices/{name}/threshold/{kind}/edit` | SSE patch | Edit-variant cell. |
| `PUT` | `/ui/devices/{name}/threshold` | SSE patch | Form-encoded `kind=...&value=N&enabled=true|false`; emits read variant on success. |
| `GET` | `/ui/devices/{name}/schedule` | SSE patch | Read-variant SCHEDULE block. |
| `GET` | `/ui/devices/{name}/schedule/edit` | SSE patch | Edit-variant SCHEDULE block. |
| `GET` | `/ui/devices/{name}/schedule/new-row` | SSE patch | Appends an empty edit row to the editor `<tbody>`. |
| `PUT` | `/ui/devices/{name}/schedule` | SSE patch | Form-encoded parallel arrays `at[]`, `action[]`, `pct[]`, `enabled`. |

The action endpoints are the dashboard's private surface. The CLI uses `/v1/*`; HomeKit uses `pkg/breezy/ops` directly. No external caller is expected on `/ui/*`.

## Page shell

`GET /{$}` returns the page shell rendered by `cmd/breezyd/ui/templates/layout.templ::Layout`.

The `{$}` anchor in the route pattern is load-bearing. Go 1.22's mux treats `GET /` as a prefix match that catches every unmatched URL; the `{$}` literal restricts the handler to the exact path `/`, so a typo on `/v1/...` returns 404 JSON instead of the HTML page. Removing the `{$}` would silently turn API typos into HTML responses.

The shell contains exactly:

- A `<head>` FOUC-prevention inline script that reads `localStorage.theme` and sets `data-theme` on `<html>` before paint (light/dark only — `auto` removes the attribute).
- `<link rel="stylesheet" href="/ui/style-{StyleHash}.css">`. `StyleHash` is computed at startup in `cmd/breezyd/ui_assets.go::init` from `sha256(style.css)[:10]`.
- `<script type="module" src="/ui/vendor/datastar-{DatastarVersion}.min.js">`. The bundle is the only JS framework; see "Vendor JS" below.
- `<body data-init="@get('/ui/sse')">`. Datastar runs the `@get` action helper on body initialization, which opens the long-lived SSE stream. There is no client-side polling and no programmatic `EventSource` construction in user code.
- `@ThemePicker()` (`cmd/breezyd/ui/templates/theme_picker.templ`).
- `<div id="global-error-banner" aria-live="polite">` — the action-error sink.
- `<div id="device-list" class="grid">` — the empty container that initial-state SSE events `append` cards into.
- An inline IIFE that owns the theme-picker click semantics (open/close + light/dark/auto toggle + `localStorage.theme` write).

`Cache-Control: no-store` is set on the shell so a daemon upgrade isn't masked by browser cache. The stylesheet and vendor JS are content-hashed and served `Cache-Control: public, max-age=31536000, immutable`.

The shell renders no device cards. Cards arrive as SSE patches once `/ui/sse` connects.

## Vendor JS

The dashboard ships exactly one vendored JavaScript bundle: `cmd/breezyd/ui/vendor/datastar-{version}.min.js` (currently 1.0.1). It's the unmodified `bundles/datastar.js` from the datastar release, served at `/ui/vendor/datastar-{version}.min.js` with a content hash documented in `ui_assets.go`. There is no second JS framework, no jQuery, no htmx, no Alpine. Every interactive behavior in the page is expressible as a datastar `data-*` attribute on a templ-rendered element.

The only hand-written JS in the page is two short inline blocks in `layout.templ`:

1. The `<head>` FOUC-prevention script (3 lines): set `data-theme` from localStorage before the body parses.
2. The `<body>` theme-picker IIFE (~20 lines): a click handler that toggles `data-theme` on `<html>` and writes `localStorage.theme`, plus an outside-click closer for the `<details class="theme-picker">` popout.

There is no separate `dashboard.js` module. If a behavior outgrows what an inline `data-on:click` expression can express cleanly, the convention is to factor it into a templ helper that returns a Go-string expression (see `cmd/breezyd/ui/templates/controls_block.templ::presetSliderExpr` for an example), not into a new JS file.

## Card

Each configured device renders a `<div class="card" data-device="{name}">` (`cmd/breezyd/ui/templates/device_card.templ::DeviceCard`). The card composes five blocks in order:

1. **Info** (`<details class="device-info" id="info-{name}" data-block="info">`) — name, IP, serial, firmware, filter status (with reset button), motor lifetime, RTC battery, faults (with reset button). The card-level power toggle lives in the `<summary>`.
2. **Energy** (`<details class="block energy" id="energy-{name}" data-block="energy">`) — instantaneous regen power and COP plus today/month/lifetime kWh totals. Renders nothing when no `EnergyView` is present (e.g., regen mode never engaged).
3. **Sensors** (`<details class="block sensors" id="sensors-{name}" data-block="sensors">`) — a 12-cell grid: editable thresholds (eCO2, VOC, RH), recovery%, four temperatures, two deltas, two RPMs.
4. **Schedule** (`<details class="block schedule" id="schedule-{name}" data-block="schedule">`) — read-variant table of entries; "edit schedule" button swaps the block for the editor variant.
5. **Controls** (`<div class="controls" data-block="controls">`) — the only non-`<details>` block. Always visible.

The card's `data-device` attribute is the per-card identity. SSE patches target `.card[data-device="{name}"]` selectors; never assume a stable DOM ID.

### Default open/closed

| Block | Default | Persists | Force-opens |
|---|---|---|---|
| Info | closed | per-card signal `$detailsOpen.info` | never (visual-only `.alert` class on fault/soiled filter) |
| Energy | closed | `$detailsOpen.energy` | never |
| Sensors | open | `$detailsOpen.sensors` | never (visual-only `.alert` class on threshold breach) |
| Schedule | closed | `$detailsOpen.schedule` | never (visual-only `.alert` class on failed fire — see daemon spec for retry semantics) |

User open/closed choice is honored regardless of alert state. Alert state is purely visual: a CSS class adds a red heading and a `⚠` prefix to the summary.

### Open/close binding pair

Each `<details>` block uses both halves of this pattern:

```templ
<details
    data-attr:open="$detailsOpen.sensors"
    ...>
    <summary data-on:click="$detailsOpen.sensors = !$detailsOpen.sensors">
        <h3>Sensors</h3>
    </summary>
    ...
</details>
```

Both halves are required:

- `data-attr:open="$detailsOpen.sensors"` — server-side, datastar reflects the signal into the `open` HTML attribute on every render and on every signal update. Without this, an SSE-rendered patch would silently revert the user's choice.
- `data-on:click="..."` on the summary — without this, native `<details>` toggling moves the DOM `open` state but does not move the signal, so the next signal-driven re-evaluation slams the block back to its prior state.

Together, the user's click writes the signal and the signal drives the attribute, so user toggles and SSE patches converge on a consistent open state across renders.

### Card states

| State | Trigger | Visual | Controls |
|---|---|---|---|
| Healthy | recent successful poll | normal | enabled |
| Stale | no successful poll for the stale window (3× poll cadence; see daemon spec) | desaturated card; `data-class:stale="$stale"` toggles a class | every `<button>` and `<input>` carries `disabled` (templ `if v.Stale { disabled }`) |
| Unreachable | configured but no successful poll has produced a Snapshot with values | placeholder card via `unreachableCard` template; lists name/IP/serial only | none rendered |
| Fault | `v.NeedsAttention` (active fault, soiled filter) | `.alert` class on Info `<details>` summary; red heading + `⚠` prefix | unaffected |

Stale/healthy is driven by `$stale` (a per-card signal patched on every push), not by re-rendering the whole card. The card outer HTML is rendered once at initial-state and never patched after; only block contents and signals change.

The unreachable variant deliberately omits all controls and schedule/energy/sensors blocks — the user almost certainly has a config typo (wrong IP, wrong password, firmware off) and the only useful information is "what does the daemon think it's looking at." Once any params land in the State cache, the next push replaces the unreachable card with a full one (initial-state on a new connection uses `mode=outer` against the existing card on reconnect; see "SSE protocol").

### Per-card signal seed

The card root carries a `data-signals` attribute initialised by `device_card.templ::initialCardSignals` and seeded with both static UI flags and runtime data:

```json
{
  "automode": false,
  "matchSpeeds": true,
  "editor": 0,
  "preset": {"1": {"supply": 50, "extract": 50}, "2": {...}, "3": {...}},
  "detailsOpen": {"info": false, "sensors": true, "energy": false, "schedule": false},
  "stale": false,
  "speedMode": "preset2",
  "airflowMode": "regeneration",
  "lastPollAge": "3s",
  "sensorsAlert": false
}
```

After initial render, the runtime fields (`stale`, `speedMode`, `airflowMode`, `lastPollAge`, `sensorsAlert`, `preset.*.{supply,extract}`) are refreshed via `datastar-patch-signals` events on every push. The static UI flags (`automode`, `matchSpeeds`, `editor`, `detailsOpen`) are user-owned and never touched by SSE. See `cmd/breezyd/ui/view.go::CardSignals`.

### Controls block

Always visible. Layout:

| Control | Element | Value source | Action |
|---|---|---|---|
| Power | toggle in Info `<summary>` | `v.Power` (`aria-pressed`) | `POST /ui/devices/{name}/power` `{on: !v.Power}` |
| Speed presets | three `<button>` chips labeled `supply/extract` | `$speedMode === 'presetN'` | `POST /ui/devices/{name}/speed {preset: N}` plus toggle of per-card `$editor` |
| Manual button | `<button>` | `v.SpeedMode === "manual"` | `POST /ui/devices/{name}/speed {manual: manualBtnPct(v)}` |
| Mode chips | four `<button>`s (auto/regen/supply/exhaust) | `$airflowMode` | `POST /ui/devices/{name}/mode {mode: ...}`, prefixed with `$editor = 0` |
| Manual slider | `<input type="range" min=10 max=100 step=1>` | `value` attribute (server-rendered) | `POST /ui/devices/{name}/speed {manual: evt.target.valueAsNumber}` debounced 200ms |
| Timer | two `<button>`s (night, turbo) | `$specialMode` (server-rendered) | `POST /ui/devices/{name}/timer {mode: ...}`; clicking the active mode posts `{mode: "off"}` |
| Heater | toggle | `v.Heater` | `POST /ui/devices/{name}/heater {on: !v.Heater}` |

The mode chips and manual slider are conditionally rendered: only when `v.SpeedMode == "manual" && v.SpecialMode == "off"`. Picking a numbered preset hides them; the preset editor is the way to change preset speeds.

Stale-disabled controls are rendered with the HTML `disabled` attribute, which suppresses both the click handler and pointer interaction.

### Manual slider

`cmd/breezyd/ui/templates/controls_block.templ::manualSliderRow`:

```templ
<input
    type="range" name="manual" min="10" max="100" step="1"
    value={ fmt.Sprintf("%d", v.ManualPct) }
    data-on:change__debounce.200ms={ postActionExpr("/ui/devices/"+v.Name+"/speed", "{manual: evt.target.valueAsNumber}") }
/>
```

The slider does not seed a `$manualPct` signal. The input's `value` attribute is the source of truth and the `@post` expression reads `evt.target.valueAsNumber` directly. Going through a signal would force signal-wins on every card re-render, clobbering the server-rendered value when a `data-signals` seed lags one poll behind a drag. The 200 ms debounce on `data-on:change` collapses bursts of drag commits into a single POST.

## SSE protocol

The push channel is `GET /ui/sse`, served by `cmd/breezyd/handlers_ui_sse.go::getUISSE`. It opens once per browser tab on body init via `data-init="@get('/ui/sse')"` and stays open for the tab's lifetime. EventSource auto-reconnects on disconnect.

### Connection setup

On request, the handler:

1. Verifies `h.PushHub` is configured (500 otherwise — a misconfiguration, not a runtime fault).
2. Clears the response writer's `WriteDeadline` via `http.NewResponseController.SetWriteDeadline(time.Time{})`. The daemon's `http.Server` enforces a 30-second `WriteTimeout` for slow-loris protection on the JSON API; SSE connections must opt out or the long-lived stream dies after 30s.
3. Sets `X-Accel-Buffering: no` (via `newSSE`). nginx's NixOS module already disables `proxy_buffering` for the dashboard location, but this header covers hand-rolled nginx, traefik, Apache, and Cloudflare Tunnels that buffer by default.
4. Reads `Last-Event-ID` from the request to distinguish cold load from reconnect. The header's *presence* is the binary signal — the dashboard does not implement event replay.
5. Subscribes to `PushHub` *before* the initial-state pass. Any `Notify` fired during initial-state lands in the bounded subscriber channel and is drained after initial-state completes; the matching cards exist on the client by then and the channel-delivered outer-mode patches resolve their selectors. Subscribing *after* the initial-state pass leaves a gap of up to one poll interval where state is silently lost on freshly-opened tabs.
6. Iterates `h.collectViews()` (`cmd/breezyd/handlers_ui_read.go::collectViews`, sorted by name) and emits one initial-state card per device.
7. Drains subscriber events and emits keepalive comments until the request context is done.

### Initial-state pass

Per-device, by `cmd/breezyd/handlers_ui_sse.go::emitInitialCard`:

| Mode | Selector | datastar mode |
|---|---|---|
| Cold load (no `Last-Event-ID`) | `#device-list` | `append` |
| Reconnect (`Last-Event-ID` present) | `.card[data-device="{name}"]` | `outer` |

Cold load grows the empty `#device-list` container by appending one card per device. Reconnect replaces the card the browser already has, in-place, by `outer`-merging against its `data-device` selector. Reconnect *must not* use `append` or every reconnect would duplicate every card.

Each initial card emits a `datastar-patch-elements` event with event ID `device:{name}`.

### Per-device push

The poller's fan-out is split in `cmd/breezyd/main.go` between two hooks:

```go
onPoll := func(name string, snap Snapshot) {
    handler.SyncHomekit(name, snap)
}
onTick := func(name string, snap Snapshot) {
    handler.PushHub.Notify(name, snap)
}
```

`onPoll` fires only on successful ticks (HomeKit characteristics must not be updated from stale data). `onTick` fires on every tick, success or failure, so the dashboard's `$lastPollAge` and `$stale` signals advance even when polls are timing out — without this the `data-class:stale="$stale"` toggle would never fire under sustained UDP failure. See `SPECIFICATION-hap.md` for HomeKit details.

`PushHub.Notify(name, snap)` (`cmd/breezyd/push_hub.go`) calls the injected render closure (which builds a `DeviceView` and runs `buildPushEvent` from `cmd/breezyd/push_render.go`) and queues a `PushEvent` on every subscriber's bounded channel (capacity 16). When a subscriber is too slow to drain, the oldest event is discarded — events are full-card snapshots, the latest supersedes prior ones, and a dropped event is never user-visible.

If the render closure returns an error, the event is dropped and subscribers receive nothing for that tick. Renders are pure templ output over a `DeviceView`; failures are exceptional and rendering noise should not stall the SSE stream or kill subscribers.

Action handlers also funnel through `Notify` after a successful write (`handlers_ui_write.go::notifyAfterWrite`). The poller and action handlers both hit the same fan-out; subscribers don't distinguish the source.

### PushEvent shape

One `PushEvent` (`cmd/breezyd/push_hub.go::PushEvent`) per device update:

```go
type PushEvent struct {
    DeviceName  string
    SignalsJSON []byte           // JSON for one datastar-patch-signals event
    Blocks      []BlockPatch     // N datastar-patch-elements events
}

type BlockPatch struct {
    Selector string  // e.g. `.card[data-device="bedroom"] [data-block="sensors"]:not([data-edit])`
    HTML     string  // outer-mode HTML
}
```

The SSE handler's `emitPushEvent` emits the signals event *first*, then one elements event per block. Signals first matters: card-outer reactive bindings (e.g. `data-class:stale="$stale"`, `data-attr:data-speed-mode="$speedMode"`) update before any block content arrives.

### Block selectors and editor preservation

Block patches use `:not([data-edit])` selectors, e.g.:

```
.card[data-device="bedroom"] [data-block="schedule"]:not([data-edit])
```

When the schedule editor is open, its root carries `data-edit="true"`. The push patch's selector then matches zero elements and the SSE event becomes a no-op. The editor is preserved across polls. Once the user saves or cancels, the block reverts to the read variant (without `data-edit`) and the next push lands.

The same pattern applies to inline threshold cells: `data-edit="true"` is set on the cell when the editor is open, and the push event's `[data-sensor-cell="{kind}"]:not([data-edit])` selector skips it. See `cmd/breezyd/push_render.go::buildPushEvent`.

The DOM-id-match-on-merge behavior of datastar is what preserves any other element-level state: re-rendering `<details id="sensors-bedroom">` does not flip its `open` attribute when `data-attr:open` is bound to a signal, because the signal's value remains the source of truth across renders.

### Event payload shape

A representative `datastar-patch-elements` event (line breaks inside `data:` are part of the SSE record):

```
event: datastar-patch-elements
id: block:bedroom
data: selector .card[data-device="bedroom"] [data-block="sensors"]:not([data-edit])
data: mode outer
data: elements <details class="block sensors alert" id="sensors-bedroom" ...>...</details>
```

A signal patch:

```
event: datastar-patch-signals
id: signals:bedroom
data: signals {"stale":false,"speedMode":"preset2","airflowMode":"regeneration","lastPollAge":"2s","sensorsAlert":true}
```

Datastar's client merges the signals into the page-level reactive store and patches the elements per selector + mode.

### Keepalive

A `time.Ticker` fires every `keepaliveInterval` (default 30s) and writes a literal `: keepalive\n\n` SSE comment line, then flushes. Datastar's client ignores comment lines; the byte traffic keeps NATs, idle TCP timers, and reverse proxies from dropping the connection. The interval is a package var so tests can shrink it.

### Disconnect and reconnect

When the request context is done (browser closes the tab, navigation, network drop), the handler returns. `defer hub.Unsubscribe(sub)` removes the subscriber and closes its events channel.

The browser's `EventSource` auto-reconnects with `Last-Event-ID` set to the last event the client saw. The reconnect path uses `mode=outer` against existing cards — see "Initial-state pass" above. The dashboard does not replay missed events; reconnect is a full re-sync.

**Known limitation:** there is no automated end-to-end test that disconnects mid-stream, edits state, then reconnects and asserts the catch-up render. The reconnect path is exercised by page-reload tests only.

## Action handlers

Convention for every `POST /ui/devices/{name}/...` handler in `cmd/breezyd/handlers_ui_write.go`:

1. Resolve the device name; 404 if unknown.
2. JSON-decode the body via `decodeJSONBody`. Decode failure → `errorBannerSSE` 422.
3. Validate fields. Validation failure → `uiValidationError` (422 + global banner).
4. Run the write through `h.doDeviceOp` (which acquires the per-device UDP mutex and uses a recording client so the State cache and HomeKit refresh follow the write).
5. On `breezy.ErrAuth` → `uiWriteError` 401 + banner.
6. On other backend error (timeout, UDP failure) → `uiWriteError` 502 + banner.
7. On success → `notifyAfterWrite(name)` (which fetches the just-refreshed snapshot from State and calls `PushHub.Notify`) and `w.WriteHeader(http.StatusOK)` with an empty body.

Why empty body on success: the actor sees the next-tick render of their card via the open `/ui/sse` connection. Returning HTML from the action handler would force every action to know how to render the card and would compete with the SSE-driven render path.

### Error banner via Datastar-Status header

Datastar's `@get`/`@post`/`@put` action helpers discard non-2xx response bodies. The dashboard cannot return a 422 response with a meaningful HTML payload — the body is dropped before datastar's response handler sees it.

The dashboard works around this with a `Datastar-Status` header convention (`cmd/breezyd/handlers_ui_write.go::errorBannerSSE`):

```go
func errorBannerSSE(w http.ResponseWriter, r *http.Request, status int, msg string) {
    w.Header().Set("Datastar-Status", strconv.Itoa(status))
    sse := newSSE(w, r)
    htmlFragment := `<div class="err-banner" role="alert">` + html.EscapeString(msg) + `</div>`
    sse.PatchElements(htmlFragment,
        datastar.WithSelector("#global-error-banner"),
        datastar.WithModeInner())
}
```

The HTTP status is 200. The semantic status (401, 422, 502) lives in the `Datastar-Status` response header for tooling and observability. The error fragment is delivered via SSE and patched into `#global-error-banner` via `mode=inner`. `Datastar-Status` must be set *before* `newSSE` because `datastar.NewSSE` flushes the response head immediately.

Every JSON action handler routes its error paths through `errorBannerSSE`, so the auth/backend/validation triple is shared plumbing. Tests pin the behavior on a representative endpoint (currently `/ui/devices/{name}/power`); per-endpoint duplication is not required.

### Banner messages

`cmd/breezyd/handlers_ui_write.go::uiBannerMsg` translates raw Go errors into user-facing strings:

| Error class | Banner string |
|---|---|
| `breezy.ErrAuth` | `device authentication failed` (401) |
| `context.DeadlineExceeded`, `context.Canceled`, `breezy.ErrTimeout`, `net.Error.Timeout()` | `device timeout (no response)` (502) |
| Other backend error | `err.Error()` (502) |
| Validation failure | the rule-specific message (422) — e.g. `mode must be one of ventilation/regeneration/supply/extract`, `preset must be 1, 2, or 3`, `manual must be 10..100` |

The banner is `aria-live="polite"` for screen readers. It is replaced wholesale on each new error (`mode=inner`) — multiple errors don't stack.

## Editors

Three inline editors override the standard "push replaces card" flow: threshold, schedule, preset.

### Threshold editor

Per sensor cell (eCO2, VOC, RH). Files: `cmd/breezyd/ui/templates/sensor_threshold.templ`, write handlers at `handlers_ui_write.go`.

Read variant (`SensorThresholdRead`):

```templ
<div class="sensor-cell" data-threshold-cell={ kind } data-sensor-cell={ kind }>
    <div class="sensor-label">{ label }</div>
    <div class="value-clickable"
         data-on:click={ datastar.GetSSE("/ui/devices/%s/threshold/%s/edit", name, kind) }
    >{ value }{ suffix }</div>
</div>
```

Click → `GET /ui/devices/{name}/threshold/{kind}/edit` returns an SSE patch that swaps the cell for the edit variant. The edit variant's root carries `data-edit="true"`, which makes the next push's selector skip the cell.

Edit variant (`SensorThresholdEdit`): a tiny `<form>` with `value`, `enabled` (hidden+checkbox dual-input pattern for "uncheck means false"), submit, cancel. Submit fires `@put('/ui/devices/{name}/threshold', {contentType: 'form'})`. The handler validates and writes via `breezy.SetThresholdConfig`, then emits the read-variant patch for the same cell. Cancel fires a plain `@get` for the read variant.

### Schedule editor

One per device. Files: `cmd/breezyd/ui/templates/schedule_block.templ`, write handler at `handlers_ui_write.go::putUISchedule`.

Read variant (`ScheduleBlock`): the read-only entry table plus enable checkbox, "edit schedule" button, and (when alert) a `⚠` warn footer. The schedule alert appears when the daemon's scheduler reports a failed fire — see `SPECIFICATION-daemon.md` for retry semantics.

Edit variant (`ScheduleBlockEdit`): the editor `<form>`. Editor root carries `data-edit="true"` and `<details ... open>` (forced open for editing). The form has:

- An enable checkbox (hidden+checkbox dual-input for the unchecked state).
- "+ add row" button — fires `@get('/ui/devices/{name}/schedule/new-row')` which returns an SSE patch with `mode=append` against the editor `<tbody>`.
- Cancel — fires `@get` for the read variant.
- Save — submits the form via `@put`.

Per row (`ScheduleEditRow`):

| Field | Element | Notes |
|---|---|---|
| At | `<input type="time" name="at" required>` | Browser-native HH:MM picker |
| Action | `<select name="action">` with five options | `data-on:change` calls `scheduleActionChangeExpr` |
| Pct | `<input type="number" name="pct" min=10 max=100>` | Carries `data-orig-pct` (initial pct); `data-on:change="evt.target.dataset.origPct = evt.target.value"` updates the per-row stash so subsequent off→on toggles restore the user's most recent pct rather than the server-render original |
| Delete | `<button class="del">×</button>` | `data-on:click="evt.target.closest('tr').remove()"` |

The Action `<select>`'s `data-on:change` handler synchronizes the pct input on toggle:

```js
const pct = evt.target.closest('tr').querySelector('input[name=pct]');
if (evt.target.value === 'off') {
    pct.value = '';
    pct.setAttribute('readonly', '');
    pct.classList.add('pct-disabled');
} else {
    pct.value = pct.dataset.origPct;
    pct.removeAttribute('readonly');
    pct.classList.remove('pct-disabled');
}
```

Off rows persist a sentinel pct of 0 on the wire (the form sends `""`; the handler treats `pct < 10 && action == "off"` as the "no value" case and stores 0). The handler validates and either:

- Returns the read-variant patch on success.
- Returns the edit-variant patch with an inline `<div class="warn">` error message (and `Datastar-Status: 422`) on validation failure. The form survives the failure with the user's input intact.

Per-row delete is purely client-side — the row is removed from the DOM; persistence happens on save.

### Preset editor

One per numbered preset (1, 2, 3). Triggered by clicking the preset chip. Files: `cmd/breezyd/ui/templates/controls_block.templ::presetEditor`.

The preset editor is rendered inline inside the controls block but hidden until the per-card `$editor` signal equals its preset number. `presetChipExpr` toggles `$editor`:

```js
$editor = $editor === 2 ? 0 : 2;
@post('/ui/devices/bedroom/speed', {payload: {preset: 2}});
```

So one click on a preset chip both writes the preset to the device *and* opens (or closes) the editor for that preset. The `$editor === 2` test is a strict equality check — only one preset editor is open at a time per card. The body-level `editor` signal from the original datastar migration design is replaced by a per-card signal.

The editor UI:

- `automode` checkbox bound to `$automode` (per-card signal, persists across polls).
- `match speeds` checkbox bound to `$matchSpeeds` (per-card signal, default `true`).
- Two range sliders (`supply`, `extract`), each `min=0 max=100 step=1`, two-way bound via `data-bind="preset.N.{side}"`.

Each slider's `data-on:change__debounce.200ms` runs `presetSliderExpr` which:

1. Reads `evt.target.value` and snaps `1..9 → 0` (firmware floor: pct < 10 isn't storable).
2. Mirrors to the other side when `$matchSpeeds`.
3. POSTs `/ui/devices/{name}/preset` with `{preset, supply, extract}` when both sides are ≥10.
4. Computes the implied airflow mode and POSTs `/ui/devices/{name}/mode` if it differs from the current mode and the editor's preset is the active one. Implied modes:
   - `automode` → `ventilation`
   - both ≥ 10 → `regeneration`
   - supply == 0, extract ≥ 10 → `extract`
   - supply ≥ 10, extract == 0 → `supply`

Errors from either POST flow through datastar's SSE error envelope into `#global-error-banner`.

The automode and match-speeds toggles are pure client-side state — they live as datastar signals (`$automode`, `$matchSpeeds`) and are persisted only for the lifetime of the card render. They do not POST to the daemon directly; the next slider drag carries their effect.

The `$preset.{n}.{supply,extract}` signals are reseeded on every card render (see `device_card.templ::presetSeed`). The cost is a slider thumb that can snap to the server's value if a poll lands mid-drag; the gain is that an external change (CLI / HomeKit / device panel) is reflected in the dashboard without page refresh.

### Editor preservation across pushes

All three editors share the same preservation mechanism: the editor's root element carries `data-edit="true"`, and the push pipeline's selectors include `:not([data-edit])`. A push event's selector matches zero elements while the editor is open, so the patch is a no-op. Once the editor exits, the next push lands on the read variant. This is implemented uniformly in `cmd/breezyd/push_render.go::buildPushEvent` for blocks and sensor cells.

## Theme

Light / dark / auto, via `localStorage.theme` and `data-theme` on `<html>`. Files: `cmd/breezyd/ui/templates/theme_picker.templ`, inline IIFE in `layout.templ`.

The picker is a `<details class="theme-picker">` in the page header. The summary is the `<h1>breezyd</h1>`. Clicking the heading opens the popout; the popout has three buttons (sun, moon, auto-system) with `data-theme-set` attributes. The `.theme-picker` class is the durable contract — the IIFE in `layout.templ` looks the picker up via `document.querySelector('.theme-picker')`, so renaming the class would silently break the popout's open/close logic.

The IIFE in `layout.templ`:

1. Forces `picker.open = false` on load (some browsers preserve `<details open>` across bfcache restores or session restore).
2. Listens for clicks on `[data-theme-set]` and:
   - For `auto`: removes `data-theme` from `<html>` and removes `localStorage.theme`.
   - For `light`/`dark`: sets `data-theme` and writes `localStorage.theme`.
3. Closes the picker on outside-click.

A `<head>` inline script runs *before* paint to restore `data-theme` from `localStorage`, preventing a flash of the wrong theme on full page load.

`auto` falls through to CSS `prefers-color-scheme` rules in `cmd/breezyd/ui/style.css`. There is no system-theme listener; `auto` follows the OS at page-load time.

## Templates

All templates live in `cmd/breezyd/ui/templates/`:

| File | Component | Purpose |
|---|---|---|
| `layout.templ` | `Layout` | Page shell (head, body, theme picker, error banner, device-list container) |
| `theme_picker.templ` | `ThemePicker` | Header + light/dark/auto popout |
| `device_card.templ` | `DeviceCard`, `InfoDetails`, `unreachableCard` | Card root + Info block + unreachable variant |
| `sensors_block.templ` | `SensorsBlock`, `PlainSensorCell` | Sensors `<details>` and the 12-cell grid |
| `sensor_threshold.templ` | `SensorThresholdRead`, `SensorThresholdEdit`, `CO2Cell`, `VOCCell`, `HumidityCell` | Editable threshold cells |
| `energy_block.templ` | `EnergyBlock` | Energy `<details>` |
| `schedule_block.templ` | `ScheduleBlock`, `ScheduleBlockEdit`, `ScheduleEditRow` | Schedule read + edit variants |
| `controls_block.templ` | `ControlsBlock`, `presetEditor`, etc. | Power/speed/mode/manual/timer/heater + preset editors |
| `error_banner.templ` | `ErrorBanner` | Standalone banner component (used in tests; the live banner is generated inline by `errorBannerSSE`) |
| `helpers.templ` | `SpeedLabel` | Tiny display helpers |

Each block is its own SSE-patchable unit. The push pipeline (`buildPushEvent`) renders each block into a separate `BlockPatch` so a single push delivers exactly one outer-mode patch per block.

### Templ codegen

The repo follows the standard templ pipeline: `.templ` source files compile to `_templ.go` siblings via `templ generate` (run by `just generate` or `just build`). Never edit `*_templ.go` files directly — the next codegen overwrites the changes.

Render-pipeline contracts are pinned by `cmd/breezyd/ui/templates/render_test.go` against goldens in `testdata/`. `just test-templ-drift` verifies the committed `_templ.go` files match what regeneration would produce; CI fails if they drift.

## Initial connection sequence

End-to-end first-paint sequence for a fresh browser tab:

1. `GET /` returns the shell (`Layout`). DOM renders empty: theme picker, empty `#global-error-banner`, empty `#device-list`.
2. Datastar parses `data-init="@get('/ui/sse')"` and opens the SSE stream.
3. Server: `getUISSE` handler runs. No `Last-Event-ID` — cold load. Subscribes to PushHub, then iterates `collectViews()`.
4. Per device: `emitInitialCard` writes a `datastar-patch-elements` event with `mode=append` against `#device-list`.
5. Client: datastar appends each card. Per-card `data-signals` initializes the per-card store. `data-attr:open` reflects defaults; controls render with their server-rendered states.
6. Server: keepalive ticker arms; handler enters the steady-state select loop.
7. Server: next poller tick → `OnTick(name, snap)` → `PushHub.Notify` → subscribers receive a `PushEvent`.
8. Server: handler emits `datastar-patch-signals` then per-block `datastar-patch-elements` events.
9. Client: signals merge; `:not([data-edit])` patches replace each block where the editor isn't open.

The initial cold-load render is fully server-rendered HTML; client-side reactivity layers on after datastar parses the `data-*` attributes.

## Testing

Test surfaces:

- **Templ goldens** (`cmd/breezyd/ui/templates/render_test.go`, `testdata/golden_*.html`) — pin the rendered HTML for representative DeviceView shapes.
- **Handler tests** (`cmd/breezyd/handlers_ui_write_test.go`, `handlers_ui_sse_test.go`) — per-action validation, error envelope shape, push-on-write, initial-state delivery.
- **Push hub tests** (`cmd/breezyd/push_hub_test.go`) — subscribe/unsubscribe, fan-out, drop-oldest backpressure.
- **Playwright E2E** (`tests/ui/dashboard.spec.ts`, etc.) — full browser against a `breezyd --backend=memory --seed pkg/breezy/fakedevice/snapshot_148.json` daemon built with `-tags breezyd_test_admin`. The tag enables `/test/devices/{name}/...` admin endpoints that drive state changes deterministically. See `cmd/breezyd/handlers_test_admin.go`.
- **Static-render** Go tests live alongside templ tests for assertions that don't need a browser. See the existing static-render coverage in `tests/ui/` and the static Go variants in the templates package.

**Known limitation:** mid-stream-disconnect E2E coverage. Reconnect via page reload and reconnect after subscribing-before-initial-state are pinned. Reconnect mid-stream (server kills the connection, browser auto-retries with `Last-Event-ID`, state is re-sent via `mode=outer`) has no automated end-to-end test.

## Configuration touchpoints

- Daemon listen address (`[daemon].listen` in `~/.config/breezy/config.toml`) controls who can reach the dashboard. Default `127.0.0.1:9876`. LAN exposure requires editing the config or using the NixOS `services.breezyd.nginx` reverse-proxy integration.
- The dashboard has no auth. `services.breezyd.nginx.basicAuthFile` is the production-shaped path for credentials.
- The NixOS module's nginx location for the dashboard sets `proxy_buffering off`, `proxy_cache off`, `proxy_http_version 1.1`, and a long `proxy_read_timeout` — without these, SSE events buffer and the connection drops.

See `nix/module.nix` for module options and `SPECIFICATION-daemon.md` for full deployment shape.

## Out of scope

- Authentication. The dashboard is single-tenant LAN; reverse-proxy auth is the deployment-level answer.
- Multi-tab synchronization beyond what SSE already provides. Each tab opens its own `/ui/sse` and renders the same state independently. There is no client-side broadcast channel.
- Mobile gestures, PWA manifest, offline mode.
- Schedule day-of-week or calendar dates. Times are 24h cyclic local wall-clock. DST behavior follows the daemon's policy — see `SPECIFICATION-daemon.md`.
- WiFi configuration, device firmware updates.
