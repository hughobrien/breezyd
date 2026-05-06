# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

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
