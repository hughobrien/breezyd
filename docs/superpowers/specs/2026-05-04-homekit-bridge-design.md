# HomeKit bridge — design

**Date:** 2026-05-04
**Status:** approved for implementation
**Repo:** `~/twinfresh`
**Issue:** [#1 — Expose Devices via HomeKit](https://github.com/hughobrien/breezyd/issues/1)

## Summary

Add a HomeKit Accessory Protocol (HAP) bridge to `breezyd`. When enabled via `[homekit].enabled` in config, the daemon publishes one **bridge accessory** over Bonjour/mDNS, with one **child accessory per configured Breezy** carrying:

- An **AirPurifier** service (power on/off, fan speed 0–100%, auto/manual target state).
- Two **mutually-exclusive Switch services** for "Supply Only" and "Extract Only" airflow modes; both off = both fans (regeneration).
- One **HumiditySensor**, one **CarbonDioxideSensor**, one **AirQualitySensor**, and four **TemperatureSensor** services (outdoor / supply / exhaust inlet / exhaust outlet).

iPhone Home app sees one tile per Breezy. Pairing PIN is auto-generated on first run, persisted to `state_dir`, and printed to logs every start.

## Motivation

The Breezy is a fixed-position whole-home ERV. Its day-to-day controls — power, fan speed, airflow mode — are exactly the controls users want from the Home app, voice ("Hey Siri, turn off the bedroom Breezy"), and HomeKit Automations (e.g. "extract-only when shower humidity rises"). Today those require the breezyd CLI, the embedded dashboard, or the vendor's app over the cloud. None of those integrate with Apple's home ecosystem the way users expect modern HVAC kit to.

The Breezy's sensor surface (humidity, eCO2, VOC, four temperatures) is also rich enough that exposing it via HomeKit gives users free integration with HomeKit Automations on conditions like "if VOC > X for Y minutes, do Z" without writing custom Prometheus alert rules.

## Non-goals

- Filter maintenance services (`FilterMaintenance` characteristic, reset hooks). Filter status remains CLI-only — most users will never look at this in HomeKit.
- Schedule editing. Schedules are on-device and not in scope for HomeKit.
- WiFi / network configuration. Same.
- Multi-tenancy. One HomeKit bridge per daemon process; each bridge serves the daemon's full device list.
- Custom characteristics for the airflow-mode question. Apple's Home app would not render them.
- Heat-recovery toggle in HomeKit (the ventilation-vs-regeneration distinction). When both Switches are off, the daemon writes mode 1 (regeneration) — the more common useful default. Users who want pure ventilation switch via the CLI.
- Apple-certified MFi licensing. Uncertified bridges still work on the user's own LAN with their own Apple ID; that's the open-source convention used by [Homebridge](https://homebridge.io) and others.

## Approach

Three layers, mirroring the Phase 1 split of issue #2:

```
pkg/homekit/                            cmd/breezyd/
  accessory.go    — build *hap.Accessory   homekit.go    — daemon glue:
                    from device model                       start HAP server,
  sync.go         — write a breezy.Status                   wire poller→sync,
                    into accessory characteristics          wire HAP writes→ops
  airquality.go   — VOC index → HomeKit
                    AirQuality enum mapping
```

`pkg/homekit` has no daemon dependency. It builds and updates accessory state from typed inputs (`breezy.Status`, target setpoints). The daemon-side glue threads it into the existing poller + ops layers.

**Library:** [`github.com/brutella/hap`](https://github.com/brutella/hap), the canonical Go HAP server. Adds one direct dependency; no other new modules.

### Topology

- **One bridge accessory** per `breezyd` process. Identified by a stable `Pin` + `SetupID` persisted to `state_dir`. Bridge name configurable; default `"breezyd"`.
- **One child accessory per configured Breezy.** Display name = the device's config key (`playroom`, `bedroom`, `office`). Manufacturer = `"Vents"`, Model = `"Twinfresh Breezy 160"`, SerialNumber = `cfg.ID`.
- All sensor + control services live on the same child accessory. iOS Home app shows one tile per Breezy; users can split into separate tiles via the standard "Show as Separate Tiles" toggle if they want.

### Sensor mappings

| Service              | Characteristic              | Source param      | Notes                                                                 |
|---------------------|-----------------------------|-------------------|-----------------------------------------------------------------------|
| `HumiditySensor`    | `CurrentRelativeHumidity`   | `0x25`            | uint8 0–100                                                           |
| `CarbonDioxideSensor` | `CarbonDioxideLevel`     | `0x27` (eCO2)     | Despite the firmware caveat that this is VOC-derived eCO2 not real NDIR, the value's shape (ppm) maps cleanly. Set `CarbonDioxideDetected` true when value > co2_threshold. |
| `AirQualitySensor`  | `AirQuality`                | `0x0320` (VOC)    | Map VOC index 0–500 to HomeKit's 5-level enum: 0–50 Excellent, 51–100 Good, 101–150 Fair, 151–200 Inferior, 201+ Poor. Also expose raw `VOCDensity` characteristic with the index value for power-user clients. |
| `TemperatureSensor` × 4 | `CurrentTemperature`     | `0x1F`/`0x20`/`0x21`/`0x22` | Each its own service named "Outdoor" / "Supply" / "Exhaust In" / "Exhaust Out". `int16` decode includes sentinel handling: `-32768` ("no sensor") and `+32767` ("short circuit") clamp to `nil` characteristic state (HomeKit accepts no value).  |

The four temperature services on one accessory render as four sub-tiles when the user taps into the Breezy in the Home app.

### Airflow mode switches

Two `Switch` services on each child accessory:

- **"Supply Only"** — when `On`, daemon writes `0xB7 = 2` (mode 2). When `Off`, daemon doesn't touch `0xB7` directly; the other switches' state determines the resulting mode (see truth table below).
- **"Extract Only"** — when `On`, daemon writes `0xB7 = 3` (mode 3).

**Mutual exclusion:** the daemon enforces "at most one of {Supply, Extract} on at a time." When the iPhone toggles "Supply Only" on, the daemon's HAP write callback:

1. Calls `SetMode(ctx, c, "supply")` (writes `0xB7 = 2`).
2. Updates the "Supply Only" characteristic to `On`.
3. Updates the "Extract Only" characteristic to `Off` if it was on. iOS clients see the state change automatically.

Truth table for "what `0xB7` value does the daemon write?":

| Supply Switch | Extract Switch | Daemon writes mode |
|---|---|---|
| Off | Off | 1 (regeneration — both fans + heat exchanger) |
| On  | Off | 2 (supply only)                              |
| Off | On  | 3 (extract only)                             |
| On  | On  | impossible (mutex enforced)                  |

The ventilation mode (`0xB7 = 0`, both fans without heat exchanger) is **not reachable from HomeKit**. Users who want it run `breezy <name> mode ventilation` from the CLI. This is deliberate: the Home app's two-switch UX is cleaner without a third "Heat Recovery" switch, and the seasonal "winter regen / summer ventilation" decision is a once-per-season action, not a daily toggle.

### AirPurifier mappings

| Characteristic              | Maps to                                              | Notes |
|-----------------------------|------------------------------------------------------|-------|
| `Active`                    | `0x01` (power) via `breezy.Power`                    |       |
| `RotationSpeed` 0–100       | `0x44` + `0x02=0xFF` via `breezy.SetSpeedManual`     | <10% snaps to 10 (firmware floor). Setting any speed implicitly switches device to manual mode (the op handles this). |
| `TargetAirPurifierState`    | manual (`0x02=0xFF`) vs auto (preset 1/2/3)          | Toggling to Auto picks preset 1 if device is currently in manual. Toggling to Manual writes the current `RotationSpeed` value via `SetSpeedManual`. |
| `CurrentAirPurifierState`   | derived from `Active` + actual fan motion            | `0` (Inactive) when off, `2` (Purifying) when fan_supply_rpm or fan_extract_rpm > 0, `1` (Idle) otherwise. |

### Pairing & PIN lifecycle

- **First start with `[homekit].enabled = true`:** daemon generates a random 8-digit PIN (the canonical HomeKit setup format `XXX-XX-XXX`), persists it to `<state_dir>/pin.txt` mode 0600, and prints it + a setup payload string to stderr. brutella/hap's `Store` interface persists pairing data (paired controllers, server keys) to the same state dir.
- **Subsequent starts:** daemon reads the existing PIN from `pin.txt` and prints it to stderr at startup so users can recover it without root access. Format: a single line like `homekit: bridge "breezyd" ready, PIN 123-45-678`.
- **Reset pairing:** delete `state_dir`. Next daemon start regenerates the PIN and forces re-pairing.
- **QR code:** brutella/hap's setup payload is a string; `breezyd` prints both the PIN and a single-line ASCII setup URI in the form `X-HM://<base36-payload>`. Users scan from iOS via the camera app or paste it.

PIN generation uses `crypto/rand`. Format spec: 8 decimal digits, sliced into `XXX-XX-XXX`. brutella/hap rejects PINs from a known-weak list (e.g. `123-45-678`, `000-00-000`); generation retries until a valid one is found.

### Config schema

New top-level `[homekit]` section in `config.toml`. Default off. Sibling of `[daemon]`.

```toml
[homekit]
# Enable the HomeKit bridge. Default: false.
enabled = true

# Bridge accessory name shown in iOS during pairing.
# Default: "breezyd".
# bridge_name = "breezyd"

# TCP port the HAP server binds to. Default: 0 (OS-assigned ephemeral).
# Pin a port if your firewall needs a fixed hole.
# port = 51827

# Directory where HomeKit persists pairing keys + the generated PIN.
# Default: $XDG_STATE_HOME/breezyd/homekit
# (~/.local/state/breezyd/homekit on most systems).
# Delete this directory to factory-reset the pairing — daemon will
# regenerate the PIN on next start.
# state_dir = "~/.local/state/breezyd/homekit"
```

The TOML loader (`internal/config`) gains a `Homekit` struct mirroring this. Validation:
- `bridge_name` must be ≤ 32 ASCII characters (Apple's limit).
- `port` must be 0 or in the range 1024..65535.
- `state_dir` is expanded for `~` and `$XDG_STATE_HOME`. Created with `0700` if absent.

NixOS module (`nix/module.nix`) gains a `services.breezyd.homekit.{enable, port, bridgeName, stateDir}` block. The module's default `stateDir` is `/var/lib/breezyd/homekit`, matching the systemd `StateDirectory=breezyd` convention.

### Data flow

**Reads (sensor → HomeKit):**

The daemon's existing poller already refreshes per-device snapshots every 30s into `*State`. When the homekit subsystem is enabled, daemon glue subscribes to poll events; after each successful poll for device X, the glue calls `homekit.Sync(accX, status)` which writes the latest values into the accessory's characteristics. brutella/hap notifies subscribed iOS controllers via the standard HAP event mechanism.

There is no HomeKit-specific polling — the existing 30s interval drives both the cache and HomeKit. The poller's fan-settle suppression (12s after a write to `0x02`/`0x44`/`0xB7`) means HomeKit characteristics may briefly show stale fan-RPM / sensor values after a write, which is correct: the device itself lies during the settle window.

**Writes (HomeKit → device):**

When an iOS controller writes a characteristic, brutella/hap invokes a Go callback registered for that characteristic. The callback maps the characteristic to a `pkg/breezy/ops` call (`Power`, `SetSpeedManual`, `SetMode`, …) and dispatches it through the existing `recordingClient` wrapper. The daemon's per-device mutex serializes the UDP request; on success, the recording wrapper's callback updates the cache via `State.WriteThrough` and notifies the poller for fan-settle suppression. Phase 1's protocol invariants (packet ordering, validation, `ErrInvalidArg`) all kick in for free.

The HAP write callback is synchronous — it returns success only after the UDP write has completed, which means the iPhone's "spinner" state matches the device's response time (typically <100ms). UDP errors propagate as HAP write failures; iOS shows "Not Responding" in the Home app, which the user can retry.

**Mutual exclusion in the airflow switches** is enforced inside the HAP write callback: when "Supply Only" is set to `On`, the callback also issues a state update setting "Extract Only" to `Off`. This appears atomic to clients because brutella/hap batches state updates per request.

## Concurrency

The daemon's `Handler.recordWrite` (Phase 1) already serializes per-device UDP and notifies the poller's fan-settle window. HomeKit writes flow through the same path via `dialRecording`. No new synchronization primitives required.

The HAP server runs on its own goroutine. Its mDNS responder runs on another. Both communicate with the daemon through the existing `Handler` + `State` + recording wrapper APIs, which are all already concurrency-safe.

## Testing

**Unit (in `pkg/homekit/`):**

- `accessory_test.go` — table tests build accessories from sample device configs; assert each has the expected services (one AirPurifier, two Switches, four TemperatureSensor, one each of Humidity / CO2 / AirQuality), with the expected default characteristic values.
- `sync_test.go` — given a `breezy.Status`, verify `Sync` writes the right characteristic values:
  - Sensor sentinels (`-32768` / `32767`) clamp to nil characteristic state.
  - VOC index → AirQuality enum boundary cases (50 → Excellent, 51 → Good, 100 → Good, 101 → Fair, etc.).
  - Power/RotationSpeed/CurrentAirPurifierState derive correctly from `Status.Configured` + `Status.Live`.
- `airquality_test.go` — direct test on the VOC-to-enum mapping with all five buckets.

**Integration (in `cmd/breezyd/homekit_test.go`):**

- Start daemon with `[homekit].enabled = true` against a `fakedevice`. Use a tempdir for `state_dir`.
- Assert HAP server listens on the configured port and responds to a basic HAP request (no actual pairing — too involved for an integration test).
- Poll-driven sync: change a value in fakedevice, wait ≥ one poll interval, assert the corresponding HomeKit characteristic has the new value.
- Write callback: invoke the `Active` characteristic's write callback with `1`, assert a write to `0x01` lands in fakedevice; same for `RotationSpeed → 0x44+0x02`, `Supply Only → 0xB7=2`.
- Mutual exclusion: turn on "Supply Only", then turn on "Extract Only", assert "Supply Only" auto-flipped off (both at the HAP characteristic level and at the device level).

**Manual:**

Pair a real iPhone against `breezyd` with HomeKit enabled. Walk through:
1. Add bridge from Home app → enter PIN → bridge + child accessories appear.
2. Toggle power, slide speed, flip Supply Only / Extract Only.
3. Verify sensor values match the dashboard.
4. Trigger a write from a Siri Shortcut.
5. Reboot the daemon — pairing persists; accessories reconnect within ~10s.

Manual e2e is documented in the README's HomeKit section but not automated.

## Verification

After landing:
- `just check-all` passes (all unit + integration tests).
- `breezyd` starts with `[homekit].enabled = true` against a fakedevice in CI; logs include the PIN.
- A real iPhone successfully pairs against a `breezyd` bound to a real Breezy on the LAN.
- Power / speed / supply / extract / sensors all behave correctly from the Home app.
- README has a HomeKit section; CLAUDE.md mentions the bridge in the Architecture section.
- CHANGELOG `[Unreleased]` has an "Added" entry.

## Out of scope (for later, if at all)

- Per-device opt-out (`[devices.X].homekit = false`). Add it when someone has 5+ Breezys and only wants 2 in HomeKit.
- HomeKit "Eve" custom characteristics for thresholds (humidity_threshold, co2_threshold, voc_threshold). Eve-specific; not Apple-blessed; only useful in third-party clients.
- Per-mode setpoints from HomeKit (preset 1/2/3 supply/extract %). The `RotationSpeed` slider already covers daily fan control.
- Apple HomeKit certification. The bridge runs uncertified; users add it manually like Homebridge.
- Heat-recovery toggle (ventilation vs regeneration) in HomeKit. Reachable via CLI only.
