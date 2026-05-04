# Twinfresh CLI / daemon — design

**Date:** 2026-05-03
**Status:** approved for implementation planning
**Repo:** `~/breezyd` (currently empty, will be initialized as a Go module)

## Summary

A Go-based control plane for Vents/Blauberg Twinfresh Elite ERV units ("Breezy" badge in the app) on the local network. Three deliverables:

1. `pkg/breezy` — protocol library (UDP/4000, FDFD/02 frame).
2. `cmd/breezyd` — long-running daemon. Polls devices, caches state, exposes JSON HTTP API and Prometheus `/metrics`.
3. `cmd/breezy` — thin CLI that calls the daemon's HTTP API.

MQTT, Home Assistant integration, and a web UI are out of scope for v1 but the daemon's internal state cache is shaped so they can be added as additional consumers later without rewriting anything.

## Context

### Devices

Three Twinfresh Elite 160 ERV units on the LAN, all unit-type `0x0011` ("Breezy"). Discovered IDs and IP mapping:

| IP | Device ID | Protocol password | Status |
|---|---|---|---|
| `192.168.1.148` | `BREEZY00000000A0` | `1111` | confirmed responsive |
| `192.168.1.152` | `BREEZY00000000A1` | `1111` | confirmed responsive |
| `192.168.1.160` | `BREEZY00000000A2` (by elimination) | unknown | silent on every common password; user fills in or factory-resets |

### Protocol (confirmed by live probing on 2026-05-03)

- Transport: UDP, port 4000, max 256 bytes.
- Frame: `FD FD 02 | id_len(1) | device_id(16 ASCII) | pwd_len(1) | password | function(1) | payload | checksum_lo, checksum_hi`. Checksum is the unsigned sum of all bytes from byte 2 (after the `FD FD` magic) through end of payload, low byte first.
- Function codes: `0x01` read, `0x02` write-no-response, `0x03` write-with-response, `0x04` increment, `0x05` decrement, `0x06` device response.
- One parameter per packet (the Atmo/Breezy variant silently drops batched requests). Multi-byte params are prefixed with `FE <len>` in responses; single-byte params come back as `<id> <value>`. Unsupported params come back as `FD <id>`.
- Default device ID for unpaired/discovery: `DEFAULT_DEVICEID`. Default password: `1111`. The 6-digit password (`111111`) printed on stickers is for AP-mode WiFi join, not the protocol.

### Prior art

- `pyecoventv2` — Python lib for older Expert series. Not Atmo-aware.
- `aglehmann/home_assistant_ecovent` (HA fork) — adds Atmo workarounds. Closest reference for the protocol variant our devices speak.
- HA community thread <https://community.home-assistant.io/t/vents-twinfresh-atmo-integration/991872> — corroborates the no-batching and high-byte-omission quirks.

We are **not** wrapping `pyecoventv2`. Fresh Go implementation, informed by but not derived from those projects.

### Security note (vendor-side, not a deliverable)

The device exposes its protocol password, the WiFi SSID, and the WiFi password in cleartext to any LAN client that knows the device ID — and the device ID is broadcastable. Network-segmenting these onto an IoT VLAN is the appropriate mitigation, separate from this project.

## Architecture

### Repo layout

```
breezy/
├── go.mod
├── pkg/breezy/                # public library
│   ├── frame.go               # FDFD/02 packet encode/decode
│   ├── frame_test.go          # codec round-trips, golden frames, checksum cases
│   ├── client.go              # UDP transport, request/response, retries, timeouts
│   ├── params.go              # param ID registry: name, type, unit, R/W flag
│   ├── discover.go            # broadcast 0x7C discovery
│   └── fakedevice/            # in-process fake server speaking the protocol
├── cmd/breezyd/
│   ├── main.go
│   ├── server.go              # HTTP routes, Prom handler
│   ├── poller.go              # per-device polling goroutine
│   ├── state.go               # in-memory state cache
│   └── integration_test.go    # gated by BREEZY_INTEGRATION=1
├── cmd/breezy/
│   └── main.go                # CLI; talks HTTP to breezyd
├── internal/config/           # TOML loader, shared by daemon + CLI
└── docs/superpowers/specs/    # this file + the param map produced by Phase 0
```

### Why a daemon owns all device traffic

UDP request/response with checksums isn't safe to fan out concurrently from independent CLI invocations — overlapping retries and packet collisions cause silent corruption. Funneling through one process serializes UDP traffic per device. CLI talks HTTP to the daemon; daemon talks UDP to devices.

## Library API (`pkg/breezy`)

```go
type Client struct{ /* unexported */ }

func NewClient(addr string, deviceID, password string) *Client
func (c *Client) Close() error

func (c *Client) ReadParam(ctx context.Context, id ParamID) (Value, error)
func (c *Client) WriteParam(ctx context.Context, id ParamID, v Value) error

func (c *Client) ReadAll(ctx context.Context) (Snapshot, error)
func (c *Client) SetSpeed(ctx context.Context, s Speed) error
func (c *Client) SetMode(ctx context.Context, m AirflowMode) error
func (c *Client) SetPower(ctx context.Context, on bool) error

func Discover(ctx context.Context, iface string) ([]Found, error)
```

`ParamID` is a typed `uint8`. `params.go` maps each known ID to a name, decoder (uint8 / uint16 / IPv4 / ASCII / packed datetime), unit (`°C`, `%`, `ppm`, `RPM`, `hours`, dimensionless), and read/write flag. Unknown params still readable as raw bytes via `ReadParam` so we can extend without recompiling logic.

## Daemon HTTP API

Default bind: `127.0.0.1:9876`. JSON in, JSON out. No auth in v1 (loopback only). Errors: `{"error": "...", "code": "..."}` with appropriate HTTP status.

```
GET  /v1/devices                       # list configured devices + last-seen state
GET  /v1/devices/{name}                # full snapshot of one device
GET  /v1/devices/{name}/params/{id}    # raw param read (passthrough, fresh, bypasses cache)
POST /v1/devices/{name}/power          # body: {"on": true}
POST /v1/devices/{name}/speed          # body: {"speed": 2}  or {"manual": 180}
POST /v1/devices/{name}/mode           # body: {"mode": "heat-recovery"}
POST /v1/devices/{name}/params/{id}    # raw param write
GET  /metrics                          # Prometheus
GET  /healthz                          # liveness
```

Cache vs. passthrough: `/v1/devices/...` aggregate endpoints read from the in-memory cache populated by the poller. `/v1/devices/{name}/params/{id}` always issues a fresh UDP request, for debugging. Writes always issue UDP and update the cache on success.

## CLI

```
breezy <name> status
breezy <name> on
breezy <name> off
breezy <name> speed <1|2|3|manual:180>
breezy <name> mode <ventilation|heat-recovery|air-supply>
breezy <name> get <param>            # raw read by name or hex id (e.g. 0x25 or "humidity")
breezy <name> set <param> <val>      # raw write

breezy ls                            # list all configured devices + at-a-glance state
breezy discover                      # broadcast scan, prints found IDs/IPs
breezy daemon-url                    # debug: prints the daemon URL it would hit
```

Device names are reserved against the global verbs (`ls`, `discover`, `daemon-url`); config loader rejects collisions. Flag `--daemon http://host:port` overrides the default address.

No `--direct` mode in v1. Daemon must be running. This keeps the path through code single and avoids the concurrent-UDP hazard.

## Prometheus metrics

Per-device labelled (`device="living_room"`, `id="BREEZY00000000A0"`):

```
breezy_power                      gauge   0/1
breezy_speed                      gauge   1..3 or 22..255
breezy_airflow_mode               gauge   enum-encoded (1=ventilation, 2=heat-recovery, 3=air-supply)
breezy_fan_rpm                    gauge
breezy_humidity_percent           gauge
breezy_co2_ppm                    gauge
breezy_temperature_celsius        gauge   label position={"intake_outdoor","intake_indoor","exhaust_outdoor","exhaust_indoor"}
breezy_filter_hours               gauge   hours of filter use
breezy_last_poll_timestamp        gauge   unix seconds
breezy_poll_errors_total          counter labels: device, kind={"timeout","checksum","auth"}
breezy_up                         gauge   1 if last poll succeeded, else 0
```

Polling interval default 30 s, configurable per-device. `/metrics` and `/v1/devices/...` aggregate endpoints both read from the cache (no on-demand UDP).

Temperature `position` labels are a starting guess. Phase 0 will replace them with empirically validated names.

## Configuration

`~/.config/breezy/config.toml`, file mode 0600 (loader refuses world-readable):

```toml
[daemon]
listen        = "127.0.0.1:9876"
poll_interval = "30s"
discovery     = "on-start"           # "on-start" | "off" | "periodic:5m"

[devices.living_room]
id       = "BREEZY00000000A0"
password = "1111"
# ip optional — if set, skips discovery for this device

[devices.bedroom]
id       = "BREEZY00000000A1"
password = "1111"

[devices.basement]
id       = "BREEZY00000000A2"
password = "TBD"                     # the silent .160; fill in once recovered
```

The CLI reads only `[daemon].listen`. The daemon reads everything. Plaintext storage is acceptable here because the device leaks the protocol password back over the LAN unauthenticated — encrypting our config would not improve the threat model.

## Discovery

On daemon start (and optionally periodic):

1. UDP-broadcast a `0x7C` (search) frame on `192.168.1.255` and `255.255.255.255` using `DEFAULT_DEVICEID` / `1111`.
2. Collect `(IP, device_id)` responses.
3. For each configured device, update its current IP. If a configured device isn't found, mark it `unreachable` but keep the daemon running and keep retrying on each poll cycle.
4. Discovered IDs that aren't in config are logged once: `unconfigured device <id> at <ip> — add a [devices.NAME] block to control it`.

`breezy discover` runs the same broadcast and prints results without touching config — useful for first-time setup.

Default is `on-start`; periodic discovery is overkill on a stable home LAN.

## Phase 0 — Empirical parameter characterization (interactive, before implementation)

Goal: every entry in `pkg/breezy/params.go` is named from observation, not inference. A live probe sweep on 2026-05-03 confirmed ~60 readable params on `.148`; semantic meaning of most is currently a guess.

Process:

1. Throwaway interactive probe (Python is fine for this phase — fastest iteration; the eventual Go lib is not a prerequisite).
2. Walk through param IDs in batches. For each: read current value, propose a write to the user (e.g. "about to flip `0x01` from 1→0"), wait for the user's go-ahead, write, then re-read.
3. User reports observed effect on the unit (fan stopped / sped up / LED changed / app shows new mode / nothing).
4. Findings recorded in `docs/superpowers/specs/2026-05-03-param-map.md`, one row per param: `id | name | type | unit | observed_behavior | safe_to_write`.
5. That doc is the source of truth that `params.go` is generated from / validated against in the implementation phase.

Safety rails:

- Skip network-config params (`0x9C–0xA3`), credential params (`0x7D` protocol password, `0x95` WiFi SSID, `0x96` WiFi password) — risk of bricking or losing remote access.
- Always re-read after a write before moving on.
- Probe one device only (`.148`); the other two stay untouched until the table is trusted.
- One write per command; no automation; user can stop at any time.

## Testing strategy

- **Unit tests** (`go test ./...`, no env required): full coverage of `frame.go` codec — encode/decode round-trips, golden hex frames captured from real devices today, checksum boundary cases.
- **Fake device** (`pkg/breezy/fakedevice`): in-process UDP server that replays Phase 0 param-sweep snapshots. Daemon tests run against it — exercises the full HTTP → state cache → UDP path without hardware.
- **Integration tests** (`cmd/breezyd/integration_test.go`): gated by `BREEZY_INTEGRATION=1` and `BREEZY_TEST_DEVICE_IP/ID/PASSWORD`. Skipped in normal runs. Run locally; not in CI.
- **Param sweep snapshot**: the raw output captured during Phase 0 is committed as a fixture so the fake server can replay it deterministically.

## Out of scope for v1

Designed-around but not built:

- MQTT bridge (would be a future `cmd/breezy-mqttd` consuming the same state cache).
- Home Assistant native integration.
- Web UI / dashboard.
- Schedule / timer / night-mode / turbo control surfaces (raw `set` works for these via param IDs in v1).
- TLS or authn on the HTTP API (loopback bind is the v1 boundary).
- Persisting state across daemon restarts (cache is in-memory).
- Recovering the password for `.160`. User-side runbook below.

## Risks

1. **Param semantics are best-effort**. Some IDs returned values during the sweep but the meaning isn't certain (e.g. `0x07` vs. `0xB7` for airflow mode). Phase 0 mitigates but cannot eliminate this — names should be conservative.
2. **Write-then-read race**. The device may not reflect a write on the next read. Implementation will retry-after-delay or accept a small staleness window.
3. **`.160` unknown password**. Not a design gap; a content gap. Spec ships with the slot present; user fills it in once recovered.

## `.160` recovery runbook (operational, not code)

If the protocol password for `.160` (`BREEZY00000000A2`) is genuinely lost:

1. Locate the unit's reset button on the controller PCB (per the printed manual).
2. Hold for 5 seconds until the unit beeps / LEDs flash.
3. Re-pair via the Vents/Blauberg app. The protocol password resets to `1111`.
4. Update `[devices.basement].password = "1111"` in the config.

## Approvals

- Architecture, APIs, CLI shape, ops model: approved by user during brainstorming on 2026-05-03.
- Spec doc itself: pending user review (see review gate below).

## Next step after this spec is approved

Invoke `superpowers-extended-cc:writing-plans` to produce a step-by-step implementation plan whose first phase is the interactive Phase 0 characterization, followed by library → daemon → CLI in that order.
