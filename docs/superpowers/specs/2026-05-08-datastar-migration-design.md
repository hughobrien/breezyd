# Datastar migration — design

Date: 2026-05-08

Spec for replacing htmx + the cookie-based UI-state hack we built on top of
it with [datastar](https://data-star.dev/) and SSE-driven push updates.
Closes the architectural gap that made #46/#49/#53 expensive: client UI
state has been server-laundered through a cookie because htmx assumes the
server owns rendering. Datastar gives client reactive state first-class
treatment and uses SSE for server pushes, which fits this dashboard's
shape (small device count, real-time updates desirable, single-tenant LAN).

## Background

The 1.1 htmx migration produced a clean substrate for read-mostly surfaces
(sensors, energy, schedule rows) but pushed all interactive state into a
2.5-week-old cookie protocol when we restored the preset editor (#53).
That work shipped, but it cost an `internal/uistate` package, four UI-state
fields on `DeviceView`, a `computeDetailsOpen` server-side merge of
defaults + cookie + force-open rules, ~210 lines of inline JS to read and
write the cookie before htmx XHRs fire, and ad-hoc `data-speed-mode` /
`data-airflow-mode` attributes so JS could read snapshot fields off the
DOM. The cookie is the only state mechanism htmx-style hypermedia leaves
us with for client-only UI concerns; the complexity isn't accidental, it's
the cost of fighting the framework.

Datastar is htmx + Alpine in one library, designed by people who hit the
same friction. Replacing both substrates removes the cookie protocol
entirely (client state is local, not server-laundered) and lets us pick up
SSE push (real-time updates without 5s polling) as a free upgrade rather
than a separate effort.

## Goals

- Drop `internal/uistate`, the cookie protocol, the four `DeviceView` UI
  fields, and `computeDetailsOpen` / `defaultOpen`. ~600 lines of code
  removed.
- Replace ~210 lines of inline JS in `layout.templ` with datastar
  attribute directives in templ components. Net JS in the page shell:
  ~25 lines (theme picker FOUC prevention only).
- Move from 5-second client polling to SSE push: every successful UDP
  poll fans out updated card HTML to subscribed browsers in real time.
- Keep templ. Keep all daemon business logic. Keep the `/v1/*` JSON API
  unchanged. Keep `pkg/breezy`, `pkg/homekit`, the energy tracker, the
  scheduler.

## Non-goals

- Datastar's signal-only updates (we always push full card HTML; partial
  signal updates over SSE aren't worth the protocol complexity at our
  scale).
- `Last-Event-ID` resume on SSE reconnect — every reconnect is a full
  re-sync. Costs slightly more bandwidth on reconnect, simpler.
- Dashboard authentication / multi-user concerns. Dashboard remains a
  single-tenant LAN tool; SSE connections aren't authenticated beyond the
  existing process boundary.
- Changes to `/v1/*` JSON. Only `/ui/*` is reshaped.

## Architecture

Three things change at the substrate level:

1. **Network library:** `htmx-2.0.4.min.js` → `datastar.min.js` (vendored,
   ~15 KB). All `hx-*` attributes become `data-*` (datastar). Action
   endpoints under `/ui/*` switch from "render HTML to response writer"
   to "write SSE event stream containing the fragment(s) to update."
   The official Go SDK (`github.com/starfederation/datastar/sdk/go`)
   wraps the wire format.

2. **Client state:** Cookie protocol gone. `internal/uistate` deleted.
   `DeviceView.DetailsOpen / EditingPreset / Automode / MatchSpeeds`
   deleted. `computeDetailsOpen` and `defaultOpen` deleted. Each device
   card declares its UI state inline via a `data-signals` attribute
   carrying initial JSON values. Force-open rules are gone — alert state
   is rendered as a `.alert` CSS class on the relevant `<details>`,
   coloring the heading red and prefixing a `⚠`. User's open/closed
   choice is honored regardless of alert state.

3. **Update path:** Client-side 5s polling gone. New endpoint
   `GET /ui/sse` opens a long-lived SSE connection per browser. The
   existing UDP poller (unchanged) gets a new hook: after each
   successful snapshot, push a `datastar-merge-fragments` event to every
   subscribed connection with the updated card HTML. Browser
   EventSource auto-reconnects on disconnect; on reconnect the daemon
   emits current snapshots for all devices so the UI rejoins cleanly.
   Action endpoints don't return HTML — they call `Notify`, return
   200 (or an SSE error fragment on 4xx/5xx), and let the actor's open
   `/ui/sse` connection deliver the state update.

The daemon's per-device UDP polling stays exactly as it is — same
poll cadence, same fan-settle window, same `dialRecording` mutex per
device, same `breezy.Client` invariants. What changes is that snapshots
fan out to subscribed browsers in real time instead of being cached and
waiting 5s for the dashboard to GET them.

## SSE protocol shape

### Action responses (per-request streams)

Every existing `/ui/*` action handler keeps its URL and method but
changes its response. Today: `200 OK` + `text/html` body containing the
rendered DeviceCard. After: handlers don't render the card themselves
on success — they call `h.PushHub.Notify(name, snap)` and return
`200 OK` with an empty body. The actor sees the update via their open
`/ui/sse` connection.

Validation errors and backend errors return SSE-format response bodies
targeting `#global-error-banner`:

```
HTTP/1.1 422 Unprocessable Entity
Content-Type: text/event-stream

event: datastar-merge-fragments
data: selector #global-error-banner
data: mergeMode inner
data: fragments <div class="err-banner" role="alert">preset must be 1..3</div>

```

Datastar's response handler applies the fragment regardless of status
code. The handler emits the fragment via the SDK:

```go
sse := datastar.NewSSE(w, r)
sse.MergeFragments(`<div class="err-banner" role="alert">...</div>`,
    datastar.WithSelector("#global-error-banner"),
    datastar.WithMergeMode(datastar.FragmentMergeModeInner))
```

### Push channel (long-lived stream)

New endpoint `GET /ui/sse`:

```go
func (h *Handler) getUISSE(w http.ResponseWriter, r *http.Request) {
    sse := datastar.NewSSE(w, r)

    // Initial state: render each device's current card and write it
    // to the stream. Same code path actions take, just iterated.
    for _, view := range h.collectViews(r) {
        h.PushHub.WriteCard(sse, view)
    }

    sub := h.PushHub.Subscribe()
    defer h.PushHub.Unsubscribe(sub)

    keepalive := time.NewTicker(30 * time.Second)
    defer keepalive.Stop()

    for {
        select {
        case <-r.Context().Done():
            return
        case ev := <-sub.Events:
            ev.WriteTo(sse)
        case <-keepalive.C:
            // Comment-line event keeps idle proxies from dropping the connection.
            _, _ = fmt.Fprint(w, ": keepalive\n\n")
            if f, ok := w.(http.Flusher); ok {
                f.Flush()
            }
        }
    }
}
```

`PushHub.WriteCard` is the single render-and-write helper used by both
the initial-state loop and the per-event broadcast — it takes a
`DeviceView`, runs the templ component, and writes a
`datastar-merge-fragments` event. `Notify(name, snap)` builds the
DeviceView internally (via the injected render function) and queues the
event onto each subscriber's channel.

### PushHub

A new type in `cmd/breezyd/push_hub.go` (~80 lines) maintains the set of
subscribed connections, owns the fan-out, and exposes
`Notify(name, snap)`:

```go
type PushHub struct {
    mu     sync.Mutex
    subs   map[*subscriber]struct{}
    render func(name string, snap Snapshot) ([]byte, error)
}

type subscriber struct {
    events chan event   // buffered, size 16
}

func (h *PushHub) Notify(name string, snap Snapshot) {
    body, err := h.render(name, snap)
    if err != nil { return }
    ev := event{
        selector: fmt.Sprintf(`.card[data-device=%q]`, name),
        mode:     "outer",
        body:     body,
    }
    h.mu.Lock()
    defer h.mu.Unlock()
    for s := range h.subs {
        select {
        case s.events <- ev:
        default:
            <-s.events       // drop oldest
            s.events <- ev
        }
    }
}
```

`render` is injected at construction and uses templ to render the
`DeviceCard` component. The hub never touches a device — pure
templ-on-cached-snapshot.

The poller calls `h.PushHub.Notify(name, snap)` after each successful
poll. Action handlers call it after each successful write. The two
sources funnel through the same path; subscribers don't distinguish.

## Client state model

Each device card declares its UI state inline. Templ emits the card root
with a `data-signals` attribute carrying initial values:

```go
templ DeviceCard(v ui.DeviceView) {
    <div class="card"
         data-device={ v.Name }
         data-signals={ initialCardSignals(v) }>
        ...
    </div>
}

func initialCardSignals(v ui.DeviceView) string {
    s := map[string]any{
        "automode":    false,
        "matchSpeeds": true,
        "detailsOpen": map[string]bool{
            "info":     false,
            "sensors":  true,
            "energy":   false,
            "schedule": false,
        },
    }
    b, _ := json.Marshal(s)
    return string(b)
}
```

The body element holds one global signal that drives the cross-card
"only one preset editor open at a time" rule:

```html
<body data-signals='{"editor": {"device": null, "preset": 0}}'
      data-on-load="@get('/ui/sse')">
```

Per-card UI preferences (`automode`, `matchSpeeds`, `detailsOpen`) are
persisted via datastar's `data-persist` to localStorage, scoped per
card-id so cards don't share each other's choices. The global `editor`
signal is intentionally ephemeral — closing all editors on page reload
is the legacy behavior and matches user expectation.

### Bindings

```html
<details data-bind:open="$detailsOpen.sensors"
         class={ "block", "sensors", templ.KV("alert", v.Sensors.AlertActive) }>
  <summary><h3>Sensors</h3></summary>
  ...
</details>

<button data-action="preset" data-value="2"
        data-on-click="$editor = $editor.device === 'bedroom' && $editor.preset === 2
                                 ? { device: null, preset: 0 }
                                 : { device: 'bedroom', preset: 2 };
                       @post('/ui/devices/bedroom/speed', {preset: 2})"
        data-attr-aria-pressed="$speedMode === 'preset2'">
  48/49
</button>

<div class="preset-editor"
     data-show="$editor.device === 'bedroom' && $editor.preset === 2">
  <label><input type="checkbox" data-bind="$automode"> automode</label>
  <label><input type="checkbox" data-bind="$matchSpeeds"> match speeds</label>
  <input type="range" min="0" max="100"
         data-bind="$preset2Supply"
         data-on-change="$preset2Supply = snapZero($preset2Supply);
                         if ($matchSpeeds) $preset2Extract = $preset2Supply;
                         @post('/ui/devices/bedroom/preset',
                               { preset: 2, supply: $preset2Supply, extract: $preset2Extract })">
</div>
```

`snapZero` is a tiny client-side helper exposed via `data-on-load` on
body — `function snapZero(v) { return v > 0 && v < 10 ? 0 : v }`.

### Alert as CSS class, server-rendered

Each block gets an `alert` class when its underlying condition fires.
The class is updated server-side on every push/action because the card
re-renders. CSS rules:

```css
.block.alert > summary > h3,
.device-info.alert > summary > h2 {
    color: var(--alert-fg);
}
.block.alert > summary::before,
.device-info.alert > summary::before {
    content: "⚠ ";
}
```

User's persisted open/closed choice is unchanged when alert flips.
Alert state is purely visual.

### Implied-mode write

The "after a slider drag, fire a `POST /mode` if the implied airflow mode
differs from current" logic moves into the datastar `data-on-change`
expression on each slider:

```
data-on-change="
  $preset2Supply = snapZero($preset2Supply);
  if ($matchSpeeds) $preset2Extract = $preset2Supply;
  if ($preset2Supply >= 10 && $preset2Extract >= 10) {
    @post('/ui/devices/bedroom/preset', {...});
    if (!$automode && $speedMode === 'preset2' && $airflowMode !== 'regeneration') {
      @post('/ui/devices/bedroom/mode', { mode: 'regeneration' });
    }
  } else {
    /* skip /preset; sub-10 not storable. Implied mode write below. */
    if (!$automode && $speedMode === 'preset2' &&
        $airflowMode !== ($preset2Supply === 0 ? 'extract' : 'supply')) {
      @post('/ui/devices/bedroom/mode',
            { mode: $preset2Supply === 0 ? 'extract' : 'supply' });
    }
  }
"
```

(In practice the inline expression lives in a `data-on-change` and may
be split across template lines; templ handles the escaping. Templ helper
functions can build the expression to keep markup readable.)

Server-side `$speedMode` and `$airflowMode` come from initial card
signals — populated from `v.SpeedMode` / `v.AirflowMode` and refreshed
on every push. No DOM-level `data-speed-mode` attrs needed.

## Files touched

### Created

- `cmd/breezyd/push_hub.go` (~80 lines) — `PushHub`, `subscriber`, fan-out.
- `cmd/breezyd/push_hub_test.go` — fan-out, drop-oldest, unsubscribe-while-broadcasting.
- `cmd/breezyd/handlers_ui_sse.go` — `getUISSE`.
- `cmd/breezyd/handlers_ui_sse_test.go` — initial state, push delivery,
  cancel-on-context-done.
- `cmd/breezyd/ui/vendor/datastar-<version>.min.js` — vendored. Content
  hash on the URL like the existing CSS.

### Deleted

- `internal/uistate/` — entire package.
- `cmd/breezyd/ui/vendor/htmx-2.0.4.min.js` and `htmx-response-targets-2.0.4.min.js`.
- `cmd/breezyd/handlers_ui_read.go::computeDetailsOpen`, `defaultOpen`.
- `cmd/breezyd/handlers_ui_read.go::getUIDeviceList`, `getUIDeviceCard`
  and their tests (replaced by `/ui/sse` initial state and per-action push).
- `DeviceView.DetailsOpen / EditingPreset / Automode / MatchSpeeds`.
- All ~210 lines of inline JS in `layout.templ` (theme-picker IIFE
  stays).

### Modified

- `cmd/breezyd/handlers_ui_write.go` — every action handler: drop HTML
  response body on success, call `h.PushHub.Notify`, return 200 OK.
  Validation/backend errors return SSE-format error envelopes.
- `cmd/breezyd/server.go` — register `GET /ui/sse`. Drop now-unused
  `/ui/devices` and `/ui/devices/{name}/card` routes.
- `cmd/breezyd/poller.go` — call `h.PushHub.Notify(name, snap)` after
  each successful poll.
- `cmd/breezyd/main.go` — construct `PushHub` with templ-render
  injection, wire into `Handler`, pass to poller.
- `cmd/breezyd/ui/templates/layout.templ` — replace inline JS + htmx
  vendor scripts with one `<script src="/ui/vendor/datastar-...js">`;
  body gets `data-signals='{"editor": ...}'` and
  `data-on-load="@get('/ui/sse')"`. Theme picker FOUC IIFE stays.
- `cmd/breezyd/ui/templates/device_card.templ` — `data-signals` seed,
  `data-bind:open` on `<details id="info-...">`, `templ.KV("alert", ...)`.
- `cmd/breezyd/ui/templates/{sensors,energy,schedule}_block.templ` —
  same pattern.
- `cmd/breezyd/ui/templates/controls_block.templ` — `data-on-click`
  expressions replace `hx-post`/`hx-vals`. `data-show` replaces
  conditional `hidden`. Drop the unused `data-preset-editor` attribute
  (datastar's `data-show` is the visibility primitive now).
- `cmd/breezyd/ui/style.css` — add `.block.alert` color rules + `⚠`
  pseudo-element. Drop `.preset-editor[hidden]` (no longer needed —
  `data-show` uses `display:none` directly).
- `cmd/breezyd/ui_assets.go` — vendor entry for datastar replaces htmx
  entries.
- `cmd/breezyd/ui/templates/render_test.go` + `testdata/golden_*.html`
  — regenerated for new attribute syntax. Editor-state-variant golden
  goes away.
- `nix/module.nix` — the auto-configured nginx location for the
  dashboard adds `proxy_buffering off`, `proxy_cache off`,
  `proxy_http_version 1.1`, and a long `proxy_read_timeout`. Without
  these, SSE events buffer and the connection drops.
- `tests/ui/dashboard.spec.ts` — selectors update; cookie-based tests
  go away; add SSE-driven update tests; cross-tab synchronization test;
  reconnect smoke test.
- `tests/ui/screenshot.ts` — adapt for SSE-driven initial paint.
- `go.mod` / `go.sum` — add `github.com/starfederation/datastar/sdk/go`.
  Run `go mod tidy`. Update `flake.nix` `vendorHash`.
- `CLAUDE.md` and `CHANGELOG.md` — describe the architecture change.

## Testing

### Unit

- `push_hub_test.go` — five tests: subscribe/unsubscribe round-trip;
  broadcast reaches N subscribers; full buffer drops oldest event;
  concurrent broadcast + unsubscribe doesn't panic; render error in one
  notify doesn't poison the hub.
- `handlers_ui_sse_test.go` — initial state event for each device,
  Notify event delivery, context-cancel clean unsubscribe.
- `handlers_ui_write_test.go` — keep validation-error and ErrAuth
  tests with SSE-format error fragment assertions. Drop success-case
  body assertions; replace with "PushHub.Notify called with (name, snap)"
  using a fake hub.
- `cmd/breezyd/ui/templates/render_test.go` — goldens regenerated for
  datastar attribute syntax. Verify: `data-signals` present on card
  root, `data-bind:open` on each `<details>`, `data-show` on preset
  panels, `class="alert"` toggled by alert state.

### Playwright

`tests/ui/dashboard.spec.ts` adapts to SSE-driven updates. Most
existing assertions move from "wait for poll" to "trigger via
fakedevice admin endpoint, expect SSE-driven update":

- Cookie-based tests deleted (cookie is gone).
- New: open dashboard, change a fakedevice value via the admin
  endpoint, assert the card updates without page reload (SSE delivery).
- New: open two browser contexts (tabs), trigger an action in tab A,
  assert tab B updates via its own SSE stream.
- New: server kills the SSE connection (close-the-handler hook in test
  daemon), assert browser auto-reconnects and view recovers.
- Existing automode + match-speeds + slider behavior tests: rewrite
  selectors against datastar attributes; behavioral assertions
  unchanged.
- The "preset editor: open state survives 5s poll (no flicker)" test:
  rewrite as "trigger SSE push, editor open state preserved." Same
  intent, different mechanism.

`tests/ui/screenshot.ts` — adapt initial-paint wait to "first SSE
event received" rather than "first /ui/devices response."

`fakedevice_admin` likely needs a "trigger a state change visible to
the daemon" hook so Playwright can drive pushes. May already exist;
verify during impl.

### Integration check

- `just check-all` clean.
- `nix-check` (or `nix build` if needed) — verify the module change is
  syntactically valid.
- Manual test on real hardware before merging: open the dashboard,
  change something via CLI or HomeKit, confirm browser updates within
  a poll cycle; reconnect by killing the daemon and confirm browser
  recovers.

## Risks

**Datastar maturity.** Younger than htmx/Alpine. Mitigation: SDK pins
us to a specific protocol version via go.mod; the project just rolled
1.0 and minor versions are additive. Track upstream releases via the
existing dep-update flow. If the SDK API churns, our wrappers in
handlers + `PushHub` are the only sites that need updating.

**EventSource heartbeat.** Browsers and intermediaries drop idle TCP
connections. Send a comment-line event every 30s on the SSE stream
(via the keepalive ticker in `getUISSE`). Cheap.

**Reconnect cost scales with device count.** Every reconnect re-emits
all device cards. At 3 devices × ~5 KB/card = 15 KB, trivial. At 30
devices = 150 KB. Not a current concern; flag for future scaling.

**No `Last-Event-ID` resume.** Documented choice in non-goals. Costs a
small amount of bandwidth on reconnect, no real downside at this scale.

**PushHub mutex under high load.** Single mutex around the subscriber
map. Fine for ≤10 subscribers; if the dashboard ever served dozens of
simultaneous browsers, the mutex would become a hot spot. Out of scope
for v1, easy to swap for `sync.Map` later if it ever matters.

**External callers of `/ui/*` endpoints.** None expected — it's the
dashboard's private surface. CLI uses `/v1/*`; HomeKit bridge uses
`pkg/breezy/ops` directly. Sanity-check during impl that nothing else
GETs `/ui/devices` or POSTs to `/ui/*`.

**Datastar SDK transitive deps.** Verify during impl: should be
near-zero (it's an SSE writer wrapper). If it pulls in something
heavy (a logging framework, an unrelated JSON library), reconsider
hand-rolling. Likely fine.

**Datastar attribute syntax in templ.** Datastar uses dot-notation in
attribute names like `data-on-click__capture` and namespaced selectors
like `data-attr-aria-pressed`. Templ accepts these but every templ
component using them needs careful escape review. Render goldens will
catch syntax mistakes.

**Inline expression complexity in templates.** The `data-on-change` for
the slider is non-trivial JS embedded in HTML. If any single
expression grows past ~5 lines, extract it into a small client helper
loaded via `data-on-load="@get('/ui/vendor/dashboard.js')"` rather than
inlining further. We start inline, factor out as needed.
