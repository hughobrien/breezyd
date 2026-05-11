# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build, test, lint

```sh
just generate        # templ codegen (writes *_templ.go; required before build)
just build           # generate + go build -> ./breezyd ./breezy
just test            # go test ./...               (fast, no race)
just test-race       # go test -race ./...         (cgo+clang; the CI command)
just test-race-flake # go test -race -count=5 ./...(heisen-race shaker)
just test-msan       # go test -msan ./...         (cgo+clang; uninit-memory reads)
just test-asan       # go test -asan ./...         (cgo+clang; OOB / UAF)
just test-staticcheck# golangci-lint run ./...     (errcheck is the strict bit)
just test-templ-drift# verify generated *_templ.go files are up-to-date
just test-ui         # Playwright e2e against breezyd+memory backend (needs test-ui-install once)
just test-test-admin # go test -tags breezyd_test_admin (the /test/... surface used by Playwright)
just coverage        # go test -coverprofile + per-package summary + total (display-only; no gate)
just lint            # go vet + gofmt-drift check
just check           # lint + fast tests + templ-drift (pre-commit gate)
just check-all       # lint + test + test-race + test-ui + templ-drift (pre-push gate)
just ci              # everything CI runs on every PR: check-all + staticcheck + asan + msan + templ-drift + test-test-admin
just check-deep      # ci + race-flake             (~5 min; pre-tag gate)
just kill-test-daemons # safety-net: SIGTERM any orphan breezyd procs from aborted UI runs
just tidy            # go mod tidy
just clean           # remove binaries, test cache, Playwright artifacts
just fmt             # gofmt -w .
just nix-check       # parse-check nix/module.nix
```

`templ` is required to build. `nix develop` provides it automatically. Outside Nix: `go install github.com/a-h/templ/cmd/templ@v0.3.x`. Running `just build` calls `templ generate` first; if `templ` is not on `$PATH` the build fails immediately.

`just test-race` (and `-msan` / `-asan` / `test-race-flake`) bake in `CGO_ENABLED=1 CC=clang` because the default `gcc` on this host lacks the TSan / MSan / ASan runtimes.

When in doubt about which gate to run: `check` is the fast pre-commit; `check-all` is the comprehensive pre-push (adds race + Playwright); `ci` reproduces what GitHub Actions runs on every PR (adds golangci-lint, asan, msan); `check-deep` is the slow paranoid sweep before tagging a release or after risky concurrency / cgo / unsafe edits. When editing `nix/module.nix`, `nix-check` is the fast syntax probe — `nix build` is the heavy alternative.

GitHub Actions runs the `ci` set as parallel jobs on every PR: `vet + race + build`, `golangci-lint`, `asan tests`, `msan tests`, and `Playwright`. The workflow definition is `.github/workflows/test.yml`.

**Project rule:** when running a check / lint / test combo more than once, add it as a recipe to `justfile` rather than re-typing — improve the file *as you go*.

Run a single package or test:

```sh
go test ./pkg/breezy/...
go test ./cmd/breezyd -run TestPoller_FanSettle
```

Live integration tests are double-gated and require real hardware. They are skipped unless **both** the `integration` build tag and `BREEZY_INTEGRATION=1` are set, plus the three target env vars:

```sh
just test-integration <ip> <id> <password>
```

These tests write to the device. Each one registers a `t.Cleanup` that restores the prior value, so re-runs leave state intact. **Never remove or weaken those cleanups** — see the project rule about not making unsanctioned writes to user hardware.

Nix flake builds work too: `nix build`, `nix develop`, `nix run .#breezy -- ls`. The flake's `vendorHash` in `flake.nix` must be updated whenever `go.sum` changes. `nix develop` includes `just`, so all recipes are available without a global install.

## Architecture

Three artefacts from one Go module (`github.com/hughobrien/breezyd`):

1. **`pkg/breezy`** — importable protocol library. Speaks the Vents Twinfresh FDFD/02 framed protocol over UDP/4000.
2. **`cmd/breezyd`** — long-running daemon. Owns *all* UDP traffic, polls every configured device, caches snapshots, exposes JSON HTTP + Prometheus `/metrics`. Two HTTP namespaces: `/v1/...` (JSON, for the CLI and external consumers) and `/ui/...` (SSE, for the templ + datastar dashboard). The page shell at `GET /{$}` — the `{$}` anchor is load-bearing: a plain `GET /` would catch every unmatched URL and turn API typos into HTML responses. The page opens one `GET /ui/sse` on load and the poller fans per-device card updates out through it. View type: `cmd/breezyd/ui/view.go::DeviceView`; Snapshot→View conversion in `ui_view.go::snapshotToView` + `handlers_ui_read.go::buildView` (the latter adds Energy and Schedule).

Push wiring: `push_hub.go::PushHub` owns subscribers and renders templ DeviceCards via an injected closure; `handlers_ui_sse.go::getUISSE` holds the long-lived response, emits initial-state cards on connect, then drains the subscriber channel as `datastar-patch-elements` events. The poller's `OnPoll` is composed in `main.go` as `SyncHomekit + PushHub.Notify`. Action handlers under `/ui/devices/{name}/...` return 200 + empty body on success (clients see the next push); validation/auth/backend failures emit a status-coded `datastar-patch-elements` into `#global-error-banner`.
3. **`cmd/breezy`** — CLI. Defaults to standalone mode (UDP directly to each configured device via `pkg/breezy/ops`). Opts into daemon mode when `--daemon URL` is passed or `[daemon].listen` is set in config. `breezy discover` always broadcasts on the LAN directly, independent of mode. `Discover()` enumerates every up, non-loopback IPv4 interface and sends to its directed-broadcast address plus a static fallback list — relevant when a host isn't on `192.168.0/1.0/24`.

`internal/config` is the shared TOML loader. `pkg/breezy/fakedevice` is an in-process UDP server that replays a captured snapshot — used by Go protocol tests (`pkg/breezy/...`) and by the poller's UDP-path tests (`cmd/breezyd/poller_test.go::TestPoller_FanSettle_*`). `cmd/fakedevice` (the standalone admin binary) was retired in v1.2; mid-test state mutation now happens via breezyd's build-tagged `/test/...` surface (see `cmd/breezyd/handlers_test_admin.go`). `tests/ui/` is a separate pnpm-managed Playwright suite (`@playwright/test`) that spawns one breezyd process (built with `-tags breezyd_test_admin`, run with `--backend=memory --seed pkg/breezy/fakedevice/snapshot_148.json`) and drives the actual dashboard. `tests/ui/screenshots/` holds committed PNGs that re-render on `just screenshot`; the README embeds the 3-col one.

### Device backend (UDP vs in-process memory)

`pkg/breezy.DeviceClient` is the seam between `breezyd` and "the device." Two implementations:

- **`*breezy.Client`** — UDP, production default. Owned per-device; serialises traffic behind a `sync.Mutex`. Used when `breezyd --backend=udp` (the default).
- **`*breezy.MemClient`** — in-process, `map[ParamID][]byte` over `sync.RWMutex`. Reads/writes return instantly. Used when `breezyd --backend=memory --seed <path>` is set; one MemClient is created per configured device, seeded from the same JSON snapshot file (the `pkg/breezy/fakedevice/snapshot_*.json` shape).

`DeviceClient.IsLocal() bool` distinguishes them so callers can gate UDP-protocol-specific behaviour (e.g., the poller's fan-settle suppression — see below).

**Local UI development:** spin up the dashboard against canned data with no hardware, no fakedevice, no UDP:

```sh
breezyd --config <some-config.toml> --backend=memory --seed pkg/breezy/fakedevice/snapshot_148.json
```

Add a `[devices.<name>]` block per card you want to render; `ip` is required by config validation but ignored in memory mode (e.g., `ip = "127.0.0.1:0"`). Build with `-tags breezyd_test_admin` if you want the `/test/devices/{name}/...` admin surface for runtime mutation; without the tag the surface returns 404. Production binaries don't ship the tag.

### Standalone CLI mode and why the daemon owns UDP

Concurrent UDP request/response with checksums isn't safe to fan out from independent processes — overlapping retries and packet collisions cause silent corruption. `breezyd` solves this by serialising traffic per device behind a `sync.Mutex` in `pkg/breezy.Client` plus a single per-device poller goroutine. The standalone CLI (default; daemon mode is opt-in via `--daemon URL` or `[daemon].listen` in config) is safe for a single sequential invocation but unsafe if users script parallel invocations against the same device — for that, run the daemon.

### HomeKit bridge (opt-in via `[homekit].enabled`)

When enabled, the daemon runs a brutella/hap HAP server that exposes each configured Breezy as a HomeKit accessory (AirPurifier + airflow-mode Switches + sensor services for humidity, eCO2, VOC, and four temperatures). The bridge runs in-process; writes flow through `pkg/breezy/ops` via the same `dialRecording` wrapper as the HTTP handlers, so every protocol invariant (packet ordering, fan-settle, validation) holds. PIN auto-generated on first run and persisted to `state_dir`. Pure accessory logic lives in `pkg/homekit`; daemon glue in `cmd/breezyd/homekit.go`.

### Energy tracking (always on)

After every successful poll, each device's `EnergyTracker` (in `cmd/breezyd/energy_tracker.go`) computes instantaneous heat-recovery W and fan-electric-draw W, accumulating heating/cooling/consumed kWh per day and lifetime. Tracking is gated to regeneration airflow_mode (the only mode where the heat exchanger is actually working) — manual, supply-only, extract-only, and ventilation modes are no-ops for the accumulator.

Per-device state lives at `<state_dir>/energy_<device>.json` and survives daemon restarts. Lifetime counters carry over; today counters reset at local midnight. The state directory is `$STATE_DIRECTORY` if systemd sets it (NixOS / production) or `$XDG_STATE_HOME/breezyd` otherwise.

The instantaneous calculation needs a per-model airflow + fan-power calibration table (`pkg/breezy/energy.go::modelCurves`). Adding a new device model is a one-line table edit. Devices whose UnitType isn't in the table surface an error in `service.energy.error`; the dashboard's ENERGY block shows the error string and `/metrics` drops the eight `breezyd_energy_*` gauges for that device.

### Schedule system (per device, opt-in via UI)

Each configured device gets a `Scheduler` goroutine (in `cmd/breezyd/scheduler.go`) that fires Power/Mode/SpeedManual writes at user-configured At-times each day. State is persisted at `<state_dir>/schedule_<device>.json` and survives restart. The schedule is event-driven, NOT state-driven: on daemon startup or schedule re-enable, the daemon does NOT immediately apply the entry-in-effect — only future transitions fire. (See `docs/superpowers/specs/2026-05-06-schedule-system-design.md` for why.)

Entries have `At | Action | Pct`. Action ∈ `{off, regeneration, ventilation, supply, extract}`; off powers the unit off, the others power-on + set the airflow mode + set speed=manual at the given Pct. Times are in the daemon host's local timezone.

Failed fires retry every 30 s for up to 10 min, abandoning early when the next entry's At-time arrives. `breezy.ErrAuth` is treated as a config error and does not retry. Failures surface as a force-expanded alert on the SCHEDULE block in the dashboard.

Editing happens exclusively from the web UI via `GET`/`PUT /v1/devices/{name}/schedule`. There are no CLI verbs and no HomeKit exposure for the schedule itself.

**DST handling.** Times are local wall-clock. The daemon honours basic DST transitions:

- Spring-forward: an entry whose At-time falls in the missing hour fires once at the first tick after the skipped hour. Handled by the existing window-detection for a running daemon.
- Fall-back: an entry whose At-time falls in the repeated hour fires exactly once, at the first occurrence. The per-entry `firedAt` map on `Scheduler` suppresses the second appearance.
- Residual edge case: if the daemon starts during the missing hour (spring-forward), entries in that hour are silently skipped. Matches the no-catch-up rule.
- Non-1h-DST zones (e.g. Lord Howe's 30-min): the firedness check uses calendar-day comparison, so any DST offset de-duplicates correctly.

### Daily RTC sync (always on)

Each configured device runs an `RTCSyncer` goroutine (in
`cmd/breezyd/rtc_sync.go`) that writes the device's RTC (params
`0x6F` + `0x70`) once shortly after daemon startup and then daily at
04:00 local time. Closes the panel-display drift introduced by DST
transitions, battery replacement (CR2032 at `0x24`), and long-term
RTC oscillator drift. Per-device, no persisted state, no
configuration knob — the cycle is fully derived from `time.Now()`
and restarts cleanly on daemon restart. Failures (UDP timeout, auth)
log a warning and continue; next 04:00 retries naturally.

### Cache vs. passthrough

- `GET /v1/devices`, `GET /v1/devices/{name}`, `/metrics` → in-memory cache populated by the poller. Reads are cheap and never block on UDP.
- `GET /v1/devices/{name}/params/{id}` → bypasses cache, always issues a fresh UDP read. Use for debugging.
- All writes (`POST /v1/devices/{name}/...`) hit UDP and update the cache on success.

### Fan-settle window (a real protocol quirk)

After a write to `0x02` (speed_mode), `0x44` (manual %), or `0xB7` (fan rotation), the unit takes 10–15s before `0x4A`/`0x4B` (fan RPMs) and `0x84` (air-quality status) reflect the new state. The poller suppresses reads of `fanSensitiveReads` for `fanSettleDuration = 12s` after any `fanWriteIDs` write. See `cmd/breezyd/poller.go`. **Don't shorten this window** — the protocol genuinely lies during that interval. The suppression is gated on `client.IsLocal()`, so it only fires for the UDP backend; `*breezy.MemClient` writes land instantly with no fan-settle window. End-to-end coverage of the suppression lives in `TestPoller_FanSettle_DropsSensitiveReads_OverUDP` (against fakedevice, real UDP).

### Failed-poll cache semantics

When a poll tick has every read fail (e.g. the in-process backend with `SetTimeoutMode(true)` returns `ErrTimeout` instantly), the poller preserves the previous tick's `Values` map rather than overwriting the cache with empty. The dashboard then renders "stale" with last-known data instead of dropping to the `unreachable` placeholder. Real-UDP timeouts are slow enough that this branch rarely fires in production.

### Sensor override (a user-visible firmware quirk)

When humidity/CO2/VOC exceed thresholds and the matching sensor is enabled, the firmware boosts the fan above the user's setting. The status output distinguishes `configured` (what the user asked for) from `live` (what's actually happening), and the `⚠` line on `breezy <name> status` only fires when `live.in_user_control` is false. Preserve that distinction in any output changes.

## Protocol invariants (`pkg/breezy/frame.go`, `client.go`)

- Frame: `FD FD | TYPE(0x02) | SIZE_ID(0x10) | ID(16 ASCII) | SIZE_PWD | PWD | FUNC | DATA | Chksum_L | Chksum_H`. Checksum = sum of bytes from `TYPE` through end of `DATA`, low byte first.
- Function codes: `0x01` read, `0x02` write-no-response, `0x03` write-with-response, `0x04` increment, `0x05` decrement, `0x06` controller response, **`0x07` auth failure** (undocumented; surfaced as `ErrAuth`).
- Multi-byte values use `FE <size> <id> <bytes...>` framing inside DATA.
- Parameter IDs ≥ `0x0100` use `FF <hi>` page prefixing inside DATA.
- Discovery: a request with the literal device ID `DEFAULT_DEVICEID` returns the unit's true ID and unit type regardless of password. **Discovery is unauthenticated.**
- Time encodings are little-endian smaller-unit-first: 2-byte = `[min, hr]`, 3-byte = `[sec, min, hr]`, 4-byte = `[min, hr, day_lo, day_hi]`.

The vendor protocol manual (`docs/superpowers/specs/breezy-manual-vendor.pdf`) is the authoritative wire-protocol reference. The full per-parameter map (id → name, type, R/W, units, observed values) lives at `docs/superpowers/specs/2026-05-03-param-map.md`. **Read those before reverse-engineering anything new.**

## CLI surface

Subject-before-verb: `breezy <device-name> <verb> [args]`. Per-device verbs (`status`, `on`/`off`, `speed`, `mode`, `heater`, `reset-filter`, `reset-faults`, `faults`, `firmware`, `efficiency`, `rtc [set]`, `get <param>`, `set <param> <val>`) and globals (`ls`, `discover`, `daemon-url`, `param`).

Reserved global names cannot be used as device names — the config loader rejects collisions in `internal/config`. Device names must also be valid JS identifiers (letters/digits/underscore, starting with a non-digit) because they appear as datastar signal-path segments (e.g. `$detailsOpen.<name>.sensors`); names with hyphens or dots would silently break the dashboard's signal parsing.

CLI exit codes: `0` success, `1` backend error (in daemon mode: HTTP `{"error","code"}` envelope rendered as `error: <msg> (<code>)`; in standalone mode: UDP/protocol error rendered as `error: <msg>`), `2` local usage error.

## Config

`~/.config/breezy/config.toml`, mode `0600` (loader refuses world-readable — passwords are stored cleartext, and the device leaks them anyway, so this is the floor). Same file is read by daemon and CLI; CLI only consumes `[daemon].listen`.

`discovery` is one of `"on-start"`, `"off"`, or `"periodic:<duration>"`.

**First-run bootstrap:** when `breezyd` is started against a config path that doesn't exist, it writes a sensible default at that path (mode 0600, parent dir at 0700 if missing) via atomic temp+rename, then exits non-zero with an "edit it" message. See `internal/config.WriteDefault` and the gate at the top of `cmd/breezyd/main.go`'s `run`. The bootstrap path is gated specifically on `errors.Is(err, os.ErrNotExist)` — other Load failures (bad TOML, wrong mode, etc.) still surface unchanged.

## Spec & design docs

Grouped by subsystem / version. Reverse-chronological within each group.

**Protocol + CLI (v1.0):**
- `docs/superpowers/specs/2026-05-03-twinfresh-cli-design.md` — design doc covering protocol decisions, daemon architecture, error semantics, status-line format. The closest thing to a "why" for the core.
- `docs/superpowers/specs/2026-05-03-param-map.md` — every parameter ID with type, units, observed values.
- `docs/superpowers/specs/breezy-manual-vendor.pdf` — vendor protocol manual.

**Standalone CLI mode (v1.0):**
- `docs/superpowers/specs/2026-05-04-standalone-mode-design.md` — why daemon owns UDP, how the CLI opts into daemon-mode.

**Dashboard substrate (v1.1 → v1.4):**
- `docs/superpowers/specs/2026-05-04-basic-ui-design.md` — original v1.1 motivation: bind-address tradeoff, optional NixOS-nginx reverse-proxy integration.
- `docs/superpowers/specs/2026-05-08-datastar-migration-design.md` — current dashboard substrate (datastar + SSE + templ). Read this before touching the dashboard.

**Device backend interface (v1.2):**
- `docs/superpowers/specs/2026-05-08-device-backend-interface-design.md` — design for the in-process `*breezy.MemClient` (the seam this CLAUDE.md describes under "Device backend").

Implementation plans live in `docs/superpowers/plans/`; plans for shipped-and-stable subsystems have been pruned. Designs are the evergreen reference.

## Out of scope (deliberate, not bugs)

No schedule editing, no WiFi reconfig, no MQTT bridge, no Home Assistant component. The state cache is shaped so a bridge could be added without rewriting the core, but adding any of these is a separate conversation, not a fix to existing scope. (The web UI was originally on this list and got shipped in 1.1 — see CHANGELOG.md.)

## Release plumbing

- `goreleaser` builds cross-platform archives on tag pushes. Build metadata (`version`, `commit`, `date`) is injected via `-ldflags` into both binaries' `main` package. The Nix derivation deliberately omits `-X main.date=…` for reproducibility.
- `nix/module.nix` exposes a NixOS service with hardened systemd settings; inline `settings` end up in the world-readable Nix store, so production deployments must use `configFile` with sops-nix/agenix for real device passwords. The module has three opt-in integrations that mirror each other in shape: `services.breezyd.prometheus.{enable,jobName,scrapeInterval}` auto-registers a Prometheus scrape job when both services are enabled; `services.breezyd.nginx.{enable,virtualHost,basicAuthFile}` attaches a `proxy_pass` location to a named virtual host so the dashboard can be exposed on the LAN while the daemon stays loopback-bound; `services.breezyd.homekit.{enable,port,bridgeName,stateDir}` enables the HomeKit bridge and manages the state directory and optional firewall port.
