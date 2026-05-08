# Behavior catalog

A single backward-looking reference for **what the breezyd dashboard, daemon, and CLI currently do**, end-to-end. Per-feature design docs in this directory remain authoritative for *why* each feature exists; this catalog owns *what is currently true*.

Test code in this repo binds to the entries below by ID — adding or changing a behavior means updating this file in the same change.

**Status:** initial skeleton (2026-05-08). Most entries below are stubs flagged with `(TODO)`. Issue #36 tracks completion.

## How to use

- Each behavior has a stable ID like `B-NN-name`. IDs do not get reused; deprecated behaviors stay listed with a strikethrough and a redirect.
- `Source specs:` links to existing per-feature design docs. If you change behavior, also update the design doc OR add a new one — the catalog refers, it does not justify.
- `Tests:` lists the Go / Playwright tests that pin this behavior. A new behavior without tests is a TODO; a behavior whose tests don't exist is a regression risk.
- Edge cases that are explicitly *out of scope* (e.g. DST handling for schedules) are listed too — easier to find a gap than infer one.

## Index

The **Tests** column is the live coverage map — `✓` = pinned by an existing test that fails on regression, `partial` = touched but not locked down, `✗` = no targeted test.

| ID | Behavior | Spec | Tests |
|----|----------|------|-------|
| **Card rendering** | | | |
| B-01 | Initial render for a healthy, polled device | done | ✓ |
| B-02 | Stale device card | done | ✓ |
| B-03 | Unreachable / never-polled device | TODO | partial |
| B-04 | Device with active fault(s) | TODO | ✗ |
| B-05 | Sensor threshold alert (eCO₂ / VOC / RH) | TODO | partial |
| B-06 | Schedule alert (failed fire) | TODO | partial |
| B-07 | Energy block: per-model calibration missing | TODO | partial |
| B-08 | Energy block: regen mode active | TODO | partial |
| B-09 | Energy block: non-regen mode | TODO | ✗ |
| B-10 | Filter near-end-of-life | TODO | ✗ |
| B-11 | Low RTC battery | TODO | ✗ |
| **Card interactions** | | | |
| B-12 | Power toggle | TODO | ✓ |
| B-13 | Heater toggle | done | ✓ |
| B-14 | Mode chip click | TODO | ✓ |
| B-15 | Preset chip click → editor open/close | TODO | ✓ |
| B-16 | Manual button click | TODO | ✗ |
| B-17 | Manual slider drag (debounced) | TODO | ✗ |
| B-18 | Preset slider with match-speeds | TODO | ✗ |
| B-19 | Preset slider with automode | TODO | ✗ |
| B-20 | Timer button click | TODO | ✗ |
| B-21 | Reset filter button | TODO | ✗ |
| B-22 | Reset faults button | TODO | ✗ |
| B-23 | Threshold editor: open → edit → save | TODO | ✓ |
| B-24 | Schedule editor: open → add row → save | TODO | partial |
| B-25 | Schedule editor: action toggle clears pct | TODO (#44) | ✗ |
| B-26 | Schedule editor: delete row | TODO | ✗ |
| B-27 | Theme picker: light / dark / auto | TODO | partial |
| B-28 | `<details>` open-state persistence across polls | TODO | ✗ |
| **Push channel** | | | |
| B-29 | Initial state on `/ui/sse` connect | done | ✓ |
| B-30 | Per-device update on poll | TODO | ✓ |
| B-31 | Reconnect via page reload | TODO | ✓ |
| B-32 | Reconnect via datastar auto-retry | TODO | ✗ |
| B-33 | Keepalive while idle | TODO | ✗ |
| B-34 | `#global-error-banner` populates on action error | done | ✓ |
| **Configuration / persistence** | | | |
| B-35 | First-run config bootstrap | TODO | partial |
| B-36 | Discovery on start / periodic | TODO | ✗ |
| B-37 | Schedule persists across daemon restart | TODO | partial |
| B-38 | Energy state persists across daemon restart | TODO | partial |
| B-39 | Device added to config without restart (or not) | TODO | ✗ |
| **HomeKit** | | | |
| B-40 | Bridge advertises configured devices | TODO | partial |
| B-41 | HomeKit write → UDP | TODO | partial |
| B-42 | HomeKit read reflects poller cache | TODO | partial |
| **CLI** | | | |
| B-43 | `breezy ls` in standalone mode | TODO | partial |
| B-44 | `breezy ls` in daemon mode | TODO | partial |
| B-45 | `breezy <name> status` | TODO | ✗ |
| B-46 | `breezy <name> on` / `off` / `speed` / `mode` | TODO | partial |
| B-47 | `breezy discover` | TODO | partial |
| B-48 | CLI exit codes | TODO | ✗ |

### Test files by area

| Area | Test files |
|------|------------|
| Card rendering (Go) | `cmd/breezyd/ui/templates/render_test.go`, `cmd/breezyd/ui/templates/testdata/golden_*.html`, `cmd/breezyd/handlers_ui_read_test.go` |
| Card rendering (E2E) | `tests/ui/dashboard.spec.ts` (`rendering` + `sensor block`), `tests/ui/smoke.spec.ts` |
| Card interactions | `cmd/breezyd/handlers_ui_write_test.go` (Go contract), `tests/ui/dashboard.spec.ts` (`controls`, `threshold editor`, E2E) |
| Push channel | `tests/ui/dashboard.spec.ts` (`SSE push`, `reconnect`, `error paths`); SSE handler is `cmd/breezyd/handlers_ui_sse.go` (no direct Go test today) |
| Configuration / persistence | `internal/config/*_test.go`, `cmd/breezyd/scheduler_test.go`, `cmd/breezyd/energy_tracker_test.go` |
| HomeKit | `pkg/homekit/*_test.go`, `cmd/breezyd/homekit_test.go` (if present) |
| CLI | `cmd/breezy/*_test.go`, `internal/config/*_test.go` |
| Datastar bundle / template integration | none today — see `datastar_contract_test.go` proposal in #36 |

---

## Card rendering

### B-01: Initial render for a healthy, polled device

**Trigger:** Browser opens `/`. Daemon has at least one successful poll for this device. Device is powered, authenticated, not in a fault state.

**Expected outcome:** A `.card[data-device="{name}"]` is appended to `#device-list` (mode `append`) via the initial-state SSE pass on `/ui/sse`. The card contains:

- Header `<h2>` with the device name.
- Power button reflecting current `Power` (aria-pressed).
- Info `<details>` (collapsed by default) with IP, serial, firmware version+date, filter status, motor hours, RTC voltage, faults.
- Energy block (collapsed by default) — content depends on B-07 / B-08 / B-09.
- Sensors block (open by default) showing eCO₂, VOC, RH, recovery, supply/exhaust temps, deltas, supply/exhaust RPMs.
- Schedule block (collapsed) — content depends on B-06 / B-24.
- Controls: SPEED chips (preset 1–3 + manual), MODE chips (auto/regen/supply/exhaust) when speed_mode is `manual`, manual fan slider when speed_mode is `manual` and special_mode is `off`, TIMER (night/turbo) and HEATER buttons.

**Edge cases:**
- Special-mode active (night/turbo): MODE chips and manual slider not rendered; "N remaining" label shown under TIMER.
- speed_mode is a preset: MODE chips and manual slider not rendered.

**Source specs:** [2026-05-04-basic-ui-design.md](./2026-05-04-basic-ui-design.md), [2026-05-08-datastar-migration-design.md](./2026-05-08-datastar-migration-design.md)

**Tests:** `tests/ui/dashboard.spec.ts` (`@smoke card renders for the configured device`), `cmd/breezyd/ui/templates/render_test.go` (`TestDeviceCardGolden/snapshot_*`).

---

### B-02: Stale device card

**Trigger:** Daemon's last poll for the device was longer than `staleAfter` ago (currently 2× poll interval), but at least one prior poll succeeded.

**Expected outcome:** Card renders with class `card stale`. All interactive controls (power, mode, preset, manual, timer, heater) have the `disabled` attribute. A "Xs ago" timestamp row appears in the info block, colored red when over threshold. The pre-stale data continues to render (this is a "card with a warning" view, not "card replaced with placeholder").

**Edge cases:**
- Newly configured device that has *never* polled successfully: see B-03 — different state, different render.
- Comes back online: poll updates card, `.stale` class removed, controls re-enabled via the SSE patch (B-30).

**Source specs:** [2026-05-04-basic-ui-design.md](./2026-05-04-basic-ui-design.md)

**Tests:** `cmd/breezyd/ui/templates/render_test.go` (`TestDeviceCardGolden/snapshot_settling`), `cmd/breezyd/ui/templates/render_test.go` (`TestDeviceCardGolden/snapshot_no_energy`).

---

### B-03: Unreachable / never-polled device (TODO)

**Tests today:** `tests/ui/dashboard.spec.ts` covers the configured-but-unreachable case at the smoke level (the `playroom` device in the live deploy renders as `card unreachable`); no Go-level render-contract test pins the structural details. Goldens in `cmd/breezyd/ui/templates/testdata/` do not include this state.

### B-04: Device with active fault(s) (TODO)

**Tests today:** none. Goldens cover only `none` / sensor-alert / schedule-alert. Need a golden + Go contract test for fault-non-empty.

### B-05: Sensor threshold alert (TODO)

**Tests today:** `cmd/breezyd/ui/templates/testdata/golden_sensor_alert.html` pins the rendered shape; no Go test asserts trigger conditions for setting the alert flag.

### B-06: Schedule alert (failed fire) (TODO)

**Tests today:** `cmd/breezyd/ui/templates/testdata/golden_schedule_alert.html` pins the render; alert-trigger logic covered indirectly by `cmd/breezyd/scheduler_test.go`.

### B-07: Energy block — calibration missing (TODO)

**Tests today:** `cmd/breezyd/ui/templates/testdata/golden_energy_error.html` pins one error-state render. `cmd/breezyd/energy_tracker_test.go` covers the calibration-missing path at the model level.

### B-08: Energy block — regen mode (TODO)

**Tests today:** `cmd/breezyd/ui/templates/testdata/golden_regen.html`. No assertion on the *transition* from non-regen to regen (when accumulators start ticking).

### B-09: Energy block — non-regen mode (TODO)

**Tests today:** none directly; `golden_no_energy.html` is the closest relative.

### B-10: Filter near-end-of-life (TODO)

**Tests today:** none. The decode is in `pkg/breezy/params.go` (`FilterStatus`), the render uses `filterStatusStr` in `device_card.templ`. Need a contract test for both ends.

### B-11: Low RTC battery (TODO)

**Tests today:** none. RTC voltage is rendered in the info row but no styling/threshold is enforced today; this entry may move to "out of scope" if there's no behavior to assert.

---

## Card interactions

### B-13: Heater toggle

**Trigger:** User clicks the HEATER button on a powered, non-stale, authenticated device.

**API call:** `POST /ui/devices/{name}/heater` with body `{"on": <inverse of current>}` and `Content-Type: application/json`. Sent via datastar `@post` from `data-on:click`.

**Expected outcome (happy path):**
- Handler responds 200 with empty body.
- `PushHub.Notify` fires within one poll interval after the daemon's next poll observes the new state.
- All subscribed `/ui/sse` streams emit a `datastar-patch-elements` event with selector `.card[data-device="{name}"]` and the updated card HTML; the heater button's `aria-pressed` flips.

**Edge cases:**
- **Stale device:** button has `disabled` attribute (B-02), click does not fire.
- **Auth failure:** see B-34.
- **UDP timeout:** see B-34 (5s `handlerOpTimeout`).
- **Validation:** missing or non-bool `on` field → 200 + `#global-error-banner` updated with "missing 'on' field". (HTTP 200 by design — datastar drops non-2xx response bodies.)

**Source specs:** [2026-05-04-basic-ui-design.md](./2026-05-04-basic-ui-design.md), [2026-05-08-datastar-migration-design.md](./2026-05-08-datastar-migration-design.md)

**Tests:** `cmd/breezyd/handlers_ui_write_test.go` (`TestUIWriteHeater_*`).

---

### B-25: Schedule editor: action toggle clears pct

**Trigger:** User has the schedule editor open. They change an existing row's action `<select>` to `off` (or away from `off`).

**Expected outcome:**
- → `off`: the row's pct `<input>` is cleared (`value=""`), gets `readonly`, gains the `pct-disabled` class.
- away from `off` (when previously `off`): the input loses `readonly` and `pct-disabled`, value defaults to a sensible number.

**Status:** **broken** — the static render is correct (`schedulePctValue`) but the `<select>` has no `data-on:change` binding, so the toggle is invisible to the DOM until form submit + server re-render. Tracked as #44.

**Source specs:** [2026-05-06-schedule-system-design.md](./2026-05-06-schedule-system-design.md)

**Tests:** none yet — adding one is part of #44.

---

### B-12: Power toggle (TODO)

**Tests today:** `cmd/breezyd/handlers_ui_write_test.go` (`TestUIWritePower_Happy`, `TestUIWritePower_NotFound`, `TestUIWritePower_BadForm`, `TestUIWritePower_BackendError`, `TestUIWritePower_AuthError`); `tests/ui/dashboard.spec.ts` (`power toggle: button click switches state and pushes new card`). Strong coverage; entry exists for catalog completeness.

### B-14: Mode chip click (TODO)

**Tests today:** `cmd/breezyd/handlers_ui_write_test.go` (`TestUIWriteMode_*`); `tests/ui/dashboard.spec.ts` (`mode chip: click triggers mode change`).

### B-15: Preset chip click → editor open/close (TODO)

**Tests today:** `tests/ui/dashboard.spec.ts` (`preset chip: click opens editor, second click closes it`); no Go-level test of the per-card `$editor` signal logic.

### B-16: Manual button click (TODO)

**Tests today:** none directly — `TestUIWriteSpeed_HappyManual` covers the endpoint, but the button → `manual=N` shortcut (using `manualBtnPct(v)`) is not asserted from the UI.

### B-17: Manual slider drag (debounced) (TODO)

**Tests today:** none. The 200ms debounce + `evt.target.valueAsNumber` payload is implicit; needs an E2E test that drags + asserts only the final value POSTs.

### B-18: Preset slider with match-speeds (TODO)

**Tests today:** none. Logic lives in `cmd/breezyd/ui/vendor/dashboard.js::presetSliderChange`; not tested.

### B-19: Preset slider with automode (TODO)

**Tests today:** none. Same as B-18 — derives an *implied* mode from supply/extract delta and POSTs `/mode` alongside `/preset`.

### B-20: Timer button click (TODO)

**Tests today:** `cmd/breezyd/handlers_ui_write_test.go` (`TestUIWriteTimer_*`); no E2E.

### B-21 / B-22: Reset filter / faults buttons (TODO)

**Tests today:** `cmd/breezyd/handlers_ui_write_test.go` (`TestUIWriteResetFilter_*`, `TestUIWriteResetFaults_*`); no E2E or confirm-dialog test.

### B-23: Threshold editor: open → edit → save (TODO)

**Tests today:** `tests/ui/dashboard.spec.ts` (`threshold editor: click → edit → save → cell re-renders via SSE patch`); Go endpoint tests under `handlers_ui_write_test.go` for the threshold PUT handler.

### B-24: Schedule editor: open → add row → save (TODO)

**Tests today:** Go-level handler tests in `cmd/breezyd/handlers_ui_write_test.go` (schedule PUT + new-row); no E2E test of the full flow.

### B-25: Schedule editor: action toggle clears pct (TODO — #44)

**Status:** broken. See dedicated entry above.

**Tests today:** none. Adding one is part of #44.

### B-26: Schedule editor: delete row (TODO)

**Tests today:** none. The delete button uses `evt.target.closest('tr').remove()` — purely client-side until form submit.

### B-27: Theme picker: light / dark / auto (TODO)

**Tests today:** `tests/ui/dashboard.spec.ts` includes a theme-related test; persistence via `localStorage` is not directly asserted.

### B-28: `<details>` open-state persistence across polls (TODO)

**Tests today:** none. Logic depends on `data-attr:open="$detailsOpen.X"` per-card signals plus the seeded `data-signals` JSON; pin with both a Go contract test on the rendered `data-signals` payload and an E2E test that opens a details, waits a poll, asserts it stays open.

---

## Push channel

### B-29: Initial state on `/ui/sse` connect

**Trigger:** Browser opens an SSE connection to `/ui/sse` (via the body's `data-init="@get('/ui/sse')"`).

**Expected outcome:** For every configured device — including never-polled and unreachable ones — the daemon emits one `datastar-patch-elements` event with selector `#device-list`, mode `append`, and the rendered card HTML as elements. Order of devices reflects iteration of the device map.

The page shell starts with an empty `<div id="device-list" class="grid"></div>`; after this initial pass it contains one `.card[data-device="..."]` per device.

**Edge cases:**
- Reconnect (B-31, B-32): same pass runs, but cards already exist in DOM. Append duplicates them — see B-32 for current handling and known limitation.
- Zero configured devices: connection still established, no patch events sent until keepalive (B-33) fires.

**Source specs:** [2026-05-04-basic-ui-design.md](./2026-05-04-basic-ui-design.md), [2026-05-08-datastar-migration-design.md](./2026-05-08-datastar-migration-design.md)

**Tests:** `tests/ui/dashboard.spec.ts` (`datastar opens /ui/sse on page load`, `@smoke card renders for the configured device`).

**Implementation:** `cmd/breezyd/handlers_ui_sse.go::getUISSE`.

---

### B-34: `#global-error-banner` populates on action error

**Trigger:** A `data-on:click`-driven action handler fails.

**Expected outcome:**
- Handler returns HTTP **200** with `Content-Type: text/event-stream` and a `Datastar-Status` header carrying the semantic status (401 / 422 / 502).
- Body is one `datastar-patch-elements` event with selector `#global-error-banner`, mode `inner`, and an `<div class="err-banner" role="alert">{message}</div>` payload.
- Datastar processes the event in the browser; the banner div now contains the error.

**Why HTTP 200, not the semantic code:** datastar's `@post` action discards response bodies on non-2xx status. Returning the semantic code as `Datastar-Status` keeps observability without losing the SSE payload. See `cmd/breezyd/handlers_ui_write.go::errorBannerSSE`.

**Edge cases by error type:**
- `breezy.ErrAuth` → status header 401, message "device authentication failed".
- UDP timeout / unreachable → status header 502, message contains "i/o timeout" or similar.
- Validation (bad JSON, missing field, out-of-range value) → status header 422, message describes the field.

**Source specs:** [2026-05-04-basic-ui-design.md](./2026-05-04-basic-ui-design.md), [2026-05-08-datastar-migration-design.md](./2026-05-08-datastar-migration-design.md)

**Tests:** `tests/ui/dashboard.spec.ts` (`auth failure surfaces in the global error banner`, `UDP timeout surfaces in the global error banner`); `cmd/breezyd/handlers_ui_write_test.go` (`TestUIWritePower_AuthError`, `TestUIWritePower_BackendError`, `TestUIWritePower_BadForm`, etc.).

---

### B-30: Per-device update on poll (TODO)

**Tests today:** `tests/ui/dashboard.spec.ts` (`SSE push` describe block — `device change in fakedevice → card updates without reload`, cross-tab variant).

### B-31: Reconnect via page reload (TODO)

**Tests today:** `tests/ui/dashboard.spec.ts` (`reconnect: EventSource reconnects after a forced close` — uses `page.reload()`).

### B-32: Reconnect via datastar auto-retry (TODO)

**Tests today:** none. Datastar's stream auto-retries on connection drop; current behavior duplicates cards (B-29 edge case). No test simulates a true mid-stream disconnect.

### B-33: Keepalive while idle (TODO)

**Tests today:** none. `keepaliveInterval = 30 * time.Second` in `handlers_ui_sse.go` is exposed as a `var` so a test could shorten it; pinning the keepalive comment line emit would be a one-liner Go test.

---

## Configuration / persistence

### B-35: First-run config bootstrap (TODO)

**Tests today:** `internal/config/*_test.go` (`WriteDefault`, mode-bit checks). Not asserted end-to-end (daemon-exits-on-first-run path).

### B-36: Discovery on start / periodic (TODO)

**Tests today:** none directly. Discovery itself is unit-tested in `pkg/breezy/`; the daemon's wiring of `discovery=on-start` / `periodic:<dur>` / `off` is not.

### B-37: Schedule persists across daemon restart (TODO)

**Tests today:** `cmd/breezyd/scheduler_test.go` includes persistence / restore tests at the `Scheduler` level. A daemon-level "stop, restart, schedule survives" test does not exist.

### B-38: Energy state persists across daemon restart (TODO)

**Tests today:** `cmd/breezyd/energy_tracker_test.go` covers the JSON round-trip. Daemon-level restart not directly tested.

### B-39: Device added to config without restart (TODO)

**Tests today:** none. Behavior may not actually be supported (config is read once at startup); this entry might move to "out of scope" once verified.

---

## HomeKit

### B-40: Bridge advertises configured devices (TODO)

**Tests today:** `pkg/homekit/*_test.go` covers accessory construction. mDNS advertisement (which depends on `RestrictAddressFamilies` including `AF_NETLINK` — see `nix/module.nix`) is not under test.

### B-41: HomeKit write → UDP (TODO)

**Tests today:** `pkg/homekit/*_test.go` covers the accessory-side write path. End-to-end "HomeKit characteristic set → breezy.SetX called" is partially exercised via mock clients.

### B-42: HomeKit read reflects poller cache (TODO)

**Tests today:** `pkg/homekit/*_test.go` covers reads against a fixture cache. Daemon-level integration with `cmd/breezyd/homekit.go` is not directly tested.

---

## CLI

### B-43: `breezy ls` (standalone) (TODO)

**Tests today:** `cmd/breezy/*_test.go` covers the verb's parser/output formatting; standalone-mode happy path is partially covered.

### B-44: `breezy ls` (daemon) (TODO)

**Tests today:** `cmd/breezy/*_test.go` covers daemon-mode dispatch; a real daemon round-trip is not asserted in the CLI tests.

### B-45: `breezy <name> status` (TODO)

**Tests today:** none directly — the status output format (live vs configured, in_user_control warning) is not pinned.

### B-46: `breezy <name> on/off/speed/mode/...` (TODO)

**Tests today:** verb-level parser tests in `cmd/breezy/*_test.go`; protocol-side breezy.SetX is well-tested in `pkg/breezy/`. End-to-end CLI → daemon → device coverage is partial.

### B-47: `breezy discover` (TODO)

**Tests today:** discovery itself in `pkg/breezy/*_test.go`; CLI verb output not directly pinned.

### B-48: CLI exit codes (TODO)

**Tests today:** none. The `0 / 1 / 2` contract in CLAUDE.md is implicit in handler code; pin with a small table-driven test.

---

## Out-of-scope behaviors (deliberately undocumented as supported)

- **DST around schedule entries.** Times are local wall-clock; spring-forward skips an entry in the missing hour, fall-back fires it twice in the repeated hour. Acceptable for residential ERV control. See [2026-05-06-schedule-system-design.md](./2026-05-06-schedule-system-design.md).
- **Concurrent CLI invocations against the same device in standalone mode.** UDP collisions cause silent corruption; users with this need should run the daemon. See CLAUDE.md.
- **WiFi reconfig, MQTT bridge, Home Assistant component.** Not implemented; not planned. See CLAUDE.md "Out of scope".
- **EventSource auto-reconnect-without-page-reload duplicates.** Datastar's fetch+stream auto-retries on connection failure; on the new connection the daemon's initial-state pass appends cards a second time, duplicating them. Acceptable v1 limitation; reconnect via page reload (B-31) is the supported path.

---

## Maintenance

When you change behavior:

1. Update the matching catalog entry (or add one). Mark with the date if it helps reviewers.
2. Update or add the per-feature design doc.
3. Update or add the tests cited under `Tests:`.
4. If the behavior is genuinely removed: leave the entry with a strikethrough and a redirect to the replacement, do not delete the ID.
