# SPECIFICATION — `breezy` CLI

The `breezy` command-line tool controls Vents Twinfresh "Breezy" energy-recovery
ventilator (ERV) units. It speaks to devices directly over UDP (standalone
mode) or through a running `breezyd` daemon (daemon mode); the same binary
handles both, chosen per invocation from flags and config.

This file specifies the CLI's user-visible contract: invocation grammar, mode
selection, configuration, every verb, output conventions, and exit codes.

Sibling specifications:

- **SPECIFICATION-daemon.md** — `breezyd` daemon and the `/v1/...` JSON API.
- **SPECIFICATION-web.md** — embedded `/ui/...` dashboard.
- **SPECIFICATION-hap.md** — HomeKit bridge.

Wire-protocol bytes are defined in `pkg/breezy/frame.go` and out of scope here.

## Invocation

```
breezy [--daemon URL] <name> <verb> [args...]      # per-device verb
breezy [--daemon URL] <global> [args...]           # global verb
breezy --version                                   # version + build metadata
breezy help | -h | --help                          # usage
```

Subject-before-verb is a hard rule: the device name appears immediately after
any global flags, and the verb follows. Dispatch in
`cmd/breezy/main.go::run` checks the first positional argument against the
reserved-name set; everything else is treated as a device name.

Reserved global names: `ls`, `discover`, `daemon-url`, `param`, `version`,
`help`. Device names colliding with any of these are rejected by the config
loader at startup.

### Examples

```sh
breezy ls
breezy discover
breezy playroom status
breezy bedroom speed manual:30
breezy office mode regeneration
breezy --daemon http://10.0.0.5:9876 playroom on
```

## Modes

The CLI runs in one of two modes per invocation. Mode selection happens in
`cmd/breezy/main.go::resolveBackend` with this precedence:

1. **`--daemon URL`** flag, if non-empty: daemon mode against that URL.
2. **`[daemon].listen`** in config, if non-empty: daemon mode against that URL.
3. **Standalone** (default): direct UDP to each configured device.

There is no fallback URL. A daemon-mode invocation against an unreachable
daemon surfaces an HTTP transport error from the first request, not a silent
fall-through to UDP. `breezy daemon-url` prints what the CLI would use, or
`(standalone — no daemon)` if it would run standalone.

### Standalone mode

The CLI opens one `*breezy.Client` per addressed device on first use and
closes them on exit (`cmd/breezy/backend.go::directBackend`). Each verb is
one or more UDP request/response exchanges with that device.

Standalone mode is safe for a single sequential CLI invocation. It is not
safe to fan out parallel invocations against the same device — overlapping
retries and packet collisions cause silent corruption. For concurrent access,
run `breezyd` and use daemon mode.

In standalone mode `ls` shows local config only (`POWER` / `MODE` / `LAST POLL`
columns render as `?` / `never` because there is no cache). A device with no
`ip = "..."` in config produces a usage-style error.

### Daemon mode

The CLI issues HTTP requests against `<daemon-url>/v1/...`. Bare `host:port`
is normalised to `http://host:port` (`cmd/breezy/main.go::normalizeURL`).
Each request has a 10 s timeout. HTTP errors decode the daemon's
`{"error", "code"}` envelope and render as `error: <msg> (<code>)`.

Daemon mode delegates aggregation, polling, caching, and concurrency control
to `breezyd`. See **SPECIFICATION-daemon.md** for endpoint contracts.

### Discovery is always direct

`breezy discover` always issues UDP, regardless of mode. It does not require
config and does not consult the daemon — see the verb entry below.

## Configuration

The CLI reads `~/.config/breezy/config.toml`, falling back to
`/etc/breezy/config.toml`. The first file found wins — there is no
merge across both locations. A missing config is not an error: the CLI
runs in standalone mode with an empty device map, which is fine for
`discover` and `--version`.

The file is shared with the daemon. The CLI consumes only:

- `[daemon].listen` — selects daemon mode when non-empty.
- `[devices.<name>]` blocks — `id`, `password`, `ip`, used in standalone mode.
  `ip` is required in standalone mode; it is optional in daemon mode (the
  daemon discovers it).

The loader (`internal/config/config.go::Load`) enforces:

- File mode `0600` whenever any password (fleet or per-device) is present.
  Password-free configs may sit at `0644` so a system-wide fallback (e.g.
  one written by the NixOS module that only sets `[daemon].listen`) can be
  world-readable.
- Device IDs must be exactly 16 ASCII characters.
- Device names must not collide with global verbs (case-insensitive).

The CLI ignores other fields (poll interval, discovery, HomeKit) — they
belong to the daemon. See **SPECIFICATION-daemon.md** for the full schema.

## Per-device verbs

Every verb takes the form `breezy <name> <verb> [args...]`. Success exits
`0`; backend errors exit `1`; usage errors exit `2`. Most write verbs print
`ok` on success.

| Verb | Args | What it does |
|---|---|---|
| `status` | — | Print structured snapshot. |
| `on` / `off` | — | Power the unit on / off. |
| `speed` | `1\|2\|3` *or* `manual:<pct>` | Select preset (1-3) or manual percentage (10-100). |
| `mode` | `ventilation\|regeneration\|supply\|extract` | Set the airflow mode. |
| `heater` | `on\|off` | Enable or disable the supply-air heater. |
| `timer` | `off\|night\|turbo` | Start or stop the special-mode timer. |
| `threshold` | `humidity\|co2\|voc <int>` | Set the over-threshold setpoint for one sensor. |
| `auto-fan` | `humidity\|co2\|voc on\|off` | Toggle the sensor's "trigger fan boost" flag. |
| `reset-filter` | — | Reset the filter-life timer. Prints `filter timer reset`. |
| `reset-faults` | — | Clear active faults. Prints `faults cleared`. |
| `faults` | — | List active faults. |
| `firmware` | — | Print firmware version + build date. |
| `efficiency` | — | Print recovery efficiency percentage. |
| `rtc` | — *or* `set <RFC3339>` | Show or set the device's wall clock. |
| `get` | `<param>` | Raw parameter read. |
| `set` | `<param> <hex>` | Raw parameter write. |

Implementations live in `cmd/breezy/commands.go::cmd<Verb>` and dispatch
through the `backend` interface in `cmd/breezy/backend.go`.

### `status`

Prints a multi-line snapshot rendered by `cmd/breezy/render.go::renderStatus`.
The output distinguishes **configured** (what the user asked for) from
**live** (what the device is actually doing):

```
playroom @ 192.168.1.148  (firmware 0.11, last poll 3s ago)
  power      : on
  mode       : regeneration
  speed      : manual 30%        (live: 5340 / 5400 rpm)
  sensors    : RH=52%  eCO2=3500ppm  VOC=350  outdoor=20.8°C  recovery=85%
  service    : filter clean (89d 9h remaining)  motor 14h 32m lifetime
  battery    : RTC 3.34 V
```

When `live.in_user_control` is `false`, a **sensor-override warning** appears
just below the header. This signals a known device behaviour: the firmware
boosts the fan above the user setting when humidity / eCO2 / VOC exceeds
threshold and the matching sensor is enabled.

```
  !! sensor override active (co2 alert, voc alert) — fan/heater may not match configured values
```

The `!!` line fires only when `in_user_control` is explicitly false; the
parenthesised alert summary is omitted if no specific alerts are flagged.
A non-`none` `service.fault_level` adds a `faults` line pointing at
`breezy <name> faults`.

In daemon mode the snapshot comes from `GET /v1/devices/{name}` (cached). In
standalone mode it is built on the fly via `breezy.GetStatus`.

### `speed`

Two forms. `breezy <name> speed <1|2|3>` selects a firmware preset.
`breezy <name> speed manual:<pct>` switches to manual mode at the given
percentage; `<pct>` must be `10..100`. Values below 10 are rejected
client-side as "below the firmware floor of 10%"; values above 100 are
rejected client-side. Both rejections exit `2` before any I/O.

### `mode`, `heater`, `timer`, `threshold`, `auto-fan`

Each accepts a small enumerated argument set, validated client-side
(case-insensitive); a typo exits `2` with the valid list inline. `threshold`
takes an integer in the sensor's native units (RH%, ppm, VOC index);
non-numeric input exits `2`.

### `rtc`

Without arguments, prints the device's current date and time on one line by
issuing fresh reads of parameters `0x6F` (`rtc_time`) and `0x70`
(`rtc_calendar`); these aren't kept in the cache, so the CLI bypasses it.

`breezy <name> rtc set <RFC3339>` parses with `time.Parse(time.RFC3339, ...)`.
Bad input exits `2`. Success: `rtc set`.

### `get` and `set`

`get <param>` issues a raw parameter read. `<param>` is resolved by
`cmd/breezy/commands.go::resolveParam`:

- a registry name (case-insensitive), e.g. `humidity`, `co2_threshold`;
- a hex id with `0x` prefix, e.g. `0x25`;
- a bare hex id, e.g. `25`.

Names take precedence over hex parsing. Output:

```
<name> (0xNNNN) = <value> <unit>
```

Unknown IDs are still readable: the raw hex bytes replace the typed value
and the name is omitted. In daemon mode the request bypasses the cache
(`/v1/devices/{name}/params/{id}`).

`set <param> <hex>` writes the literal byte payload, with or without a
leading `0x`. Read-only parameters in the registry are rejected client-side
with exit `2`. The CLI does not validate payload bytes — the device (or
daemon) rejects nonsensical values. Use the typed verbs above for normal
operation; `set` is for diagnostics.

## Global verbs

### `ls`

Prints a fixed-width table: `NAME`, `IP`, `POWER`, `MODE`, `LAST POLL`.
Empty config: `(no devices configured)`. Rows sorted by name.

In **daemon mode** the data comes from `GET /v1/devices` (the poller's cache);
unreachable devices have `(unreachable)` appended to the last-poll column.
In **standalone mode** there is no cache; `POWER` / `MODE` render as `?`,
`LAST POLL` as `never`.

### `discover`

`breezy discover [-p PASSWORD] [target...]` enumerates Breezy devices on
the LAN. Each responder is printed on its own line:

```
192.168.1.148  id=BREEZY00000000A0  type=185 (Breezy 160)
```

Two query modes:

- **No targets** (default): UDP-broadcast on every up, non-loopback IPv4
  interface to that interface's directed-broadcast address (plus
  `255.255.255.255`). Bootstrap path; works without any config.
- **Positional targets**: each is an IP or `host:port`. The wildcard
  discovery request is sent unicast to each. Useful when the LAN drops
  broadcasts (Wi-Fi AP isolation, mesh hops).

Flags: `-p PASSWORD` / `--password PASSWORD` / `--password=PASSWORD`
overrides the factory-default discovery password (`1111`). The vendor
manual states discovery is unauthenticated, but some firmware drops
wildcard requests with a mismatched password; pass the real password if
discovery yields no responders even though the units are reachable.

The discovery timeout is 3 seconds. If no responders are seen, the CLI
prints a short troubleshooting list (UDP/4000, broadcast suppression,
non-default password) and exits `0` — empty discovery is not an error.

Discovery is independent of mode: it always speaks UDP directly, never
through the daemon, and does not require any `[devices.*]` config.

### `daemon-url`

Prints the URL the CLI would use, or `(standalone — no daemon)` when
neither `--daemon URL` nor `[daemon].listen` is set. Useful for sanity-
checking config layering.

### `param`

Prints the entire static parameter registry as a wide table: `ID` (4-digit
hex), `NAME`, `TYPE`, `UNIT`, `CAPS` (`R`/`W`/`I`/`D`), `DESCRIPTION`. The
data comes from `pkg/breezy/params.go` and is compiled into the binary; no
backend round-trip and exits `0` unconditionally.

### `version` and `help`

`breezy version` (or `breezy --version`) prints
`breezy <version> (commit <sha>, built <date>)`. The three values are
injected at build time via `-ldflags`; an unbuilt local binary reports
`dev` / `none` / `unknown`.

`breezy help`, `breezy -h`, `breezy --help`: print usage and exit `0`.
Misuse (unknown verb, missing args) prints a one-line usage hint and
exits `2`.

## Output conventions

- Output is human-readable text on stdout. Errors and usage hints go to
  stderr.
- Error lines are prefixed `error: ` (lower-case `e`).
- Successful writes print a short confirmation: `ok` for most verbs;
  verb-specific phrasing (`filter timer reset`, `faults cleared`,
  `rtc set`) where it conveys more information than `ok`.
- The sensor-override marker on `status` is the literal `!!` prefix
  (ASCII; no Unicode warning sigil), printed only when
  `live.in_user_control` is explicitly `false`.
- The CLI does not emit JSON. Scripts that need machine-readable output
  should call the daemon's `/v1/...` endpoints directly. See
  **SPECIFICATION-daemon.md**.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. |
| `1` | Backend error: in daemon mode an HTTP error (the daemon's `{"error", "code"}` envelope rendered as `error: <msg> (<code>)`); in standalone mode a UDP / protocol error rendered as `error: <msg>`. |
| `2` | Local usage error: bad args or pre-I/O validation failure (e.g. `speed manual:5`, `mode foo`, `set <read-only>`, missing config for a referenced device). |

Rationale for the `1` / `2` split is documented in
`docs/superpowers/specs/2026-05-03-twinfresh-cli-design.md`.

## Live integration tests

Live integration tests under `pkg/breezy/...` are double-gated and require
real hardware. They are skipped unless **both** the `integration` build tag
and `BREEZY_INTEGRATION=1` are set, plus three target env vars
(`BREEZY_TEST_DEVICE_IP`, `_ID`, `_PASSWORD`). The
`just test-integration <ip> <id> <password>` recipe wires this up.

These tests write to the device. Each one registers a `t.Cleanup` that
restores the prior parameter value, so re-runs leave device state intact.
The non-destructive-across-runs guarantee depends on those cleanups; they
must not be removed or weakened.

This affects the CLI's behavioural promises only insofar as: every verb that
writes a parameter is observable to the firmware as a persistent change.
There is no dry-run or transaction mode — `set`, `speed`, `mode`, `heater`,
`reset-filter`, `reset-faults`, `rtc set`, `threshold`, and `auto-fan` all
take effect immediately and persist across device reboots.
