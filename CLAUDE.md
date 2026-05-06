# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build, test, lint

```sh
just build           # go build -> ./breezyd ./breezy
just test            # go test ./...               (fast, no race)
just test-race       # go test -race ./...         (cgo+clang; the CI command)
just test-race-flake # go test -race -count=5 ./...(heisen-race shaker)
just test-msan       # go test -msan ./...         (cgo+clang; uninit-memory reads)
just test-asan       # go test -asan ./...         (cgo+clang; OOB / UAF)
just test-staticcheck# golangci-lint run ./...     (errcheck is the strict bit)
just test-ui         # Playwright e2e (needs test-ui-install once)
just lint            # go vet + gofmt-drift check
just check           # lint + fast tests           (pre-commit gate)
just check-all       # lint + test + test-race + test-ui (pre-push gate)
just check-deep      # race-flake + msan + asan + staticcheck (~3 min; pre-tag)
just tidy            # go mod tidy
just clean           # remove binaries, test cache, Playwright artifacts
just fmt             # gofmt -w .
just nix-check       # parse-check nix/module.nix
```

`just test-race` (and `-msan` / `-asan` / `test-race-flake`) bake in `CGO_ENABLED=1 CC=clang` because the default `gcc` on this host lacks the TSan / MSan / ASan runtimes.

When in doubt about which gate to run: `check` is the fast pre-commit; `check-all` is the comprehensive pre-push (adds race + Playwright); `check-deep` is the slow paranoid sweep before tagging a release or after risky concurrency / cgo / unsafe edits. When editing `nix/module.nix`, `nix-check` is the fast syntax probe — `nix build` is the heavy alternative.

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
2. **`cmd/breezyd`** — long-running daemon. Owns *all* UDP traffic, polls every configured device, caches snapshots, exposes JSON HTTP + Prometheus `/metrics`. Also serves an embedded single-page dashboard from `cmd/breezyd/ui/index.html` (HTML + inlined CSS/JS) at `GET /{$}` — the `{$}` anchor is load-bearing: a plain `GET /` pattern would catch every unmatched URL and silently turn API typos into HTML responses. The handler lives in `cmd/breezyd/ui.go` and is one `go:embed` plus 12 lines of `http.HandlerFunc`.
3. **`cmd/breezy`** — CLI. Defaults to standalone mode (UDP directly to each configured device via `pkg/breezy/ops`). Opts into daemon mode when `--daemon URL` is passed or `[daemon].listen` is set in config. `breezy discover` always broadcasts on the LAN directly, independent of mode. `Discover()` enumerates every up, non-loopback IPv4 interface and sends to its directed-broadcast address in addition to a static fallback list — relevant when a host isn't on `192.168.0/1.0/24`.

`internal/config` is the shared TOML loader. `pkg/breezy/fakedevice` is an in-process UDP server that replays a captured snapshot — every non-integration Go test runs against it. `tests/ui/` is a separate pnpm-managed Playwright suite (`@playwright/test`) that tests the embedded dashboard's HTTP-call contract via `page.route()` mocking — no real daemon involved. `tests/ui/screenshots/` holds committed PNGs that re-render on `just screenshot`; the README embeds the 3-col one.

### Standalone mode (default)

The CLI also runs without the daemon — opening UDP per-invocation via `pkg/breezy/ops` against each configured device. This is the default; daemon mode is opt-in via `--daemon URL` or `[daemon].listen` in config. Within a single CLI invocation, `pkg/breezy.Client` serialises UDP behind a mutex. Across multiple CLI invocations against the same device, no coordination exists — the same hazard `discover` has applies. If users script parallel invocations against the same device, they should run the daemon.

### Why a daemon owns UDP

Concurrent UDP request/response with checksums isn't safe to fan out from independent CLI invocations: overlapping retries and packet collisions cause silent corruption. `breezyd` serialises traffic per device behind a `sync.Mutex` in `pkg/breezy.Client` and a single per-device poller goroutine. The CLI in standalone mode opens its own UDP socket for the duration of the command, which is safe for a single sequential invocation but unsafe if multiple processes run concurrently against the same device.

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

### Cache vs. passthrough

- `GET /v1/devices`, `GET /v1/devices/{name}`, `/metrics` → in-memory cache populated by the poller. Reads are cheap and never block on UDP.
- `GET /v1/devices/{name}/params/{id}` → bypasses cache, always issues a fresh UDP read. Use for debugging.
- All writes (`POST /v1/devices/{name}/...`) hit UDP and update the cache on success.

### Fan-settle window (a real protocol quirk)

After a write to `0x02` (speed_mode), `0x44` (manual %), or `0xB7` (fan rotation), the unit takes 10–15s before `0x4A`/`0x4B` (fan RPMs) and `0x84` (air-quality status) reflect the new state. The poller suppresses reads of `fanSensitiveReads` for `fanSettleDuration = 12s` after any `fanWriteIDs` write. See `cmd/breezyd/poller.go`. **Don't shorten this window** — the protocol genuinely lies during that interval.

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

Reserved global names cannot be used as device names — the config loader rejects collisions in `internal/config`.

CLI exit codes: `0` success, `1` backend error (in daemon mode: HTTP `{"error","code"}` envelope rendered as `error: <msg> (<code>)`; in standalone mode: UDP/protocol error rendered as `error: <msg>`), `2` local usage error.

## Config

`~/.config/breezy/config.toml`, mode `0600` (loader refuses world-readable — passwords are stored cleartext, and the device leaks them anyway, so this is the floor). Same file is read by daemon and CLI; CLI only consumes `[daemon].listen`.

`discovery` is one of `"on-start"`, `"off"`, or `"periodic:<duration>"`.

**First-run bootstrap:** when `breezyd` is started against a config path that doesn't exist, it writes a sensible default at that path (mode 0600, parent dir at 0700 if missing) via atomic temp+rename, then exits non-zero with an "edit it" message. See `internal/config.WriteDefault` and the gate at the top of `cmd/breezyd/main.go`'s `run`. The bootstrap path is gated specifically on `errors.Is(err, os.ErrNotExist)` — other Load failures (bad TOML, wrong mode, etc.) still surface unchanged.

## Spec & design docs

- `docs/superpowers/specs/2026-05-03-twinfresh-cli-design.md` — v1.0 design doc covering protocol decisions, daemon architecture, error semantics, status-line format. The closest thing to a "why" for the core.
- `docs/superpowers/specs/2026-05-03-param-map.md` — every parameter ID with type, units, observed values.
- `docs/superpowers/specs/breezy-manual-vendor.pdf` — vendor protocol manual.
- `docs/superpowers/specs/2026-05-04-basic-ui-design.md` — v1.1 design doc for the embedded dashboard, the bind-address tradeoff, and the optional NixOS-nginx reverse-proxy integration.
- `docs/superpowers/specs/2026-05-04-justfile-migration-design.md` — design notes for the Make → just transition.
- `docs/superpowers/specs/2026-05-04-discover-investigation.md` — analysis of the two unrelated causes behind `breezy discover` failures (the code defect, fixed in v1.1; and the QEMU-NAT environmental constraint, documented).
- `docs/superpowers/plans/2026-05-03-twinfresh-cli.md` — original implementation plan; matches the v1.0 scope.
- `docs/superpowers/plans/2026-05-04-basic-ui.md` — eight-task implementation plan executed for v1.1 (web UI, controls, nginx integration, docs, Playwright tests, screenshots, config bootstrap, discover diagnosis).

## Out of scope (deliberate, not bugs)

No schedule editing, no WiFi reconfig, no MQTT bridge, no Home Assistant component. The state cache is shaped so a bridge could be added without rewriting the core, but adding any of these is a separate conversation, not a fix to existing scope. (The web UI was originally on this list and got shipped in 1.1 — see CHANGELOG.md.)

## Release plumbing

- `goreleaser` builds cross-platform archives on tag pushes. Build metadata (`version`, `commit`, `date`) is injected via `-ldflags` into both binaries' `main` package. The Nix derivation deliberately omits `-X main.date=…` for reproducibility.
- `nix/module.nix` exposes a NixOS service with hardened systemd settings; inline `settings` end up in the world-readable Nix store, so production deployments must use `configFile` with sops-nix/agenix for real device passwords. The module has three opt-in integrations that mirror each other in shape: `services.breezyd.prometheus.{enable,jobName,scrapeInterval}` auto-registers a Prometheus scrape job when both services are enabled; `services.breezyd.nginx.{enable,virtualHost,basicAuthFile}` attaches a `proxy_pass` location to a named virtual host so the dashboard can be exposed on the LAN while the daemon stays loopback-bound; `services.breezyd.homekit.{enable,port,bridgeName,stateDir}` enables the HomeKit bridge and manages the state directory and optional firewall port.
