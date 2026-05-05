# Timer (night/turbo) controls — design

**Date:** 2026-05-05
**Status:** approved, pending implementation plan

## Goal

Make the unit's special-mode timer (`0x0007`: 0=off, 1=night, 2=turbo) controllable from the embedded webui, and show the firmware-supplied countdown (`0x000B`) when a timer is active. Add the matching typed REST endpoint, `pkg/breezy/ops` helper, and CLI verb so the new capability is reachable from every existing surface, not just the UI.

The fan-affecting nature of `0x0007` writes also means closing one existing protocol-correctness gap: the poller's fan-settle window must cover this write.

## Non-goals

- Polling or editing the configured durations (`0x0302` night_duration, `0x0303` turbo_duration). The user has confirmed "remaining is fine".
- Exposing the timer through the HomeKit bridge.
- Distinguishing frost-protection in the override-warn line (separate code path, separate decision).

## Terminology

The protocol, the CLI status output, and the JSON status field all use **`night`** and **`turbo`**. The UI will follow suit. The user's request used "sleep"; this is treated as casual terminology for the same mode and not surfaced.

## Architecture

```
                 webui  ──POST /timer──┐
                                       │
            CLI ──/timer──daemon mode──┤
                                       │
   CLI ──standalone────────────────────┤        cmd/breezyd
              │                        │       ┌─────────────┐
              │                        ├──────►│ Handler     │
              │                                │  /timer     │
              │                                └──────┬──────┘
              │       pkg/breezy/ops                  │
              └──────────► SetTimer ◄─────────────────┘
                              │
                              ▼
                      pkg/breezy.Client
                      WriteParam(0x0007)
```

This mirrors the existing layering for Power, Mode, Speed, and Heater. No new module boundaries.

## Backend changes

### `pkg/breezy/ops.SetTimer`

New function, modeled exactly on `SetHeater`:

```go
func SetTimer(ctx context.Context, c DeviceClient, mode string) error
```

- Accepts `"off"`, `"night"`, `"turbo"`. Anything else → `fmt.Errorf("ops: unknown timer mode %q", mode)`.
- Maps to bytes `0x00`, `0x01`, `0x02` and writes `0x0007`.
- No read-back; matches the existing write-only ops pattern.

### `POST /v1/devices/{name}/timer`

New handler in `cmd/breezyd/handlers_device.go` alongside `postHeater`, registered from `server.go` next to the other write routes. Body shape:

```json
{ "mode": "off" | "night" | "turbo" }
```

Behavior matches `postHeater`:

- 200 on success with body `{"ok": true}`.
- 400 (`bad_request`) on missing/unknown mode, malformed JSON.
- 404 (`not_found`) on unknown device name.
- 502 (`upstream`) on UDP/protocol error.
- The handler dials via `h.dialRecording(name)` and calls `breezy.SetTimer` against the wrapped client. Cache update and `Poller.NoticeWrite` happen automatically because `recordingClient` reports every write through `Handler.recordWrite` → `notice`. No bespoke handler-side cache plumbing is required (and adding any would diverge from `/heater`).

### `cmd/breezyd/poller.go` — add `0x0007` to `fanWriteIDs`

Today, `fanWriteIDs = {0x0002, 0x0044, 0x00B7}` and `fanSensitiveReads = {0x004A, 0x004B, 0x0084}`. Writing `0x0007` (entering or leaving turbo/night) immediately changes fan RPM and air-quality status, but the suppression window does not currently apply. Add `0x0007` to `fanWriteIDs`. No change to `fanSensitiveReads`.

This is a real fix: without it, polling within ~12 s of a turbo write would record stale RPMs into the cache and the UI would show wrong fan speeds.

### CLI verb `breezy <name> timer <mode>`

Mirrors `breezy <name> heater on|off`:

- Daemon mode: `POST /v1/devices/{name}/timer`.
- Standalone: opens UDP, calls `ops.SetTimer`.
- Exit codes per the existing convention (0 success, 1 backend, 2 usage).
- No display of the response — quiet on success, like other writers.

### Status JSON — unchanged

`BuildStatus` already populates `live.special_mode` (string: `off`/`night`/`turbo`/`unknown(N)`) from `0x0007`, and `live.special_mode_remaining_seconds` (int) from `0x000B` when present. The UI will consume these without server-side changes.

## Webui changes

All edits in `cmd/breezyd/ui/index.html`.

### Controls block — add "Timer" row

After the Power toggle and before the Mode segmented control. New 3-way segmented row identical in shape to the existing Mode/Speed seg:

```
Timer
[ off ]  [ night ]  [ turbo ]
5h 12m remaining           ← only when special_mode != "off"
```

- `aria-pressed="true"` on whichever button matches `live.special_mode`.
- Disabled state follows the same `inFlight[name] || stale` rule as the other controls.
- Countdown line uses the existing `humanSeconds(snap.live?.special_mode_remaining_seconds)` helper. The line is suppressed entirely when `special_mode === "off"` so an idle timer doesn't render a stray "0s remaining".

### Click handler

Add a `case "timer":` to the existing button-click delegation in `index.html`. POSTs `{ mode }` to `/v1/devices/{name}/timer` using the existing toast/error/inflight machinery. No bespoke retry, no optimistic UI state — same shape as `case "heater":`.

### Fix `overrideLine()` mis-attribution

Today, when a timer is running and no sensor alert is set, the Fans block renders:

> ⚠ sensor override — fan above setting

…with an empty reasons list. The cause is the timer, not a sensor. Update the function to:

- If `live.special_mode === "turbo"` and no sensor alerts: `⚠ timer active (turbo) — fan above setting`.
- If `live.special_mode === "night"` and no sensor alerts: `⚠ timer active (night) — fan slowed`.
- If sensor alerts are set: keep existing copy (`⚠ sensor override (humidity, co2, voc) — fan above setting`). Sensor alerts win over timer copy because they're the more actionable cause; the timer is visible in its own block anyway.
- Frost (`0x030B`) stays a silent override here — out of scope.

## Tests

### Go unit tests

- `pkg/breezy/ops_test.go::TestSetTimer` — table-driven over `{off, night, turbo}`, asserting the recorded `WriteParam` call sees `(0x0007, []byte{0|1|2})`. Bad mode returns the typed error and emits no write.
- `cmd/breezyd/server_test.go::TestHandler_PostTimer` — POST then read back `0x0007` via `GET /params/0x0007` and assert the hex matches the chosen mode. Mirrors `TestHandler_PostHeater`'s shape. A separate small case asserts 400 on bad mode body. The generic `TestHandler_NotFound_OnUnknownDevice` already covers 404 once the route is registered.
- `cmd/breezyd/poller_test.go` — extend the existing fan-settle test to assert `NoticeWrite(0x0007)` triggers the suppression window for `fanSensitiveReads`.

### Playwright

- New case in `tests/ui/`: `page.route()` returns a snapshot with `live.special_mode = "turbo"` and `special_mode_remaining_seconds = 5400`. Assert:
  - The "turbo" button has `aria-pressed="true"`.
  - The countdown text "1h 30m remaining" is visible.
  - The Fans block shows `⚠ timer active (turbo) — fan above setting` (and not the misattributed sensor copy).
- Second case: `special_mode = "off"`, no countdown line in the DOM.

### Screenshot regeneration

After the UI changes land, run `just screenshot`. Commit the updated PNGs alongside the change. The README embeds the 3-col one; that's the screenshot the user will see.

## CLI integration test

The protocol-side write of `0x0007` is already exercised in `pkg/breezy/integration_test.go` indirectly. No new live integration test is required (and we follow the no-unsanctioned-writes rule: any future integration test for `SetTimer` would need explicit cleanup that restores prior `0x0007` and `0x000B` state, like the existing night-duration test does).

## Documentation

- `README.md` mode/control list (if it enumerates control verbs) gets `timer` added.
- `CHANGELOG.md` entry for the next release.

## Open questions

None as of design approval. Implementation plan to follow.
