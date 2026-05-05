# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/hughobrien/breezyd/compare/v1.5.2...HEAD
[1.5.2]: https://github.com/hughobrien/breezyd/releases/tag/v1.5.2
[1.5.1]: https://github.com/hughobrien/breezyd/releases/tag/v1.5.1
[1.5.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.5.0
[1.4.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.4.0
[1.3.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.3.0
[1.2.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.2.0
[1.1.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.1.0
[1.0.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.0.0
