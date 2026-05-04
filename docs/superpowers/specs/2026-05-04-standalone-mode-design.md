# Standalone CLI mode — design

**Date:** 2026-05-04
**Status:** approved for implementation
**Repo:** `~/twinfresh`
**Issue:** [#2 — Standalone Mode](https://github.com/hughobrien/breezyd/issues/2)

## Summary

Make the `breezy` CLI usable without `breezyd`. By default — when no daemon is configured — the CLI talks UDP directly to the device via `pkg/breezy.Client`, using device entries from `~/.config/breezy/config.toml`. When a daemon **is** configured (either through `--daemon URL` or `[daemon].listen` in the config), the CLI uses the existing HTTP path. The two paths share their per-verb logic by extracting it to a new `pkg/breezy/ops.go` library that both the daemon's HTTP handlers and the CLI's standalone backend call.

## Motivation

Today the CLI is 100% daemon-coupled — every verb except `discover` issues HTTP to `breezyd` (63 daemon-URL/HTTP references in `cmd/breezy/`). That works for users running the daemon, but two real use cases are unserved:

- **No-install / first-run.** A user clones the repo, runs `breezy discover`, copies the device IDs into a `config.toml`, and reasonably expects `breezy playroom status` to *just work*. Today they get connection-refused unless they also start the daemon.
- **Lightweight / portable.** Users who only ever issue ad-hoc commands have no use for a long-running daemon, the embedded dashboard, or `/metrics`. The cost of running `systemd --user start breezyd` is small but non-zero, and the daemon's persistence is overhead they don't need.

The project has not been deployed yet, so reversing the default (standalone first, daemon opt-in) does not break anyone.

## Non-goals

- Not relaxing the daemon's role for users who *do* run it. The daemon remains the only place caching, polling, the web UI, fan-settle suppression, and Prometheus metrics live.
- Not adding any CLI feature that's only available in standalone mode. The two paths produce equivalent outputs for the same inputs, modulo the `ls` distinction (see below).
- Not changing how the daemon is configured, packaged, or run.
- Not protecting against simultaneous CLI invocations against the same device. That hazard exists today via `discover` and is acknowledged with a doc note, not a code fix.

## Approach

Two backends, one CLI surface. Verb dispatch in `cmd/breezy/main.go` is unchanged; each per-verb handler in `commands.go` calls a `backend` interface method instead of `httpJSON` directly. Two implementations:

- `daemonBackend` — wraps the existing HTTP plumbing. Today's behavior, isolated.
- `directBackend` — opens a `*breezy.Client` per device on demand, calls high-level operations from `pkg/breezy/ops.go`.

The daemon's existing per-verb HTTP handlers are rewritten to call the same `pkg/breezy/ops` functions (so the protocol logic — e.g. "manual speed writes `0x44` then `0x02=0xFF` in one packet" — has a single home).

### `pkg/breezy/ops.go`

A new file in the existing `pkg/breezy` package. High-level operations against a small `Client` interface:

```go
// Client is the minimal subset of *breezy.Client that pkg/breezy/ops
// needs. The concrete *breezy.Client satisfies it; tests and the
// daemon's recording wrapper substitute their own implementations.
type Client interface {
    ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error)
    WriteParams(ctx context.Context, writes []ParamWrite) error
}
```

Operations (in declaration order, matching the CLI verb surface):

```go
func Power(ctx context.Context, c Client, on bool) error
func SetSpeedPreset(ctx context.Context, c Client, preset int) error // 1..3
func SetSpeedManual(ctx context.Context, c Client, pct int) error    // 10..100
func SetMode(ctx context.Context, c Client, mode string) error       // ventilation|regeneration|supply|extract
func SetHeater(ctx context.Context, c Client, on bool) error
func ResetFilter(ctx context.Context, c Client) error
func ResetFaults(ctx context.Context, c Client) error
func SetRTC(ctx context.Context, c Client, t time.Time) error
func GetFirmware(ctx context.Context, c Client) (FirmwareMetaValue, error)
func GetEfficiency(ctx context.Context, c Client) (int, error)
func GetFaults(ctx context.Context, c Client) ([]FaultCode, error)
func GetStatus(ctx context.Context, c Client) (Snapshot, error)
```

Each function:

1. Validates inputs (range checks, mode-string mapping, etc.). Returns a typed `error` rooted in `pkg/breezy` for callers to classify.
2. Builds the appropriate `[]ParamWrite` (writes) or calls `ReadParams` (reads).
3. For reads, decodes via the existing typed `Value` machinery in `params.go`.

`Snapshot` currently lives in `cmd/breezyd/state.go`; it migrates to `pkg/breezy` as part of this work so `GetStatus` can return it. The daemon imports it from there afterwards. `FaultCode` is a *new* type introduced by `pkg/breezy/ops.go` — today the daemon emits faults as anonymous `[]map[string]any` (see `cmd/breezyd/handlers_service.go:54`); we replace that with a typed `[]FaultCode{Code int, Kind string}` so the ops layer and CLI both speak the same shape.

### Daemon recording wrapper

The daemon today calls `recordWrite(name, writes)` after every successful write to suppress fan-settle re-reads in the poller. To preserve that without requiring `pkg/breezy/ops` to know about the cache, wrap the client at the daemon's edge:

```go
// recordingClient implements pkg/breezy.Client by delegating to an
// inner client and notifying a callback on every successful write.
// The poller uses the callback to register fan-settle suppression.
type recordingClient struct {
    inner  breezy.Client
    record func([]breezy.ParamWrite)
}

func (r *recordingClient) ReadParams(ctx context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
    return r.inner.ReadParams(ctx, ids)
}
func (r *recordingClient) WriteParams(ctx context.Context, writes []breezy.ParamWrite) error {
    if err := r.inner.WriteParams(ctx, writes); err != nil {
        return err
    }
    r.record(writes)
    return nil
}
```

`Handler.dial(name)` returns a `*recordingClient` instead of a raw `*breezy.Client`. Ops never know.

### `cmd/breezy/backend.go`

New file. Defines:

```go
type backend interface {
    Status(ctx context.Context, name string) (snapshotResp, error)
    Power(ctx context.Context, name string, on bool) error
    SpeedPreset(ctx context.Context, name string, preset int) error
    SpeedManual(ctx context.Context, name string, pct int) error
    Mode(ctx context.Context, name string, mode string) error
    Heater(ctx context.Context, name string, on bool) error
    ResetFilter(ctx context.Context, name string) error
    ResetFaults(ctx context.Context, name string) error
    Faults(ctx context.Context, name string) ([]fault, error)
    Firmware(ctx context.Context, name string) (string, string, error) // version, buildDate
    Efficiency(ctx context.Context, name string) (int, error)
    RTC(ctx context.Context, name string) (string, string, error) // dateStr, timeStr
    SetRTC(ctx context.Context, name string, t time.Time) error
    GetParam(ctx context.Context, name string, id breezy.ParamID) ([]byte, error)
    SetParam(ctx context.Context, name string, id breezy.ParamID, value []byte) error
    Devices(ctx context.Context) ([]lsRow, error)
    DaemonURLString() string // "" in standalone, the URL in daemon mode
}
```

Two implementations in the same file:

- `daemonBackend{url string}` — the existing HTTP plumbing in `commands.go` lifts behind these methods. `httpJSON` and friends move to `backend.go` or stay in `main.go` — choose at edit time based on what reads cleanest.
- `directBackend{devices map[string]config.Device}` — opens a `*breezy.Client` per call (or caches per name; see "Lifecycle" below) and calls `pkg/breezy/ops`.

Per-verb handlers in `commands.go` shrink to: parse args → call `b.Foo(...)` → render. The HTTP and validation specifics that today live in those functions migrate either into `backend.go` (for daemon-side details) or `pkg/breezy/ops.go` (for protocol details).

### Backend resolution

In `main.go`'s `run`, after flag parsing but before dispatch:

```go
b, err := resolveBackend(*daemon, cfg)
if err != nil {
    fmt.Fprintln(stderr, "error:", err)
    return 1
}
```

`resolveBackend(override string, cfg *config.Config) (backend, error)`:

1. If `override != ""`: return `&daemonBackend{normalizeURL(override)}`.
2. Else if `cfg != nil && cfg.Daemon.Listen != ""`: return `&daemonBackend{normalizeURL(cfg.Daemon.Listen)}`.
3. Else: return `&directBackend{cfg.Devices}`.

There is no fallback if daemon mode is opted in but the daemon is unreachable. The user sees a clear error from the first HTTP attempt: `error: daemon at 127.0.0.1:9876 unreachable: connection refused`. That's a deliberate consequence of the user choosing daemon mode — we don't silently route writes around it.

`defaultDaemonURL` (`http://127.0.0.1:9876`) is removed.

### Lifecycle in `directBackend`

`*breezy.Client` holds an open UDP socket. The CLI is a one-shot process: every invocation opens, does its work, and exits. Two reasonable strategies:

- **Open-per-call.** Every backend method opens, uses, closes a fresh client. Simple. Status (~20 reads in one packet via `ReadParams`) is one open; multi-step verbs may pay multiple opens.
- **Lazy + memoize per name.** First call to a given device opens a client, stashes it on `directBackend`. `directBackend.Close()` (called from `main.run`'s defer) closes any open ones.

We use **lazy + memoize**. Most verbs touch a single device; this gives one open/close per device per invocation. The bookkeeping is small (a `map[string]*breezy.Client` plus a `Close()`).

### `ls` in standalone

`directBackend.Devices(ctx)` returns one `lsRow` per device in config — `Name`, `ID`, `IP`. Power/Mode/LastPoll fields are zero-valued (`Power *bool` stays `nil`, `Mode` stays `""`). `renderLs` already substitutes `?` for empty `Mode` and unknown `Power`; `LastPoll` empty already prints `never`. The output reads honestly:

```
NAME      ID                  IP                POWER  MODE  LAST POLL
bedroom   BREEZY00000000A1    192.168.1.152     ?      ?     never
office    BREEZY00000000A2    192.168.1.160     ?      ?     never
playroom  BREEZY00000000A0    192.168.1.148     ?      ?     never
```

A second-pass enhancement (out of scope): if `LastPoll` is empty AND we're in standalone, render the `LAST POLL` column as `(no daemon)` instead of `never`. Decide during implementation if it's worth it.

### `daemon-url` global

`backend.DaemonURLString()` returns `""` for `directBackend`. The CLI prints `(standalone — no daemon)` in that case rather than the URL.

## Concurrency

`pkg/breezy.Client` already serializes per-Client UDP I/O behind a mutex (`pkg/breezy/client.go`). Within a single CLI invocation there is no race.

The only hazard introduced by standalone mode is the same one `discover` already has: two `breezy` processes (or one CLI + the daemon) issuing UDP to the *same* device at the same instant. Overlapping requests can produce silent checksum corruption. We don't fix this in code; we document it:

> If you script multiple `breezy` invocations against the same device in parallel — or run a CLI command while the daemon is actively polling that device — run the daemon and use the CLI in daemon mode. The daemon serializes per-device UDP behind a mutex; standalone CLI processes do not coordinate with each other.

This text goes in README's CLI surface section and in CLAUDE.md's Architecture section.

## Migration

The project has no deployed users. Behavior change: a config without `[daemon].listen` no longer falls through to `127.0.0.1:9876`. CHANGELOG entry under `[Unreleased]`:

> **Breaking:** the CLI defaults to standalone mode (direct UDP) when no daemon is configured. Previously it tried `http://127.0.0.1:9876`. To keep the old behavior, set `[daemon] listen = "127.0.0.1:9876"` in `~/.config/breezy/config.toml` or pass `--daemon http://127.0.0.1:9876`.

The first-run config bootstrap (`internal/config.WriteDefault` at `internal/config/config.go:182`) currently emits an active `[daemon]` block with `listen = "127.0.0.1:9876"`. Phase 2 rewrites the template so daemon mode is **opt-in**: the entire `[daemon]` block is commented out by default, with a one-line comment explaining how to enable it. New first-run users land in standalone, which matches the motivation.

The bootstrap test in `internal/config/config_test.go` that currently asserts the active `listen = "127.0.0.1:9876"` line (line 302) needs updating in lockstep.

## Testing

Two layers:

1. **`pkg/breezy/ops_test.go`.** Table-driven coverage for every op against `pkg/breezy/fakedevice` (already exists; the daemon poller tests already use it). Each op gets at least one happy-path case plus its validation failures. The protocol invariants (e.g. order of `0x44`/`0x02` writes for manual speed) are asserted by capturing the ParamWrite slice with a recording client in front of the fakedevice.
2. **`cmd/breezy/main_test.go`.** Today's tests use `runCLI(t, srv, ...)` against an `httptest.Server`; that exercises `daemonBackend`. Add a parallel helper `runStandalone(t, fake, ...)` that builds a `directBackend` pointed at a `fakedevice` instance. Critically: the same black-box assertions (stdout shape, exit codes, error envelopes) should pass against both. That's the strongest signal that the refactor preserved behavior.

Daemon handler tests in `cmd/breezyd/` shrink to verifying the JSON parsing layer, the recording wrapper, and the cache-update plumbing — the protocol logic moved to `pkg/breezy/ops` tests.

`just check-all` (lint + race + UI) is the merge gate.

## Phasing

Two shippable phases. Each is its own implementation plan; the spec covers both.

### Phase 1 — extract `pkg/breezy/ops` and refactor the daemon

No CLI changes; no user-visible behavior change.

- Create `pkg/breezy/ops.go` with every operation from the list above.
- Move `Snapshot`, `FaultCode`, and any other types currently in `cmd/breezyd/` that the ops need into `pkg/breezy/`.
- Add `recordingClient` in `cmd/breezyd/`.
- Rewrite `handlers_device.go` and `handlers_service.go` to call ops + recording wrapper. Each handler ends up ~8 lines.
- Move daemon handler tests that exercised protocol logic into `pkg/breezy/ops_test.go`. Keep handler tests for JSON parsing and cache plumbing.
- `just check-all` clean.

This phase is the riskiest part of the refactor and proves the ops layer in production before any CLI relies on it. If something goes wrong, the changes are mechanical and easy to revert.

### Phase 2 — backend interface and standalone path

User-visible: standalone mode lights up.

- Create `cmd/breezy/backend.go` with the `backend` interface and both implementations.
- Rewrite `cmd/breezy/commands.go` to delegate to `backend` methods.
- Replace `resolveDaemonURL` with `resolveBackend`. Remove `defaultDaemonURL`.
- Update `daemon-url` global to handle the standalone case.
- Update `cmdLs` rendering for `directBackend.Devices` (zero-valued live fields).
- Add `runStandalone` test helper and dual-backend tests for every CLI verb.
- README and CLAUDE.md updates: standalone-by-default, when to use daemon, concurrency note.
- CHANGELOG `[Unreleased]` entry.
- Update `internal/config.WriteDefault` if we change the default config to have `[daemon].listen` commented out.

After Phase 2 lands, Issue #2 closes.

## Out of scope (deliberate)

- Auto-fallback when daemon is configured but unreachable (rejected in brainstorming Q2). If the user configured a daemon, they want it; we surface the error rather than silently take a different path.
- A `--standalone` override flag for one-off invocations when daemon is configured (Q4). YAGNI.
- Caching, polling, fan-settle suppression, web UI, `/metrics` in standalone mode. Those remain daemon-only.
- Cross-process UDP coordination. The user reaches for the daemon if they need it.
- Standalone-only verbs. The two backends produce the same outputs from the same verbs.

## Verification

After Phase 1:
- `just check-all` passes.
- The daemon's existing CLI test cases (`cmd/breezyd/server_test.go`, etc.) still pass against the rewritten handlers.
- Live integration tests (`just test-integration`) still pass.

After Phase 2:
- `breezy --version` works without a daemon configured (no daemon URL resolution needed for that path).
- `breezy ls` against a config with three devices and no `[daemon].listen` prints the device table with empty live columns.
- `breezy playroom status` against a real device, no daemon running, returns the same `status` output as the daemon-mediated path.
- `breezy --daemon http://127.0.0.1:9876 playroom status` with the daemon running returns the daemon's cached state.
- `BREEZY_INTEGRATION=1` integration tests pass.
- `just check-all` passes.
