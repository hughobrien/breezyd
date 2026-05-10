# SPECIFICATION-hap.md

The HomeKit bridge exposes each configured Breezy ERV unit as a HomeKit
accessory inside the `breezyd` daemon process. It is opt-in, runs in-process
using the brutella/hap library, and reuses the daemon's poller cache for
reads and the daemon's `dialRecording` write path for writes — every
protocol invariant the HTTP API relies on holds for HomeKit writes too.

This document specifies what the bridge exposes, how each HomeKit
characteristic maps to a Breezy parameter, and what an operator needs to
deploy and pair it. Daemon process lifecycle, polling internals, and the
wire protocol live in their respective specs:

- Daemon process lifecycle, poll interval, cache semantics, energy/
  schedule subsystems → `SPECIFICATION-daemon.md`
- Web dashboard / SSE / `/ui` action handlers → `SPECIFICATION-web.md`
- `breezy` CLI surface → `SPECIFICATION-cli.md`
- Wire-level frame format, parameter encoding → `pkg/breezy/frame.go` and
  the vendor manual at `docs/superpowers/specs/breezy-manual-vendor.pdf`

## Opt-in

The bridge is disabled by default and only starts when `[homekit].enabled
= true` is set in the daemon's TOML config. With the flag absent or false,
`StartHomekit` returns a no-op stop function, no HAP server is started,
and `SyncHomekit` is a no-op for every poll tick (`cmd/breezyd/homekit.go::StartHomekit`,
`::SyncHomekit`).

Configuration block (parsed by `internal/config/config.go::Homekit`):

```toml
[homekit]
enabled     = true
bridge_name = "breezyd"          # default "breezyd"; max 32 chars
port        = 0                  # default 0 (ephemeral); else 1024..65535
state_dir   = ""                 # default $XDG_STATE_HOME/breezyd/homekit
```

Validation at config load: `bridge_name` falls back to `"breezyd"` when
blank and is rejected if longer than 32 characters; `port` must be `0`
or in `1024..65535` (`0` = OS-assigned ephemeral, advertised via mDNS);
`state_dir` is tilde- and `$XDG_STATE_HOME`-expanded.

## Setup

### State directory resolution

The HomeKit state directory holds the brutella/hap pairing database and
the bridge's PIN. Resolution order at startup (`StartHomekit`,
`internal/config/config.go::expandStateDir`):

1. The literal `state_dir` from TOML (after `~` expansion)
2. `$XDG_STATE_HOME/breezyd/homekit` if `XDG_STATE_HOME` is set
3. `$HOME/.local/state/breezyd/homekit` otherwise

Under the NixOS module, `state_dir` is pinned to
`/var/lib/breezyd/homekit`, which sits inside the systemd unit's
`StateDirectory=breezyd`. systemd creates and owns the parent path; the
daemon `MkdirAll`s the leaf directory at boot with mode `0700`.

The directory is the unit of HomeKit identity: deleting it factory-resets
pairing (the bridge appears as a fresh, unpaired accessory on next
start).

### PIN handling

PIN file: `<state_dir>/pin.txt`, mode `0600`, contents are exactly eight
ASCII digits (`cmd/breezyd/homekit.go::loadOrGeneratePin`).

On every startup the daemon reuses the existing PIN if it parses cleanly
and is not on the brutella/hap weak-PIN list (`00000000`, `11111111`,
…, `12345678`, `87654321`); otherwise it generates a random eight-digit
PIN and atomically writes it back. The PIN is logged once at INFO at
startup, formatted as `XXXX-XXXX` (cosmetic; iOS accepts both forms):

```
HomeKit PIN: 1234-5678
```

There is no "display PIN on demand" CLI verb — `pin.txt` is the source
of truth. Deleting the file forces a regeneration on next start (and
typically requires re-pairing).

## Accessory model

The bridge advertises a single HomeKit *bridge accessory* with one
*child accessory* per configured device. Bridge identity:

- Name: `cfg.BridgeName` (default `"breezyd"`)
- Manufacturer: `"Vents"`
- Model: `"breezyd"`

Per-device accessory identity (one per Breezy):

- Name: the device's config-block name
- SerialNumber: the 16-char Breezy device ID
- Manufacturer: `"Vents"`
- Model: `"Twinfresh Breezy 160"` (fixed; the daemon supports only one
  Twinfresh family today)

Bridge construction lives in `cmd/breezyd/homekit.go::StartHomekit`;
per-device accessory construction is in
`pkg/homekit/accessory.go::NewBreezyAccessory`.

### Accessory ID stability

Device names are sorted lexicographically before being added to the
bridge so brutella/hap's sequential aid assignment is stable across
daemon restarts. iOS Home caches the `(aid → tile)` mapping locally,
so without the sort a restart would swap which tile drives which unit.
Adding a device alphabetically *after* existing devices shifts no aids;
adding one alphabetically before renumbers subsequent devices and may
require re-adding the affected tiles in iOS Home.

### Per-device services

Each child accessory runs as `accessory.TypeAirPurifier` and exposes
the following services (all `pkg/homekit/accessory.go::NewBreezyAccessory`):

| Service                 | Purpose                                                | Notes                                                         |
| ----------------------- | ------------------------------------------------------ | ------------------------------------------------------------- |
| AccessoryInformation    | Identity (Name, SerialNumber, Manufacturer, Model)     | SerialNumber is the 16-char Breezy device ID                  |
| AirPurifier             | Power, current/target state, rotation speed, fault     | Primary tile in iOS Home                                      |
| Switch (Supply Only)    | Sets airflow_mode = `supply`                           | Mutually exclusive with Extract Only                          |
| Switch (Extract Only)   | Sets airflow_mode = `extract`                          | Mutually exclusive with Supply Only                           |
| Switch (Heater)         | Toggles auxiliary reheater                             | Device-side firmware may activate the reheater autonomously for frost protection regardless of this switch |
| Switch (Night)          | Activates "night" special-mode timer                   | Mutually exclusive with Turbo                                 |
| Switch (Turbo)          | Activates "turbo" special-mode timer                   | Mutually exclusive with Night                                 |
| HumiditySensor          | Live %RH                                               |                                                               |
| CarbonDioxideSensor     | eCO₂ ppm + threshold-triggered detected flag           |                                                               |
| AirQualitySensor        | VOC index → 5-level enum + raw VOCDensity              | Mapping in `pkg/homekit/airquality.go::AirQuality`            |
| TemperatureSensor (×4)  | Outdoor, Supply, Exhaust In, Exhaust Out               | Range expanded to [-40, 85]°C; sentinels skipped              |
| FilterMaintenance       | FilterChangeIndication, FilterLifeLevel, ResetFilter   | Native iOS "change filter" UI                                 |
| BatteryService          | RTC coin-cell voltage as percentage + low-battery flag | `ChargingState` is hardcoded `2` (not chargeable)             |

Each non-AccessoryInformation service has explicit `Name` and
`ConfiguredName` characteristics so iOS Home distinguishes services of
the same type (five Switches, four TemperatureSensors). `ConfiguredName`
is user-editable in the Home app; `Name` is the spec-mandated fallback
label.

### Display name

The HAP-visible name title-cases the config-key device name —
underscores and hyphens become spaces, the first letter of each word
is uppercased (`pkg/homekit/accessory.go::titleCaseName`). So
`[devices.guest_room]` renders as `Guest Room` in iOS while remaining
`guest_room` in metric labels, log lines, and CLI output.

### Lifecycle

In `cmd/breezyd/main.go::run`: devices load and validate, pollers start,
then `StartHomekit` runs against the already-validated device set;
`defer homekitStop()` cancels the HAP server context and waits for
`ListenAndServe` to return on graceful shutdown. Devices that fail
config validation never reach `StartHomekit`. Devices that pass
validation but are unreachable still get an accessory; characteristics
hold HAP defaults until the first successful poll.

## Read path

Characteristic reads are driven entirely by the daemon's poller cache —
HomeKit never triggers a UDP read directly. After every poll tick, the
poller's `OnPoll` callback fans out to two consumers in sequence
(`cmd/breezyd/main.go::run`):

```go
onPoll := func(name string, snap Snapshot) {
    handler.SyncHomekit(name, snap)
    handler.PushHub.Notify(name, snap)
}
```

`SyncHomekit` translates the snapshot into a `breezy.Status` via
`breezy.BuildStatus` and calls `homekit.Sync` (`pkg/homekit/sync.go`),
which writes every characteristic whose source field is present in the
status. Missing fields leave the prior characteristic value unchanged —
no `SetValue` call is issued, so iOS continues to show the last-known
reading rather than zeroing out. Temperature sentinels (`|v| ≥ 1000.0`)
are similarly skipped, so a disconnected probe never flips iOS to a
fake reading.

Read freshness is bounded by the daemon's poll interval (default 30s;
see daemon spec). An unreachable device retains its cached values in
iOS as long as the daemon's failed-poll cache keeps them. Per-fan RPM
gates `CurrentAirPurifierState`: 0 when power is off, 2 (Purifying)
when either fan reports `> 0` RPM, 1 (Idle) when powered with both
fans at zero (e.g. mid-fan-settle).

## Write path

Every HomeKit characteristic write is registered via
`OnValueRemoteUpdate` (`cmd/breezyd/homekit.go::registerWriteCallbacks`)
and routed through `Handler.doDeviceOpBackground`, which opens a
`recordingClient` via `dialRecording` — the same wrapper the HTTP
handlers use (`cmd/breezyd/server.go::dialRecording`). That wrapper:

1. Acquires the per-device UDP mutex.
2. Caps the op at `handlerOpTimeout` (5s) via a derived context.
3. Records every successful `WriteParams` call back into the daemon's
   cache so the next poll-driven push reflects the new state.
4. Releases the lock and closes the socket on return (LIFO defer).

Because writes share the cache + lock with every other write path,
every protocol invariant the HTTP API holds applies identically to
HomeKit writes:

- Per-device serialisation: two HomeKit writes against the same device
  cannot interleave on the wire.
- Fan-settle window: writes to `0x0002`, `0x0044`, or `0x00B7`
  trigger a 12-second suppression of `0x004A`/`0x004B`/`0x0084` reads
  on the poller (UDP backend only — see daemon spec). HomeKit reads
  during that window keep returning the pre-write cached values, which
  is correct: the firmware is reporting stale data on the wire.
- Validation: ops that reject input (`SetSpeedManual` clamping `pct ∈
  [10, 100]`, `SetMode` rejecting unknown modes, etc.) return
  `breezy.ErrInvalidArg`; the callback logs the error at ERROR level
  and the iOS slider snaps back to its prior value.
- Auth handling: `breezy.ErrAuth` is returned through the same path
  and logged; iOS sees the write fail.

Errors from any callback are logged at `slog.Error` with the device
name and a short tag; they never panic. A single device's write
failure does not affect the bridge or other devices.

### Write-side mutual exclusion

Some HomeKit characteristics map to mutually-exclusive device states.
The callbacks enforce mutual exclusion both on the device (via the
write op) and in the local accessory state, so iOS sees the correct
switch state immediately:

- **Supply Only / Extract Only**: turning on one switch forces the
  other off and writes `airflow_mode = supply` or `extract`. Turning
  the active switch off writes `airflow_mode = regeneration`. See
  `homekit.go::switchAirflow`.
- **Night / Turbo**: turning on one switch forces the other off and
  writes the matching `0x0007` special-mode value. Turning the active
  switch off writes `mode = off`. See `homekit.go::switchTimer`.
- **TargetAirPurifierState**: writing `1` (Auto) calls
  `SetSpeedPreset(1)`; writing `0` (Manual) is a no-op — the
  RotationSpeed callback drives manual speed instead.

### Per-write parameter table

| HomeKit write                                       | breezy op                  | Param ID(s)            |
| --------------------------------------------------- | -------------------------- | ---------------------- |
| AirPurifier.Active (0/1)                            | `breezy.Power`             | `0x0001`               |
| AirPurifier.TargetAirPurifierState = 1 (Auto)       | `breezy.SetSpeedPreset(1)` | `0x0002`               |
| AirPurifier.RotationSpeed (10..100, clamped)        | `breezy.SetSpeedManual`    | `0x0044`, then `0x0002 = 0xFF` |
| Switch[Supply Only].On / Switch[Extract Only].On    | `breezy.SetMode`           | `0x00B7`               |
| Switch[Heater].On                                   | `breezy.SetHeater`         | `0x0068`               |
| Switch[Night].On / Switch[Turbo].On                 | `breezy.SetTimer`          | `0x0007`               |
| FilterMaintenance.ResetFilterIndication             | `breezy.ResetFilter`       | `0x0065`               |

## Mapping table (read path)

`pkg/homekit/sync.go::Sync` writes the following characteristics from
`breezy.Status` fields. The status fields themselves come from the
parameter IDs listed in the rightmost column (decoded by
`pkg/breezy/status.go::BuildStatus`).

| HomeKit characteristic                       | Status field                    | Source param ID(s)         | Notes                                                                 |
| -------------------------------------------- | ------------------------------- | -------------------------- | --------------------------------------------------------------------- |
| AirPurifier.Active                           | `configured.power`              | `0x0001`                   | 0 = inactive, 1 = active                                              |
| AirPurifier.CurrentAirPurifierState          | `configured.power` + RPMs       | `0x0001`, `0x004A`, `0x004B` | 0 inactive, 1 idle (powered, RPM=0), 2 purifying (any RPM > 0)      |
| AirPurifier.TargetAirPurifierState           | `configured.speed_mode`         | `0x0002`                   | `manual` → 0; otherwise 1 (Auto)                                      |
| AirPurifier.RotationSpeed                    | `live.fan_supply_pct` (fallback `configured.manual_pct`) | `0x004A`, `0x0044` | Live commanded supply % preferred so iOS shows current behaviour in preset modes |
| AirPurifier.StatusFault                      | `service.fault_level`           | `0x0083`                   | 0 if `none`, else 1                                                   |
| Switch[Supply Only].On                       | `configured.airflow_mode`       | `0x00B7`                   | true iff mode == `supply`                                             |
| Switch[Extract Only].On                      | `configured.airflow_mode`       | `0x00B7`                   | true iff mode == `extract`                                            |
| Switch[Heater].On                            | `configured.heater_enabled`     | `0x0068`                   |                                                                       |
| Switch[Night].On                             | `live.special_mode`             | `0x0007`                   | true iff mode == `night`                                              |
| Switch[Turbo].On                             | `live.special_mode`             | `0x0007`                   | true iff mode == `turbo`                                              |
| HumiditySensor.CurrentRelativeHumidity       | `sensors.humidity_pct`          | `0x0025`                   |                                                                       |
| CarbonDioxideSensor.CarbonDioxideLevel       | `sensors.eco2_ppm`              | `0x0027`                   |                                                                       |
| CarbonDioxideSensor.CarbonDioxideDetected    | `sensors.eco2_ppm` vs threshold | `0x0027`, `0x001A`         | 1 iff eCO₂ > configured threshold; 0 when the threshold is not configured |
| AirQualitySensor.AirQuality                  | `sensors.voc_index` → enum      | `0x0320`                   | Boundaries 0/50/100/150/200; see `pkg/homekit/airquality.go`          |
| AirQualitySensor.VOCDensity                  | `sensors.voc_index`             | `0x0320`                   | Raw index value, written verbatim                                     |
| TemperatureSensor[Outdoor].CurrentTemperature     | `sensors.temp_outdoor_c`        | `0x001F`             | Sentinels (`|v| ≥ 1000`) skipped                                      |
| TemperatureSensor[Supply].CurrentTemperature      | `sensors.temp_supply_c`         | `0x0020`             | Sentinels skipped                                                     |
| TemperatureSensor[Exhaust In].CurrentTemperature  | `sensors.temp_exhaust_inlet_c`  | `0x0021`             | Sentinels skipped                                                     |
| TemperatureSensor[Exhaust Out].CurrentTemperature | `sensors.temp_exhaust_outlet_c` | `0x0022`             | Sentinels skipped                                                     |
| FilterMaintenance.FilterChangeIndication     | `service.filter_status`         | `0x0088`                   | 0 if `clean`, else 1                                                  |
| FilterMaintenance.FilterLifeLevel            | `service.filter_remaining_seconds` / `filter_total_seconds` | `0x0064`, `0x0063` | Clamped 0..100                                                  |
| BatteryService.BatteryLevel                  | `service.rtc_battery_volts`     | `0x0024`                   | Linear from 2.5 V (0%) to 3.0 V (100%)                                |
| BatteryService.StatusLowBattery              | `service.rtc_battery_volts`     | `0x0024`                   | 1 iff voltage ≤ 2.7 V                                                 |
| BatteryService.ChargingState                 | (constant)                      | —                          | Always 2 (not chargeable)                                             |

## Operational footprint

### NixOS module knobs

`nix/module.nix` exposes the bridge as
`services.breezyd.homekit.{enable,port,bridgeName,stateDir}`:

```nix
services.breezyd = {
  enable = true;
  homekit = {
    enable     = true;
    port       = 0;                       # 0 = ephemeral
    bridgeName = "breezyd";
    stateDir   = "/var/lib/breezyd/homekit";
  };
};
```

When `services.breezyd.configFile` is set (operator owns the config),
the module still adjusts the systemd unit (`StateDirectory`, firewall)
but does **not** inject a `[homekit]` block into the file — the
operator must add it.

### Firewall

When `services.breezyd.openFirewall = true` and
`services.breezyd.homekit.enable = true`:

- TCP port `cfg.homekit.port` is opened (only if non-zero — an
  ephemeral port cannot be pre-opened in a stateful firewall).
- UDP port `5353` (mDNS) is opened. The brutella/hap library does its
  own mDNS responder (no avahi needed), but inbound UDP/5353 has to
  reach the daemon for iOS to discover the bridge and maintain
  connectivity.

If the daemon is bound to a non-loopback address and `port` is left at
`0`, ensure the firewall configuration allows the OS-assigned ephemeral
port, or pin a fixed port.

### systemd hardening

The bridge runs inside the daemon's existing hardened systemd unit
(`ProtectSystem=strict`, `PrivateTmp=true`, `MemoryDenyWriteExecute=true`,
`SystemCallFilter=@system-service ~@privileged`, …; see
`nix/module.nix` `serviceConfig`). The HomeKit-specific requirement is
that `AF_NETLINK` stays in `RestrictAddressFamilies` — Go's
`net.Interfaces()`, called by brutella/hap's mDNS responder, silently
returns an empty list without netlink, leaving the HAP server running
but advertising on zero interfaces with no error log.

### State directory layout

After pairing, `<state_dir>` holds `pin.txt` (mode 0600) and the
per-pairing entries written by `hap.NewFsStore`. Backing up the
directory backs up pairing state; restoring it onto a new host
re-presents the same bridge identity so existing pairings resume.

## Limitations

The following are deliberately out of scope.

- **Known limitation:** the per-device daily schedule has no HomeKit
  exposure. Editing happens exclusively from the web dashboard; HomeKit
  Automations can replicate the intent on the iOS side using the
  AirPurifier and Switch characteristics.
- **Known limitation:** WiFi reconfiguration of the underlying device
  is not exposed. Use the vendor app or the device's physical button
  sequence.
- **Known limitation:** `StatusFault` flips to 1 when *any* fault is
  active but does not enumerate which one. Use `breezy <name> faults`
  or the dashboard's SERVICE block for detail. There is no HomeKit-side
  fault-reset action.
- **Known limitation:** the firmware version is read from the device
  but is not propagated to the AccessoryInformation `FirmwareRevision`
  characteristic.
- **Known limitation:** per-preset editing (presets 1/2/3 with per-fan
  supply/extract percentages) is web- and CLI-only. HomeKit exposes
  manual speed via `RotationSpeed` and a single "Auto" preset via
  `TargetAirPurifierState`; switching between named presets 2 and 3 is
  not addressable.
- **Known limitation:** the recovery-efficiency reading and the
  energy-tracking block (heat-recovery W, daily/lifetime kWh) are
  dashboard- and Prometheus-only.
- **Known limitation:** sensor-override behaviour (the firmware
  boosting the fan above the user's setting when humidity/CO₂/VOC
  thresholds are exceeded) is not surfaced as a separate signal.
  `RotationSpeed` reflects the live commanded percentage, so the slider
  value is honest, but iOS shows no indication of *why* it differs from
  the stored manual setting.
