# SPECIFICATION-daemon

Runtime behavior of `breezyd`, the long-running daemon that owns all UDP
traffic to Vents Twinfresh ERV ("Breezy") units.

Sibling specs: `SPECIFICATION-cli.md` (the `breezy` CLI),
`SPECIFICATION-web.md` (the `/ui/...` namespace, SSE, dashboard),
`SPECIFICATION-hap.md` (the HomeKit bridge accessory model). The wire
protocol lives in `pkg/breezy/frame.go` and
`docs/superpowers/specs/breezy-manual-vendor.pdf`; bytes are out of scope
here.

## Process lifecycle

### Entry and flags

`cmd/breezyd/main.go::main` parses flags, prints `--version` and exits if
requested, then calls `run(context.Background())`. `run` is the testable
form: returns errors instead of calling `os.Exit`.

| Flag | Default | Meaning |
|---|---|---|
| `--config` | `~/.config/breezy/config.toml` | TOML config path |
| `--addr` | (empty; falls through to `[daemon].listen`, then `127.0.0.1:9876`) | Override listen address |
| `--log-level` | `info` | `debug`/`info`/`warn`/`error`; unknown values silently default to `info` |
| `--version` | (false) | Print version metadata and exit |
| `--backend` | `udp` | Device backend: `udp` or `memory` |
| `--seed` | (empty) | JSON snapshot to seed every device when `--backend=memory` |

Validation: `--seed` requires `--backend=memory`; `--backend` accepts only
`udp` or `memory`. Build metadata (`version`, `commit`, `date`) is injected
at link time by goreleaser.

### Config loading and first-run bootstrap

`internal/config.Load(path)` is called first. If the file doesn't exist
(`errors.Is(err, os.ErrNotExist)`), `internal/config.WriteDefault(path)`
writes a starter TOML file (mode 0600; parent dir 0700 if missing; atomic
temp+rename) and `run` returns non-zero with an "edit it and re-run"
message. The bootstrap branch is gated specifically on `os.ErrNotExist`;
all other `Load` errors (bad TOML, world-readable mode, invalid
`discovery`, reserved-name collision) surface unchanged. The daemon does
NOT auto-add discovered devices — the operator must supply passwords.

### Wiring sequence

After config loads, in order: build per-device `*breezy.MemClient`
instances when `--backend=memory` (each seeded from `--seed`; all call
sites for a device share the same instance so handler writes are
immediately visible to poller reads); compute the listen address
(`--addr` > `[daemon].listen` > `127.0.0.1:9876`); build the
`DeviceRegistry` (IPs missing a port get `:4000` appended); if
`discovery == "on-start"`, run a 3-second discovery probe and update
each device's IP (failures log and proceed); construct the `State`
cache, the Prometheus `Metrics` collector, and the `Handler`; compose
`OnPoll` as `handler.SyncHomekit + handler.PushHub.Notify`; spawn one
`Poller` and one `Scheduler` goroutine per device with an IP
(`startPollers`; devices without an IP log a warning and are skipped
until discovery resolves them); if `discovery ==
"periodic:<duration>"`, spawn the periodic discovery goroutine; if
`[homekit].enabled`, start the HAP server; register HTTP routes on a
`net/http.ServeMux` and start `srv.ListenAndServe`.

`handler.Pollers` and `handler.Schedulers` must be populated BEFORE the
goroutines start so the first poll's `OnPoll → PushHub.Notify` always
sees a populated map (without this ordering the race detector fires).

HTTP server timeouts: `ReadHeaderTimeout=5s`, `ReadTimeout=10s`,
`WriteTimeout=30s`, `IdleTimeout=60s`. The `WriteTimeout` interaction
with long-lived SSE is documented in `SPECIFICATION-web.md`.

### Signals and shutdown

`run` blocks on a `select` over parent context cancellation, `SIGINT` /
`SIGTERM`, and the server-error channel. On any: `srv.Shutdown` with a
5s deadline drains in-flight requests; the root context is cancelled so
pollers and schedulers exit; `pollersWg.Wait()` blocks (up to another
5s) for in-flight ticks. If pollers don't exit within the deadline a
warning is logged and the process returns anyway. The synchronous wait
exists because earlier fire-and-forget shutdowns let `main` return
while pollers were still mid-tick, racing the global slog state at
teardown.

Logging: `slog` text handler to `stderr`. No JSON output mode.

## Configuration

`~/.config/breezy/config.toml`. Loader: `internal/config/config.go::Load`.

The loader enforces mode 0600 IFF the file contains any password (either
`[daemon].password` or any `[devices.<name>].password`); password-free
files may be 0644. The device leaks the protocol password back over the
LAN unauthenticated, so encrypted-config wouldn't improve the threat
model — 0600 just keeps other local users from reading it.

```toml
[daemon]
listen        = "127.0.0.1:9876"
poll_interval = "30s"
discovery     = "on-start"          # "on-start" | "off" | "periodic:<go-duration>"
password      = ""                  # optional fleet-wide protocol password

[homekit]
enabled     = false
bridge_name = "breezyd"
port        = 0                     # 0 = ephemeral; otherwise 1024-65535
state_dir   = ""                    # default: $XDG_STATE_HOME/breezyd/homekit

[devices.playroom]
id       = "BREEZY00000000A0"       # MUST be exactly 16 ASCII chars
password = "testpwd"                # inherits [daemon].password if empty
ip       = "192.168.1.148"          # optional; discovery resolves if absent
```

Validation rules: `[devices.<name>].id` must be exactly 16 ASCII chars;
device names matching reserved CLI verbs (`ls`, `discover`, `daemon-url`,
`param`; case-insensitive) are rejected so `breezy <name> ...` is never
ambiguous with the verb form; `discovery` must be `"on-start"`, `"off"`,
or `"periodic:<duration>"` (parsed via `time.ParseDuration`);
`poll_interval` defaults to `30s` if absent; `[homekit].bridge_name` ≤ 32
chars; `port` is 0 or 1024-65535.

`[daemon].listen` is intentionally NOT defaulted by the loader — the
empty-string sentinel lets the CLI distinguish "no daemon configured"
from "daemon at custom address". The daemon applies its own default
after `Load` returns.

A `[devices.<name>]` block with empty `password` inherits
`[daemon].password`. The fleet-wide password is also used for the
discovery probe when set (works around firmware variants that drop
wildcard requests with a password mismatch despite the spec).

### State directory resolution

`cmd/breezyd/main.go::daemonStateDir` precedence: (1) `$STATE_DIRECTORY`
(set by systemd when `StateDirectory=breezyd` is in the unit, the NixOS
module's canonical case; survives `ProtectSystem=strict` because systemd
pre-creates and chowns it); (2) `$XDG_STATE_HOME/breezyd`; (3)
`$HOME/.local/state/breezyd`. Created with mode 0700 if missing. Energy
tracking and the scheduler tolerate a missing state dir (log warning,
run with non-persistent state).

## Device backend abstraction

The `breezy.DeviceClient` interface (`pkg/breezy/ops.go`) is the seam:

```go
type DeviceClient interface {
    ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error)
    WriteParams(ctx context.Context, writes []ParamWrite) error
    IsLocal() bool
}
```

| Implementation | When used | `IsLocal()` |
|---|---|---|
| `*breezy.Client` | `--backend=udp` (default). UDP/4000, one per device, traffic serialised internally. | `false` |
| `*breezy.MemClient` | `--backend=memory`. In-process `map[ParamID][]byte`. Reads/writes return instantly. | `true` |

`IsLocal()` lets callers gate UDP-protocol-specific behavior (the
fan-settle window) without case-analysis on the concrete type. Two
factories live in `cmd/breezyd/main.go`. `makeClientFactory` (used by
HTTP handlers) returns the per-device `MemClient` when one exists,
otherwise dials a fresh `*breezy.Client`. Always consults the
`DeviceRegistry` so periodic-discovery IP updates take effect on the
next request without bouncing connections. `Poller.NewClient` is set to
the same `MemClient` for memory-backed devices; in UDP mode the poller
dials its own `*breezy.Client` per tick.

**Local UI development:**
`breezyd --backend=memory --seed pkg/breezy/fakedevice/snapshot_148.json`
runs the dashboard against canned data with no UDP, fakedevice, or
hardware. Each `[devices.<name>]` block still requires `id` and an `ip`
(any value, e.g. `127.0.0.1:0`); `ip` is ignored in memory mode but
required by config validation.

## Polling

One `Poller` goroutine per configured device with an IP
(`cmd/breezyd/poller.go`). The poller is the only writer of the
per-device `Snapshot` in the `State` cache. `Poller.Run` ticks
immediately on start (callers see fresh data without waiting one
interval), then every `[daemon].poll_interval` (default 30s).

`Poller.tick` (per-tick work): (1) compute `idsForThisTick` — the full
`ReadIDs` set, minus `fanSensitiveReads` if the fan-settle window is
active; (2) acquire the per-device UDP mutex (`Poller.LockUDP`), held
for the entire tick so concurrent HTTP handler writes can't interleave
at the UDP layer; (3) dial a `PollerClient` — UDP-mode dials a fresh
`*breezy.Client`, memory-mode returns the shared `*breezy.MemClient`;
(4) read the IDs in batches of `pollBatchSize = 30` (bounds packet size
under the 256-byte FDFD/02 limit); per-batch errors are logged and
counted but don't abort the tick; (5) **failed-poll cache semantics** —
if a batch fails or the dial fails, reuse the prior `Snapshot`'s
`Values` AND `LastPoll` so the dashboard renders "stale" with
last-known data instead of dropping to "unreachable", and so
`LastPoll` reflects the most recent *successful* poll (which is what
the 3×poll-interval stale gate and the `breezyd_last_poll_timestamp`
Prometheus alert pattern require). Matters most for in-process
backends where forced timeouts return instantly; real-UDP timeouts
are slow enough that this branch rarely fires in production;
(6) record the `Snapshot` (success-or-failure) into `State`; (7) on
full success, call `Energy.Tick(values, now)` and the `OnPoll`
callback.

`defaultReadIDs` returns ~40 parameter IDs sorted by ID to minimise
FDFD/02 page-switch markers per packet, covering every metric the JSON
snapshot, dashboard, and `/metrics` need plus firmware/efficiency/faults
for cache-driven endpoints. `classifyErr` maps poll errors to a small
label set used by `breezy_poll_errors_total`: `checksum`, `auth`,
`timeout`, `other`. Order matters: `breezy.ErrAuth` outranks generic
net-level errors so a wrong-password device is reported as `auth`.

### Per-device UDP serialisation

Concurrent UDP request/response with checksums is unsafe to fan out
from multiple goroutines or processes — overlapping retries and packet
collisions cause silent corruption. The daemon serialises per device:
`*breezy.Client` holds an internal `sync.Mutex` around every
request/response, and `Poller.udpMu` (acquired by `Poller.LockUDP`)
serialises the entire tick (dial → read batches → close) against any
HTTP handler writing to the same device. `Handler.lockDevice` acquires
it on every device-write request; the `Scheduler` participates by being
passed `Poller.LockUDP` as its `LockUDP` closure. Without this
discipline a poll and a write could interleave at the UDP packet level
(separate Client instances, independent sockets) and the poll's
response could overwrite a just-written cache value with the device's
pre-write reading.

### Fan-settle window

After a write to any of `fanWriteIDs` — `0x0002` (speed_mode), `0x0007`
(timer; night/turbo ramps), `0x0044` (speed_manual_pct), `0x00B7`
(fan_rotation_direction), or any of the preset supply/extract pairs
`0x003A`–`0x003F` (editing the active preset ramps the running fan
immediately) — the unit takes 10–15s before the live params reflect the
new state. The poller suppresses reads of `fanSensitiveReads`
(`0x004A`, `0x004B`, `0x0084`) for `fanSettleDuration = 12s` after such
a write.

Suppression is gated on `!p.lastClient.IsLocal()` — the most recent
successfully-dialed client decides. `*breezy.MemClient` writes land
instantly so memory-backed devices skip it. UDP-path
coverage: `poller_test.go::TestPoller_FanSettle_DropsSensitiveReads_OverUDP`.
`Poller.NoticeWrite(id)` is the entry point: HTTP handlers reach it via
`recordingClient → Handler.recordWrite → Handler.notice`; the Scheduler
reaches it the same way. Writes to non-fan-affecting params are no-ops.

**Do not shorten the 12s window.** The protocol genuinely lies during
that interval.

## Discovery

`pkg/breezy.Discover` (UDP broadcast) runs in two cases: **`on-start`** —
one wildcard probe with a 3-second timeout before the pollers start
(failures log and proceed); and **`periodic:<duration>`** — a goroutine
ticks at the configured cadence for the daemon's lifetime, refreshing
IPs.

Discovery uses the literal device ID `DEFAULT_DEVICEID` so the device
returns its true ID and unit type regardless of password. The probe
password is `[daemon].password` if set, else the factory default
`"1111"` (`breezy.DefaultDiscoveryPassword`). When `[daemon].password` is
non-empty AND non-default, `breezy.DiscoverWithPassword` is used in place
of `Discover` — some firmware silently drops wildcard requests with a
password mismatch despite the spec.

For each `Found`: matching `ID` → `DeviceRegistry.UpdateIP` to
`<IP>:4000`; unknown ID → log once at INFO with a "add a `[devices.NAME]`
block to control it" hint.

## JSON HTTP API (`/v1/...`)

Default bind is `127.0.0.1:9876` (loopback only; no auth). All routes
return JSON.

### Error envelope

```json
{"error": "<human readable>", "code": "<stable_code>"}
```

| Code | Status | When |
|---|---|---|
| `not_found` | 404 | Unknown device, unsupported param ID, missing cache |
| `bad_request` | 400 | Malformed body, bad enum value, `breezy.ErrInvalidArg` |
| `read_only` | 403 | Write to a registry-tagged read-only param |
| `device_unreachable` | 502 | UDP timeout, checksum mismatch, dial failure |
| `auth_failed` | 502 | `breezy.ErrAuth` (function code 0x07) |
| `internal` | 500 | Anything else |

`cmd/breezyd/server.go::classifyClientErr` is the single mapping point.

### Routes — device reads

| Method | Path | Source | Notes |
|---|---|---|---|
| `GET` | `/v1/devices` | cache | Sorted one-line summary per configured device |
| `GET` | `/v1/devices/{name}` | cache | Full structured snapshot (`breezy.BuildStatusWithEnergy`) plus `service.schedule` and `service.energy` |
| `GET` | `/v1/devices/{name}/firmware` | cache | Decodes 0x0086 |
| `GET` | `/v1/devices/{name}/efficiency` | cache | Decodes 0x0129 |
| `GET` | `/v1/devices/{name}/faults` | cache | Decodes 0x007F as a list of `{code, kind}` pairs |
| `GET` | `/v1/devices/{name}/params/{id}` | **fresh UDP** | Bypasses cache; for debugging |
| `GET` | `/v1/devices/{name}/schedule` | scheduler | In-memory schedule + `last_apply` |

`{id}` parsing in `parseParamID` is **hex-by-default**: `"0x0044"`,
`"0044"`, `"44"`, and `"B7"` are all hex. `"10"` is hex 0x10 (=16), not
decimal ten. The CLI almost always uses named params via the registry,
so the ambiguity rarely bites in practice.

The aggregate JSON shape for `GET /v1/devices/{name}` is documented in
`docs/superpowers/specs/2026-05-03-twinfresh-cli-design.md`;
`service.schedule` and `service.energy` blocks are added by the daemon
glue (`handlers_device.go::getDevice`).

### Routes — device writes

All hit UDP and update the cache on success.

| Method | Path | Body | Operation |
|---|---|---|---|
| `POST` | `/v1/devices/{name}/power` | `{"on": bool}` | `breezy.Power` |
| `POST` | `/v1/devices/{name}/speed` | `{"preset":1-3}` XOR `{"manual":10-100}` | `breezy.SetSpeedPreset` / `breezy.SetSpeedManual` |
| `POST` | `/v1/devices/{name}/preset` | `{"preset":1-3,"supply":10-100,"extract":10-100}` | `breezy.SetPresetSpeed` |
| `POST` | `/v1/devices/{name}/mode` | `{"mode":"ventilation\|regeneration\|supply\|extract"}` | `breezy.SetMode` |
| `POST` | `/v1/devices/{name}/heater` | `{"on": bool}` | `breezy.SetHeater` |
| `POST` | `/v1/devices/{name}/timer` | `{"mode":"off\|night\|turbo"}` | `breezy.SetTimer` |
| `POST` | `/v1/devices/{name}/threshold` | `{"kind":"humidity\|co2\|voc","value":?,"enabled":?}` | `breezy.SetThresholdConfig` (at least one of value/enabled) |
| `POST` | `/v1/devices/{name}/filter/reset` | (empty) | `breezy.ResetFilter` |
| `POST` | `/v1/devices/{name}/faults/reset` | (empty) | `breezy.ResetFaults` |
| `POST` | `/v1/devices/{name}/rtc` | `{"time":"RFC3339"}` | `breezy.SetRTC` (writes 0x6F + 0x70) |
| `POST` | `/v1/devices/{name}/params/{id}` | `{"hex":"<hex>"}` | Raw write; read-only enforced in `breezy.WriteParams` |
| `PUT` | `/v1/devices/{name}/schedule` | `{"enabled":bool,"entries":[…]}` | Replaces schedule wholesale (no UDP write) |

Each device write goes through `Handler.dialRecording`, which wraps the
underlying client in a `recordingClient`. On a successful write the
recorder fires `Handler.recordWrite`, which calls `State.WriteThrough`
to update the cache and `Poller.NoticeWrite(id)` for each written ID
(arming the fan-settle window when relevant). A single 5-second timeout
(`handlerOpTimeout`) bounds the entire dial-plus-op for every
device-write handler — this is not the 12s fan-settle window, which is
a post-write read-suppression policy, not a deadline on the write.

### Routes — misc

`GET /healthz` returns `{"ok": true}`. `GET /metrics` is the Prometheus
exposition. `GET /{$}` serves the page shell — the `{$}` anchor is
load-bearing because a plain `GET /` would catch every unmatched URL and
turn API typos into HTML responses. The `/ui/...` namespace is
documented in `SPECIFICATION-web.md`.

### Cache vs. passthrough

| Source | Routes |
|---|---|
| Cache (cheap, no UDP) | `GET /v1/devices`, `GET /v1/devices/{name}`, `GET /v1/devices/{name}/{firmware,efficiency,faults}`, `GET /metrics`, `GET /v1/devices/{name}/schedule` |
| Fresh UDP | `GET /v1/devices/{name}/params/{id}` |
| UDP write + cache write-through | every `POST` device route. `PUT /v1/devices/{name}/schedule` updates the persisted schedule, not the device. |

## Prometheus `/metrics`

`cmd/breezyd/metrics.go::Metrics` owns one hermetic `prometheus.Registry`
populated lazily before each scrape (`metricsHandler` walks the cache
and the per-device energy trackers, calling `Update` and `SetEnergy`
once per device). No Go runtime metrics, no collectors leaking in from
imported libraries.

Per-device gauges are labelled `device="<name>", id="<16-byte ID>"`
unless noted otherwise. Missing params are silently skipped — gauges
retain their last-set value rather than reset to zero. Stale values are
signalled exclusively by `breezy_last_poll_timestamp` and `breezy_up`,
which Prometheus operators are accustomed to alerting on.

**Configured/state:** `breezy_power`, `breezy_airflow_mode`,
`breezy_speed_mode`, `breezy_speed_manual_pct`, `breezy_heater_enabled`,
`breezy_humidity_threshold_pct`, `breezy_co2_threshold_ppm`,
`breezy_voc_threshold_index`, `breezy_{humidity,co2,voc}_sensor_enabled`,
`breezy_filter_timeout_days`.

**Live:** `breezy_fan_rpm{fan="supply|extract"}`,
`breezy_heater_running`, `breezy_in_user_control`, `breezy_special_mode`,
`breezy_special_mode_remaining_seconds`,
`breezy_sensor_alert{sensor="humidity|co2|voc"}`,
`breezy_recovery_efficiency_pct`, `breezy_frost_protection_active`.
`breezy_in_user_control` is `0` whenever a sensor alert is active or a
special timer is running — i.e. live fan RPMs may diverge from the
user's configured setting. `breezy_heater_running` may be `1` even when
`breezy_heater_enabled = 0` because frost protection auto-fires the
heater.

**Sensors:** `breezy_humidity_percent`, `breezy_eco2_ppm` (eCO2 —
computed from the VOC sensor, not a true NDIR reading; the metric name
reflects this), `breezy_voc_index`,
`breezy_temperature_celsius{position="outdoor|supply|exhaust_inlet|exhaust_outlet"}`.
Temperature sentinels (`±32767`, "not measured") are silently skipped.

**Service / health:** `breezy_filter_status`,
`breezy_filter_remaining_seconds`, `breezy_motor_lifetime_seconds`,
`breezy_rtc_battery_volts`, `breezy_fault_level`.

**Daemon health:** `breezy_last_poll_timestamp` (unix seconds),
`breezy_poll_errors_total{kind="checksum|auth|timeout|other"}` (counter),
`breezy_up`, `breezy_info` (constant-1 gauge with `firmware` and
`build_date` labels).

**Energy** — eight gauges labelled by `device` only (no `id`, since
energy is a daemon-side construct, not a per-firmware-version series):
`breezyd_energy_recovered_watts` (signed: positive=heating,
negative=cooling), `breezyd_energy_consumed_watts` (fan electric draw,
magnitude), and
`breezyd_energy_{heating,cooling,consumed}_{today,month,lifetime}_kwh`.
When `EnergyTracker.Error` is non-empty (unsupported UnitType — no
calibration data), every previously-emitted sample for that device is
dropped via `DeleteLabelValues` so `/metrics` doesn't expose phantom
zeros.

## Energy tracking

Always on. One `EnergyTracker` per device
(`cmd/breezyd/energy_tracker.go`), state persisted at
`<state_dir>/energy_<device>.json`. State survives daemon restart;
lifetime counters carry over; today/month counters reset at local
midnight / first-of-month.

`EnergyTracker.Tick(values, now)` runs after every successful poll.
Sequence: (1) date/month rollover zeros today/month counters when
crossed; (2) first-tick priming — when `LastTick` is zero, set it and
return without accumulating (the first tick after `Load` mustn't claim
the prior daemon run's elapsed wall time); (3) dt is capped at `dtCap =
300s` so a long pause (network out, sleep/resume) can't produce a
runaway jump and negative dt is clamped to zero; (4) UnitType lookup
(`0x00B9`) — missing or no calibration → set the error string on the
tracker, zero instantaneous gauges, skip accumulation; (5) regen-only
gate (`0x00B7`) — accumulates only when `mode == 1` (regeneration), the
only mode where the heat exchanger is actually working; other modes
(manual, supply-only, extract-only, ventilation) zero the instantaneous
gauges and skip accumulation; (6) inputs: supply + extract pct
(`CommandedFanPct`), supply temp (`0x0020`), outdoor temp (`0x001F`);
(7) math — recovered W uses average pct as airflow proxy, consumed W is
the per-fan sum; accumulate `|W| × dt / 3.6e6` into the right
today/month/lifetime counter (heating if Δ>0, cooling if Δ<0); consumed
is always accumulated; (8) persist via atomic temp+rename (mode 0600).

The model-curve calibration table is `pkg/breezy/energy.go::modelCurves`;
adding a new model is a one-line table edit. Devices whose UnitType
isn't in the table surface their error string in `service.energy.error`
on the dashboard, and the eight `breezyd_energy_*` gauges drop from
`/metrics` for that device. Malformed state file at `Load` → starts
fresh + `slog.Warn` (a hand-edited file can never corrupt the running
daemon).

## Scheduler

One `Scheduler` per device (`cmd/breezyd/scheduler.go`), started by
`startPollers` next to the `EnergyTracker`. Cancelled by the same root
context. Fires `Power`/`SetMode`/`SetSpeedManual` writes at
user-configured At-times each day; the schedule loops every 24 hours.

State at `<state_dir>/schedule_<device>.json`. Editing happens
exclusively through the web UI's `GET`/`PUT /v1/devices/{name}/schedule`
endpoints — no CLI verbs and no HomeKit exposure.

### On-disk shape

The persisted file holds `version`, `enabled` (bool), `entries` (array of
`{at:"HH:MM", action, pct}`), and `last_apply` (the most recent fire
attempt: `at`, `fired`, `ok`, `err`, `retries`). `action` ∈
`{off, regeneration, ventilation, supply, extract}`. `off` issues
`Power(false)` only; the rest issue `Power(true) → SetMode →
SetSpeedManual(pct)`. `pct` is required even when `action=off` (UI greys
the field; the value is ignored at the wire).

Validation rules (`Scheduler.validate`): `entries` length ≤ 24; `at` is
`HH:MM` with hours 0–23, minutes 0–59; no two entries share `at`;
`pct ≤ 100` always; `pct ≥ 10` for non-off actions; unknown `action`
rejects the file.

`PUT` body is the same shape minus `last_apply`. Validation failures
return 400 `bad_request`. On success the in-memory state is replaced,
`last_apply` is cleared (a fresh schedule starts fresh), and the file
is written atomically (temp+rename, mode 0600). The next minute tick
picks up the new schedule.

### Tick loop and window detection

`Scheduler.Run` aligns to the next `:00` second so subsequent ticks land
within a few hundred ms of `HH:MM:00`, then drives `Scheduler.tick` once
a minute. `tick` is the test seam — package-private and called directly
with synthetic times.

Each tick evaluates the half-open window `(lastTick, nowMinute]` for
matches. Midnight wraparound (`nowMinute < lastTick`) becomes the union
`(lastTick, 1440) ∪ [0, nowMinute]`. If multiple entries match (daemon
paused for >1 minute crossed several At-times), only the **latest**
fires; earlier ones are stale.

### Event-driven, not state-driven

On daemon startup, schedule edit, or schedule re-enable the daemon does
NOT immediately apply the entry-in-effect; only future transitions fire.
The first tick after `Run` starts records `lastTick = nowMinute` via the
`haveLastTick` sentinel and fires nothing. The sentinel exists because
minute-of-day 0 (00:00) is itself a valid value, so a plain `lastTick ==
0` check would mis-fire a midnight startup. Manual overrides between
transitions are permitted — UI/HomeKit/CLI changes are not re-asserted
by the scheduler until the next entry's At-time arrives. Disabling the
schedule does not touch the unit; it only stops future entries firing.

### Retry policy

On a transient fire failure (timeout, checksum, dial error) a retry is
installed: `nextAttempt = now + 30s` (`retryCadence`),
`deadline = now + 10m` (`retryDeadline`). Subsequent ticks attempt the
retry when `now ≥ nextAttempt`, bumping `attempts`. Transitions:
**succeed** → clear retry, `lastApply.ok = true`; **deadline reached** →
clear retry, `lastApply.ok` stays false (UI keeps the alert);
**superseded by next entry** → if a newer entry's `At` lies in the
current tick window, drop the in-flight retry and fire the newer entry
instead; **schedule disabled** or **edited** → retry cleared.

`breezy.ErrAuth` is treated as a config error: log once at WARN, set
`lastApply.err = "auth_failed: ..."`, do NOT install a retry.

The schedule appears under `service.schedule` on
`GET /v1/devices/{name}`, mirroring `service.energy`. The response
shape adds a derived `alert` bool (`last_apply != nil &&
!last_apply.ok`) so the UI doesn't have to descend into `last_apply` to
decide whether to force-expand the SCHEDULE block.

**Known limitation: DST.** Times are wall-clock local. Spring-forward
skips an entry whose `at` lies in the missing hour; fall-back fires an
entry in the repeated hour twice. Acceptable for residential ERV control
given the surrounding ±1-minute tick precision.

## Push hub (poll → SSE fan-out)

`cmd/breezyd/push_hub.go::PushHub` is the in-memory subscriber registry
that backs the dashboard's SSE stream. Producers (the poller's `OnTick`
hook — every tick, success or failure — and action handlers'
post-write paths) call `PushHub.Notify(name, snap)`. The hub
renders a structured `PushEvent` (one signal payload + a list of block
patches) via the closure injected at construction time, then queues it
onto every subscriber's bounded channel (`pushHubBufferSize = 16`).
Backpressure: when a subscriber is too slow to drain, the oldest event
is discarded — pushed events are full-card snapshots, so the latest
supersedes prior ones and a dropped event is never user-visible.

Subscriber lifecycle, the `/ui/sse` long-lived response, and the
`datastar-patch-elements` / `datastar-patch-signals` event semantics are
documented in `SPECIFICATION-web.md`.

## Build-tag admin surface (`breezyd_test_admin`)

`cmd/breezyd/handlers_test_admin.go` registers a `/test/...` namespace
ONLY when the `breezyd_test_admin` build tag is set. Production binaries
do not include this tag and the routes return 404.
`cmd/breezyd/handlers_test_admin_off.go` provides an unconditional no-op
`mountTestAdmin` so the call site in `Handler.mux()` always compiles.

| Method | Path | Body | Effect |
|---|---|---|---|
| `POST` | `/test/devices/{name}/params/{id}` | `{"value":"<hex>"}` | `MemClient.SetParamValue` |
| `POST` | `/test/devices/{name}/inject-error` | `{"kind":"auth\|timeout\|none"}` | Arms `MemClient.SetAuthFailureMode` / `SetTimeoutMode` |
| `POST` | `/test/devices/{name}/reset` | (empty) | Clears injected faults; restores seed params |

All endpoints type-assert the device's client to `*breezy.MemClient`. The
assertion fails (returns 400) when `--backend=udp` is in use — the surface
only makes sense against the in-process backend. Consumed by the
Playwright suite (`tests/ui/`), which spawns one breezyd built with
`-tags breezyd_test_admin --backend=memory --seed
pkg/breezy/fakedevice/snapshot_148.json`.

## Protocol invariants the daemon depends on

The daemon assumes the following behaviors of `pkg/breezy`:

- **Per-client serialisation.** Every UDP request/response is serialised
  by an internal `sync.Mutex` in `*breezy.Client`. The daemon layers a
  per-device `Poller.udpMu` on top so polls and handler writes can't
  interleave even though they may use distinct client instances.
- **Single poller goroutine per device.** Concurrent UDP request/response
  with checksums isn't safe to fan out — overlapping retries and packet
  collisions cause silent corruption. This is why running multiple
  `breezyd` processes against the same device set is unsafe; the
  standalone CLI serialises through one process for the same reason.
- **`ErrAuth` typed.** Wrong-password responses arrive as undocumented
  function code `0x07`. `pkg/breezy/frame.go::DecodeResponse` surfaces
  these as `breezy.ErrAuth`. The daemon maps that onto `auth_failed` in
  HTTP responses, `auth` in poll-error metrics, and a no-retry config
  error in the scheduler.
- **Discovery is unauthenticated.** A request with the literal device ID
  `DEFAULT_DEVICEID` returns the unit's true ID and unit type regardless
  of password — the daemon's `on-start` and `periodic` discovery rely on
  this.
- **Multi-byte and high-page framing.** The library handles `FE <size>`
  and `FF <hi>` markers transparently; the daemon doesn't reach into
  wire bytes. `pollBatchSize = 30` keeps total per-packet framing under
  the 256-byte protocol limit.

## Out of scope

The daemon deliberately does not provide: TLS or auth on the HTTP API
(loopback bind is the v1 boundary); persisted Snapshot cache across
daemon restarts (the cache is in-memory; energy and schedule state DO
persist separately); WiFi reconfiguration writes (a wrong write
disconnects the device from the network and forces a factory-AP-mode
reset; the vendor's app handles this); MQTT bridge or Home Assistant
native integration (the state cache is shaped so a bridge could be
added without rewriting the core); schedule day-of-week or
calendar-date support (24-hour loop, full stop); auto-add of
discovered-but-unconfigured devices (the operator must supply passwords
explicitly).
