# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.1.0] - 2026-05-12

Minor release: two UX features (inline-edit schedule, optimistic-UI cascades) plus dashboard polish + correctness fixes uncovered while exercising v2.0.2 against the three production devices. No `/v1` API or CLI surface change.

### Added

- **Inline-edit schedule** (#236). The schedule block's two-mode (read + edit) UX collapses into a single always-editable view. Row fields autosave on `change`; a small `+ add row` button appends rows below the table; the "edit schedule" / "save" / "cancel" buttons are gone. While the block is `[open]` the push pipeline's `[data-block="schedule"]:not([data-edit])` selector filters patches, so mid-typed inputs survive polls; patches resume on collapse. Backed by a new `ScheduleRow` exported templ component (single source of truth) and a `PUT` that returns empty SSE on success — the form's DOM state is already correct client-side, and the next poll repaints. Validation failures route through `errorBannerSSE` like every other action handler. Spec: `docs/superpowers/specs/2026-05-11-schedule-inline-edit-design.md`.
- **Optimistic-UI cascades + `clickAction` helper** (#240). Replaces six ad-hoc speculative-UI fixes with a single `clickAction` helper plus a cascade table (`cmd/breezyd/ui/templates/cascades.go`). Each click handler is now a one-liner naming its primary signal write; implied cross-signal updates come from the cascade table. Adds `effPower(power, special)` derivation in `layout.templ` for the one state externally-induced clients can leave incoherent (panel button starts a timer with `$power=false`). Adds a server-side mirror: `SetSpeedPreset` / `SetSpeedManual` ops also write `0x0007=0`, keeping the daemon's cache coherent with firmware behaviour and fixing the MemClient-backed Playwright failure mode. Spec: `docs/superpowers/specs/2026-05-11-optimistic-ui-cascades-design.md`.

### Fixed

- **Power-off didn't clear an externally-set timer** (#238, #241). The firmware runs the fans whenever `0x0007` (night/turbo) is set, regardless of `0x0001`. **Turbo can be activated via the IR remote without touching 0x0001 at all.** A power-off device with turbo running reported `power=off`, the dashboard showed the power button as off, and clicking it was a no-op. Three coordinated fixes: (1) view-derive `Power = (0x0001 == 1) OR (special_mode != "off")` so the dashboard tells the truth when the firmware is running fans regardless of how the timer was activated; (2) `postUITimer` activate writes `Power(true)` before `SetTimer` so the cache reflects the on-state immediately; (3) `pkg/breezy/ops.Power(false)` writes `SetTimer(off)` after the power write — encoded at the ops layer per #241 after a 2026-05-12 firmware probe on `192.168.1.148` confirmed firmware clears `0x0007` on a 1→0 power transition, so all callers (UI, `/v1`, HomeKit, scheduler) get a coherent cache, not just the UI handler.
- **Preset chip expanded the editor on every click** (#239). The chip's click handler unconditionally toggled the editor. Now matches radio-button intuition: clicking a non-active chip selects that preset and closes any open editor; clicking the already-active chip toggles the editor. Generated handler branches on `$specialMode === 'off' && $speedMode === 'preset<n>'`.
- **Manual slider drag occasionally posted the wrong value** (#241). The `manual slider drag posts dragged value` Playwright test flaked 40–60% under heavy load. Datastar's `data-bind` plugin fires the change handler synchronously during signal→input syncs — both initial and on every SSE-pushed signal update. The synthetic events have `isTrusted=false` and the input's current value, producing no-op `{manual:50}` POSTs that pre-empted dragged-value POSTs. Replaced `data-bind` with the explicit split that doesn't synthesise change events: `data-effect="el.value = $_manualPct.X"` (signal → input.value, direct property assignment, no DOM event), `data-on:input` (live drag mirror into signal), and `data-on:change` (POST on release, debounce removed so SSE pushes can't reset `el.value` between dispatch and handler-fire).
- **Number inputs accepted out-of-range values** (#234). HTML5 `min`/`max` only fire on form-submit and the manual / preset pct inputs aren't in a form. Typing `5` into the manual pct input left `5` in the field, the change handler posted `{manual: 5}`, the server 422'd, and the input stayed visibly out-of-range until the next poll. New `manualChangeExpr` helper clamps to `[10, 100]` (`NaN → 10` for empty input) before reflecting into the signal and posting; the preset supply/extract handler clamps to `[0, 100]` (`NaN → 0`) ahead of the existing `1..9 → 0` snap, and writes the clamped value back into `evt.target.value` so the input snaps visually too.
- **`100` clipped its rightmost digit in `.val-input`** (#235). Global `* { box-sizing: border-box }` made `width: 3ch` refer to the outer edge; padding + border ate into the content area, leaving less than 3 character widths. Switched to `box-sizing: content-box` on `.val-input` so `width: 3ch` refers to the content area.
- **"Enabled" checkbox glow guidance** (#237). The schedule's "enabled" checkbox is disabled while there are no rows (the v2.0.2 forced-off invariant) but still looks clickable. Clicking the label now flips a per-card `$_addRowAttn` signal that the `+ add row` button picks up via `data-class:attention`, glowing once to point the user at the action they need to take first. Mirrors the `attentionIfOff` power-button pattern and reuses the `power-attention` keyframes (1200ms / one out-and-back pulse).
- **MODE-to-slider vspace** (#237). Manual pane's slider was crowding the MODE chip row above it. Bumped `.fan-slider-row margin-top` from `0.5rem` to `1rem`.

### Changed

- `pkg/breezy/ops.Power(false)` now writes `SetTimer(off)` after the power write so the daemon's cache stays coherent with firmware behaviour. Callers no longer need to follow `Power(false)` with an explicit `SetTimer(off)`; the UI handler's redundant call was removed (#241).
- `SetSpeedPreset` / `SetSpeedManual` ops also write `0x0007=0` so the cache reflects the firmware's "timer clears on speed-mode change" behaviour (#240).

## [2.0.2] - 2026-05-11

Patch release: a second wave of dashboard bug fixes uncovered while exercising v2.0.1 against the three production devices. No `/v1` API or CLI surface change.

### Fixed

- **Schedule save silently cleared all entries** (#230). `scheduleSubmitExpr` produced a bare `@put('…/schedule')` — datastar's default `contentType: 'json'` sent the (empty) signal store as the body, and the handler's `r.ParseForm()` only parses URL-encoded bodies. Every row parsed as absent, `Replace(enabled, [])` cleared the schedule on save. Fixed by injecting `{contentType: 'form'}` via the new `withDatastarOpts` helper in `helpers.templ`. The same pattern is now applied to `thresholdSubmitExpr` and every hand-rolled `@verb('url', …)` string — `postActionExpr`, `powerButtonExpr`, `timerClickExpr`, `heaterClickExpr`, and the two inline `@post` calls in `presetSliderExpr` now wrap `datastar.PostSSE / PutSSE` so URL formatting / escaping stays inside the SDK helper.
- **Manual pane stayed visible when clicking a preset chip** (#230). The MODE chips + manual slider were rendered conditionally in templ on `v.SpeedMode == "manual"`. Clicking a preset chip opened the preset editor (`data-edit="true"`), which filtered the controls-block patch — the manual pane stayed in the DOM despite the speed-mode change. Wrapped the pane in a div with `data-show="$speedMode.<name> === 'manual' && $specialMode.<name> === 'off'"` so it reacts to live signals regardless of HTML-patch filtering.
- **Manual + preset editor pct readouts were not editable** (#230). Replaced the read-only `<span class="val">` next to each slider with `<input type="number">` two-way-bound to the same signal as the slider. New `.val-input` CSS: borderless, tabular-nums, dotted hover-underline (matches the threshold-cell pattern the user identified), thin focus border, spinner buttons suppressed.
- **Match-speeds mirror was only on slider release** (#230). The mirror lived inside `presetSliderExpr` (the debounced `change` handler), so the other side visually lagged a full release behind the dragged side. Added `data-on:input` on each slider/input that mirrors on every input event when `$matchSpeeds.<name>` is true; the existing change handler still runs snap-to-zero + the POST on release.
- **Power button crowded the IP value on info-expand** (#230). Added `.device-info[open] > summary { margin-bottom: 0.75rem }` so the absolutely-positioned power button at the top-right of the wrap has breathing room above the first row's right-aligned value.
- **Empty schedule needed an explicit "+ add row" click** (#230). `scheduleEditFrag` now seeds one default row when entering edit with no entries, so the user lands in an editable state rather than a "no entries" wall.
- **Turbo countdown was empty after click** (#231). For users whose device hadn't yet returned `0x0303` (turbo duration) — or whose firmware doesn't expose it — the optimistic seed of `$specialModeRemainingSeconds` from the missing `$turboDurationSeconds` was 0, and `fmtRemaining(0)` returned an empty string. The countdown line was visible (via `data-show`) but blank for ~12s until the next poll filled it in. Now falls back to a `"<mode> timer active"` placeholder while the seconds signal is 0 but a timer mode is set; the next poll replaces it with the real `"Xh Ym remaining"`. Replaced an earlier attempt to hardcode duration defaults in the click handler (brittle, see PR feedback).
- **Collapsed schedule summary gave no hint about when it would next fire** (#231). New `next event: HH:MM` indicator right-pinned in the summary, computed server-side as the smallest entry `At` strictly after the current local-wall-clock minute (wrapping to the earliest entry when all of today's have already fired). Hidden via `data-show` when the block is expanded (the table below is the source of truth there) and absent server-side unless both enabled and non-empty.
- **"Enabled with no entries" was a reachable UI state** (#231). `Scheduler.SetEnabled(true)` and `Scheduler.Replace(true, [])` now coerce `enabled→false` when entries are empty. Mirrored on the frontend: the read-variant "enabled" checkbox renders `disabled` when `len(entries) == 0`; the edit-variant per-row delete button unticks + disables the checkbox if it removes the last row; the edit-variant "+ add row" button re-enables it before fetching a fresh row.
- **Night/turbo chips stayed lit + countdown stayed visible after power-off** (#232). Verified live against firmware 0.11 on `192.168.1.148`: writing `0x0001=0` while a timer is running resets `0x0007` (timer mode) and `0x000B` (countdown) to 0 device-side. The dashboard's power button now mirrors that optimistically: on→off clicks also set `$specialMode='off'` and `$specialModeRemainingSeconds=0` so the chips de-highlight and the countdown vanishes instantly rather than after the next poll.
- **Preset chip stayed pressed while a timer was active** (#229). The chip's `aria-pressed` binding only considered `$speedMode` — the device's `speed_mode` value is unchanged when a timer kicks in, so the chip stayed lit even though night/turbo was the actual driver. Now gated on `$specialMode === 'off'` so the chip de-highlights for the duration of the timer and re-highlights once it expires.
- **Timer countdown line was slow to appear after click** (#229). The "X remaining" text was rendered server-side inside an `if v.SpecialMode != "off"` branch that only landed on the next controls-block HTML patch — filtered out by `[:not([data-edit])]` while the preset editor was open. Converted to a signal-driven div: `data-show` on `$specialMode`, `data-text` via the new page-level `fmtRemaining` helper reading `$specialModeRemainingSeconds`, and a 1-second `data-on-interval` decrement.
- **Preset chip label was stale while the slider was being dragged** (#228). The chip text used the server-rendered value frozen at the last controls-block patch; while the editor was open the patch was filtered. Switched to `data-text` reading the per-side `$preset.<name>[n]` signal so the chip's "x/y" readout updates live.
- **Attention glow on the power button pulsed twice instead of once** (#227, #226). The CSS animation was `power-attention 0.6s ease-in-out 2 alternate` (1.2s total), but the JS `setTimeout` that flips `$_attention` back was 1500ms — the class hung around for an extra 300ms and the animation restarted before being removed. Aligned the timeout to 1200ms.
- **Dark-mode contrast regressions on inactive segmented buttons** (#224). User-agent default `ButtonText` color rendered near-black on the dark theme in Chromium-on-Linux without a `color-scheme` hint. Anchored the inactive state to `var(--fg)` so contrast holds on both themes; the active `aria-pressed` branch keeps `var(--accent-text)`.
- **Power button green, heater button red, attention glow on off state** (#225). Power chip de-highlighted while in the off state was harder to spot than the muted-grey treatment in v1.x; restored the green active / red heater treatment. When a non-power chip is clicked while the device is off, the power button briefly pulses to draw the eye there.
- **Editor / preset / automode / matchSpeeds signals were page-global** (#223). Same shape as the v2.0.1 cross-card-bleed fix (#29) but for the four signals scoped to the preset editor. Toggling automode on one card flipped it on every card; opening the preset-2 editor on card A also opened it on B and C. All four signals now nested under `<deviceName>` via the same per-card pattern.

### Changed

- Added `withDatastarOpts(expr, opts)` helper in `helpers.templ`; all hand-rolled `@verb('url', …)` strings in templates now wrap `datastar.PostSSE / GetSSE / PutSSE` rather than building the action expression from scratch. SDK helper owns URL formatting / escaping; the wrapper supports per-action options the SDK doesn't model. Keeps the dashboard 100% SDK-idiomatic per the v2.0.0 datastar-go round-2 simplification target.

## [2.0.1] - 2026-05-11

Patch release: dashboard bug fixes + dep refresh since v2.0.0. No new features; no `/v1` or CLI surface change.

### Fixed

- **Cross-card signal bleed** (#29, #25). On a multi-device deployment, `$speedMode`, `$airflowMode`, `$stale`, `$lastPollAge`, `$sensorsAlert`, and `$detailsOpen` were page-global signals — every card's SSE signal patch overwrote the previous, so cards visually reflected whichever device pushed most recently. Symptoms ranged from "open Sensors on card A also opens it on B and C" to invariant violations like "both `manual` and a `presetN` button highlighted on the same card simultaneously." All per-card signals now scoped per device via the nested-map pattern `$<signal>.<deviceName>`. Datastar's deep-merge keeps sibling devices' values coexisting.
- **Energy tracker accumulated while device was powered off** (#31). The accumulator's regen-mode gate (`airflow_mode == 1`) passed even when `power == 0`, so a unit left in regeneration mode but powered off ticked instantaneous W and kWh into the day/lifetime counters even though fans weren't moving air. Added a power-state gate (`0x0001 == 1`) immediately after the regen gate. Found during a live-deployment audit of v2.0.0; two of three test units were silently double-counting.
- **Info-block patch nested orphan power buttons in the DOM** (#32). PR #215 (a11y fix) wrapped `<details>` + power-button in a `<div class="device-info-wrap">` to keep the button visible when the panel collapses, but kept `data-block="info"` on the inner `<details>`. The push pipeline's outer-mode patch selector targeted `[data-block="info"]`, so each poll replaced `<details>` with a wrap-containing-details-plus-button — nesting a new wrap inside the original wrap and leaving the previous power button behind as an orphan. Symptom: two power buttons in the DOM ~5% of the time; visible to keyboard users as a duplicate tabstop and caught by Playwright's `getByRole` strict-mode check. Moved `data-block="info"` to the wrap div so the patch boundary matches the template root.
- **`<details>` summary contained an interactive `<button>`** (a11y, #24). The power button in `InfoDetails` sat inside `<summary>` with `data-on:click__stop` to avoid the summary's toggle firing. Keyboard/AT users got inconsistent behaviour and the a11y audit flagged three elements (once per device card). Moved the button to a sibling AFTER `<summary>` but inside `<details>`, positioned absolutely top-right via CSS.
- **Manual slider pct display stale until release** (#26). The `<span class="val">` rendered `v.ManualPct` server-side and didn't update until the next SSE patch (~one poll cycle). Now bound to a per-card local signal `$_manualPct.<name>` via `data-bind`; the val span reads it live via `data-text`. Underscore prefix keeps the signal client-only.
- **Schedule `enabled` checkbox required entering edit mode** (#27). The read variant rendered the checkbox `disabled`; toggling required clicking "edit schedule" → toggle → save. New `POST /ui/devices/{name}/schedule/enabled` endpoint plus `Scheduler.SetEnabled` flip ONLY the enabled bit (no entry mutation, no `firedAt` clear). Read-variant checkbox now POSTs via `data-on:change`.
- Identifier-safety validation on device names in `internal/config.Load`. Names must match `^[A-Za-z_][A-Za-z0-9_]*$` since they appear as datastar signal-path segments after the per-card-signal refactor.

### Changed

- Go modules: `a-h/templ` 0.3.1001 → 0.3.1020 (direct); `prometheus/common`, `procfs`, `miekg/dns`, `klauspost/compress`, `golang.org/x/*` indirect minor/patch bumps.
- JS devDeps (test-only; nothing ships to users): `@playwright/test` ^1.49 → ^1.59; `typescript` ^5.6 → ^6.0 (major; test suite compiles + runs unchanged); `tsx` ^4.19 → ^4.21.
- Nixpkgs flake input bumped to nixos-unstable @ 2026-05-05.
- `flake.nix::vendorHash` recomputed for the new `go.sum`.
- Regenerated `*_templ.go` files with `templ v0.3.1020` (silences the version-check warning during `templ generate`; output semantically identical).
- `examples/` directory added at repo root with `breezyd.service` and `breezyd.toml`. The README's Linux+systemd section now points at them instead of inlining ~60 lines.

## [2.0.0] - 2026-05-11

Major milestone covering six days of dashboard, scheduling, energy, and substrate work. `/v1/*` JSON API and CLI surface are unchanged from `1.8.x`; the bump is driven by the dashboard substrate replacement (htmx → datastar + SSE + templ), the new always-on subsystems (schedule, energy tracking, daily RTC sync), and the DST handling fix.

### Added

- Datastar replaces htmx and the cookie-based UI-state machinery. Single library for client reactivity and server interaction; per-card UI state lives in `data-signals`; visibility flips via `data-attr-open` and `data-show`. Roughly 600 lines of dashboard code go away (the `internal/uistate` package, the `breezy-ui` cookie protocol, four `DeviceView` UI fields, `computeDetailsOpen`, and ~210 lines of inline JS).
- Server-Sent Events push channel: `GET /ui/sse` opens a long-lived stream per browser. The daemon's poller fans out updated cards to every subscribed connection on each successful UDP poll, replacing the dashboard's 5-second client-side polling. Reconnects re-emit current state. A 30-second SSE comment-line keepalive keeps idle connections open through intermediaries; the daemon clears its 30s `WriteTimeout` for the SSE handler so streams aren't terminated early.
- Alert-as-CSS: when a sensor or schedule alert fires, the relevant `<details>` block heading turns red with a `⚠` prefix. The user's open/closed choice is preserved across alert state changes (no more force-open).
- Server-rendered dashboard via `templ`. Two HTTP namespaces: `/v1/...` (JSON, unchanged — CLI uses this) and `/ui/...` (now SSE event streams, the dashboard uses this).
- Dark mode: auto via `prefers-color-scheme`, manual override via theme picker on the title. Choice persists to localStorage.
- CSS extracted from `index.html` to a content-hashed `/ui/style-<hash>.css` for proper caching. CSS custom properties used for theming throughout.
- Build-tagged `breezyd_test_admin` admin control surface on the daemon: an HTTP plane under `/test/...` that lets Playwright mutate the in-process `MemClient` without real hardware.
- In-process `pkg/breezy.MemClient` backend selectable via `breezyd --backend=memory --seed <path>`. Lets the dashboard run against a canned `pkg/breezy/fakedevice/snapshot_*.json` for UI development with no UDP, no fakedevice process, no real device.
- Restored inline SPEED preset editor (#53). Clicking a preset chip toggles a panel with supply + exhaust sliders, an `automode` checkbox, and a `match speeds` checkbox; slider drags POST to `/ui/devices/{name}/preset`, with snap-1..9-to-0 + match-speeds-mirror + implied-mode logic running through datastar signals.
- NixOS module's optional nginx integration sets the SSE-friendly directives (`proxy_buffering off`, `proxy_cache off`, `proxy_http_version 1.1`, `proxy_set_header Connection ""`, `proxy_read_timeout 1d`) so SSE streams flush immediately and survive past nginx's default 60s read timeout.
- DST-aware scheduler (#200). An entry whose At-time falls in the spring-forward missing hour fires once at the first tick after the skipped hour; fall-back entries fire exactly once (the first occurrence). Per-entry `firedAt` tracking persists across daemon restart. Replaces the documented "v1 limitation" with deterministic behaviour.
- Daily RTC sync per device (#212). `RTCSyncer` goroutine writes the device's RTC (params `0x6F` + `0x70`) once ~30s after daemon startup and then daily at 04:00 local time. Closes the panel-display drift introduced by DST transitions, CR2032 battery replacement, and long-term oscillator drift. Always-on, no configuration.
- SSE debug rows in the theme-picker popout (#191). Three live rows ("last update", "events", "stream") wired entirely client-side off the existing SSE stream — diagnoses panel-grey symptoms (stream alive vs. one device's poll cycle).
- `deps.md` at the repo root: every direct dep across Go / JS / Nix with one-line justification and critical-assessment notes. Adopted as the canonical reference for "why is this dep here?"

### Fixed

- Inline editors (schedule, threshold, preset slider) no longer get clobbered by poll-driven SSE pushes (#65). Each open editor is preserved across polls until the user saves or cancels. The fix also closes a latent bug where the threshold edit form's submit silently failed (datastar's default JSON `contentType` vs. the handler's `r.ParseForm()` form-encoded expectation), which was previously masked by the whole-card poll refresh overwriting the form immediately.
- Preset chip's `aria-pressed` state now updates while the preset editor is open (#65). The pressed state was previously frozen during edit mode because the controls-block patch was suppressed (correct, to preserve the editor); the fix makes `aria-pressed` signal-driven via the existing `$speedMode` signal so it updates via `datastar-patch-signals` even when the HTML patch misses.
- `automode` checkbox in the preset editor now defaults **unchecked** (was checked in the legacy SPA). Toggling automode from checked → unchecked while the active preset's fans are both ≥ 10 % now fires a `regeneration` mode write immediately (#46).
- Schedule editor validation errors now reach the client (#70). Closes the latent `contentType` mismatch where the threshold/schedule form's submit silently failed against the JSON-default handler.
- Schedule editor pct restores the user's last-edited value when toggling action away from "off" and back (#66).
- Schedule editor's "enabled" checkbox now reflects the current state (#78).
- PushHub subscribes BEFORE the SSE initial-state pass (#75). Closes a race where a state change occurring during the per-device initial-state writes could be lost.
- Brand title and theme-picker header now read "breezyd" instead of "breezy" (#190).
- HAP weak / malformed PIN regeneration on startup (#132) — security hardening.
- Snapshot.LastPoll preserved across failed poll ticks (#178). Dashboard now renders "stale with last-known data" instead of dropping to the unreachable placeholder when the in-process backend returns ErrTimeout instantly.
- Energy tracker re-primes LastTick on local date rollover (#164) — fixes a calculation gap at midnight.
- Manual slider posts the dragged value, not a stale signal seed (#116). Drag responsiveness restored.
- `<details>` summary clicks correctly toggle open state across the datastar reactive cycle (#118). The `data-attr:open` enforcement no longer reverts the user's click.
- UDP timeout errors translated to a human-readable banner string in the dashboard (#61).
- CLI exit codes for help and missing-device usage paths aligned with spec (#133/#134/#135).
- `data-show` elements include `style="display:none"` so they don't flash in before datastar binds (#71).

### Changed

- The dashboard's SSE push pipeline now emits one `datastar-patch-signals` event plus per-block `datastar-patch-elements` events instead of one full-card outer patch (#65). Card-outer reactive state (`stale` class, speed/airflow `data-*` attributes, "X ago" stale row, sensors-block alert class) flows through datastar signals; the card outer is never HTML-patched after initial render. SSE reconnects use the `Last-Event-ID` header to detect cold load vs. reconnect and avoid duplicating cards.
- Each block carries `data-block="<key>"`; sensor cells carry `data-sensor-cell="<key>"`. Edit variants (schedule, threshold) carry `data-edit="true"` statically; the controls block carries it reactively via `data-attr:data-edit` so the preset editor's slider value is preserved during a poll. Poll-driven patch selectors target `:not([data-edit])` and silently drop when an editor is open. The browser console will show one `PatchElementsNoTargetsFound` warning per poll per open editor — this is by design (the mechanism by which patches are dropped to preserve open editors), not a flood.

- The dashboard substrate is now datastar + SSE. `/ui/devices/{name}/...` action endpoints return 200 + empty body on success (subscribers see the new card via the SSE push channel) or a status-coded `datastar-patch-elements` event into `#global-error-banner` on failure. Threshold and schedule fragment endpoints emit SSE patch events targeting their cells. The `/v1/*` JSON API is unchanged.
- The `GET /ui/devices` and `GET /ui/devices/{name}/card` routes are removed — the SSE push channel replaces them.
- `just build` now depends on `just generate` (templ codegen). The `templ` CLI is a required build prerequisite: provided by `nix develop`, or `go install github.com/a-h/templ/cmd/templ@v0.3.x` outside Nix.
- `just check` and `just ci` include `just test-templ-drift` (verifies generated `*_templ.go` files match sources) and `just test-fakedevice-admin` (builds with the admin build tag).
- Playwright suite reduced from 1600 to ~200 lines, covering the SSE-driven user-visible behaviors (initial render, action clicks, cross-tab synchronization, threshold edit, error banner, reconnect-via-reload).

### Removed

- `internal/uistate/` package and the `breezy-ui` cookie protocol.
- `cmd/breezyd/ui/vendor/htmx-2.0.4.min.js` and `htmx-response-targets-2.0.4.min.js`.
- `DeviceView.{DetailsOpen, EditingPreset, Automode, MatchSpeeds, PostError}` fields.
- `computeDetailsOpen` and `defaultOpen` helpers.
- `cmd/breezyd/ui/templates/device_list.templ` (the `/ui/devices` route is gone).
- The editor-open render-golden variant (`golden_editor_open_preset2.html`) — under `data-show`, the card HTML is editor-state-independent.
- `cmd/breezyd/ui/legacy.js` (the JS-rendered SPA's event handlers; replaced by the `dashboard.js` helper plus a small inline FOUC-prevention + theme-picker block in the page shell).
- Live-drag slider feedback (`.val` text update during drag). Slider value is updated after the SSE patch lands.
- `cmd/fakedevice` (the standalone fakedevice admin binary). Mid-test state mutation now happens via `breezyd`'s build-tagged `/test/...` surface against the in-process `MemClient`.
- The htmx vendor bundle (`htmx-2.0.4.min.js`, `htmx-response-targets-2.0.4.min.js`).
- `internal/uistate/` package and the `breezy-ui` cookie protocol.
- `pkg/breezy.BuildStatusWithEnergy` (single-caller YAGNI wrapper; the energy attachment is now two inline lines at the call site).
- `State.RecordPoll` (cosmetic alias for `State.Set`).
- Direct `prometheus/client_model` dep (replaced by `prometheus/testutil.CollectAndCompare` in the one test that used it).
- The `flake-utils` flake input (helper inlined via `nixpkgs.lib.genAttrs`).

- Daemon-driven per-device 24-hour cyclic schedule. Each device's card has a new collapsible SCHEDULE block with an `At | Action | Pct` table editor; entries fire writes (Power → SetMode → SetSpeedManual, or Power(false) for "off") at each At-time. State persists to `<state_dir>/schedule_<device>.json`. On transient write failure the daemon retries every 30 s for up to 10 min (abandoned earlier when superseded by the next entry); `breezy.ErrAuth` is treated as a config error and not retried. The dashboard auto-expands the SCHEDULE block with a `⚠` line when the most recent fire failed.
- New `GET`/`PUT /v1/devices/{name}/schedule` endpoints. PUT replaces the schedule wholesale (≤24 entries, action ∈ off/regeneration/ventilation/supply/extract, pct 10–100, no duplicate At-times); validation failures return 400 `bad_request`. Edits clear any in-flight retry and the previous fire's alert banner.
- New `service.schedule` block on `GET /v1/devices/{name}` with `enabled`, `entries`, the derived `alert` flag, and (when present) `last_apply` for UI rendering.
- Daemon-side energy tracking: per-device heating-recovered, cooling-recovered, and fan-electric-consumed kWh counters, accumulated only during regeneration airflow mode. State persists to `<state_dir>/energy_<device>.json` across daemon restarts. Today counters roll over at local midnight; lifetime counters never reset.
- New `service.energy` block on the JSON snapshot with `instant_w` (signed: positive heating, negative cooling), `consumed_w`, `heating_today_kwh` / `cooling_today_kwh` / `consumed_today_kwh`, and corresponding `_lifetime_kwh` fields. Devices whose UnitType isn't in the per-model calibration table surface a human-readable error in `service.energy.error`.
- Eight new Prometheus gauges exposed at `/metrics`: `breezyd_energy_recovered_watts`, `breezyd_energy_consumed_watts`, plus `_today_kwh` and `_lifetime_kwh` variants for heating/cooling/consumed.
- New ENERGY block on the dashboard (collapsed by default, `<details>` element). Shows the live wattage line plus a 3-column 2-row grid: heating / cooling / consumed × today / lifetime. Hidden when `service.energy` is missing; replaced by the error string when the device's model has no calibration.
- New NOTICE block at the bottom of each card, absorbing the existing sensor-override and timer-active warnings (previously rendered inside the Speed control). Hidden entirely when no warning applies.

### Changed

- The NixOS module's `StateDirectory = "breezyd"` is now unconditional (was previously gated on `cfg.homekit.enable`). Energy state and HomeKit pairing files share the same directory under `/var/lib/breezyd/`.
- `pkg/breezy.commandedFanPct` (unexported) renamed to `pkg/breezy.CommandedFanPct` (exported) so daemon-side code can reuse the speed-mode-aware fan-pct resolution.
- `/v1` write handlers share a generic `postV1WriteJSON` helper mirroring the existing `postUIWriteJSON` on the `/ui` side; behaviour and response envelope unchanged.
- CLI ack-pattern commands share a `runOp(op, successMsg, stdout, stderr)` helper. Exit codes (0/1/2) and stdout/stderr strings unchanged.
- Dashboard daemon picks a free port dynamically in Playwright fixtures (`launchBackend`); previously hardcoded ports occasionally collided.
- Schedule retry on transient write failure abandoned at 10 minutes (was implicit; now explicit constant `retryDeadline`) and gated correctly when superseded by the next entry's At-time.
- `cmd/breezyd/handler_test_helpers_test.go`: shared `newTestHandler` / `newTestState` / `setRunFlags` test fixtures introduced; 9 high-duplication test files migrated.

## [1.8.1] - 2026-05-05

### Changed

- Speed control's fan info now sits inside the slider rows instead of as separate kv lines above. Each fan gets a `<rpm>  [slider-bar]  <pct%>` row — supply on top with the interactive manual slider, extract below as a disabled visual mirror (the device has a single shared manual %, so an interactive extract slider would mislead). The right-side pct shows live fan pct in all modes; in preset mode the slider thumb still resets to 10 as the manual re-entry signal.

## [1.8.0] - 2026-05-05

### Added

- Per-preset speed editing across the stack:
  - `pkg/breezy.SetPresetSpeed(ctx, c, preset, supply, extract)` writes `0x003A/0x003B`, `0x003C/0x003D`, `0x003E/0x003F` (preset 1/2/3 supply/extract) with 10-100% bounds.
  - `POST /v1/devices/{name}/preset` daemon endpoint with body `{"preset":1-3,"supply":10-100,"extract":10-100}`.
  - `BuildStatus` exposes `configured.preset{1,2,3}` so consumers can read the stored percentages without an extra round trip.
  - Web dashboard: clicking a preset button opens an inline editor for that preset (a "match speeds" checkbox plus split supply/extract sliders). Clicking the same preset again closes it; clicking a different preset switches to it and opens its editor. "Match speeds" defaults on so a single drag moves both sliders.
  - The six preset-speed param IDs are now in the poller's `fanWriteIDs` so editing the active preset triggers the existing 12 s settle window that hides `0x4A/0x4B/0x84` (fan RPMs and air-quality status) during the ramp.

### Changed

- Dashboard Sensors block uses a shared 3-column grid: top row is RH | eCO₂ | VOC (clicking a value still opens the threshold editor below the grid); below are two 3-cell rows for the four temperature sensors plus a Δ cell per row. Supply path's Δ is positive (heat gained crossing the recovery exchanger); exhaust path's Δ is negative (heat lost). Recovery efficiency stays as a single row below the grids. JSON status field names unchanged — labels and layout only.
- Speed control absorbs the standalone "Fans" block and is promoted to the top of the Controls block (above Mode, Timer, Heater). Live supply/extract pct+rpm now sit at the top of Speed, immediately above the preset row and slider, so the configured-vs-live comparison is one glance.
- Power button moved to the right of the device-name row (~12ch wide) instead of stretching full-width below it. Heater button joins the Timer segmented control on a single row.
- Card header simplified: the global "refreshed Ns ago" indicator is removed; per-card timestamps render only when a card is stale (>90 s without a poll). The device-name `<h2>` is now the disclosure trigger for Device Info — clicking it expands/collapses the same `<details>` panel that previously had a dedicated summary.
- Hover styling: active (`aria-pressed="true"`) buttons keep their accent colour on hover instead of greying out, with a subtle drop shadow as the hover affordance.
- Selecting a preset resets the manual slider to 10% and hides its `%` readout (the slider has nothing meaningful to show in preset mode; it acts purely as a re-entry path back to manual).
- Micro-cleanups: fan-line slash dropped (`30% 5340rpm` instead of `30% / 5340rpm`); voltage and VOC index unit suffixes drop their preceding space; ip and serial rows swapped; VOC tooltip clarifies "index" units.

### Fixed

- Preset edits to the currently-active preset no longer cause cache flicker. The poller's settle window now suppresses RPM/air-quality reads during the ~12 s ramp following a write to any of `0x003A`–`0x003F`, matching the existing behaviour for `0x0044` (manual percent).

## [1.7.2] - 2026-05-05

### Changed

- Dashboard "Device Info" `<details>` summary is renamed to "Device" (just the noun).
- Mode and Timer rows in the Controls block are reordered so Mode appears before Timer.
- The Device `<details>` open-state now persists across the 5 s grid re-render. Previously every poll wiped the user's manual expand. A capture-phase `toggle` listener tracks per-card state in a small `deviceInfoOpen` dict; the auto-expand-on-fault rule still wins when an alarm appears mid-session.

## [1.7.1] - 2026-05-05

### Changed

- Dashboard card header restructured: serial / ip / firmware version / firmware date now live inside a top-of-card "Device Info" `<details>` element, collapsed by default. The standalone Service block is gone — its rows (filter, motor, RTC, faults) merged into Device Info. Auto-expands when `fault_level != "none"` or `filter_status != "clean"`. The bottom `fw 0.11 · …` line is removed.
- Sensor threshold rows show only the live value by default. Clicking the value opens an inline editor labelled "set alert ≥ N" with the threshold input, save, and cancel buttons, so the gesture's intent is unambiguous and the row stays uncluttered when not editing.
- Stale cards (no poll in >90 s) now also receive `filter: grayscale(1)` in addition to the existing 50% opacity, so the green power dot, red alert values, red heater toggle, etc. all desaturate to grey — colour signals can't mislead while the data is potentially out of date.
- Power and Heater toggle labels are lowercase (`power`, `heater`) to match the dashboard's prevailing lowercase aesthetic.
- Manual-speed slider has a left-side spacer that mirrors the right `%` readout's width, so the slider track sits visually centred in the row instead of pushed left.
- Unit-suffixed numbers drop their preceding space: `20.8°C`, `3500ppm`, `5340rpm`. `%` was already space-less.

## [1.7.0] - 2026-05-05

### Added

- New night/turbo special-mode timer support across the stack:
  - `pkg/breezy.SetTimer(ctx, c, mode)` writes `0x0007` (off/night/turbo).
  - `POST /v1/devices/{name}/timer` daemon endpoint with body `{"mode":"off|night|turbo"}`.
  - `breezy <name> timer <off|night|turbo>` CLI verb.
  - Web dashboard now shows a Timer row in the Controls block with a 3-way segmented control and a `Xh Ym remaining` countdown when active.
- Sensor alert thresholds are editable from the dashboard:
  - `pkg/breezy.SetThreshold(ctx, c, kind, value)` writes humidity (`0x0019`), co2 (`0x001A`), or voc (`0x031F`) with per-kind range and step validation (humidity 40-80, co2 400-2000 step 10, voc 50-250).
  - `POST /v1/devices/{name}/threshold` daemon endpoint with body `{"kind":"humidity|co2|voc","value":N}`.
  - Each editable sensor row now reads `value · alert threshold`; clicking the value opens an inline editor with the right min/max/step constraints.
- The dashboard's Fans block shows the commanded fan percentage alongside RPM (`X% / Y rpm`); preset percentages (`0x003A`-`0x003F`) are now polled so the value is correct in preset modes too.
- The card header now shows the 16-byte FDFD device ID between the device name and IP.
- HomeKit bridge gains five new services per Breezy and one optional characteristic on the existing AirPurifier:
  - `FilterMaintenance` — iOS shows a native filter-replacement indicator and Apple Home's "reset filter" gesture writes through `breezy.ResetFilter`. `FilterLifeLevel` is computed from the configured filter-replacement interval (`0x0063`, also added to the daemon's JSON status as `service.filter_total_seconds`).
  - `BatteryService` for the RTC coin-cell. Voltage maps linearly to 0-100% across 2.5-3.0 V, with `StatusLowBattery=1` at ≤2.7 V (~40%).
  - `Heater`, `Night`, `Turbo` — three named `Switch` services wired to `breezy.SetHeater` and `breezy.SetTimer`. Night and Turbo are mutually exclusive (turning on either cancels the other; turning off either cancels the timer entirely).
  - `StatusFault` characteristic on the AirPurifier service — `1` when `service.fault_level != "none"` so iOS shows the fault badge.

### Changed

- The dashboard's current sensor value goes red when the firmware's over-threshold flag is set, giving a glanceable alert state alongside the existing `⚠` warn line under Fans.
- The Power and Heater toggles now share a 2-wide row at the top of the Controls block instead of stacking vertically.
- The Speed control no longer has an explicit "manual" button — interacting with the slider is the gesture; preset 1/2/3 deselect when the slider moves.
- HomeKit's RotationSpeed slider now reflects `live.fan_supply_pct` (the firmware's currently-commanded percentage) instead of `configured.manual_pct`, so the slider position is correct in preset modes too. Drag-to-change still writes the manual percentage as before.
- Webui Timer seg drops the redundant `off` button. Tapping the active mode (Night or Turbo) toggles it off; tapping the other one swaps modes. Mirrors the toggle semantics already used by the HomeKit Night/Turbo switches.
- Webui Power and Heater toggles drop their "● on" / "○ off" trailing text since the button background already conveys state. Heater additionally uses a red active colour (`#b22`) instead of the default green so the two toggles are visually distinct when both are on. `aria-pressed` still carries state for screen readers.
- Webui Service block is collapsed by default (`<details>`/`<summary>`). It auto-expands when `service.fault_level != "none"` OR `service.filter_status != "clean"`, so attention-needing states surface without a click.

### Fixed

- The override-warn line under Fans previously attributed a timer-driven override to "sensors" when no sensor alerts were set. It now reads `⚠ timer active (turbo) — fan above setting` or `⚠ timer active (night) — fan slowed` when the cause is the timer; sensor copy is preserved when sensor alerts are set.
- `0x0007` is now in `fanWriteIDs`, so the existing 12 s fan-settle window applies after a timer write — fan RPM and air-quality reads are correctly suppressed during the ramp.
- Per-device UDP traffic is now serialised between the poller goroutine and HTTP handler writes via a shared `Poller.udpMu`. Previously each path opened its own `breezy.Client` (with independent sockets and mutexes), so a poll's read could race a concurrent write at the UDP layer and overwrite a just-written cache value with the device's pre-write reading. Symptom: clicking Power (or any other write) in the webui or HomeKit could show the toggle briefly snap back to the old state until the next poll caught up. CLAUDE.md described the intended serialisation but the implementation hadn't matched it across both code paths.
- HomeKit accessory order is now deterministic (sorted by device name) so iOS Home's cached aid → tile mapping survives daemon restarts. Previously Go's randomised map iteration could swap accessories across restarts; users would see the "Office" tile drive a different unit until they re-paired.

## [1.6.9] - 2026-05-05

### Changed

- HomeKit accessory and AirPurifier service names now display Title Case (`Playroom`, `Bedroom`, `Office`) instead of the lowercase config-key form (`playroom`, `bedroom`, `office`). Underscores and hyphens become spaces (`guest_room` → `Guest Room`); already-capitalised keys round-trip unchanged. Internal `Accessory.Info.Name` keeps the original config-key form so metric labels, log lines, and the daemon's device map are unchanged — only the iOS-facing label is title-cased.

## [1.6.8] - 2026-05-05

### Fixed

- HomeKit sub-services (Supply Only / Extract Only switches and the four temperature sensors) now display their proper labels in iOS Home instead of the generic `Switch` / `Switch 2` / `Sensor` / `Sensor 2` fallbacks. We were setting the `Name` characteristic, which the HAP spec mandates for multi-service-of-same-type accessories — but iOS Home actually reads `ConfiguredName` (the user-editable label) for the per-service rows. Now we set both: the right label appears by default AND the user can rename in Home settings.

## [1.6.7] - 2026-05-05

### Changed

- HomeKit PIN log line now formats as `XXXX-XXXX` (4-4) instead of `XXX-XX-XXX` (3-2-3). Easier to read off the log; iOS accepts the same 8 digits in either form during pairing. The actual PIN handed to brutella/hap is still the raw 8-digit value — only the log display changed.

## [1.6.6] - 2026-05-05

### Fixed

- **HomeKit bridge was silently advertising on zero interfaces under the NixOS module's systemd hardening.** Diagnosed via tcpdump on UDP/5353 against a real Apple Home-pairing failure: the daemon's HAP server was running fine, the firewall was open (TCP `homekit.port` + UDP/5353 from v1.6.5), but the bridge never appeared in "Add Accessory". Root cause: `RestrictAddressFamilies = [ "AF_INET" "AF_INET6" "AF_UNIX" ]` excluded `AF_NETLINK`, which Go's `net.Interfaces()` calls on Linux to enumerate interfaces. Without netlink the call silently returns an empty list, `dnssd.MulticastInterfaces()` then has nothing to advertise on, and the bridge runs in mDNS-deaf mode — no log line, no error, just dead silence on UDP/5353. Added `AF_NETLINK` to the allowlist; verified that `breezyd._hap._tcp.local` advertisements appear on the wire under the hardened sandbox after the fix. Apple Home discovery works again.

## [1.6.5] - 2026-05-05

### Fixed

- NixOS module now opens **UDP/5353 for mDNS** (in addition to the HAP TCP port) when `services.breezyd.openFirewall = true` and `services.breezyd.homekit.enable = true`. Without this, iPhones couldn't discover the bridge in Apple Home even though pairing would have otherwise worked. The HAP library does its own mDNS broadcasting (no avahi needed), so the only ingredient missing was the firewall hole.

### Documentation

- README NixOS HomeKit example now shows `openFirewall = true` + a pinned `homekit.port` explicitly, with a paragraph explaining why both are needed (default `port = 0` is ephemeral and can't be pre-opened in the firewall).
- README Linux + systemd HomeKit caveat lists all three firewall steps a non-NixOS host needs: `StateDirectory=breezyd`, pinned TCP port + `ufw allow N/tcp`, and `ufw allow 5353/udp` for mDNS.

## [1.6.4] - 2026-05-05

### Added

- The CLI's config loader now falls back to `/etc/breezy/config.toml` when `~/.config/breezy/config.toml` doesn't exist. This lets a system-wide install hand the CLI the daemon URL without every user writing their own home-directory config. The two paths are tried in order; explicit user config still wins.
- The NixOS module now writes `/etc/breezy/config.toml` (mode 0644, contents: just `[daemon].listen`) so `breezy ls` Just Works for every user on the host with no per-user setup. No passwords land in this file — passwords stay in `/run/breezyd/breezyd.toml` (mode 0600, daemon-readable only).
- Mode buttons in the dashboard are now labelled `auto / regen / supply / extract` (was `vent / regen / sup / ext`). Protocol values unchanged; this is a UX-only relabel — `auto` matches what the device's `ventilation` mode appears to actually do (datasheet's "auto sensor mode"), and `supply` / `extract` use the protocol's full names.

### Changed

- Config loader's mode-0600 enforcement now only fires when the file actually contains passwords (any `[devices.*].password` or `[daemon].password`). Previously the loader rejected anything not 0600 unconditionally, which made the system fallback file (which has no secrets) impossible to write at the world-readable mode it needs. Behaviour for typical user configs is unchanged: they still have passwords, so the strict check still applies. New tests pin both directions.

### Documentation

- README restructured around three audiences (casual reader, operator, developer). New top-of-doc `## At a glance` block leads with a concrete `breezy ls` table demo + example commands as proof-of-value. New `## Install` triage section points each environment at its own self-contained operator path: `## NixOS` (the module, 4 steps), `## Nix anywhere` (`nix profile install` + standalone CLI), `## Linux + systemd` (binary download + optional hardened systemd unit). New `## Developing` cluster at the bottom collects Build from source / Project layout / Testing / Pointers to deeper docs as ### subsections. Old `## Status` section dissolved (in-scope list folds into the hook, out-of-scope into Known limitations).

## [1.6.3] - 2026-05-05

### Documentation

- README NixOS section restructured into a single end-to-end flow: discover → configure → rebuild & use, with Prometheus / HomeKit / "what the module does" moved to subsections after the main path. Reuses real device IDs/IPs from the running fleet for the example, includes a `breezy ls` table demo so readers can see what success looks like, and threads the static-IP fallback into step 3 (where it belongs as an alternative configuration shape) instead of dangling as a sidebar.

## [1.6.2] - 2026-05-05

### Changed

- The NixOS module now adds the `breezy` CLI to `environment.systemPackages` automatically when `services.breezyd.enable = true`. Same derivation produces both binaries, so the CLI is free; users almost always want it on `PATH` to talk to the daemon they just enabled. Drops the manual `environment.systemPackages = [ config.services.breezyd.package ];` step from the README NixOS instructions. No opt-out knob — anyone in the niche "daemon but no CLI" case can shadow the binary.

## [1.6.1] - 2026-05-05

### Documentation

- README NixOS example trimmed to non-default settings: drops `listen` / `poll_interval` / `discovery` (all defaulted by the loader or the daemon's listen-fallback), collapses `[daemon].password` to dotted-key form, and surfaces the optional per-device `ip` field that wasn't visible in the example before.
- README NixOS section gains a follow-up snippet for the static-IP fallback: if `journalctl -u breezyd` shows `discovery complete found=0` while your units are reachable, configure each device's `ip` directly and the daemon skips broadcast.

## [1.6.0] - 2026-05-05

### Added

- New `[daemon].password` config field (and matching `services.breezyd.settings.daemon.password` on NixOS) sets a fleet-wide protocol password. It's used for the daemon's startup and periodic wildcard discovery probes — replacing the hardcoded factory `"1111"` — and inherited by any `[devices.NAME]` block that omits its own `password`. Most users have all units on the same password and can now set it once instead of per-device. Per-device `password = "..."` still overrides for mixed-fleet hosts. When `[daemon].password` is unset the behaviour is unchanged: discovery uses `"1111"`, and devices keep whatever password (or empty) they were configured with.

  This fixes the silent `discovery complete found=0` log line on hosts where the units are reachable but use a non-default password — same firmware quirk we patched on the CLI side in v1.5.0 with `breezy discover -p PASSWORD`. Add `password = "your-pw"` to `[daemon]` and discovery will populate device IPs at startup again.

## [1.5.2] - 2026-05-05

### Changed

- `breezy discover` empty-result output is now a single unified guidance block (`things to check:` — UDP/4000, broadcast suppression, password mismatch) regardless of broadcast vs unicast mode, instead of branching on the path. The previous v1.5.1 form repeated the firewall hint across two of three branches; this is tighter and reads as one checklist instead of overlapping advice.

### Fixed

- `flake.nix` no longer triggers the deprecated-alias evaluation warning by replacing `pkgs.system` with `pkgs.stdenv.hostPlatform.system` in the `nixosModules.default` wrapper, per the upstream nixpkgs rename. No behavior change; just silences the warning on recent nixpkgs.

## [1.5.1] - 2026-05-04

### Changed

- `breezy discover` now prints the human-readable model name alongside the raw `device_type` byte (e.g. `type=17 (Breezy 160)`) instead of just the magic number. New exported helper `pkg/breezy.UnitTypeName(uint16) string` maps the four known codes (17/20/22/24) and returns `unknown(<n>)` for anything else, sourced from `docs/superpowers/specs/2026-05-03-param-map.md`.
- `breezy discover` no-results output now nudges the operator to check that UDP/4000 is open on the host. A local firewall silently dropping inbound replies looks identical to no devices answering, and that's the next thing to rule out after broadcast-suppression / password-mismatch.

### Fixed

- `nixosModules.default` now defaults `services.breezyd.package` to the flake's own build for the host's system, instead of throwing because `pkgs.breezyd` doesn't exist in nixpkgs. Users importing `inputs.breezyd.nixosModules.default` no longer have to set the package manually. (The option is still settable for overrides.)

## [1.5.0] - 2026-05-04

### Fixed

- **`breezy discover` actually works against real hardware now.** Diagnosed via tcpdump on UDP/4000 against three production Breezy 160 units: real firmware replies to a wildcard discovery request with the device's OWN 16-byte ID in the frame header and `SIZE_PWD=0` — *not* echoing the wildcard ID + password the client sent. The strict `DecodeResponse` rejected every reply with `ErrIDMismatch` / `ErrPwdMismatch`, so `breezy discover` returned "no devices" against any real LAN even when units were reachable. The bug had been silent since v1.0; the test suite (against `pkg/breezy/fakedevice`) passed because `fakedevice` made the same wrong assumption. New `pkg/breezy.DecodeDiscoveryResponse` does relaxed framing-only validation; `fakedevice` now mirrors real-wire behaviour (its own ID + empty password); regression test `TestDecodeDiscoveryResponse_RealWireFormat` pins the wire format observed on hardware.

### Added

- `breezy discover` accepts `-p PASSWORD` (or `--password=PASSWORD`) to override the factory-default discovery password (`"1111"`). The vendor manual says wildcard discovery is unauthenticated but some firmware silently drops requests when the password doesn't match; passing the real password works around it. Works in both broadcast and unicast modes:
  - `breezy discover -p testpwd`
  - `breezy discover -p testpwd 192.168.1.148 192.168.1.152`
- Library: new `pkg/breezy.DiscoverWithPassword(ctx, password)` and `pkg/breezy.DiscoverAtWithPassword(ctx, targets, password)` exported functions. The existing `Discover` and `DiscoverAt` are unchanged — they delegate to the new functions with `DefaultDiscoveryPassword`.

## [1.4.0] - 2026-05-04

### Added

- `breezy discover` accepts positional IP arguments and sends the wildcard discovery request unicast to each, instead of broadcasting. Workaround for networks that drop UDP broadcasts (Wi-Fi AP isolation, mesh hops, separate VLANs) where pinging the device works but broadcast doesn't reach it. Bare-arg form: `breezy discover 192.168.1.148 192.168.1.152`. The bare `breezy discover` form (no args) still broadcasts as before.

## [1.3.0] - 2026-05-04

### Added

- HomeKit bridge: `breezyd` exposes each configured Breezy as a HomeKit accessory (AirPurifier + Supply/Extract Switches + humidity, eCO2, VOC, four temperature sensors). Opt-in via `[homekit].enabled` in `~/.config/breezy/config.toml`. PIN auto-generated and printed every start; reset by deleting the state directory. NixOS module gains `services.breezyd.homekit.{enable, port, bridgeName, stateDir}`. (#1)

### Fixed

- The daemon's POST handlers now route `dial`/`dialRecording` errors through `classifyClientErr` instead of hardcoding HTTP 500 + `internal`. A device that's powered off or off-network now correctly surfaces as HTTP 502 + `device_unreachable`. Regression introduced during the standalone-mode handler refactor (#2).
- `flake.nix`: bump `version` to 1.3.0 and update `vendorHash` for brutella/hap transitive deps so `nix run github:hughobrien/breezyd#breezy` and `nix build` succeed.

## [1.2.0] - 2026-05-04

### Added

- `breezy` CLI runs without `breezyd` for ad-hoc commands. By default — when no daemon is configured — the CLI talks UDP directly to each configured device via `pkg/breezy/ops`. Run the daemon when you want polling, caching, `/metrics`, the embedded dashboard, or per-device write coordination across multiple CLI processes. (#2)
- New `pkg/breezy/ops.go` library carries the per-verb protocol logic (Power, SetSpeedPreset/Manual, SetMode, SetHeater, ResetFilter, ResetFaults, SetRTC, GetStatus, GetFirmware, GetEfficiency, GetFaults). Used by both the daemon's HTTP handlers and the CLI's standalone path. (Phase 1 of #2)
- `breezy param` global lists the static parameter registry — id, name, type, unit, capabilities, description — to help users discover what `get` / `set` accept. (#3)

### Changed

- **Breaking:** the CLI no longer falls back to `http://127.0.0.1:9876` when no daemon is configured. To keep the old behavior, set `[daemon] listen = "127.0.0.1:9876"` in `~/.config/breezy/config.toml` or pass `--daemon http://127.0.0.1:9876`. New first-run config has the `[daemon]` block commented out — new users land in standalone mode.
- `breezy daemon-url` prints `(standalone — no daemon)` when no daemon is configured.
- Daemon HTTP handlers are now thin wrappers around `pkg/breezy/ops`. JSON shape unchanged. (Phase 1 of #2)

## [1.1.0] - 2026-05-04

### Added

- Single-page web dashboard at `GET /` on the daemon, embedded into the binary
  via `go:embed`. Three columns of cards (one per configured device) with live
  sensors, fan RPMs, service info, firmware, and the four high-level controls
  (power / airflow mode / speed / heater). Auto-refreshes every 5 s; cards
  desaturate when the last poll is more than 90 s old; sensor-override warning
  fires when `live.in_user_control` is false.
- NixOS module integration `services.breezyd.nginx.{enable, virtualHost,
  basicAuthFile}` for fronting the daemon with nginx (with optional basic auth
  via a sops-managed htpasswd file). Mirrors the existing
  `services.breezyd.prometheus` shape; the daemon stays loopback-bound while
  nginx is the LAN-facing service.
- First-run config bootstrap: when `breezyd` is started against a missing
  config file, it writes a sensible default at the requested path (mode 0600,
  parent directory created at 0700 if missing) and exits with a friendly
  "edit it" message. Atomic write (temp + rename); refuses to clobber existing
  files.
- Playwright end-to-end tests under `tests/ui/` (pnpm-managed) covering the
  UI's HTTP-call contract via `page.route()` mocking. `just test-ui-install`
  + `just test-ui` run the suite.
- `just screenshot` recipe + committed PNGs of the dashboard in 3-col and
  1-col viewports under `tests/ui/screenshots/`. The dashboard screenshot is
  embedded near the top of the README and re-renders on each `just screenshot`
  run.
- `just lint` (and CI) now fails on `gofmt` drift in addition to running
  `go vet`.
- `just check-all` recipe — full pre-push gate: lint + tests + race + Playwright.
- `just nix-check` recipe — fast parse-check for `nix/module.nix`.
- `cmd/breezyd` HTTP server now sets explicit `Read`, `Write`, and `Idle`
  timeouts in addition to the existing `ReadHeaderTimeout`, so a slow or
  wedged client can't hold a goroutine indefinitely.
- Web dashboard's `.toast` and `.err-banner` carry `role="alert"` so
  assistive tech announces failures and daemon-unreachable events.

### Fixed

- `Discover()` now enumerates every up, non-loopback IPv4 interface and sends
  to its directed-broadcast address in addition to the static list. Previously
  hosts on subnets other than `192.168.0.0/24` or `192.168.1.0/24` could never
  see their own LAN devices.
- `Handler.mux` lazy-initialisation in `cmd/breezyd/server.go` was a data race
  on the first burst of concurrent requests after start. Switched to
  `sync.Once`.

### Documentation

- `docs/superpowers/specs/2026-05-04-discover-investigation.md` captures the
  two-cause analysis behind the discover fix (code defect + the QEMU-NAT
  environmental constraint that's invisible to the breezyd library).
- README has a new Web UI section (with a "Behind nginx (NixOS)" subsection),
  the dashboard screenshot near the top, and a new Security paragraph
  covering the listener-exposure tradeoff.

## [1.0.0] - 2026-05-03

Initial public release.

### Added

- `pkg/breezy` protocol library for the Vents Twinfresh Breezy native UDP/4000
  protocol: FDFD/02 frame codec (multi-byte and high-page parameter support),
  UDP transport with batched read/write, retries and context cancellation,
  parameter registry with typed `Decode`/`Encode`, and LAN device discovery
  via UDP wildcard ID broadcast.
- `pkg/breezy/fakedevice` in-process protocol-speaking server that replays
  captured parameter snapshots, used by the unit tests.
- `internal/config` TOML loader that requires mode `0600` on the config file
  and validates daemon and per-device sections.
- `cmd/breezyd` daemon: per-device poller with batching and a fan-write
  settle window, in-memory state cache with deep-copied snapshots, JSON HTTP
  API with structured snapshots and write-notice hooks, Prometheus
  `/metrics` collector, and graceful shutdown.
- `cmd/breezy` HTTP CLI with a "subject before verb" surface
  (`breezy <device> <verb> [args]`) for sensors, control (power, mode,
  speed, heater), filter/fault resets, RTC set, raw param `get`/`set`,
  firmware/efficiency/status/faults, and `breezy ls` / `breezy discover`.
- `--version` flag on both `breezyd` and `breezy`, populated by goreleaser
  ldflags.
- Live integration tests gated by both the `integration` build tag and
  `BREEZY_INTEGRATION=1`.
- Rapid property tests for the FDFD/02 codec.
- Cross-platform release archives (linux amd64/arm64, darwin amd64/arm64,
  windows amd64) built by GoReleaser on tag pushes.

### Security

- Documented the device firmware's cleartext leak of the protocol password
  (param `0x7D`), WiFi SSID (`0x95`), and WiFi password (`0x96`) over UDP/4000
  to anyone on the LAN who knows the 16-character device ID. Mitigation is
  network segmentation; this project does not add cryptography on top of the
  wire protocol.
- Daemon refuses to start unless the config file is mode `0600`, since device
  passwords are stored in cleartext.

[Unreleased]: https://github.com/hughobrien/breezyd/compare/v1.6.9...HEAD
[1.6.9]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.9
[1.6.8]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.8
[1.6.7]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.7
[1.6.6]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.6
[1.6.5]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.5
[1.6.4]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.4
[1.6.3]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.3
[1.6.2]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.2
[1.6.1]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.1
[1.6.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.6.0
[1.5.2]: https://github.com/hughobrien/breezyd/releases/tag/v1.5.2
[1.5.1]: https://github.com/hughobrien/breezyd/releases/tag/v1.5.1
[1.5.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.5.0
[1.4.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.4.0
[1.3.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.3.0
[1.2.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.2.0
[1.1.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.1.0
[1.0.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.0.0
