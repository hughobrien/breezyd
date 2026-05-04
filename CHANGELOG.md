# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[1.0.0]: https://github.com/hughobrien/breezyd/releases/tag/v1.0.0
