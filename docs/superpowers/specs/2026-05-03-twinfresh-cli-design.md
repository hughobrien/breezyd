# Twinfresh CLI / daemon — design

**Date:** 2026-05-03
**Status:** approved for implementation; revised after Phase 0 characterization and reading the vendor protocol manual.
**Repo:** `~/breezyd`

## Summary

A Go-based control plane for Vents Twinfresh Breezy ERV units on the local network. The same hardware is sold as **Breezy 160** in Europe and **Twinfresh Elite 160 Pro** in North America; both speak the protocol documented here. Three deliverables:

1. `pkg/breezy` — protocol library (UDP/4000, FDFD/02 frame).
2. `cmd/breezyd` — long-running daemon. Polls devices, caches state, exposes JSON HTTP API and Prometheus `/metrics`.
3. `cmd/breezy` — thin CLI that calls the daemon's HTTP API.

MQTT, Home Assistant integration, web UI, and schedule editing are out of scope for v1 but the daemon's internal state cache is shaped so they can be added later without rewriting the core.

## Context

### Devices

Three Breezy 160 units (device type `0xB9 = 17`) on the LAN. All confirmed responsive on protocol password `testpwd` (operator changed all three from the factory default `1111` during Phase 0).

| IP | Device ID | Location |
|---|---|---|
| `192.168.1.148` | `BREEZY00000000A0` | playroom |
| `192.168.1.152` | `BREEZY00000000A1` | bedroom |
| `192.168.1.160` | `BREEZY00000000A2` | office |

### Protocol

Documented in the vendor manual (`docs/superpowers/specs/breezy-manual-vendor.pdf`) and corroborated by live probing. Authoritative summary:

- **Transport:** UDP/4000, max 256-byte packets.
- **Frame:** `FD FD | TYPE(0x02) | SIZE_ID(0x10) | ID(16 ASCII) | SIZE_PWD | PWD | FUNC | DATA | Chksum_L | Chksum_H`. Checksum = sum of bytes from `TYPE` through end of `DATA`, low byte first.
- **Function codes:** `0x01` read, `0x02` write-no-response, `0x03` write-with-response, `0x04` increment, `0x05` decrement, `0x06` controller response.
- **Auth-failure response (firmware behavior, undocumented):** wrong password → device replies with function `0x07` and 2-byte payload `01 <byte>`. Decoder must surface this as a typed `ErrAuth`, not a generic decode error.
- **Special command bytes inside DATA:** `0xFC` change FUNC, `0xFD` "parameter not supported," `0xFE <size>` next param value uses `<size>` bytes, `0xFF <hi>` change parameter-number high byte (page).
- **Multi-byte writes** use `FE <size> <id> <bytes...>` framing in DATA.
- **Page mechanism** routes parameter IDs > 0xFF: prefix DATA with `FF <hi>` to switch the high byte for subsequent params in that packet.
- **Discovery is unauthenticated:** sending any request with the literal device ID `DEFAULT_DEVICEID` returns the device's true ID and unit type regardless of password.
- **Time encoding** is consistent: little-endian "smaller unit first." 2-byte = `[min, hr]`, 3-byte = `[sec, min, hr]`, 4-byte = `[min, hr, day_lo, day_hi]`.
- **Fan-speed settle time:** the unit needs ~10-15 s after a write to `0x02`, `0x44`, or `0xB7` before `0x4A`/`0x4B` reflect the new state. The poller must not query these fields immediately after a write.
- **Sensor override:** when humidity/CO2/VOC are over their thresholds AND the corresponding sensor is enabled, the device boosts the fan to a sensor-driven level **regardless of the user's manual %** or the active preset. This is a user-visible quirk, not a bug — the CLI's status output must distinguish "configured" from "live" so users aren't confused.

### Phase 0 outcome (now complete)

The full parameter map is at `docs/superpowers/specs/2026-05-03-param-map.md`, reconciled with the vendor manual. Of the parameters the device exposes, ~50 are confirmed and useful for v1; a handful (`0x06`, `0x2B`, `0x93`, `0x9F`, `0xA1`, `0x030C`, `0x0329`) are undocumented internals that we leave unmapped.

### Security note (vendor-side, not a deliverable)

The device exposes its protocol password (`0x7D`), the WiFi SSID (`0x95`), and the WiFi password (`0x96`) in cleartext to any LAN client that knows the device ID. Network-segmenting these onto an IoT VLAN is the appropriate mitigation, separate from this project.

## Architecture

### Repo layout

```
breezy/
├── go.mod
├── pkg/breezy/                # public library
│   ├── frame.go               # FDFD/02 packet encode/decode (incl. ErrAuth on func 0x07)
│   ├── frame_test.go          # codec round-trips, golden frames, checksum cases
│   ├── client.go              # UDP transport, request/response, retries, timeouts
│   ├── params.go              # param ID registry: name, type, unit, R/W flag
│   ├── values.go              # typed value encoding/decoding (uint8/16, int16, IPv4, ASCII, time fields)
│   ├── discover.go            # broadcast 0x7C discovery
│   └── fakedevice/            # in-process fake server speaking the protocol
├── cmd/breezyd/
│   ├── main.go
│   ├── server.go              # HTTP routes
│   ├── poller.go              # per-device polling goroutine
│   ├── state.go               # in-memory state cache
│   ├── metrics.go             # Prometheus collector
│   └── integration_test.go    # gated by BREEZY_INTEGRATION=1
├── cmd/breezy/
│   └── main.go                # CLI; talks HTTP to breezyd
├── internal/config/           # TOML loader, shared by daemon + CLI
└── docs/superpowers/specs/    # this file + the param map + the vendor manual
```

### Why a daemon owns all device traffic

UDP request/response with checksums isn't safe to fan out concurrently from independent CLI invocations — overlapping retries and packet collisions cause silent corruption. Funneling through one process serializes UDP traffic per device. CLI talks HTTP to the daemon; daemon talks UDP to devices.

## Library API (`pkg/breezy`)

```go
type Client struct{ /* unexported */ }

func NewClient(addr string, deviceID, password string, opts ...Option) (*Client, error)
func (c *Client) Close() error

// Single-param read/write. Multi-byte values handled transparently
// via FE <size> framing, and high-page params (>= 0x0100) handled
// transparently via FF <hi> prefixing.
func (c *Client) ReadParam(ctx context.Context, id ParamID) (Value, error)
func (c *Client) WriteParam(ctx context.Context, id ParamID, v Value) error

// High-level helpers built on the primitives.
func (c *Client) ReadAll(ctx context.Context) (Snapshot, error)
func (c *Client) SetPower(ctx context.Context, on bool) error
func (c *Client) SetSpeed(ctx context.Context, s Speed) error      // preset 1-3 or manual N% (10..100)
func (c *Client) SetMode(ctx context.Context, m AirflowMode) error // ventilation, regeneration, supply, extract
func (c *Client) ResetFilter(ctx context.Context) error            // writes 0x65
func (c *Client) ResetFaults(ctx context.Context) error            // writes 0x80
func (c *Client) Faults(ctx context.Context) ([]Fault, error)      // reads 0x7F
func (c *Client) Firmware(ctx context.Context) (FirmwareInfo, error) // decodes 0x86

func Discover(ctx context.Context, broadcasts []string) ([]Found, error)

// Typed errors the caller should case on:
var (
    ErrAuth        = errors.New("breezy: authentication failed (function 0x07)")
    ErrUnsupported = errors.New("breezy: parameter unsupported by device")
    ErrReadOnly    = errors.New("breezy: parameter is read-only")
    ErrChecksum    = errors.New("breezy: checksum mismatch")
)
```

`ParamID` is a typed `uint16`. `params.go` maps each known ID (drawn from the param-map document, which is itself reconciled with the vendor manual) to a name, value type, unit, and capability flags (R / W / INC / DEC). Writes to read-only params return `ErrReadOnly` without going to the wire. Unknown params are still readable as raw bytes via `ReadParam` for forward-compatibility.

`Value` is an interface with concrete types `Uint8`, `Uint16`, `Int16`, `IPv4`, `ASCII`, `TimeOfDay (3-byte sec/min/hr)`, `Duration (2-byte min/hr)`, `RemainingTime (4-byte min/hr/day(2))`, `Date (4-byte day/dow/month/year)`, `FirmwareMeta (6-byte)`, `AlertBitmap (5-byte)`, `Raw`.

## Daemon HTTP API

Default bind: `127.0.0.1:9876`. JSON in, JSON out. No auth in v1 (loopback only). Errors: `{"error": "...", "code": "..."}` with appropriate HTTP status.

```
GET  /v1/devices                                  # list configured devices + last-seen state
GET  /v1/devices/{name}                           # full snapshot of one device
GET  /v1/devices/{name}/firmware                  # decode 0x86
GET  /v1/devices/{name}/efficiency                # decode 0x0129
GET  /v1/devices/{name}/faults                    # decode 0x7F (list)
GET  /v1/devices/{name}/params/{id}               # raw param read (passthrough, fresh, bypasses cache)

POST /v1/devices/{name}/power                     # body: {"on": true}
POST /v1/devices/{name}/speed                     # body: {"preset": 2}  or  {"manual": 30}
POST /v1/devices/{name}/mode                      # body: {"mode": "regeneration"}
POST /v1/devices/{name}/heater                    # body: {"on": true}
POST /v1/devices/{name}/filter/reset              # writes 0x65
POST /v1/devices/{name}/faults/reset              # writes 0x80
POST /v1/devices/{name}/rtc                       # body: {"time": "2026-05-03T22:36:00"}, writes 0x6F + 0x70
POST /v1/devices/{name}/params/{id}               # raw param write

GET  /metrics                                     # Prometheus
GET  /healthz                                     # liveness
```

Cache vs. passthrough: `/v1/devices` aggregate endpoints read from the in-memory cache populated by the poller. `/v1/devices/{name}/params/{id}` always issues a fresh UDP request, for debugging. Writes always issue UDP and update the cache on success.

JSON shape for `GET /v1/devices/{name}`:

```json
{
  "name": "playroom",
  "id": "BREEZY00000000A0",
  "ip": "192.168.1.148",
  "last_poll": "2026-05-03T22:36:00Z",
  "configured": {
    "power": true,
    "speed_mode": "manual",
    "manual_pct": 30,
    "airflow_mode": "regeneration",
    "heater_enabled": false,
    "co2_threshold_ppm": 800,
    "humidity_threshold_pct": 65
  },
  "live": {
    "fan_supply_rpm": 5340,
    "fan_extract_rpm": 5400,
    "heater_running": false,
    "in_user_control": false,
    "sensor_alerts": {"humidity": false, "co2": true, "voc": true}
  },
  "sensors": {
    "humidity_pct": 52,
    "eco2_ppm": 3500,
    "voc_index": 350,
    "temp_outdoor_c": 20.8,
    "temp_supply_c": 21.9,
    "temp_exhaust_inlet_c": 21.6,
    "temp_exhaust_outlet_c": 20.9,
    "recovery_efficiency_pct": 85
  },
  "service": {
    "filter_status": "clean",
    "filter_remaining_seconds": 7732560,
    "motor_lifetime_seconds": 52320,
    "rtc_battery_volts": 3.34,
    "fault_level": "none",
    "frost_protection_active": false
  },
  "firmware": {"version": "0.11", "build_date": "2025-03-21"}
}
```

`live.in_user_control` is `true` when no sensor alert is active and no special mode is running — i.e. the device is doing what the user asked. When `false`, expect `live.fan_*_rpm` to diverge from `configured.manual_pct` × max-RPM.

## CLI

```
# Device-targeted verbs (device name first):
breezy <name> status
breezy <name> on
breezy <name> off
breezy <name> speed <1|2|3>             # preset
breezy <name> speed manual:<pct>        # 10..100; rejected below 10 (firmware floor)
breezy <name> mode <ventilation|regeneration|supply|extract>
breezy <name> heater <on|off>
breezy <name> reset-filter
breezy <name> reset-faults
breezy <name> faults                    # list active alarms/warnings
breezy <name> firmware
breezy <name> efficiency                # current heat-recovery efficiency %
breezy <name> rtc                       # show
breezy <name> rtc set <RFC3339>         # set
breezy <name> get <param>               # raw read by name or hex id (e.g. 0x25 or "humidity")
breezy <name> set <param> <val>         # raw write

# Globals:
breezy ls                               # list configured devices + at-a-glance state
breezy discover                         # broadcast scan, prints found IDs/IPs
breezy daemon-url                       # debug: prints the daemon URL it would hit
```

Device names reserved against the global verbs (`ls`, `discover`, `daemon-url`); config loader rejects collisions. Flag `--daemon http://host:port` overrides the default address.

`breezy <name> status` distinguishes "configured" from "live" — when the sensor-override behavior is in effect, the output shows both:

```
playroom @ 192.168.1.148  (firmware 0.11, last poll 3s ago)
  power      : on
  mode       : regeneration
  speed      : manual 30%        (live: 5340 / 5400 rpm)   ⚠  CO2 alert driving fan above setting
  sensors    : RH=52%  eCO2=3500ppm  VOC=350  outdoor=20.8°C  recovery=85%
  service    : filter clean (89d 9h remaining)  motor 14h 32m lifetime
  battery    : RTC 3.34 V
```

The `⚠` line on `speed` is shown only when `live.in_user_control` is false. Mirrors the manual's documented sensor-override behavior so users understand why their setting "isn't taking."

## Prometheus metrics

Per-device labelled (`device="playroom"`, `id="BREEZY00000000A0"`).

**Configured/state gauges:**
```
breezy_power                          0/1
breezy_airflow_mode                   enum: 0=ventilation,1=regeneration,2=supply,3=extract
breezy_speed_mode                     enum: 1-3 preset, 255=manual
breezy_speed_manual_pct
breezy_heater_enabled                 0/1 (user toggle)
breezy_humidity_threshold_pct
breezy_co2_threshold_ppm
breezy_voc_threshold_index
breezy_humidity_sensor_enabled        0/1
breezy_co2_sensor_enabled             0/1
breezy_voc_sensor_enabled             0/1
breezy_filter_timeout_days
```

**Live state gauges:**
```
breezy_fan_rpm{fan="supply|extract"}
breezy_heater_running                 0/1 (firmware-driven; can be 1 even with heater_enabled=0 during frost protection)
breezy_in_user_control                0/1 (0 when sensor override or special mode is active)
breezy_special_mode                   enum: 0=off,1=night,2=turbo
breezy_special_mode_remaining_seconds
breezy_sensor_alert{sensor="humidity|co2|voc"}  0/1 (decoded from 0x84)
breezy_recovery_efficiency_pct
breezy_frost_protection_active        0/1
```

**Sensors:**
```
breezy_humidity_percent
breezy_eco2_ppm
breezy_voc_index
breezy_temperature_celsius{position="outdoor|supply|exhaust_inlet|exhaust_outlet"}
```

**Service / health:**
```
breezy_filter_status                  0=clean,1=soiled
breezy_filter_remaining_seconds
breezy_motor_lifetime_seconds
breezy_rtc_battery_volts
breezy_fault_level                    0=none,1=alarm,2=warning
```

**Daemon health:**
```
breezy_last_poll_timestamp            unix seconds
breezy_poll_errors_total{kind="timeout|checksum|auth|other"} counter
breezy_up                             0/1 (1 if last poll succeeded)
breezy_info{firmware="0.11", build_date="2025-03-21"} = 1   gauge with labels for diagnostics
```

`eco2` (rather than `co2`) is intentional — the metric name reflects that it's a CO2-equivalent computed from a VOC sensor, not a true NDIR CO2 reading. Users running stoichiometric checks against actual CO2 monitors are warned by the metric name.

Polling interval default 30 s, configurable per-device. `/metrics` and aggregate JSON endpoints both read from the cache (no on-demand UDP).

## Configuration

`~/.config/breezy/config.toml`, file mode 0600 (loader refuses world-readable):

```toml
[daemon]
listen        = "127.0.0.1:9876"
poll_interval = "30s"
discovery     = "on-start"           # "on-start" | "off" | "periodic:5m"

[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
# ip optional — if set, skips discovery for this device

[devices.bedroom]
id       = "BREEZY00000000A1"
password = "testpwd"

[devices.office]
id       = "BREEZY00000000A2"
password = "testpwd"
```

The CLI reads only `[daemon].listen`. The daemon reads everything. Plaintext storage is acceptable here because the device leaks the protocol password back over the LAN unauthenticated — encrypting our config would not improve the threat model.

## Discovery

On daemon start (and optionally periodic):

1. UDP-broadcast a request for parameter `0x7C` on `192.168.1.255` and `255.255.255.255` using `DEFAULT_DEVICEID` and the password from any configured device (or `1111` as fallback). The response is unauthenticated, so any password works for this query.
2. Collect `(IP, device_id)` responses.
3. For each configured device, update its current IP. If a configured device isn't found, mark it `unreachable` but keep the daemon running and keep retrying on each poll cycle.
4. Discovered IDs that aren't in config are logged once: `unconfigured device <id> at <ip> — add a [devices.NAME] block to control it`.

`breezy discover` runs the same broadcast and prints results without touching config — useful for first-time setup.

Default is `on-start`; periodic discovery is overkill on a stable home LAN.

## Testing strategy

- **Unit tests** (`go test ./...`, no env required): full coverage of `frame.go` codec — encode/decode round-trips, golden hex frames captured during Phase 0, checksum boundary cases, function-`0x07` auth-failure surface as `ErrAuth`, multi-byte writes use `FE` framing, high-page reads/writes use `FF` prefixing.
- **Fake device** (`pkg/breezy/fakedevice`): in-process UDP server that replays Phase 0 param-sweep snapshots. Daemon tests run against it — exercises the full HTTP → state cache → UDP path without hardware.
- **Integration tests** (`cmd/breezyd/integration_test.go`): gated by `BREEZY_INTEGRATION=1` and `BREEZY_TEST_DEVICE_IP`/`_ID`/`_PASSWORD`. Skipped in normal runs. Run locally; not in CI.
- **Param-table cross-check test:** a unit test that asserts every `(name, id, type)` row in `params.go` matches the param-map markdown, so they can't drift apart silently.

## Out of scope for v1

Designed-around but not built:

- MQTT bridge (would be a future `cmd/breezy-mqttd` consuming the same state cache).
- Home Assistant native integration.
- Web UI / dashboard.
- **Schedule editing** (`0x77`). The schedule's read/write protocol is documented but uses an indexed multi-byte access pattern that doubles the library surface; defer until someone asks for it. The CLI can still report whether the schedule is enabled (`0x72`) and the live "schedule active speed" (`0x0306`).
- **WiFi reconfiguration** (`0x94`/`0x95`/`0x96`/`0x99`/`0x9A`/`0x9B`-`0x9E`/`0xA0`/`0xA2`). Risky (a wrong write disconnects the device); the app handles it. If exposed later, behind a `--unsafe-wifi` flag.
- **Sensor "invert" mode** (value `2` for `0x0F`/`0x11`/`0x0315`). The CLI `<name> sensors humidity on|off` covers the common case; the API still surfaces value=2 if read.
- **TLS or authn on the HTTP API** (loopback bind is the v1 boundary).
- **Persisting state across daemon restarts** (cache is in-memory; restart re-polls).

## Risks

1. **Sensor-override surprise.** Users will set a manual % and the fan will run higher because a sensor is over threshold. The CLI status output and the `breezy_in_user_control` metric mitigate this by making it explicit, but the surprise is fundamental to the device's design.
2. **WiFi-config write danger.** Writing a wrong value to `0x9C-0x9E` or `0x94`/`0x9B` could disconnect the device from the network; the unit then needs to be reset to AP mode and re-paired via the app. This is why we don't expose those endpoints in v1.
3. **Frost protection conflates user heater toggle.** The Prom metric `breezy_heater_running` may be 1 even when the user has `breezy_heater_enabled = 0`, because frost protection auto-fires the heater. Documented; not a bug.
4. **Per-unit max RPM differs.** The playroom unit tops out around 5340 rpm at 100% manual; the office around 5900. Don't normalize to "% of max" without per-device calibration. The metric is raw RPM.

## Approvals

- Architecture, APIs, CLI shape, ops model: approved by user during brainstorming on 2026-05-03.
- Phase 0 outcome and revised v1 scope: approved by user on 2026-05-03 after vendor-manual reconciliation.

## Next step

Resume the implementation plan at `docs/superpowers/plans/2026-05-03-twinfresh-cli.md`, starting from Task 3 (frame codec). The plan's Task 6 (param registry) now reads `2026-05-03-param-map.md` rather than discovering from a Phase 0 session — that work is complete.
