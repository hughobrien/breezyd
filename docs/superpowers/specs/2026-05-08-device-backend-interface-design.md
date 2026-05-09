# Design: separate UI from device comms via DeviceBackend interface

**Status:** approved 2026-05-08
**Tracks:** v1.2 architecture

## Problem

Today the dashboard, action handlers, poller, HomeKit bridge, and scheduler all import `pkg/breezy/ops` directly. To run or test the UI you need a real UDP peer — the `pkg/breezy/fakedevice` server. This conflates two unrelated concerns:

1. **Rendering the dashboard from snapshots** (templ + datastar + SSE).
2. **Talking to a Vents Twinfresh over FDFD/02 UDP** (frame, checksum, retry, fan-settle window).

Symptoms in practice:

- `just screenshot` and `just test-ui` spawn one breezyd plus three fakedevice UDP servers. An aborted run leaks orphan processes that hold loopback ports — every UI dev cycle risks tripping the same orphan-cleanup recipe.
- Working on the dashboard locally requires either running against real hardware (per the project rule against unsanctioned writes, this is risky) or replicating the fakedevice spawn dance by hand.
- The protocol-specific concerns (auth retries, fan-settle suppression) bleed into handler code that has no business knowing about them.

## Goal

A clean seam between "what the dashboard does" and "how a device is actually controlled." Production keeps the UDP path. UI development and Playwright tests run against a single in-process binary with no UDP, no spawn ceremony, and no orphans.

## Non-goals

- Replacing the `pkg/breezy/fakedevice` UDP server. It still earns its keep as a fixture for the Go protocol tests in `pkg/breezy/...` and `cmd/breezyd/...`. After this change it just stops being part of the UI test surface.
- Changing the wire protocol or the Snapshot type.
- HomeKit or scheduler behavioural changes — they continue to call the same verbs, just through the new interface.

## Design

### The interface

A new file `pkg/breezy/backend.go` defines:

```go
type DeviceBackend interface {
    // Devices returns the configured-device map keyed by name.
    Devices() map[string]Device

    // Snapshot returns the current state of a single device. The backend
    // owns staleness — UDP returns the last successful poll; memory
    // returns its in-memory state.
    Snapshot(ctx context.Context, name string) (Snapshot, error)

    // Mutations. Each verb mirrors a function in pkg/breezy/ops. Returns
    // ErrAuth on 0x07; backend-specific errors otherwise. The action handlers
    // do not need to know which backend they hit.
    Power(ctx context.Context, name string, on bool) error
    Speed(ctx context.Context, name string, mode SpeedMode, manualPct int) error
    Mode(ctx context.Context, name string, mode AirflowMode) error
    Heater(ctx context.Context, name string, on bool) error
    Timer(ctx context.Context, name string, mode TimerMode) error
    Threshold(ctx context.Context, name string, sensor SensorKind, value int) error
    Preset(ctx context.Context, name string, n int, supply, extract int) error
    ResetFilter(ctx context.Context, name string) error
    ResetFaults(ctx context.Context, name string) error
    SetRTC(ctx context.Context, name string, t time.Time) error
}
```

Verb naming matches the existing `pkg/breezy/ops` functions. The `Devices()` accessor exists because the poller and config validation need the configured-device map without going through the cache.

### UDP backend

`pkg/breezy/udpbackend.go`. Wraps the existing `*Client` per device plus the per-device mutex and fan-settle window state currently scattered in `cmd/breezyd/poller.go`. Each method delegates to the matching `pkg/breezy/ops.X` function. Roughly:

```go
type udpBackend struct {
    devices  map[string]Device
    clients  map[string]*Client       // one per device, lazy
    mu       map[string]*sync.Mutex   // serialises writes per device
    settle   map[string]time.Time     // fan-settle suppression deadline
}

func (b *udpBackend) Power(ctx context.Context, name string, on bool) error {
    cli, mu, err := b.client(name)
    if err != nil { return err }
    mu.Lock(); defer mu.Unlock()
    return ops.Power(ctx, cli, on)
}
```

The fan-settle window logic stays here, not in the poller — that's the right home for "this protocol fact" rather than leaking into call sites.

### Memory backend

`pkg/breezy/membackend.go`. Holds `map[string]*Snapshot` behind a `sync.RWMutex`. Each method:

1. Looks up the device's snapshot.
2. Mutates the relevant field — Power → `snap.Power.Status`; Speed → `snap.SpeedMode + snap.Manual`; Mode → `snap.AirflowMode`; Threshold → `snap.Sensors.<kind>.Threshold`; etc.
3. Updates `snap.LastPoll = time.Now()`.
4. Returns nil.

Constructors:

```go
func NewMemBackend(devices map[string]Device) *memBackend          // empty snapshots
func NewMemBackendFromFile(devices, path string) (*memBackend, error) // seed JSON
```

Concurrency: per-device write lock matches UDP backend's contract. Reads return a copy of the snapshot, never the live pointer, so callers can't mutate the backend by accident.

#### Faking firmware quirks

The memory backend does **not** simulate the fan-settle window or other firmware oddities. Those exist *because* of UDP and don't belong in an in-memory model. UI tests that need to verify "stale row appears after long quiet" can manipulate the backend's `LastPoll` field via a test helper, not via simulating UDP retries.

### Wiring in `cmd/breezyd`

- New flag: `--backend=udp|memory` (default `udp`). With `memory`: optional `--seed <path.json>` to preload snapshots, otherwise empty.
- `main.go` constructs the backend once, threads it into:
  - The poller (replaces direct `ops` calls)
  - Action handlers under `/v1/...` and `/ui/devices/...`
  - The HomeKit bridge (`cmd/breezyd/homekit.go`)
  - The scheduler (`cmd/breezyd/scheduler.go`)
- The poller for the memory backend is degenerate: `for { backend.Snapshot(name); push }`. Same loop structure, no UDP timing concerns.

The mechanical refactor across consumers is roughly: replace `ops.Power(ctx, cli, on)` with `backend.Power(ctx, name, on)`. Call sites stop knowing the device's address or the per-device client.

### Test surface impact

| Surface | Before | After |
|---|---|---|
| `pkg/breezy/...` Go tests | fakedevice (UDP) | unchanged — still fakedevice |
| `cmd/breezyd/...` Go tests | fakedevice (UDP) | unchanged — still fakedevice (the poller-via-UDP path is genuinely tested here) |
| Playwright `tests/ui/...` | fakedevice (UDP) + breezyd | one breezyd `--backend=memory --seed <fixture.json>` |
| `just screenshot` | fakedevice (UDP) + breezyd | one breezyd `--backend=memory --seed <fixture.json>` |
| `cmd/fakedevice` admin binary | spawned by Playwright | retired (see Playwright fault-injection migration below) |
| Local UI dev | requires fakedevice spawn | `breezyd --backend=memory --seed <devices.json>` |

The fakedevice module shrinks to its honest job: a Go-level UDP fixture for protocol tests.

### Playwright fault-injection migration

The current `tests/ui/fixtures.ts` exposes five test hooks that talk to `cmd/fakedevice`'s admin HTTP plane: `setDeviceState`, `simulateFanSettle`, `simulateAuthFailure`, `simulateUDPTimeout`, `reset`. Each maps cleanly to the new world:

| Fixture hook | Today | After |
|---|---|---|
| `setDeviceState` | POST to fakedevice admin → fakedevice rewrites its snapshot | POST to a new build-tagged breezyd endpoint `/test/devices/{name}/snapshot` that mutates the memory backend |
| `simulateAuthFailure` | fakedevice returns 0x07 on next read | memory backend has a configurable "next call returns ErrAuth" knob, set via `/test/devices/{name}/inject-error` |
| `simulateUDPTimeout` | fakedevice swallows the next packet | same knob, returns `breezy.ErrTimeout` |
| `reset` | fakedevice reloads the seed snapshot | memory backend reloads the seed snapshot |
| `simulateFanSettle` | fakedevice returns stale fan readings for N ms | **does not port** — the fan-settle window is a UDP-protocol fact. Move the one Playwright test that uses this to a Go-level test against the UDP fakedevice. |

The new `/test/...` endpoints live behind a build tag (`backend_test_admin` or similar) so the production binary doesn't ship them. `tests/ui/fixtures.ts` keeps the same TypeScript surface; only its target URL changes.

### Out of scope (explicit)

- The raw `GET/PUT /v1/devices/{name}/params/{id}` debug endpoint. The high-level interface doesn't expose raw param read/write. Decision deferred to implementation: either drop the endpoint, or expose it only when the backend is UDP (type-assert at the handler).
- Energy tracking — operates on Snapshots, no change.
- Schedule — fires writes through the backend, works for both implementations.
- Cross-cutting: behavior catalog and golden-render tests don't change shape; they may gain a new "memory backend" suite.

## Effort

One PR, target diff:

| Section | Approx |
|---|---|
| `backend.go` interface | 60 lines |
| `udpbackend.go` (mostly forwarding) | 200 lines |
| `membackend.go` | 250 lines |
| Consumer refactor (poller, handlers, HomeKit, scheduler) | 100 lines changed |
| Tests for memory backend | 200 lines |
| Playwright migration (`tests/ui/global-setup.ts`, `screenshot.ts`) | 100 lines changed (mostly removed) |
| Retire `cmd/fakedevice` admin binary | -300 lines |

Net: roughly +500/-300, with the UDP-backend wrapper being almost entirely mechanical. Reviewable in one sitting.

## Migration

Single PR. The sequence within the PR:

1. Land `backend.go` interface and `udpbackend.go`. UDP backend passes Go test suite unchanged.
2. Refactor consumers (poller, handlers, HomeKit, scheduler) to take the backend by injection.
3. Add `membackend.go` and the `--backend=memory --seed` flag.
4. Switch Playwright + screenshot scripts to memory backend.
5. Retire `cmd/fakedevice` admin binary and its build-tagged tests.

Each step compiles cleanly on its own; the PR can be reviewed commit-by-commit if needed.

## Risk

- Behaviour drift in the UDP backend during the wrap. Mitigated by: existing Go tests gate every consumer's call paths, and the wrapper is mechanical.
- Memory-backend semantics quietly diverging from real device behaviour (e.g., a write that the firmware silently rejects but the memory backend accepts). Mitigated by: the memory backend models *user-observable state*, not protocol responses; tests for protocol edge cases stay against UDP fakedevice.
- Loss of Playwright coverage for the UDP→cache path. Acceptable — that path is fully covered by `cmd/breezyd/poller_test.go` against fakedevice, and adding it to the UI test surface was always incidental.
