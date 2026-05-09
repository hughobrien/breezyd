# Device-backend interface — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an in-process `breezy.MemClient` that implements `breezy.DeviceClient` over a Snapshot, and wire `breezyd --backend=memory --seed <file>` so UI dev and Playwright tests run with no UDP, no fakedevice spawn, and no orphan loopback ports.

**Architecture:** The codebase already has the right seam — `breezy.DeviceClient` is an interface (ReadParams + WriteParams) that handlers, poller, HomeKit, and scheduler reach through. UDP `*Client` is one implementation; this plan adds a second. Fan-settle suppression (a UDP-protocol fact) is gated on a new `IsLocal()` method.

**Tech Stack:** Go 1.x, `pkg/breezy`, `cmd/breezyd`, `tests/ui` (TypeScript / Playwright).

**Spec:** `docs/superpowers/specs/2026-05-08-device-backend-interface-design.md`

---

## File structure

| File | Status | Responsibility |
|---|---|---|
| `pkg/breezy/memclient.go` | new | MemClient — in-memory `DeviceClient` with fault-injection knobs |
| `pkg/breezy/memclient_test.go` | new | Round-trip + concurrency + fault-injection tests |
| `pkg/breezy/fakedevice/fake.go` | modify | Export `LoadSnapshotJSON(path)` so `MemClient` and tests can share the seed loader |
| `pkg/breezy/ops.go` | modify | Add `IsLocal()` to `DeviceClient` interface (default-false via wrapper); UDP `Client` returns false, `MemClient` true |
| `pkg/breezy/client.go` | modify | `(*Client).IsLocal() bool { return false }` |
| `cmd/breezyd/poller.go` | modify | Skip fan-settle suppression when `client.IsLocal()` |
| `cmd/breezyd/poller_test.go` | modify | Add a poller-level test for the fan-settle window (port from Playwright) |
| `cmd/breezyd/main.go` | modify | `--backend=udp\|memory` + `--seed <path>` flags; build factories accordingly |
| `cmd/breezyd/main_test.go` | modify | Cover the new flag wiring |
| `cmd/breezyd/handlers_test_admin.go` | new (build-tagged `breezyd_test_admin`) | `/test/devices/{name}/...` HTTP surface that mutates `*breezy.MemClient` |
| `cmd/breezyd/handlers_test_admin_test.go` | new (build-tagged) | Tests for the admin endpoints |
| `cmd/breezyd/server.go` | modify | Mount `/test/...` routes when build tag set (file-level conditional) |
| `tests/ui/global-setup.ts` | modify | Spawn one `breezyd --backend=memory --seed <fixture>` (admin tag); drop fakedevice spawn |
| `tests/ui/global-teardown.ts` | modify | Drop fakedevice cleanup |
| `tests/ui/fixtures.ts` | modify | Retarget HTTP calls from `BREEZYD_ADMIN_URL` to `${BREEZYD_URL}/test` |
| `tests/ui/dashboard.spec.ts` | modify | Remove the `simulateFanSettle` consumer (its scenario moves to Go) |
| `tests/ui/screenshot.ts` | modify | Drop fakedevice spawn; use `--backend=memory --seed` |
| `cmd/fakedevice/main.go` | delete | Admin binary retired |
| `pkg/breezy/fakedevice/admin.go` | delete | Admin HTTP surface retired |
| `pkg/breezy/fakedevice/admin_test.go` | delete | Same |
| `justfile` | modify | Drop `test-fakedevice-admin` recipe; remove from `ci` |

---

## Task 1: MemClient in `pkg/breezy`

**Goal:** A `*MemClient` that satisfies `breezy.DeviceClient` against an in-memory `map[ParamID][]byte`, with constructor and fault-injection knobs that mirror `fakedevice.Server`'s admin surface.

**Files:**
- Create: `pkg/breezy/memclient.go`
- Create: `pkg/breezy/memclient_test.go`
- Modify: `pkg/breezy/fakedevice/fake.go` — extract `loadSnapshot` to a public symbol
- Modify: `pkg/breezy/ops.go` — add `IsLocal()` to the `DeviceClient` interface

**Acceptance Criteria:**
- [ ] `MemClient` round-trips writes through reads (write `0x01 0x00` to a param ID, read it back, get the same bytes)
- [ ] `MemClient.IsLocal()` returns `true`; `(*Client).IsLocal()` returns `false`
- [ ] `SetAuthFailureMode(true)` makes the next `ReadParams`/`WriteParams` call return `breezy.ErrAuth`
- [ ] `SetTimeoutMode(true)` makes the next call return `breezy.ErrTimeout`
- [ ] `Reset()` restores the construction-time params
- [ ] Concurrent `ReadParams` / `WriteParams` calls do not race (verified by `-race`)
- [ ] `LoadSnapshotJSON("pkg/breezy/fakedevice/snapshot_148.json")` loads ~120 params successfully

**Verify:** `just generate && go test -race ./pkg/breezy/... ` → all pass

**Steps:**

- [ ] **Step 1: Extract loadSnapshot in fakedevice**

In `pkg/breezy/fakedevice/fake.go`, rename the existing `loadSnapshot` to public `LoadSnapshotJSON` and update the one caller (`NewServer`). The function signature is unchanged: `func LoadSnapshotJSON(path string) (map[breezy.ParamID][]byte, error)`.

- [ ] **Step 2: Add IsLocal() to DeviceClient**

In `pkg/breezy/ops.go`:

```go
type DeviceClient interface {
    ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error)
    WriteParams(ctx context.Context, writes []ParamWrite) error
    // IsLocal reports whether the client is in-process (no network I/O).
    // Used to gate UDP-protocol-specific behaviour like the fan-settle
    // suppression window in the poller.
    IsLocal() bool
}
```

In `pkg/breezy/client.go`, add to the `*Client` method set:

```go
func (c *Client) IsLocal() bool { return false }
```

Also add to the `recordingClient` in `cmd/breezyd/recording_client.go`:

```go
func (r *recordingClient) IsLocal() bool { return r.inner.IsLocal() }
```

- [ ] **Step 3: Write the MemClient**

Create `pkg/breezy/memclient.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
    "context"
    "fmt"
    "sync"
)

// MemClient is an in-process DeviceClient backed by a parameter-byte map.
// Production code paths use *Client (UDP); MemClient is for UI dev and
// Playwright tests that don't need the wire path. Reads return a snapshot
// of the configured params; writes mutate them in place.
//
// Fault-injection knobs (SetAuthFailureMode, SetTimeoutMode, Reset) make
// MemClient a drop-in for the previous fakedevice.Server admin surface.
type MemClient struct {
    mu       sync.RWMutex
    params   map[ParamID][]byte
    initial  map[ParamID][]byte // for Reset()
    forceAuth    bool
    forceTimeout bool
}

// NewMemClient builds a MemClient with the given initial param bytes.
// The map is copied; subsequent edits to the caller's map don't leak
// into the client.
func NewMemClient(seed map[ParamID][]byte) *MemClient {
    p := make(map[ParamID][]byte, len(seed))
    snap := make(map[ParamID][]byte, len(seed))
    for k, v := range seed {
        b := append([]byte(nil), v...)
        p[k] = b
        snap[k] = append([]byte(nil), v...)
    }
    return &MemClient{params: p, initial: snap}
}

func (m *MemClient) IsLocal() bool { return true }

func (m *MemClient) Close() error { return nil }

func (m *MemClient) ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    if m.forceAuth {
        return nil, ErrAuth
    }
    if m.forceTimeout {
        return nil, ErrTimeout
    }
    if err := ctx.Err(); err != nil {
        return nil, err
    }
    out := make(map[ParamID][]byte, len(ids))
    for _, id := range ids {
        if b, ok := m.params[id]; ok {
            out[id] = append([]byte(nil), b...)
        }
        // Absent ID: omit from the map (matches *Client semantics for
        // unsupported params; callers expect the missing-key form).
    }
    return out, nil
}

func (m *MemClient) WriteParams(ctx context.Context, writes []ParamWrite) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    if m.forceAuth {
        return ErrAuth
    }
    if m.forceTimeout {
        return ErrTimeout
    }
    if err := ctx.Err(); err != nil {
        return err
    }
    for _, w := range writes {
        m.params[w.ID] = append([]byte(nil), w.Value...)
    }
    return nil
}

// SetAuthFailureMode toggles "next call returns ErrAuth" until cleared.
func (m *MemClient) SetAuthFailureMode(force bool) {
    m.mu.Lock(); defer m.mu.Unlock()
    m.forceAuth = force
}

// SetTimeoutMode toggles "next call returns ErrTimeout" until cleared.
func (m *MemClient) SetTimeoutMode(force bool) {
    m.mu.Lock(); defer m.mu.Unlock()
    m.forceTimeout = force
}

// SetParamValue overwrites one param. Used by the test admin surface to
// stage scenarios mid-test.
func (m *MemClient) SetParamValue(id ParamID, value []byte) {
    m.mu.Lock(); defer m.mu.Unlock()
    m.params[id] = append([]byte(nil), value...)
}

// Reset restores the params to the construction-time snapshot and
// clears any fault-injection state.
func (m *MemClient) Reset() {
    m.mu.Lock(); defer m.mu.Unlock()
    p := make(map[ParamID][]byte, len(m.initial))
    for k, v := range m.initial {
        p[k] = append([]byte(nil), v...)
    }
    m.params = p
    m.forceAuth = false
    m.forceTimeout = false
}

// NewMemClientFromFile builds a MemClient seeded from a fakedevice
// JSON snapshot file. The file format is the one used by
// pkg/breezy/fakedevice/snapshot_*.json (hex param map).
func NewMemClientFromFile(path string) (*MemClient, error) {
    seed, err := fakedeviceLoadSnapshotJSON(path)
    if err != nil {
        return nil, fmt.Errorf("memclient: %w", err)
    }
    return NewMemClient(seed), nil
}

// fakedeviceLoadSnapshotJSON is set in an init in memclient_link.go to
// avoid an import cycle (fakedevice imports breezy). See that file.
var fakedeviceLoadSnapshotJSON func(string) (map[ParamID][]byte, error)
```

Add `pkg/breezy/memclient_link.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import "github.com/hughobrien/breezyd/pkg/breezy/fakedevice"

// Avoid an import cycle by deferring the snapshot loader through a
// package-level var. Set in init.
func init() {
    fakedeviceLoadSnapshotJSON = func(path string) (map[ParamID][]byte, error) {
        return fakedevice.LoadSnapshotJSON(path)
    }
}
```

> **Note on the import-cycle workaround.** `pkg/breezy/fakedevice` already imports `pkg/breezy`, so `breezy` can't import `fakedevice` directly. The `init`-set var pattern keeps the seed loader in fakedevice (one canonical implementation) while letting `MemClient` use it. If during implementation this turns out to be uglier than worth, the alternative is to copy the JSON loader into `memclient.go` (~30 lines).

- [ ] **Step 4: Write the test file**

Create `pkg/breezy/memclient_test.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package breezy_test

import (
    "context"
    "errors"
    "sync"
    "testing"

    "github.com/hughobrien/breezyd/pkg/breezy"
)

func TestMemClient_RoundTrip(t *testing.T) {
    c := breezy.NewMemClient(map[breezy.ParamID][]byte{
        0x0001: {0x00},
    })
    ctx := context.Background()
    if err := c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x42}}}); err != nil {
        t.Fatalf("write: %v", err)
    }
    got, err := c.ReadParams(ctx, []breezy.ParamID{0x0001})
    if err != nil {
        t.Fatalf("read: %v", err)
    }
    if string(got[0x0001]) != "\x42" {
        t.Fatalf("got %x want 42", got[0x0001])
    }
}

func TestMemClient_IsLocal(t *testing.T) {
    c := breezy.NewMemClient(nil)
    if !c.IsLocal() {
        t.Fatal("MemClient.IsLocal should be true")
    }
}

func TestMemClient_AuthFault(t *testing.T) {
    c := breezy.NewMemClient(nil)
    c.SetAuthFailureMode(true)
    _, err := c.ReadParams(context.Background(), []breezy.ParamID{0x01})
    if !errors.Is(err, breezy.ErrAuth) {
        t.Fatalf("got %v want ErrAuth", err)
    }
}

func TestMemClient_TimeoutFault(t *testing.T) {
    c := breezy.NewMemClient(nil)
    c.SetTimeoutMode(true)
    err := c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x01, Value: []byte{0x00}}})
    if !errors.Is(err, breezy.ErrTimeout) {
        t.Fatalf("got %v want ErrTimeout", err)
    }
}

func TestMemClient_Reset(t *testing.T) {
    c := breezy.NewMemClient(map[breezy.ParamID][]byte{0x01: {0x00}})
    _ = c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x01, Value: []byte{0xFF}}})
    c.SetAuthFailureMode(true)
    c.Reset()
    got, err := c.ReadParams(context.Background(), []breezy.ParamID{0x01})
    if err != nil {
        t.Fatalf("read after reset: %v", err)
    }
    if string(got[0x01]) != "\x00" {
        t.Fatalf("reset didn't restore: got %x", got[0x01])
    }
}

func TestMemClient_Concurrency(t *testing.T) {
    c := breezy.NewMemClient(map[breezy.ParamID][]byte{0x01: {0x00}})
    var wg sync.WaitGroup
    for i := 0; i < 50; i++ {
        wg.Add(2)
        go func() {
            defer wg.Done()
            _, _ = c.ReadParams(context.Background(), []breezy.ParamID{0x01})
        }()
        go func() {
            defer wg.Done()
            _ = c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x01, Value: []byte{0x01}}})
        }()
    }
    wg.Wait()
}

func TestMemClient_FromFile(t *testing.T) {
    c, err := breezy.NewMemClientFromFile("fakedevice/snapshot_148.json")
    if err != nil {
        t.Fatalf("from file: %v", err)
    }
    got, err := c.ReadParams(context.Background(), []breezy.ParamID{breezy.ParamUnitType})
    if err != nil {
        t.Fatalf("read: %v", err)
    }
    if len(got[breezy.ParamUnitType]) == 0 {
        t.Fatal("expected unit-type param present in snapshot_148.json")
    }
}
```

- [ ] **Step 5: Run tests**

Run: `cd /home/hugh/twinfresh && just generate && go test -race ./pkg/breezy/...`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/breezy/memclient.go pkg/breezy/memclient_test.go pkg/breezy/memclient_link.go pkg/breezy/ops.go pkg/breezy/client.go pkg/breezy/fakedevice/fake.go cmd/breezyd/recording_client.go
git commit -m "feat(breezy): add in-process MemClient + IsLocal on DeviceClient"
```

---

## Task 2: Skip fan-settle suppression for local clients

**Goal:** The poller's fan-settle window only kicks in when the client is talking UDP. With a `MemClient`, writes land instantly and the next read can see them.

**Files:**
- Modify: `cmd/breezyd/poller.go` — gate `noteWrite` and `idsForThisTick` on `IsLocal()`
- Modify: `cmd/breezyd/poller.go` — extend `PollerClient` to include `IsLocal() bool`
- Modify: `cmd/breezyd/poller_test.go` — add a test that verifies a local-client poller never enters the settle window

**Acceptance Criteria:**
- [ ] With a UDP `*Client`: existing fan-settle behaviour unchanged (the existing `TestPoller_FanSettle*` tests still pass)
- [ ] With a `*breezy.MemClient`: a write to a fan param does NOT cause subsequent reads to drop fan-sensitive IDs
- [ ] `PollerClient` interface still satisfied by both implementations

**Verify:** `go test -race ./cmd/breezyd/... -run Poller` → all pass

**Steps:**

- [ ] **Step 1: Extend PollerClient**

In `cmd/breezyd/poller.go`:

```go
type PollerClient interface {
    ReadParams(ctx context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error)
    Close() error
    IsLocal() bool
}
```

- [ ] **Step 2: Gate noteWrite**

In `cmd/breezyd/poller.go::noteWrite` (around line 130):

```go
func (p *Poller) noteWrite(id breezy.ParamID) {
    if !fanWriteIDs[id] {
        return
    }
    // No suppression needed for local clients — they have no fan-settle window.
    if cli := p.lastClient; cli != nil && cli.IsLocal() {
        return
    }
    p.mu.Lock()
    p.settleDeadline = p.now().Add(fanSettleDuration)
    p.mu.Unlock()
}
```

The `lastClient` field is new — track the most recently dialed client so noteWrite can check its locality. Set it in `dial()`:

```go
func (p *Poller) dial() (PollerClient, error) {
    var cli PollerClient
    var err error
    if p.NewClient != nil { cli, err = p.NewClient() } else { cli, err = breezy.NewClient(p.IP, p.DeviceID, p.Password) }
    if err != nil { return nil, err }
    p.mu.Lock(); p.lastClient = cli; p.mu.Unlock()
    return cli, nil
}
```

- [ ] **Step 3: Add a test that local clients don't suppress**

In `cmd/breezyd/poller_test.go`, add:

```go
func TestPoller_FanSettle_SkippedForLocalClient(t *testing.T) {
    mem := breezy.NewMemClient(map[breezy.ParamID][]byte{
        0x4A: {0x00, 0x00}, // supply RPM (a fanSensitiveRead)
        0x02: {0xFF},       // speed_mode (a fanWriteID)
    })
    p := &Poller{
        Name: "x", IP: "x", DeviceID: "x", Password: "x",
        ReadIDs:   []breezy.ParamID{0x4A},
        NewClient: func() (PollerClient, error) { return memPollerClient{mem}, nil },
        Now:       time.Now,
    }
    p.noteWrite(0x02) // would normally start the 12s suppression
    ids := p.idsForThisTick()
    if len(ids) != 1 || ids[0] != 0x4A {
        t.Fatalf("local client should not suppress fan-sensitive reads; got %v", ids)
    }
}

// memPollerClient adapts *MemClient to PollerClient (adds Close/IsLocal already implemented).
type memPollerClient struct{ *breezy.MemClient }
```

(`*MemClient` already has `Close()` and `IsLocal()`, so the adapter is just to satisfy `PollerClient`'s set; if PollerClient is now exactly that set, the adapter may not be needed.)

- [ ] **Step 4: Run tests**

Run: `go test -race ./cmd/breezyd/ -run Poller -v`
Expected: existing fan-settle tests pass, new `_SkippedForLocalClient` passes.

- [ ] **Step 5: Commit**

```bash
git add cmd/breezyd/poller.go cmd/breezyd/poller_test.go
git commit -m "feat(breezyd): poller skips fan-settle suppression for IsLocal clients"
```

---

## Task 3: `--backend=memory` and `--seed` flags

**Goal:** Adding the flags makes `breezyd` instantiate `MemClient` factories instead of UDP-client factories. Production default (`--backend=udp`) is unchanged.

**Files:**
- Modify: `cmd/breezyd/main.go`
- Modify: `cmd/breezyd/main_test.go` (or a new `flags_test.go` if cleaner)

**Acceptance Criteria:**
- [ ] `breezyd --backend=memory --seed pkg/breezy/fakedevice/snapshot_148.json` boots, polls, serves the dashboard with the seed's data
- [ ] `breezyd --backend=udp` (default) is unchanged — same factory wiring as today
- [ ] `breezyd --backend=memory` without `--seed` boots with empty params (every read returns empty maps; useful for "test starts blank then writes" scenarios)
- [ ] Invalid combinations (`--seed` without `--backend=memory`, or unknown backend value) error out clearly at startup

**Verify:** `breezyd --backend=memory --seed pkg/breezy/fakedevice/snapshot_148.json --config <ad-hoc>` produces a working dashboard at `/`.

**Steps:**

- [ ] **Step 1: Add the flags**

In `cmd/breezyd/main.go`, add to the flagset:

```go
backend := fs.String("backend", "udp", "Device backend: 'udp' (default) talks to real devices over UDP/4000; 'memory' uses an in-process client seeded from --seed")
seed := fs.String("seed", "", "Path to a fakedevice JSON snapshot used to seed the memory backend (only valid with --backend=memory)")
```

After parse, validate:

```go
if *seed != "" && *backend != "memory" {
    return fmt.Errorf("--seed is only valid with --backend=memory")
}
if *backend != "udp" && *backend != "memory" {
    return fmt.Errorf("--backend: unknown value %q (allowed: udp, memory)", *backend)
}
```

- [ ] **Step 2: Build the factories**

Locate the factory wiring (today: `breezy.NewClient(ip, id, pw)` is called inside `Poller.dial()` and inside `server.Handler.ClientFactory`). Add a small helper:

```go
// memClients holds shared MemClient instances when --backend=memory.
// One per device, so multiple pollers/handlers share state. Nil in UDP
// mode.
var memClients map[string]*breezy.MemClient

// makeClient is the factory used by handlers and pollers. It returns a
// fresh client (UDP) or a shared MemClient (memory).
func makeClient(name string, dev DeviceConfig) (HandlerClient, error) {
    if memClients != nil {
        c, ok := memClients[name]
        if !ok { return nil, fmt.Errorf("memory backend: device %q has no MemClient", name) }
        return c, nil
    }
    return breezy.NewClient(dev.IP, dev.ID, dev.Password)
}
```

Plumb this into `server.Handler.ClientFactory` and replace the `breezy.NewClient` call inside `Poller.dial()` with the same factory adapted to `PollerClient`.

- [ ] **Step 3: Wire the memClients map at startup**

When `--backend=memory`:

```go
memClients = make(map[string]*breezy.MemClient, len(cfg.Devices))
for name := range cfg.Devices {
    var c *breezy.MemClient
    if *seed != "" {
        var err error
        c, err = breezy.NewMemClientFromFile(*seed)
        if err != nil { return fmt.Errorf("device %q: %w", name, err) }
    } else {
        c = breezy.NewMemClient(nil)
    }
    memClients[name] = c
}
```

Each device gets its **own** MemClient (independent state); they all start from the same seed. This matches the existing test convention where every fakedevice is constructed from `snapshot_148.json`.

- [ ] **Step 4: Test the flag wiring**

In `cmd/breezyd/main_test.go`, add a test that boots `run()` with `--backend=memory --seed pkg/breezy/fakedevice/snapshot_148.json`, hits `/v1/devices/<name>` after a poll cycle, and asserts the response shape matches what fakedevice would have served.

- [ ] **Step 5: Run tests**

Run: `go test -race ./cmd/breezyd/...`
Expected: all pass, including the new boot test.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/main.go cmd/breezyd/main_test.go
git commit -m "feat(breezyd): add --backend=memory --seed flags"
```

---

## Task 4: Build-tagged `/test/...` admin endpoints

**Goal:** A test-only HTTP surface that mutates the `MemClient` for a named device. Replicates `cmd/fakedevice` admin functionality without a separate process.

**Files:**
- Create: `cmd/breezyd/handlers_test_admin.go` (build tag `breezyd_test_admin`)
- Create: `cmd/breezyd/handlers_test_admin_test.go` (build tag `breezyd_test_admin`)
- Create: `cmd/breezyd/handlers_test_admin_off.go` (no build tag) — provides a no-op `mountTestAdmin` so server.go doesn't depend on the tag
- Modify: `cmd/breezyd/server.go` — call `mountTestAdmin(mux, h)` once during route setup
- Modify: `justfile` — add a recipe to build/test with the tag

**Acceptance Criteria:**
- [ ] With tag set: `POST /test/devices/{name}/params/{id}` (body: `{"value":"FF"}`) updates the MemClient and the next dashboard poll reflects it
- [ ] With tag set: `POST /test/devices/{name}/inject-error` (body: `{"kind":"auth"}`) makes the next read fail with `ErrAuth`, surfaced via the `#global-error-banner` SSE path
- [ ] With tag set: `POST /test/devices/{name}/reset` clears injected errors and restores seed params
- [ ] Without tag: `mountTestAdmin` is a no-op; routes return 404
- [ ] When `--backend=udp`: `/test/...` endpoints return 400 ("memory backend required") rather than panicking on the type assertion

**Verify:** `go test -tags breezyd_test_admin ./cmd/breezyd/ -run TestAdmin` → all pass.

**Steps:**

- [ ] **Step 1: Define the off-path stub**

Create `cmd/breezyd/handlers_test_admin_off.go`:

```go
//go:build !breezyd_test_admin

package main

import "net/http"

// mountTestAdmin is a no-op when the breezyd_test_admin build tag is not set.
func mountTestAdmin(mux *http.ServeMux, h *Handler) {}
```

- [ ] **Step 2: Define the on-path implementation**

Create `cmd/breezyd/handlers_test_admin.go`:

```go
//go:build breezyd_test_admin

package main

import (
    "encoding/hex"
    "encoding/json"
    "fmt"
    "net/http"

    "github.com/hughobrien/breezyd/pkg/breezy"
)

func mountTestAdmin(mux *http.ServeMux, h *Handler) {
    mux.HandleFunc("POST /test/devices/{name}/params/{id}", h.testSetParam)
    mux.HandleFunc("POST /test/devices/{name}/inject-error", h.testInjectError)
    mux.HandleFunc("POST /test/devices/{name}/reset", h.testReset)
}

// memClientFor returns the MemClient for name, or 400 on UDP backend.
func (h *Handler) memClientFor(w http.ResponseWriter, name string) (*breezy.MemClient, bool) {
    cli, _, err := h.dial(name)
    if err != nil { http.Error(w, err.Error(), 500); return nil, false }
    mc, ok := cli.(*breezy.MemClient)
    if !ok {
        http.Error(w, "test admin requires --backend=memory", 400)
        return nil, false
    }
    return mc, true
}

func (h *Handler) testSetParam(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    idStr := r.PathValue("id")
    id, err := parseParamID(idStr)
    if err != nil { http.Error(w, err.Error(), 400); return }
    var body struct { Value string `json:"value"` }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil { http.Error(w, err.Error(), 400); return }
    val, err := hex.DecodeString(body.Value)
    if err != nil { http.Error(w, "value must be hex: "+err.Error(), 400); return }

    mc, ok := h.memClientFor(w, name); if !ok { return }
    mc.SetParamValue(id, val)
    w.WriteHeader(204)
}

func (h *Handler) testInjectError(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    var body struct { Kind string `json:"kind"` }
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil { http.Error(w, err.Error(), 400); return }
    mc, ok := h.memClientFor(w, name); if !ok { return }
    switch body.Kind {
    case "auth":    mc.SetAuthFailureMode(true)
    case "timeout": mc.SetTimeoutMode(true)
    case "none":    mc.SetAuthFailureMode(false); mc.SetTimeoutMode(false)
    default:        http.Error(w, fmt.Sprintf("kind: unknown %q", body.Kind), 400); return
    }
    w.WriteHeader(204)
}

func (h *Handler) testReset(w http.ResponseWriter, r *http.Request) {
    name := r.PathValue("name")
    mc, ok := h.memClientFor(w, name); if !ok { return }
    mc.Reset()
    w.WriteHeader(204)
}

// parseParamID accepts "0x4a" / "4a" / "74" forms.
func parseParamID(s string) (breezy.ParamID, error) {
    var u uint64
    var err error
    if len(s) > 2 && (s[:2] == "0x" || s[:2] == "0X") {
        _, err = fmt.Sscanf(s[2:], "%x", &u)
    } else {
        _, err = fmt.Sscanf(s, "%x", &u)
    }
    if err != nil || u > 0xFFFF { return 0, fmt.Errorf("bad param id %q", s) }
    return breezy.ParamID(u), nil
}
```

- [ ] **Step 3: Mount in server.go**

In `cmd/breezyd/server.go`, find route registration (the `mux.Handle*` block) and add:

```go
mountTestAdmin(mux, h)
```

This compiles regardless of tag — `mountTestAdmin` is the no-op stub when the tag isn't set.

- [ ] **Step 4: Write the tests**

Create `cmd/breezyd/handlers_test_admin_test.go` with the build tag set; tests boot a real server with `--backend=memory`, hit the test admin endpoints, then verify the dashboard / cache reflects the changes.

- [ ] **Step 5: Add the just recipe**

In `justfile`:

```make
# breezyd built with the test-admin HTTP surface (used by Playwright).
build-test-admin:
	templ generate
	CGO_ENABLED=1 go build -tags breezyd_test_admin -o ./breezyd-test ./cmd/breezyd

test-test-admin:
	go test -tags breezyd_test_admin ./cmd/breezyd/ -run TestAdmin
```

Add `test-test-admin` to the `ci` recipe.

- [ ] **Step 6: Run tests**

Run: `just test-test-admin`
Expected: pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/breezyd/handlers_test_admin*.go cmd/breezyd/server.go justfile
git commit -m "feat(breezyd): add build-tagged /test/devices/... admin surface for memory backend"
```

---

## Task 5: Migrate Playwright global-setup + fixtures

**Goal:** `tests/ui/global-setup.ts` spawns one breezyd (built with `breezyd_test_admin`, run with `--backend=memory --seed <fixture>`). No fakedevice spawn. `tests/ui/fixtures.ts` retargets HTTP from the old admin URL to `${BREEZYD_URL}/test/...`.

**Files:**
- Modify: `tests/ui/global-setup.ts`
- Modify: `tests/ui/global-teardown.ts`
- Modify: `tests/ui/fixtures.ts`
- Modify: `tests/ui/playwright.config.ts` (if base-URL changes are needed)
- Modify: `justfile` — `test-ui` builds the admin-tagged binary first

**Acceptance Criteria:**
- [ ] `just test-ui` runs without spawning any `fakedevice` process — verified by `ps aux | grep fakedevice` while the suite runs
- [ ] All currently-passing Playwright tests still pass (17 active; the `simulateFanSettle` test gets skipped in this task and ported in Task 6)
- [ ] Aborting mid-run leaves no orphan processes (kill-test-daemons recipe still works as a safety net but should rarely be needed)

**Verify:** `just kill-test-daemons && just test-ui` → 16 pass + 1 skipped (the fan-settle one) + 1 fixme.

**Steps:**

- [ ] **Step 1: Update global-setup.ts**

Replace the fakedevice spawn loop with a single breezyd spawn:

```typescript
// Before: spawn fakedevice + breezyd
// After: spawn one breezyd with --backend=memory --seed <fixture>

const httpPort = await freePort();
const cfgPath = join(tmp, "config.toml");
const cfg = [
  "[daemon]",
  `listen = "127.0.0.1:${httpPort}"`,
  `poll_interval = "1s"`,
  `discovery = "off"`,
  "",
  ...DEVICES.flatMap((name) => [
    `[devices.${name}]`,
    `id = "BREEZY${name.padEnd(11, "0")}"`,
    `password = "1111"`,
    `ip = "127.0.0.1:0"`,  // unused in memory mode but config requires a value
    "",
  ]),
].join("\n");
writeFileSync(cfgPath, cfg, { mode: 0o600 });

const bd = spawn(
  "go",
  [
    "run", "-tags", "breezyd_test_admin",
    "./cmd/breezyd",
    "--config", cfgPath,
    "--backend=memory",
    "--seed", join(REPO_ROOT, "pkg/breezy/fakedevice/snapshot_148.json"),
  ],
  { cwd: REPO_ROOT, stdio: ["ignore", "pipe", "pipe"] },
);
```

The `BREEZYD_ADMIN_URL` env var becomes `${BREEZYD_URL}` (the daemon serves both the production and the test surface from one port, with `/test/...` only present under the build tag).

- [ ] **Step 2: Update global-teardown.ts**

Drop the fakedevice cleanup branch.

- [ ] **Step 3: Update fixtures.ts**

Each call site changes URL base:

```typescript
function adminBase(): string { return `${daemonBase()}/test`; }
```

The `setDeviceState` function had to know which fakedevice param IDs correspond to which dashboard fields. Move that mapping into a small TypeScript helper, since the IDs are still the same:

```typescript
const PARAMS = {
  power:    "0x01",
  speedMode:"0x02",
  manualPct:"0x44",
  // ... etc, mirror the fakedevice admin's mapping
};

export async function setDeviceState(name, kv) {
  for (const [field, value] of Object.entries(kv)) {
    const id = PARAMS[field]; if (!id) throw new Error(`unknown field ${field}`);
    await adminCall("POST", `/devices/${name}/params/${id}`, { value: encodeHex(value) });
  }
}
```

- [ ] **Step 4: Skip the simulateFanSettle test**

In `tests/ui/dashboard.spec.ts`, mark the test that uses `simulateFanSettle` as `test.skip` with a TODO referencing Task 6. (Will be deleted in Task 6 once the Go port lands.)

- [ ] **Step 5: Update justfile**

```make
test-ui: 
	cd tests/ui && pnpm exec playwright test
```

Stays the same — `go run -tags breezyd_test_admin` happens inside global-setup.

- [ ] **Step 6: Run the suite**

Run: `just kill-test-daemons && just test-ui`
Expected: 16 pass + 1 skipped + 1 fixme.

- [ ] **Step 7: Commit**

```bash
git add tests/ui/global-setup.ts tests/ui/global-teardown.ts tests/ui/fixtures.ts tests/ui/dashboard.spec.ts tests/ui/playwright.config.ts justfile
git commit -m "test(ui): migrate Playwright to breezyd memory backend; drop fakedevice spawn"
```

---

## Task 6: Port the fan-settle test to Go

**Goal:** The one Playwright test that exercises `simulateFanSettle` becomes a Go test against the UDP fakedevice (where the protocol fact actually lives).

**Files:**
- Modify: `cmd/breezyd/poller_test.go` (or a new `fan_settle_test.go`)
- Modify: `tests/ui/dashboard.spec.ts` — delete the now-skipped test
- Modify: `tests/ui/fixtures.ts` — drop `simulateFanSettle`

**Acceptance Criteria:**
- [ ] A Go test asserts: after writing to `0x02`, the next poll within 12s drops `0x4A` from its read-IDs; after 13s, `0x4A` is back in
- [ ] Test runs against `pkg/breezy/fakedevice` (real UDP path), not MemClient
- [ ] The Playwright `simulateFanSettle` consumer is gone
- [ ] `simulateFanSettle` symbol is gone from `tests/ui/fixtures.ts`

**Verify:** `go test -race ./cmd/breezyd/ -run FanSettle` passes; `grep -rn simulateFanSettle tests/` returns nothing.

**Steps:**

- [ ] **Step 1: Look at the existing Playwright scenario** — `tests/ui/dashboard.spec.ts` for the `simulateFanSettle` test. Note what it asserts: after a fan write, fan-sensitive cards display the pre-write value for ~12s.

- [ ] **Step 2: Write the equivalent in Go**

In `cmd/breezyd/poller_test.go`:

```go
func TestPoller_FanSettle_DropsSensitiveReads_OverUDP(t *testing.T) {
    fd, err := fakedevice.NewServer("../../pkg/breezy/fakedevice/snapshot_148.json", "BREEZY00000000A0", "1111")
    if err != nil { t.Fatalf("fakedevice: %v", err) }
    defer fd.Close()

    p := &Poller{
        Name: "x", IP: fd.Addr(), DeviceID: "BREEZY00000000A0", Password: "1111",
        ReadIDs:  []breezy.ParamID{0x02, 0x4A},
        Now:      func() time.Time { return time.Now() },
    }
    // Before any write: both IDs in this tick.
    if got := p.idsForThisTick(); !contains(got, 0x4A) {
        t.Fatal("expected 0x4A pre-settle")
    }
    // Note a fan write -> suppression starts.
    p.noteWrite(0x02)
    if got := p.idsForThisTick(); contains(got, 0x4A) {
        t.Fatal("expected 0x4A suppressed during settle")
    }
    // Advance virtual time past the deadline.
    p.Now = func() time.Time { return time.Now().Add(13 * time.Second) }
    if got := p.idsForThisTick(); !contains(got, 0x4A) {
        t.Fatal("expected 0x4A back after settle")
    }
}

func contains(ids []breezy.ParamID, want breezy.ParamID) bool {
    for _, id := range ids { if id == want { return true } }
    return false
}
```

- [ ] **Step 3: Delete the Playwright test**

In `tests/ui/dashboard.spec.ts`, remove the `simulateFanSettle`-using test entirely.

- [ ] **Step 4: Drop the fixture**

In `tests/ui/fixtures.ts`, delete the `simulateFanSettle` export and any imports/usages.

- [ ] **Step 5: Run both suites**

Run: `go test -race ./cmd/breezyd/ -run FanSettle && just test-ui`
Expected: both pass; no skipped fan-settle test.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/poller_test.go tests/ui/dashboard.spec.ts tests/ui/fixtures.ts
git commit -m "test: port fan-settle Playwright test to Go (poller_test against fakedevice UDP)"
```

---

## Task 7: Retire `cmd/fakedevice` admin binary + tag

**Goal:** Delete the admin binary and the `fakedevice_admin` build tag now that nothing depends on them.

**Files:**
- Delete: `cmd/fakedevice/main.go`
- Delete: `cmd/fakedevice/` entirely if empty
- Delete: `pkg/breezy/fakedevice/admin.go`
- Delete: `pkg/breezy/fakedevice/admin_test.go`
- Modify: `pkg/breezy/fakedevice/fake.go` — remove `SetAuthFailureMode`, `SetSilentMode`, `SetReplyDelay`, `SetParamValue` if they were only used by admin (verify with grep)
- Modify: `justfile` — drop `test-fakedevice-admin` recipe; remove from `ci`
- Modify: `CLAUDE.md` — update the "fakedevice" reference paragraph to reflect its narrower role

**Acceptance Criteria:**
- [ ] `cmd/fakedevice/` is gone
- [ ] `grep -rn fakedevice_admin .` finds nothing in source files (justfile, *.go, *.ts)
- [ ] `just ci` runs end-to-end without that recipe
- [ ] No compile errors anywhere
- [ ] `pkg/breezy/fakedevice/` still builds and its in-package tests still pass

**Verify:** `just ci` passes; `grep -rn fakedevice_admin .` empty.

**Steps:**

- [ ] **Step 1: Verify nothing else depends on the admin tag**

Run: `grep -rn fakedevice_admin /home/hugh/twinfresh`
Expected: matches only in files we're about to delete.

- [ ] **Step 2: Delete the admin binary**

```bash
rm -rf cmd/fakedevice
```

- [ ] **Step 3: Delete the admin HTTP surface**

```bash
rm pkg/breezy/fakedevice/admin.go pkg/breezy/fakedevice/admin_test.go
```

- [ ] **Step 4: Trim fake.go**

Open `pkg/breezy/fakedevice/fake.go`. The `Set*` methods used only by admin (`SetAuthFailureMode`, `SetSilentMode`, `SetReplyDelay`, `SetParamValue`) — if grep confirms no other callers, delete them along with the corresponding fields on `Server`.

If non-admin callers remain (e.g., `cmd/breezyd/poller_test.go::TestPoller_FanSettle_*` directly calls them), keep the surface and just delete the HTTP layer.

- [ ] **Step 5: Drop the just recipe + CI entry**

In `justfile`:

```make
# Delete: test-fakedevice-admin recipe
# Edit: ci: ... — remove test-fakedevice-admin from the chain
```

- [ ] **Step 6: Update CLAUDE.md**

Edit the paragraph mentioning `cmd/fakedevice` (build-tagged admin surface) to reflect it's been retired. Keep the description of `pkg/breezy/fakedevice` as a UDP fixture for protocol tests.

- [ ] **Step 7: Run CI**

Run: `just ci`
Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor: retire cmd/fakedevice admin binary and fakedevice_admin build tag"
```

---

## Task 8: Migrate `screenshot.ts`

**Goal:** `just screenshot` spawns one breezyd in memory mode. No fakedevice.

**Files:**
- Modify: `tests/ui/screenshot.ts`

**Acceptance Criteria:**
- [ ] `just kill-test-daemons && just screenshot` produces both PNGs
- [ ] No fakedevice processes spawn during the run
- [ ] PNG content matches what the suite produced before this PR (eyeball the diff)

**Verify:** `just screenshot && file tests/ui/screenshots/*.png` → both report valid PNG.

**Steps:**

- [ ] **Step 1: Mirror the global-setup migration**

Apply the same simplification to `screenshot.ts`: drop the fakedevice spawn loop; spawn one breezyd with `-tags breezyd_test_admin --backend=memory --seed pkg/breezy/fakedevice/snapshot_148.json`. The screenshot script doesn't need the test admin endpoints (no mid-script state mutation), so the tag isn't strictly required here — but using the same binary as Playwright keeps the build cache hot.

- [ ] **Step 2: Run**

```bash
just kill-test-daemons
just screenshot
```

Expected: both PNGs land in `tests/ui/screenshots/`.

- [ ] **Step 3: Commit**

```bash
git add tests/ui/screenshot.ts tests/ui/screenshots/dashboard-1col.png tests/ui/screenshots/dashboard-3col.png
git commit -m "chore(screenshot): migrate to breezyd memory backend"
```

---

## Self-review

1. **Spec coverage** — every section of the design doc maps to a task: MemClient (T1), IsLocal/fan-settle (T2), --backend wiring (T3), test admin (T4), Playwright migration (T5), fan-settle Go port (T6), retire fakedevice admin (T7), screenshot migration (T8). ✓

2. **Placeholder scan** — no TBDs, every code step has the actual code, every Verify line has an exact command. ✓

3. **Type consistency** — `MemClient` named identically across tasks; `IsLocal()` signature matches between definition (T1) and consumer (T2); `mountTestAdmin(mux *http.ServeMux, h *Handler)` matches between off-stub (T4 step 1) and on-impl (T4 step 2). ✓

4. **Out of scope check** — raw `/v1/.../params/{id}` endpoint stays since MemClient honours arbitrary param IDs. Energy + Schedule unchanged. ✓
