# Basic UI — design

**Date:** 2026-05-04
**Status:** approved for implementation
**Repo:** `~/twinfresh`

## Summary

Add a single-page, three-column dashboard served by `breezyd` itself, embedded into the binary via `go:embed`. One card per configured device, polling `GET /v1/devices/<name>` every 5 seconds, with controls that POST to existing handlers for the high-level options the operator changes day-to-day: power, mode, speed, heater. No new HTTP routes for state — only a single `GET /` for the page itself.

This consciously revisits the v1 "no web UI" decision documented in `docs/superpowers/specs/2026-05-03-twinfresh-cli-design.md`. The original spec said the cache was shaped so a UI could be added later without rewriting the core; this is that "later".

## Motivation

- The CLI is the primary control surface and stays the source of truth, but a glanceable dashboard is genuinely useful for "I'm walking past the playroom; is the unit boosting because of CO2?" questions that would otherwise require typing `breezy playroom status`.
- The daemon already polls every device every 30s and serves a JSON snapshot. The UI is mostly a render of state we already produce.
- All four "high level" controls (power, mode, speed, heater) already have HTTP write handlers. The UI doesn't drive new device behavior; it's a thinner, faster path to behavior the CLI already exposes.

## Scope

In scope:
- New `GET /` route on the existing daemon HTTP server, serving a single self-contained HTML file (CSS + JS inlined).
- Three-column responsive layout: three cards side-by-side on viewports ≥ 900px, single column below.
- Per-card display: header (name, IP, power state, last-poll timestamp + stale indicator), sensors block, fans block, service block, firmware footer.
- Per-card controls: power toggle, mode (four buttons), speed (preset 1/2/3 + manual % slider snapping to 5%), heater toggle.
- Auto-refresh every 5 s via `fetch('/v1/devices/<name>')`.
- README + CLAUDE.md sections covering the bind-address change and the security implications.

Out of scope:
- Schedule editing.
- Filter reset / fault reset / RTC set / raw param edit (CLI-only, low frequency).
- Auth, TLS, CSRF tokens. The threat model is "trusted LAN, same as the unit firmware". A UI doesn't change that.
- Build step, JS framework, CSS framework, CDN dependency. The whole UI is one HTML file plus one Go file that embeds it.
- Mobile-specific gestures or PWA manifests. Responsive single-column on small viewports is the only mobile concession.
- An RPC or websocket layer. Polling at 5s matches the daemon's own poll cadence closely enough that streaming is not warranted for v1 of the UI.

## Architecture

### Files

```
cmd/breezyd/
├── ui.go            # handler + go:embed of the UI dir
├── ui/
│   └── index.html   # the entire UI (HTML + inlined <style> + inlined <script>)
└── ui_test.go       # smoke tests: GET / returns 200 with correct content-type
```

`server.go`'s `routes()` gains one line registering `GET /` against the new handler.

### Why one file

The whole UI is small enough to live in a single HTML document with `<style>` and `<script>` blocks. Splitting into separate `app.js` / `style.css` files would force route plumbing for the asset tree without buying maintainability — the JS is a few hundred lines at most. If the UI grows, splitting can come with that growth; YAGNI now.

### Embedding

```go
// cmd/breezyd/ui.go
package main

import (
    _ "embed"
    "net/http"
)

//go:embed ui/index.html
var indexHTML []byte

func (h *Handler) getIndex(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    w.Header().Set("Cache-Control", "no-store")
    _, _ = w.Write(indexHTML)
}
```

`Cache-Control: no-store` is the simplest way to avoid stale UI after a daemon upgrade. Production traffic is loopback or LAN; cache headers don't matter for performance.

### Routing

Add to `server.go`'s `routes()`:

```go
mux.HandleFunc("GET /", h.getIndex)
```

Go 1.22's mux uses longest-prefix-match, so `GET /v1/devices/{name}` etc. continue to win over the `/` root. There is no risk of the UI handler intercepting API traffic.

### Bind address

The daemon's `listen` config default stays `127.0.0.1:9876`. To use the UI from another device on the LAN, the operator changes it to `0.0.0.0:9876` (or a specific LAN IP) in `~/.config/breezy/config.toml`. The README and CLAUDE.md sections will document this and the implied attack-surface change.

No new config keys, no flag changes.

## UI design

### Layout

Single page with a small header (`breezy` + commit/version + last refresh ago) and a 3-column CSS grid below.

```
┌─ breezy v1.0.0 · refreshed 2s ago ────────────────────────────────┐
│                                                                   │
│  ┌─ playroom ─────┐ ┌─ bedroom ──────┐ ┌─ office ───────┐         │
│  │ 192.168.1.148  │ │ 192.168.1.152  │ │ 192.168.1.160  │         │
│  │ ● on · 3s ago  │ │ ● on · 2s ago  │ │ ○ off · 4s ago │         │
│  │                │ │                │ │                │         │
│  │ Sensors        │ │ Sensors        │ │ Sensors        │         │
│  │   RH 52%       │ │   RH 47%       │ │   RH 41%       │         │
│  │   eCO2 3500    │ │   eCO2 600     │ │   —            │         │
│  │   VOC 350      │ │   VOC 120      │ │   —            │         │
│  │   out 20.8°C   │ │   out 20.7°C   │ │   out 20.6°C   │         │
│  │   sup 21.9°C   │ │   sup 21.6°C   │ │   sup —        │         │
│  │   recovery 85% │ │   recovery 84% │ │   recovery —   │         │
│  │                │ │                │ │                │         │
│  │ Fans           │ │ Fans           │ │ Fans           │         │
│  │   sup 5340 rpm │ │   sup 3120 rpm │ │   sup 0 rpm    │         │
│  │   ext 5400 rpm │ │   ext 3180 rpm │ │   ext 0 rpm    │         │
│  │   ⚠ CO2 driving│ │                │ │                │         │
│  │     fan high   │ │                │ │                │         │
│  │                │ │                │ │                │         │
│  │ Service        │ │ Service        │ │ Service        │         │
│  │  filter 89d    │ │  filter 41d    │ │  filter 102d   │         │
│  │  motor 14h32m  │ │  motor 18h05m  │ │  motor 9h11m   │         │
│  │  RTC 3.34 V    │ │  RTC 3.31 V    │ │  RTC 3.29 V    │         │
│  │  faults: none  │ │  faults: none  │ │  faults: none  │         │
│  │                │ │                │ │                │         │
│  │ Controls       │ │ Controls       │ │ Controls       │         │
│  │  Power [● on]  │ │  Power [● on]  │ │  Power [○ off] │         │
│  │  Mode          │ │  Mode          │ │  Mode          │         │
│  │  [vent][regen] │ │  [vent][regen] │ │  [vent][regen] │         │
│  │  [sup ][ext  ] │ │  [sup ][ext  ] │ │  [sup ][ext  ] │         │
│  │  Speed         │ │  Speed         │ │  Speed         │         │
│  │  ( ) 1 (•) 2   │ │  (•) 1 ( ) 2   │ │  ( ) 1 ( ) 2   │         │
│  │  ( ) 3 ( ) man │ │  ( ) 3 ( ) man │ │  ( ) 3 (•) man │         │
│  │  [━━━━━━━] 30% │ │  [━━━━━━━] —   │ │  [━━━━━━━] 50% │         │
│  │  Heater [○ off]│ │  Heater [○ off]│ │  Heater [○ off]│         │
│  │                │ │                │ │                │         │
│  │ fw 0.11 · 2025 │ │ fw 0.11 · 2025 │ │ fw 0.11 · 2025 │         │
│  └────────────────┘ └────────────────┘ └────────────────┘         │
└───────────────────────────────────────────────────────────────────┘
```

CSS:

```css
.grid { display: grid; grid-template-columns: repeat(3, 1fr); gap: 1rem; }
@media (max-width: 900px) { .grid { grid-template-columns: 1fr; } }
```

### Per-card behavior

- **Stale indicator:** if `last_poll` is older than 90 s (3× the daemon poll cadence) the card gets a desaturated style and the timestamp shows in red. Same threshold the spec uses for `breezy_up=0`.
- **Sensor override warning:** when `live.in_user_control == false`, render the `⚠ <reason>` line under the speed control. The reason is derived from `live.sensor_alerts.{humidity,co2,voc}` — at most three booleans, joined with " / " when multiple are true (e.g. `⚠ CO2 / VOC alert driving fan above setting`). Mirror the wording the CLI uses; see `cmd/breezy/render.go` for the existing string.
- **Disabled controls:** when the card is stale OR a write is in flight, controls go `aria-disabled="true"` and click-to-fire suppresses. Re-enabled on the next successful poll.
- **Optimistic updates:** none. Click → POST → wait for the next poll → render. Simpler, and the existing fan-settle behavior in the daemon means optimistic UI would lie about state during the 12-second settle window anyway.
- **Em dash `—` for missing values:** when a sensor returns `nil` (device unreachable, unsupported, or not yet polled).

### Speed control

Two pieces:
- A radio-group of four options: `1`, `2`, `3`, `manual`.
- A range slider, `min=10 max=100 step=5`, only enabled when `manual` is selected.

Selecting a preset radio fires `POST /v1/devices/<name>/speed` with `{"preset": N}` immediately. Selecting `manual` does NOT fire on its own — it just enables the slider. The slider fires `POST /v1/devices/<name>/speed` with `{"manual": pct}` on `change` (mouseup / blur), not `input` (every drag tick), to avoid flooding the daemon during slider drags.

### Polling

`setInterval(refresh, 5000)`. `refresh()` issues `GET /v1/devices/<name>` for each configured device in parallel (Promise.all), updates the DOM, and updates the "refreshed Ns ago" header.

A failed fetch for one device does not block the others — that device's card just goes stale.

The list of device names is fetched once at page load via `GET /v1/devices`.

## Error handling

- Page load fetch (`GET /v1/devices`) failure → render a single error banner across the top of the grid: `cannot reach daemon — is breezyd running?`. Retry every 5 s.
- Per-card fetch failure → card goes stale (90 s threshold) and the timestamp turns red. The whole page is not affected.
- POST failure → small inline toast under the failing control, with the daemon's error envelope's `error` text. Toast clears on next successful poll for that device.

No client-side validation beyond the slider's HTML `min`/`max`/`step`. The daemon already validates and returns useful error messages; surfacing them is enough.

## Testing

- `cmd/breezyd/ui_test.go`:
  - `GET /` returns 200 with `Content-Type: text/html; charset=utf-8` and a non-empty body.
  - `GET /` returns the embedded HTML byte-for-byte (smoke test that `go:embed` is wired).
  - `GET /v1/devices` continues to return JSON (regression that the new `GET /` doesn't intercept).
- The HTML/JS itself is not unit-tested. It's small and the data shape is the existing `getDevice` response, which has its own tests in `server_test.go`. Manual smoke test on a real `breezyd` instance is the verify step.

## Security implications

The daemon's HTTP API has no authentication. Binding it to a LAN-reachable address means anyone on the same network can:
- enumerate configured device names and IDs (`GET /v1/devices`)
- read or write any device parameter (`GET /POST /v1/devices/{name}/params/{id}`)
- including parameters that change the device's protocol password and WiFi credentials

Mitigation, in priority order:
1. **VLAN segmentation.** Same recommendation the README already gives for the units themselves: an IoT VLAN that the rest of your home LAN can't reach, plus only the host running `breezyd` permitted into that VLAN. The UI being on the same network as the units means the trust boundary is already there.
2. **Reverse proxy with auth.** Put `breezyd` behind nginx with basic auth or a TLS-terminating proxy if LAN trust isn't enough. This is a deployment choice; not a daemon feature.
3. **Continue to default `127.0.0.1:9876`.** The default stays loopback so a fresh install is not surprised into LAN exposure. The operator opts in by editing the config.

The spec adds a section to README's "Security" block (and CLAUDE.md) describing the LAN-bind tradeoff, with a pointer to the VLAN segmentation recommendation already there.

## Out of scope (deliberate, mirroring v1's "no" list)

- Schedule editing — still on-device only.
- WiFi reconfig — vendor app only.
- Home Assistant or MQTT — the JSON API and Prometheus surface remain the integration path.
- Authentication. If the LAN trust boundary doesn't fit your environment, run `breezyd` behind a reverse proxy.

## Risks

- **Bind-address misuse.** A user enables LAN access and forgets the implications; a roommate/guest on the same WiFi pokes the units. Mitigation: README + CLAUDE.md change explicitly enumerate the surface area.
- **Polling load.** 3 devices × `GET /v1/devices/<name>` every 5 s = 0.6 req/s, all cache-served. Trivial on the daemon. If the unit count grows past ~20, the polling shape may need rethink.
- **HTML drift vs. snapshot shape.** The UI binds to specific JSON keys. If a future change renames a field in `snapshot.go`, the UI silently breaks. Mitigation: the HTML smoke test in `ui_test.go` doesn't catch this; rely on manual verification when changing snapshot fields, and keep the UI's read surface small enough that an audit is cheap.
- **Browser cache after upgrade.** `Cache-Control: no-store` on `GET /` should handle this, but some browsers still keep the page in bfcache. Mitigation: a hard refresh after upgrade. Documented in the README change.
