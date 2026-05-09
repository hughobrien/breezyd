# Design: separate UI from device comms via in-process DeviceClient

**Status:** approved 2026-05-08 (revised same day to Path A — simpler implementation)
**Tracks:** v1.2 architecture

## Problem

Today the dashboard, action handlers, poller, HomeKit bridge, and scheduler all reach `pkg/breezy.Client` (or `pkg/breezy/ops.X` over a `breezy.DeviceClient`) backed by a real UDP socket. To run or test the UI you need a real UDP peer — the `pkg/breezy/fakedevice` server.

Symptoms:

- `just screenshot` and `just test-ui` spawn one breezyd plus three fakedevice UDP servers. Aborted runs leak orphan processes that hold loopback ports.
- Working on the dashboard locally requires either real hardware (project rule against unsanctioned writes makes this risky) or replicating the fakedevice spawn dance manually.
- The Playwright suite ships the `cmd/fakedevice` admin binary just to mutate device state mid-test.

## Goal

UI development and Playwright tests run against a single in-process binary with no UDP, no spawn ceremony, and no orphans. Production keeps UDP.

## Non-goals

- Replacing `pkg/breezy/fakedevice`. It still earns its keep as a UDP fixture for protocol tests in `pkg/breezy/...` and `cmd/breezyd/poller_test.go`. After this change it just stops being part of the UI test surface.
- Changing the wire protocol or the `Snapshot` type.
- HomeKit / scheduler / energy-tracking behavioural changes — they continue to call ops verbs through the same `breezy.DeviceClient` interface.

## Design

The codebase already has the right seam: `breezy.DeviceClient` is an interface (`ReadParams` + `WriteParams`) that handlers, poller, HomeKit, and scheduler all reach through. The UDP `*Client` is one implementation. The plan adds a second.

### MemClient — in-process DeviceClient

New file `pkg/breezy/memclient.go`. Holds `map[ParamID][]byte` (matching how the fakedevice already models device state) behind a `sync.RWMutex`. Implements `DeviceClient`:

```go
type MemClient struct {
    mu     sync.RWMutex
    params map[ParamID][]byte

    // Fault-injection (test admin surface).
    forceAuthErr bool
    forceTimeout bool
}

func NewMemClient() *MemClient
func NewMemClientFromFile(path string) (*MemClient, error)  // loads JSON map[ParamID][]byte

func (m *MemClient) ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error)
func (m *MemClient) WriteParams(ctx context.Context, writes []ParamWrite) error
func (m *MemClient) Close() error  // no-op; kept to satisfy callers that defer Close
```

Reads return a snapshot of the requested IDs. Writes mutate `params`. Both are O(n) over the request size; concurrency-safe.

#### Fault-injection knobs

```go
func (m *MemClient) SetAuthFailureMode(force bool)  // returns ErrAuth on next call
func (m *MemClient) SetTimeoutMode(force bool)      // returns ErrTimeout on next call
func (m *MemClient) SetParamValue(id ParamID, b []byte)  // direct mutation
func (m *MemClient) Reset()                          // restore to construction-time state
```

These mirror the existing `fakedevice.Server` admin surface. Tests for the memory path use them; production never sets them.

### Seed format

The seed file is the same JSON shape `pkg/breezy/fakedevice` already loads — `map[ParamID][]byte` — so existing fixtures (`pkg/breezy/fakedevice/snapshot_148.json`) work unchanged.

### Wiring in `cmd/breezyd`

- New flag `--backend=udp|memory` (default `udp`). With `memory`: optional `--seed <path>` to preload params (otherwise empty client per device).
- `main.go` chooses the constructor: existing `breezy.NewClient(addr)` for UDP, `breezy.NewMemClientFromFile(seed)` for memory.
- The constructor lives behind a small `clientFactory(name string) (breezy.DeviceClient, error)` closure so call sites are unchanged.

### Fan-settle suppression

The fan-settle window is a UDP-protocol fact. The poller currently suppresses reads of `fanSensitiveReads` for 12s after writes to `fanWriteIDs`. With a MemClient there's nothing to suppress — writes land instantly and reads see them.

Implementation: `breezy.DeviceClient` grows a `IsLocal() bool` method. UDP `*Client` returns `false`; `*MemClient` returns `true`. The poller's fan-settle gate becomes `if !client.IsLocal() { …suppress… }`.

This is a tiny addition rather than a wholesale interface split. Documented behavioural difference: when `--backend=memory`, the dashboard reflects writes immediately (no settling). UI tests that need to assert on fan-settle behaviour stay in Go against the UDP fakedevice (one such test today; see migration table).

### Test admin endpoints

New file `cmd/breezyd/handlers_test_admin.go`, behind build tag `breezyd_test_admin`. Exposes:

- `POST /test/devices/{name}/params/{id}` — body: `{"value":[byte,byte,…]}` → `client.SetParamValue`
- `POST /test/devices/{name}/inject-error` — body: `{"kind":"auth"|"timeout"|"none"}`
- `POST /test/devices/{name}/reset`

These handlers cast the device's `breezy.DeviceClient` to `*breezy.MemClient`; if the cast fails (i.e., `--backend=udp`), they return 400. Routes are only registered when the build tag is set, so production never has the surface.

### Test surface impact

| Surface | Before | After |
|---|---|---|
| `pkg/breezy/...` Go tests | fakedevice (UDP) | unchanged |
| `cmd/breezyd/poller_test.go` | fakedevice (UDP) | unchanged — fan-settle path stays here |
| `cmd/breezyd/handlers_*_test.go` | fakedevice (UDP) | mostly unchanged; some can switch to MemClient for speed |
| Playwright `tests/ui/...` | fakedevice + breezyd (admin tag) | one breezyd `--backend=memory --seed <fixture.json>` (admin tag) |
| `just screenshot` | fakedevice + breezyd (admin tag) | one breezyd `--backend=memory --seed <fixture.json>` |
| `cmd/fakedevice` admin binary | spawned by Playwright | retired |
| Local UI dev | requires fakedevice spawn | `breezyd --backend=memory --seed <fixture>.json` |

### Playwright fault-injection migration

`tests/ui/fixtures.ts` currently exposes five hooks against the `cmd/fakedevice` admin HTTP surface. Each maps cleanly:

| Fixture hook | Today | After |
|---|---|---|
| `setDeviceState` | POST to fakedevice admin → fakedevice rewrites a param | POST to breezyd's `/test/devices/{name}/params/{id}` |
| `simulateAuthFailure` | fakedevice flips its `forceAuthErr` flag | POST `/test/devices/{name}/inject-error` `{"kind":"auth"}` |
| `simulateUDPTimeout` | fakedevice swallows next packet | POST `/test/devices/{name}/inject-error` `{"kind":"timeout"}` |
| `reset` | fakedevice reloads seed | POST `/test/devices/{name}/reset` |
| `simulateFanSettle` | fakedevice returns stale fan readings | **does not port** — port the one consumer test to Go (`poller_test.go`) against the UDP fakedevice |

The TypeScript surface of `fixtures.ts` is unchanged; only its base URL moves from `BREEZYD_ADMIN_URL` to `BREEZYD_URL` (the same daemon now exposes `/test/...`).

## Out of scope (explicit)

- Raw `GET/PUT /v1/devices/{name}/params/{id}` debug endpoint stays as-is. Reads work through the same MemClient. Writes work too — MemClient's `WriteParams` accepts any param ID.
- Energy tracking — operates on `Snapshot`s; no change.
- Schedule — fires writes through the same `DeviceClient`; works unchanged.

## Effort

Single PR:

| Section | Approx |
|---|---|
| `memclient.go` + tests | 250 + 200 |
| `--backend` / `--seed` wiring in `main.go` | 50 |
| `IsLocal()` plumbing on poller's fan-settle gate | 30 |
| `handlers_test_admin.go` (build-tagged) | 100 |
| Playwright migration (`global-setup.ts`, `fixtures.ts`) | 50 changed, 100 removed |
| Move one Playwright test to Go (`poller_test.go`) | 80 |
| Retire `cmd/fakedevice` admin binary + build tag | -300 |
| `screenshot.ts` migration | 40 changed, 60 removed |

Net diff: roughly +400/-300, mostly in new code that lives next to the existing `Client` and is exercised by tests. Reviewable in one sitting.

## Migration

One PR, ordered so each commit compiles cleanly:

1. Add `MemClient` + tests.
2. Add `IsLocal()` on `DeviceClient` and the poller's gated suppression.
3. Add `--backend` / `--seed` flags and wiring.
4. Add build-tagged `/test/...` admin handlers.
5. Migrate Playwright (`global-setup.ts`, `fixtures.ts`, drop fakedevice spawn).
6. Port the fan-settle Playwright test to Go.
7. Retire `cmd/fakedevice` admin code and its build tag.
8. Migrate `screenshot.ts`.

## Risk

- MemClient diverging from real device behaviour. Mitigated: MemClient models user-observable state (param map round-trips), not protocol responses; protocol edge cases stay tested against the UDP fakedevice.
- A Playwright test that depended on UDP-specific behaviour (e.g., fan-settle staleness) and isn't already covered in Go. Mitigated: surveyed `tests/ui/fixtures.ts`; the only such hook is `simulateFanSettle`, with a planned port in Task 6.
- Loss of UDP→cache→dashboard end-to-end coverage in the UI suite. Acceptable — that path is fully covered by `cmd/breezyd/poller_test.go` against fakedevice. UI tests that pretend to test it were already a redundant layer.
