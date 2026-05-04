# breezyd

[![License: GPL v3](https://img.shields.io/badge/License-GPL%20v3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Reference](https://pkg.go.dev/badge/github.com/hughobrien/breezyd.svg)](https://pkg.go.dev/github.com/hughobrien/breezyd)
[![Release](https://img.shields.io/github/v/release/hughobrien/breezyd)](https://github.com/hughobrien/breezyd/releases)

A Go library, daemon, and CLI for controlling [Vents Twinfresh
Breezy](https://ventilation-system.com/) energy-recovery ventilators over the
local network. It speaks the device's native UDP/4000 protocol directly — no
cloud account, no MQTT broker, no vendor app, no Home Assistant integration.
LAN only. The daemon polls every configured unit, caches state, exposes a
JSON HTTP API plus Prometheus `/metrics`, and serializes UDP traffic so that
concurrent requests don't corrupt each other.

## Status

This is the v1 scope. It is feature-complete for the things it sets out to do,
and intentionally does not try to do more.

In scope:
- Sensor metrics: humidity, eCO2, VOC, supply/extract/exhaust temperatures,
  fan RPMs, recovery efficiency, filter remaining time, motor lifetime, RTC
  battery, fault codes.
- Control: power, airflow mode (ventilation / regeneration / supply / extract),
  speed (preset 1-3 or manual 10-100 %), heater, filter timer reset, fault
  reset, RTC set.
- Per-device snapshots and Prometheus metrics.
- `breezy discover` for first-time bootstrap.

Out of scope (see "Known limitations" below):
- No schedule editing. Scheduling is on-device and the v1 CLI does not poke at it.
- No WiFi reconfig.
- No MQTT bridge or Home Assistant component.
- No web UI.

Security caveat: the device leaks its protocol password and WiFi credentials in
cleartext to any LAN client that knows the device ID. Put these units on an
IoT VLAN. Details in the [Security](#security) section.

## Install

Pre-built binaries for Linux (amd64/arm64), macOS (amd64/arm64), and Windows
(amd64) are published on the [GitHub Releases
page](https://github.com/hughobrien/breezyd/releases). Download the archive
for your platform and extract `breezyd` and `breezy` somewhere on `$PATH`:

```sh
# Linux amd64 example
curl -sSL -o breezyd.tar.gz \
  https://github.com/hughobrien/breezyd/releases/latest/download/breezyd_Linux_x86_64.tar.gz
tar -xzf breezyd.tar.gz breezyd breezy
sudo install -m 0755 breezyd breezy /usr/local/bin/
breezyd --version
```

## Build from source

Requires Go 1.22+ (developed on 1.26). No other system dependencies for the
binaries themselves; building with `-race` (the default `make test`) needs a
working C toolchain.

```sh
make build       # produces ./breezyd and ./breezy
make test        # go test -race ./...
make lint        # go vet ./...
```

On this dev host `-race` requires `CGO_ENABLED=1 CC=clang` because the host
gcc lacks the TSan runtime; `make test` honours environment overrides.

## Configuration

The daemon and CLI both read `~/.config/breezy/config.toml`. The file must be
mode `0600` — the loader refuses to start otherwise, because device passwords
are stored in cleartext.

Example `~/.config/breezy/config.toml`:

```toml
[daemon]
listen        = "127.0.0.1:9876"
poll_interval = "30s"
discovery     = "on-start"   # "on-start" | "off" | "periodic:<duration>"

[devices.playroom]
id       = "BREEZY00000000A0"
password = "<your password>"
ip       = "192.168.1.148"   # optional; if absent, discovery resolves it

[devices.bedroom]
id       = "BREEZY00000000A1"
password = "<your password>"
ip       = "192.168.1.152"

[devices.office]
id       = "BREEZY00000000A2"
password = "<your password>"
ip       = "192.168.1.160"
```

Run `breezy discover` once on a fresh install to learn each unit's 16-character
device ID; copy them into the config file along with your protocol password.

## First run

```sh
mkdir -p ~/.config/breezy
$EDITOR ~/.config/breezy/config.toml          # see example above
chmod 600 ~/.config/breezy/config.toml

./breezyd &                                   # starts on 127.0.0.1:9876
./breezy ls                                   # one-line summary per device
./breezy playroom status                      # full structured snapshot
```

The daemon logs to stderr. Stop it with SIGINT/SIGTERM; it shuts down within
five seconds.

## CLI overview

`breezy --help` is the source of truth. The shape is "subject before verb",
so per-device commands read naturally:

| Command                              | What it does                                 |
| ------------------------------------ | -------------------------------------------- |
| `breezy ls`                          | one-line table of every configured device   |
| `breezy discover`                    | LAN broadcast (bypasses daemon)             |
| `breezy playroom status`             | full structured snapshot                     |
| `breezy bedroom on` / `off`          | power                                        |
| `breezy bedroom speed manual:30`     | set fan to 30 % manual                       |
| `breezy bedroom speed 2`             | switch to preset 2                           |
| `breezy office mode regeneration`    | airflow mode (ventilation / regeneration / supply / extract) |
| `breezy office heater on`            | toggle the auxiliary heater                  |
| `breezy playroom faults`             | list active fault codes                      |
| `breezy playroom firmware`           | firmware version + build date                |
| `breezy playroom efficiency`         | recovery efficiency %                        |
| `breezy playroom rtc`                | show device clock                            |
| `breezy playroom rtc set 2026-05-03T22:00:00-07:00` | set device clock          |
| `breezy playroom reset-filter`       | clear the filter timer                       |
| `breezy playroom reset-faults`       | clear active fault flags                     |
| `breezy playroom get humidity`       | raw param read by name or hex                |
| `breezy playroom set 0x25 1e`        | raw param write (hex)                        |

The CLI exit codes are: `0` success, `1` daemon/HTTP error (with the daemon's
error envelope rendered as `error: <msg> (<code>)`), `2` local usage error.

## Prometheus

The daemon exposes `/metrics` in Prometheus exposition format. Scrape it like
any other target:

```yaml
# prometheus.yml
scrape_configs:
  - job_name: breezy
    static_configs:
      - targets: ['localhost:9876']
```

Each metric is labelled with `device="<name>"` and `id="<16-char id>"`. A few
useful queries:

```promql
# Indoor temperature per device
breezy_temperature_celsius{location="indoor"}

# Any sensor over its alert threshold (humidity / co2 / voc)
max by (device) (breezy_sensor_alert) > 0

# Recovery efficiency, room by room
breezy_recovery_efficiency_pct

# Filter time remaining, in days
breezy_filter_remaining_seconds / 86400

# Has any device gone unreachable in the last 5 minutes?
time() - breezy_last_poll_timestamp > 300
```

`breezy_up{device="..."}` is `1` while the poller is reaching the unit and `0`
otherwise; the corresponding `breezy_last_poll_timestamp` is the unix time of
the last successful read.

## Project layout

```
breezyd/
├── pkg/breezy/                # protocol library (importable)
│   ├── frame.go               # FDFD/02 packet codec
│   ├── client.go              # UDP transport, retries, timeouts
│   ├── params.go              # parameter registry (id, type, R/W, units)
│   ├── values.go              # typed value codecs
│   ├── discover.go            # LAN broadcast
│   └── fakedevice/            # in-process protocol-speaking fake for tests
├── cmd/breezyd/               # the daemon (HTTP + Prometheus + poller)
├── cmd/breezy/                # the CLI (talks HTTP to the daemon)
├── internal/config/           # TOML config loader, shared by both
├── tools/                     # Phase 0 Python probes (one-off, kept for reference)
└── docs/superpowers/specs/    # design doc, parameter map, vendor PDF manual
```

## Testing

```sh
go test ./...                   # unit tests (default; uses fakedevice)
make test                       # same, with -race
go vet ./...                    # static checks
```

Live integration tests against real hardware are gated by both the
`integration` build tag and `BREEZY_INTEGRATION=1`, plus three env vars
identifying the target device. Example:

```sh
BREEZY_INTEGRATION=1 \
BREEZY_TEST_DEVICE_IP=192.168.1.148 \
BREEZY_TEST_DEVICE_ID=BREEZY00000000A0 \
BREEZY_TEST_DEVICE_PASSWORD=<your password> \
go test -tags integration ./pkg/breezy/...
```

These tests write to the device — each one registers a `t.Cleanup` that
restores the prior value, so re-runs leave the unit in its original state.

On this dev host, `-race` requires a CGO toolchain. If your default `gcc`
lacks the TSan runtime, build the race tests with clang:

```sh
CGO_ENABLED=1 CC=clang go test -race ./...
```

## Security

The Breezy firmware will hand out its own protocol password (param `0x7D`),
the WiFi SSID (`0x95`), and the WiFi password (`0x96`) over UDP/4000 in
cleartext, to any client on the same broadcast domain that knows the
16-character device ID. Discovery is itself unauthenticated — anyone on
the LAN can enumerate every Breezy unit and read those parameters.

Mitigation is networking, not software: put the units on an IoT VLAN that
cannot reach the rest of your home LAN, and only allow the host running
`breezyd` into that VLAN. This project does not add cryptography on top of
the wire protocol — that would not change the threat model, since the
device firmware itself answers in cleartext.

## Known limitations (v1)

These are deliberate omissions, not bugs. Each is a design choice; see the
spec for the full rationale.

- **No schedule editing.** The device's seven-day schedule is on-board; the
  CLI exposes the live state but does not let you re-program it. Out of
  scope for v1 — the operator's stated workflow uses the unit's own buttons.
- **No WiFi reconfig.** Changing the WiFi SSID/password from the CLI is
  technically possible but operationally hazardous (one bad write strands the
  unit). Use the vendor app for this.
- **No MQTT bridge.** The HTTP API and Prometheus surface cover every use
  case the operator has so far. The state cache is shaped so a bridge could
  be added later without rewriting the core.
- **No Home Assistant integration.** Same reasoning. Anyone who wants HA
  integration can build a REST sensor on top of `/v1/devices/<name>` or
  scrape `/metrics`.

## Pointers to deeper docs

- `docs/superpowers/specs/2026-05-03-twinfresh-cli-design.md` — full design
  doc: protocol decisions, daemon architecture, error semantics, status-line
  format, etc.
- `docs/superpowers/specs/2026-05-03-param-map.md` — every parameter ID the
  device exposes, with type, units, observed values, and notes from Phase 0
  characterization.
- `docs/superpowers/specs/breezy-manual-vendor.pdf` — vendor protocol manual,
  the authoritative reference for the wire protocol. Cached locally for offline
  reading; the canonical copy is published by Vents at
  <https://ventilation-system.com/download/breezy-manual-21433.pdf>.
- `docs/superpowers/specs/breezy-datasheet-vendor.pdf` — hardware datasheet.
  Canonical copy at
  <https://ventilation-system.com/download/breezy-datasheet-21437.pdf>.

## Credits

This project would not have been possible without the published protocol
documentation from **Ventilation Systems Ltd. (Vents)**. The Breezy / Breezy
Eco connection-instruction manual at
<https://ventilation-system.com/download/breezy-manual-21433.pdf> documents the
full wire protocol, packet structure, function codes, and parameter table that
this library implements. Reading the manual confirmed (and in places
corrected) the empirical reverse-engineering captured during Phase 0 of this
project. Thanks to Vents for publishing it openly.

The bundled copies of the manual and datasheet under
`docs/superpowers/specs/` are provided for convenience and remain © Vents.
Refer to the canonical URLs above for the latest versions.

## License

Copyright (C) 2026 Hugh O'Brien

This program is free software: you can redistribute it and/or modify it under
the terms of the GNU General Public License as published by the Free Software
Foundation, either version 3 of the License, or (at your option) any later
version (`SPDX-License-Identifier: GPL-3.0-or-later`).

This program is distributed in the hope that it will be useful, but WITHOUT
ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS
FOR A PARTICULAR PURPOSE. See the [LICENSE](LICENSE) file for the full text
of the GNU General Public License v3.

This project is not affiliated with or endorsed by Ventilation Systems Ltd.
"Vents" and "Twinfresh" are trademarks of their respective owners.
