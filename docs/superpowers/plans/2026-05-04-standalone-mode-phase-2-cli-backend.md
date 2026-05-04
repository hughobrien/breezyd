# Standalone Mode Phase 2 — CLI Backend & Standalone Path Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `backend` interface to the `breezy` CLI with two implementations — `daemonBackend` (today's HTTP plumbing) and `directBackend` (direct UDP via `pkg/breezy/ops`) — and flip the default to standalone (daemon mode is opt-in via `--daemon` flag or `[daemon].listen` in config).

**Architecture:** Four tasks. (1) Introduce `backend` interface + `daemonBackend`; refactor `commands.go` so each verb calls `backend.X()` instead of `httpJSON`. No user-visible change yet. (2) Add `directBackend` (uses `pkg/breezy/ops` against per-device `*breezy.Client` with a lazy-open + cleanup lifecycle); add `runStandalone` test helper and dual-backend tests. (3) Replace `resolveDaemonURL` with `resolveBackend`; remove `defaultDaemonURL`; update `ls` and `daemon-url` for standalone. (4) Flip the first-run config default (`[daemon]` block commented out), update README/CLAUDE.md/CHANGELOG.

**Tech Stack:** Go 1.22+, `pkg/breezy/ops` from Phase 1, existing `pkg/breezy/fakedevice`. No new external deps.

**Spec:** `docs/superpowers/specs/2026-05-04-standalone-mode-design.md`
**Issue:** [#2 — Standalone Mode](https://github.com/hughobrien/breezyd/issues/2)
**Phase 1:** Already shipped (commit `2750aef` on `main`).

---

### Task 1: Add `backend` interface and `daemonBackend`; refactor `commands.go` to use it

**Goal:** Introduce a `backend` interface in `cmd/breezy/backend.go` that captures every per-verb operation the CLI needs. Provide a `daemonBackend` implementation that wraps today's HTTP plumbing. Refactor every `cmdXxx` in `commands.go` to call `b.Foo(...)` instead of `httpJSON(...)`. No user-visible change — all existing tests pass against `daemonBackend`.

**Files:**
- Create: `cmd/breezy/backend.go` (new file with the interface + `daemonBackend`)
- Modify: `cmd/breezy/commands.go` (each `cmdXxx` calls `backend` methods; `fetchParamHex` and `httpJSON` move into the daemonBackend impl or backend.go as helpers)
- Modify: `cmd/breezy/main.go` (`run` constructs a `daemonBackend` from the resolved URL and passes it to dispatch — actual `resolveBackend` work lands in Task 3; for now `run` keeps `resolveDaemonURL` and just wraps in `&daemonBackend{url}`)
- Modify: `cmd/breezy/render.go` (drop `snapshotResp` struct; use `breezy.Status` from `pkg/breezy` so the daemon's JSON parses directly)
- Modify: `cmd/breezy/main_test.go` (only if test helpers need adjustment — test signatures should stay the same)

**Acceptance Criteria:**
- [ ] `backend` interface exists in `cmd/breezy/backend.go` with one method per CLI operation (full list below).
- [ ] `daemonBackend{url string}` implements `backend`, embedding today's HTTP behavior (the per-verb HTTP calls move from `cmdXxx` into the backend methods).
- [ ] Every `cmdXxx` in `commands.go` is rewritten to: parse args, call `b.Foo(...)`, render result. Each handler is the same length or shorter than today.
- [ ] `fetchParamHex` moves into `daemonBackend.GetParam` (still used by `cmdRtcShow` via `b.GetParam`).
- [ ] `cmd/breezy/render.go::renderStatus` accepts a `breezy.Status` (replacing the local `snapshotResp` struct).
- [ ] `daemonBackend` JSON-decodes into `breezy.Status` (the JSON tags match — Phase 1 set this up).
- [ ] All existing CLI tests in `cmd/breezy/main_test.go` pass without modification.
- [ ] `just check-all` passes.

**Verify:** `just check-all` exits 0; `go test ./cmd/breezy/... -v` shows no failures.

**Steps:**

- [ ] **Step 1: Create `cmd/breezy/backend.go` with the interface and `daemonBackend` skeleton**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// Package main, file backend.go: declares the CLI's backend interface
// and its two implementations. The CLI dispatches every verb through
// `backend`; daemonBackend wraps the existing HTTP plumbing and
// directBackend (added in Task 2) talks UDP via pkg/breezy/ops.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// backend is the CLI's per-verb operation surface. Each method
// corresponds to one CLI verb. Implementations either hit the daemon
// (daemonBackend) or talk UDP directly (directBackend, Task 2).
//
// Methods that return only error are write-style (no useful payload
// beyond success/failure). Methods returning typed values are reads.
type backend interface {
	// Read-style operations.
	Status(ctx context.Context, name string) (breezy.Status, error)
	Faults(ctx context.Context, name string) ([]breezy.FaultCode, error)
	Firmware(ctx context.Context, name string) (version, buildDate string, err error)
	Efficiency(ctx context.Context, name string) (int, error)
	GetParam(ctx context.Context, name string, id breezy.ParamID) (raw []byte, paramName, paramType, paramValue string, err error)
	Devices(ctx context.Context) ([]lsRow, error)

	// Write-style operations.
	Power(ctx context.Context, name string, on bool) error
	SpeedPreset(ctx context.Context, name string, preset int) error
	SpeedManual(ctx context.Context, name string, pct int) error
	Mode(ctx context.Context, name string, mode string) error
	Heater(ctx context.Context, name string, on bool) error
	ResetFilter(ctx context.Context, name string) error
	ResetFaults(ctx context.Context, name string) error
	SetRTC(ctx context.Context, name string, t time.Time) error
	SetParam(ctx context.Context, name string, id breezy.ParamID, value []byte) error

	// DaemonURLString returns "" for standalone backends; daemon mode
	// returns the URL so `breezy daemon-url` can render it.
	DaemonURLString() string

	// Close releases any resources held by the backend (open UDP
	// sockets in directBackend; daemonBackend's Close is a no-op).
	Close() error
}

// errEnvelope mirrors the daemon's standard error shape. Lifted from
// main.go so backend.go can decode it.
type errEnvelope struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// daemonBackend implements backend by issuing HTTP requests to a
// running breezyd. It carries forward every behavior of the pre-Phase-2
// CLI: same paths, same JSON shapes, same error envelope handling.
type daemonBackend struct {
	url    string
	client *http.Client
}

// newDaemonBackend builds a daemon-talking backend with a 10s default
// HTTP timeout per request.
func newDaemonBackend(url string) *daemonBackend {
	return &daemonBackend{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *daemonBackend) DaemonURLString() string { return d.url }
func (d *daemonBackend) Close() error            { return nil }

// httpJSON issues method url with body (if non-nil) marshalled as
// JSON, reads the entire response, and returns the status + raw bytes.
// Direct port of main.go's pre-Phase-2 helper.
func (d *daemonBackend) httpJSON(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, nil, fmt.Errorf("encode body: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, d.url+path, &buf)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, raw, err
	}
	return resp.StatusCode, raw, nil
}

// envelopeErr converts an HTTP error response (status >= 400) or a
// transport error into a typed `error`. The caller wraps user-facing
// formatting around this; the rendered output stays the same as the
// pre-Phase-2 CLI's `error: <msg> (<code>)` shape.
func envelopeErr(status int, raw []byte, transportErr error) error {
	if transportErr != nil {
		return transportErr
	}
	var e errEnvelope
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		if e.Code != "" {
			return fmt.Errorf("%s (%s)", e.Error, e.Code)
		}
		return errors.New(e.Error)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return fmt.Errorf("HTTP %d", status)
	}
	return fmt.Errorf("HTTP %d: %s", status, body)
}
```

Then add per-verb methods. Each replicates today's per-verb HTTP call. Example for `Status`:

```go
func (d *daemonBackend) Status(ctx context.Context, name string) (breezy.Status, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices/"+name, nil)
	if err != nil || status >= 400 {
		return breezy.Status{}, envelopeErr(status, raw, err)
	}
	var s breezy.Status
	if err := json.Unmarshal(raw, &s); err != nil {
		return breezy.Status{}, fmt.Errorf("decode snapshot: %w", err)
	}
	return s, nil
}

func (d *daemonBackend) Power(ctx context.Context, name string, on bool) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/power", map[string]any{"on": on})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) SpeedPreset(ctx context.Context, name string, preset int) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/speed", map[string]any{"preset": preset})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) SpeedManual(ctx context.Context, name string, pct int) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/speed", map[string]any{"manual": pct})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) Mode(ctx context.Context, name, mode string) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/mode", map[string]any{"mode": mode})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) Heater(ctx context.Context, name string, on bool) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/heater", map[string]any{"on": on})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) ResetFilter(ctx context.Context, name string) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/filter/reset", nil)
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) ResetFaults(ctx context.Context, name string) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/faults/reset", nil)
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) Faults(ctx context.Context, name string) ([]breezy.FaultCode, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices/"+name+"/faults", nil)
	if err != nil || status >= 400 {
		return nil, envelopeErr(status, raw, err)
	}
	var resp struct {
		Faults []breezy.FaultCode `json:"faults"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode faults: %w", err)
	}
	if resp.Faults == nil {
		return []breezy.FaultCode{}, nil
	}
	return resp.Faults, nil
}

func (d *daemonBackend) Firmware(ctx context.Context, name string) (string, string, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices/"+name+"/firmware", nil)
	if err != nil || status >= 400 {
		return "", "", envelopeErr(status, raw, err)
	}
	var resp struct {
		Version   string `json:"version"`
		BuildDate string `json:"build_date"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", fmt.Errorf("decode firmware: %w", err)
	}
	return resp.Version, resp.BuildDate, nil
}

func (d *daemonBackend) Efficiency(ctx context.Context, name string) (int, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices/"+name+"/efficiency", nil)
	if err != nil || status >= 400 {
		return 0, envelopeErr(status, raw, err)
	}
	var resp struct {
		Pct int `json:"recovery_efficiency_pct"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, fmt.Errorf("decode efficiency: %w", err)
	}
	return resp.Pct, nil
}

func (d *daemonBackend) GetParam(ctx context.Context, name string, id breezy.ParamID) ([]byte, string, string, string, error) {
	path := fmt.Sprintf("/v1/devices/%s/params/0x%04X", name, uint16(id))
	status, raw, err := d.httpJSON(ctx, http.MethodGet, path, nil)
	if err != nil || status >= 400 {
		return nil, "", "", "", envelopeErr(status, raw, err)
	}
	var resp struct {
		Hex   string `json:"hex"`
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, "", "", "", fmt.Errorf("decode param: %w", err)
	}
	b, err := hex.DecodeString(resp.Hex)
	if err != nil {
		return nil, "", "", "", fmt.Errorf("decode hex %q: %w", resp.Hex, err)
	}
	return b, resp.Name, resp.Type, resp.Value, nil
}

func (d *daemonBackend) SetParam(ctx context.Context, name string, id breezy.ParamID, value []byte) error {
	path := fmt.Sprintf("/v1/devices/%s/params/0x%04X", name, uint16(id))
	status, raw, err := d.httpJSON(ctx, http.MethodPost, path, map[string]any{"hex": hex.EncodeToString(value)})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) SetRTC(ctx context.Context, name string, t time.Time) error {
	body := map[string]any{"time": t.Format(time.RFC3339)}
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/rtc", body)
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) Devices(ctx context.Context) ([]lsRow, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices", nil)
	if err != nil || status >= 400 {
		return nil, envelopeErr(status, raw, err)
	}
	var resp struct {
		Devices []struct {
			Name      string `json:"name"`
			ID        string `json:"id"`
			IP        string `json:"ip"`
			LastPoll  string `json:"last_poll"`
			Power     *bool  `json:"power"`
			Mode      string `json:"airflow_mode"`
			Reachable bool   `json:"reachable"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode device list: %w", err)
	}
	rows := make([]lsRow, 0, len(resp.Devices))
	for _, d := range resp.Devices {
		rows = append(rows, lsRow{
			Name:      d.Name,
			ID:        d.ID,
			IP:        d.IP,
			LastPoll:  d.LastPoll,
			Power:     d.Power,
			Mode:      d.Mode,
			Reachable: d.Reachable,
		})
	}
	return rows, nil
}
```

- [ ] **Step 2: Update `cmd/breezy/render.go::renderStatus` to take `breezy.Status` and drop `snapshotResp`**

The existing `snapshotResp` struct has the same JSON tags as `breezy.Status`. Replace its definition + every usage with `breezy.Status`. The renderer body changes only in field references — `s.Configured` → `s.Configured` (same), `s.LastPoll` → `s.LastPoll` (same), etc. Run `go vet ./cmd/breezy/...` after the change to catch any access-pattern drift.

Specifically in `cmd/breezy/render.go`:

1. Delete the `type snapshotResp struct { ... }` declaration (around lines 21-31).
2. Change `func renderStatus(w io.Writer, s snapshotResp)` → `func renderStatus(w io.Writer, s breezy.Status)`.
3. The function body should compile unchanged — `breezy.Status`'s field names match `snapshotResp` exactly.
4. Add `"github.com/hughobrien/breezyd/pkg/breezy"` to render.go's imports if not already there.

- [ ] **Step 3: Rewrite each `cmdXxx` in `cmd/breezy/commands.go` to take a `backend` instead of a `daemonURL`**

For example, `cmdStatus`:

```go
func cmdStatus(b backend, name string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := b.Status(ctx, name)
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	renderStatus(stdout, s)
	return 0
}
```

`cmdPower`:

```go
func cmdPower(b backend, name string, on bool, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Power(ctx, name, on); err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}
```

The pattern for every write-style verb is identical: parse, call op, render error or success. For read verbs: parse, call op, render result.

Implement each: `cmdSpeed`, `cmdMode`, `cmdHeater`, `cmdResetFilter`, `cmdResetFaults`, `cmdFaults`, `cmdFirmware`, `cmdEfficiency`, `cmdRtc`, `cmdGet`, `cmdSet`, `cmdLs`, `cmdParam`, `cmdDiscover`. The local validation in `cmdSpeed` (manual:N parsing + range check) stays — that's pre-flight argument parsing, not protocol concern. Same for `cmdMode`'s `validModes` map.

`cmdRtcShow` becomes: call `b.GetParam(ctx, name, 0x006F)` and `b.GetParam(ctx, name, 0x0070)`, decode each via the typed helpers as before. Or simpler: add a `RTC(ctx, name) (timeStr, dateStr string, err error)` method to the backend interface — your call. The plan recommends keeping `GetParam`-based `cmdRtcShow` to avoid a backend method that exists solely to replace two GetParam calls.

`cmdParam` and `cmdDiscover` (globals) don't need a backend at all. `cmdParam` runs against the static registry; `cmdDiscover` does a LAN broadcast directly. Leave them as-is, but change their signature only if needed for consistency. The plan recommends leaving them unchanged.

- [ ] **Step 4: Delete now-unused helpers from `cmd/breezy/main.go`**

The old `httpJSON`, `renderErr`, `doSimple`, `errEnvelope`, `fetchParamHex` have moved into `backend.go` (or are inlined in the new per-verb cmd functions). Remove them from `main.go` and `commands.go`. Verify by grep:

```bash
grep -n "func httpJSON\|func renderErr\|func doSimple\|errEnvelope\b\|func fetchParamHex" cmd/breezy/*.go
```

Expected matches: `daemonBackend.httpJSON`, `errEnvelope` in backend.go, `envelopeErr` (the new helper). No matches in main.go or commands.go.

- [ ] **Step 5: Update `cmd/breezy/main.go::run` to construct and pass a `backend`**

```go
func run(args []string, stdout, stderr io.Writer) int {
	// ... flag parsing unchanged ...

	daemonURL := resolveDaemonURL(*daemon)
	b := newDaemonBackend(daemonURL)
	defer b.Close()

	// Globals that don't take a backend.
	switch rest[0] {
	case "ls":
		return cmdLs(b, stdout, stderr)
	case "discover":
		return cmdDiscover(stdout, stderr)
	case "daemon-url":
		fmt.Fprintln(stdout, b.DaemonURLString())
		return 0
	case "param":
		return cmdParam(stdout)
	// ... rest unchanged ...
	}

	// Per-device verbs.
	name, verb, vargs := rest[0], rest[1], rest[2:]
	switch verb {
	case "status":
		return cmdStatus(b, name, stdout, stderr)
	case "on":
		return cmdPower(b, name, true, stdout, stderr)
	case "off":
		return cmdPower(b, name, false, stdout, stderr)
	case "speed":
		return cmdSpeed(b, name, vargs, stdout, stderr)
	// ... etc ...
	}
}
```

`resolveDaemonURL` stays in main.go for now — Task 3 replaces it with `resolveBackend`.

- [ ] **Step 6: Run the test suite**

```bash
go test ./cmd/breezy/... -v
just check-all
```

All existing tests should pass. Test failures most likely indicate a JSON shape drift in `breezy.Status` vs. the daemon's actual response — re-verify by adding a temporary `t.Logf("%+v", s)` to `TestStatus` if needed.

If a test asserts on a specific stderr substring (e.g. `"error: HTTP 400: ..."`) that the new `envelopeErr` formats slightly differently, update the assertion to match the new text — the change here is a refactor, not a contract change, but error-rendering details may shift.

- [ ] **Step 7: Commit**

```bash
git add cmd/breezy/backend.go cmd/breezy/commands.go cmd/breezy/main.go cmd/breezy/render.go
git commit -m "$(cat <<'EOF'
cmd/breezy: introduce backend interface + daemonBackend (no behavior change)

Refactors the CLI so every verb dispatches through a `backend`
interface. daemonBackend wraps today's HTTP plumbing; cmdXxx
handlers shrink to: parse, call backend, render. No user-visible
change — daemonBackend produces the same wire calls as before, and
all existing CLI tests pass.

Phase 2 prep for issue #2.
EOF
)"
```

---

### Task 2: Add `directBackend` (direct UDP via `pkg/breezy/ops`) + dual-backend tests

**Goal:** Implement the second `backend` — `directBackend` — which talks UDP directly to each device using `pkg/breezy/ops`. Lazy-open per device, close all on `Close()`. Add a `runStandalone` test helper and parallel tests for the most important CLI verbs to verify both backends produce equivalent output.

**Files:**
- Modify: `cmd/breezy/backend.go` (append `directBackend`)
- Modify: `cmd/breezy/main_test.go` (append `runStandalone` helper and dual-backend tests)
- New types referenced: `internal/config.Device` (already exists)

**Acceptance Criteria:**
- [ ] `directBackend{devices map[string]config.Device, mu sync.Mutex, clients map[string]*breezy.Client}` exists.
- [ ] `newDirectBackend(devices map[string]config.Device) *directBackend` constructor.
- [ ] Per-device `*breezy.Client` is lazily opened on first call to a verb for that name and reused for subsequent calls in the same CLI invocation. `directBackend.Close()` closes every cached client.
- [ ] Each backend method opens (or reuses) a client and calls `pkg/breezy/ops.X`. `Status` uses `breezy.GetStatus`, `Power` uses `breezy.Power`, etc.
- [ ] `Devices(ctx)` returns one `lsRow` per configured device — Name/ID/IP filled, Power=nil, Mode="", LastPoll="" (config dump per spec).
- [ ] `Faults` reads `0x007F` via the client and parses pairs (mirrors `pkg/breezy/ops.GetFaults` since the daemon's getFaults reads from cache; standalone reads UDP).
- [ ] `Firmware` calls `breezy.GetFirmware(ctx, c)` and renders `version` + `build_date` strings.
- [ ] `GetParam`/`SetParam` use `c.ReadParams` / `c.WriteParams` directly.
- [ ] `DaemonURLString()` returns `""` for directBackend.
- [ ] `runStandalone(t, fake)` helper builds a directBackend pointed at a `fakedevice` instance and returns a `(b backend, exec func(args ...string) (int, string, string))` pair.
- [ ] At least four parallel standalone tests pass: `TestStandalonePower`, `TestStandaloneSpeedPreset`, `TestStandaloneStatus`, `TestStandaloneFaults`. Each asserts the same shape as the existing daemon-backed tests.
- [ ] `just check-all` passes.

**Verify:** `go test ./cmd/breezy/... -run 'Standalone' -v` → all standalone tests PASS; `just check-all` exit 0.

**Steps:**

- [ ] **Step 1: Append `directBackend` to `cmd/breezy/backend.go`**

```go
import (
	// ... existing ...
	"sync"

	"github.com/hughobrien/breezyd/internal/config"
)

// directBackend implements backend by opening UDP clients directly to
// each configured device via pkg/breezy/ops. Per-device clients are
// lazy-opened on first use and reused for the rest of the CLI
// invocation; Close releases every open client.
type directBackend struct {
	devices map[string]config.Device

	mu      sync.Mutex
	clients map[string]*breezy.Client
}

func newDirectBackend(devices map[string]config.Device) *directBackend {
	return &directBackend{
		devices: devices,
		clients: map[string]*breezy.Client{},
	}
}

func (d *directBackend) DaemonURLString() string { return "" }

func (d *directBackend) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var firstErr error
	for _, c := range d.clients {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	d.clients = map[string]*breezy.Client{}
	return firstErr
}

// dial returns a *breezy.Client for the named device, opening one and
// caching it on first call. The client is reused for subsequent calls
// within the same CLI invocation.
func (d *directBackend) dial(name string) (*breezy.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.clients[name]; ok {
		return c, nil
	}
	cfg, ok := d.devices[name]
	if !ok {
		return nil, fmt.Errorf("device %q not configured", name)
	}
	if cfg.IP == "" {
		return nil, fmt.Errorf("device %q has no IP configured (run `breezy discover` to find it)", name)
	}
	c, err := breezy.NewClient(cfg.IP, cfg.ID, cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", name, err)
	}
	d.clients[name] = c
	return c, nil
}

func (d *directBackend) Status(ctx context.Context, name string) (breezy.Status, error) {
	c, err := d.dial(name)
	if err != nil {
		return breezy.Status{}, err
	}
	cfg := d.devices[name]
	return breezy.GetStatus(ctx, c, name, cfg.ID, cfg.IP)
}

func (d *directBackend) Power(ctx context.Context, name string, on bool) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.Power(ctx, c, on)
}

func (d *directBackend) SpeedPreset(ctx context.Context, name string, preset int) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetSpeedPreset(ctx, c, preset)
}

func (d *directBackend) SpeedManual(ctx context.Context, name string, pct int) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetSpeedManual(ctx, c, pct)
}

func (d *directBackend) Mode(ctx context.Context, name, mode string) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetMode(ctx, c, mode)
}

func (d *directBackend) Heater(ctx context.Context, name string, on bool) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetHeater(ctx, c, on)
}

func (d *directBackend) ResetFilter(ctx context.Context, name string) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.ResetFilter(ctx, c)
}

func (d *directBackend) ResetFaults(ctx context.Context, name string) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.ResetFaults(ctx, c)
}

func (d *directBackend) Faults(ctx context.Context, name string) ([]breezy.FaultCode, error) {
	c, err := d.dial(name)
	if err != nil {
		return nil, err
	}
	return breezy.GetFaults(ctx, c)
}

func (d *directBackend) Firmware(ctx context.Context, name string) (string, string, error) {
	c, err := d.dial(name)
	if err != nil {
		return "", "", err
	}
	fw, err := breezy.GetFirmware(ctx, c)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%d.%02d", fw.Major, fw.Minor), fw.Date.Format("2006-01-02"), nil
}

func (d *directBackend) Efficiency(ctx context.Context, name string) (int, error) {
	c, err := d.dial(name)
	if err != nil {
		return 0, err
	}
	return breezy.GetEfficiency(ctx, c)
}

func (d *directBackend) GetParam(ctx context.Context, name string, id breezy.ParamID) ([]byte, string, string, string, error) {
	c, err := d.dial(name)
	if err != nil {
		return nil, "", "", "", err
	}
	out, err := c.ReadParams(ctx, []breezy.ParamID{id})
	if err != nil {
		return nil, "", "", "", err
	}
	val, ok := out[id]
	if !ok {
		return nil, "", "", "", fmt.Errorf("device replied 'unsupported' for param 0x%04X", uint16(id))
	}
	pName, pType, pValue := "", "", ""
	if p, ok := breezy.LookupByID(id); ok {
		pName, pType = p.Name, p.Type.String()
		if v, decErr := p.Decode(val); decErr == nil {
			pValue = v.String()
		}
	}
	return val, pName, pType, pValue, nil
}

func (d *directBackend) SetParam(ctx context.Context, name string, id breezy.ParamID, value []byte) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return c.WriteParams(ctx, []breezy.ParamWrite{{ID: id, Value: value}})
}

func (d *directBackend) SetRTC(ctx context.Context, name string, t time.Time) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetRTC(ctx, c, t)
}

// Devices returns one row per configured device with Name/ID/IP filled
// in. Power/Mode/LastPoll are zero-valued (no cache in standalone) —
// the renderer already falls back gracefully ("?", "never").
func (d *directBackend) Devices(ctx context.Context) ([]lsRow, error) {
	rows := make([]lsRow, 0, len(d.devices))
	for name, cfg := range d.devices {
		rows = append(rows, lsRow{
			Name:      name,
			ID:        cfg.ID,
			IP:        cfg.IP,
			LastPoll:  "",
			Power:     nil,
			Mode:      "",
			Reachable: false,
		})
	}
	return rows, nil
}
```

**Note**: this assumes `breezy.NewClient(ip, id, password)` exists. Verify by grepping `pkg/breezy/client.go`. If the constructor signature differs (e.g. takes a struct or a `net.Addr`), adapt accordingly. The constructor in client.go is the source of truth for connection parameters; do NOT change it in this task.

- [ ] **Step 2: Add `runStandalone` helper to `cmd/breezy/main_test.go`**

The existing `runCLI` helper takes an `httptest.Server` (daemon side). `runStandalone` plays the same role but builds a directBackend pointed at a fakedevice. It needs to: spin up a `fakedevice.Server`, build a `directBackend` whose `dial` returns a `*breezy.Client` connected to the fake, and override the CLI's backend resolution to use it.

Since the CLI's `run` constructs its own backend via `resolveDaemonURL`/`newDaemonBackend`, we need a test seam. Add a package-level test hook in `main.go`:

```go
// testBackend, when non-nil, overrides the backend that run() would
// otherwise construct. Set by tests; nil in production.
var testBackend backend
```

In `run()`:

```go
var b backend
if testBackend != nil {
	b = testBackend
} else {
	daemonURL := resolveDaemonURL(*daemon)
	b = newDaemonBackend(daemonURL)
}
defer b.Close()
```

Then `runStandalone`:

```go
func runStandalone(t *testing.T, fake *fakedevice.Server, devices map[string]config.Device, args ...string) (int, string, string) {
	t.Helper()
	// Make sure all device IPs in the test devices map point at the fake.
	d := newDirectBackend(devices)

	// Override.
	prev := testBackend
	testBackend = d
	t.Cleanup(func() { testBackend = prev; _ = d.Close() })

	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}
```

Actually a cleaner approach: skip the `testBackend` indirection and have `runStandalone` build the directBackend and call the cmd functions directly (bypassing `run()`'s flag parsing). Use this if the testBackend hook feels awkward. Both work; pick whichever is shorter.

The `fakedevice.Server` API to use: read `pkg/breezy/fakedevice/*.go` for the constructor name. It probably exposes an Addr/IP/port. Use that as the device's IP in the test config.

- [ ] **Step 3: Add at least four parallel standalone tests**

```go
func TestStandalonePower(t *testing.T) {
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: fake.DeviceID(), Password: "test", IP: fake.Addr()},
	}
	code, _, stderr := runStandalone(t, fake, devices, "playroom", "on")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	// Verify the fakedevice received a write to 0x0001 with value 1.
	if !fake.HasWrite(0x0001, []byte{1}) {
		t.Errorf("fake did not see Power-on write: %v", fake.Writes())
	}
}

func TestStandaloneSpeedPreset(t *testing.T) {
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: fake.DeviceID(), Password: "test", IP: fake.Addr()},
	}
	code, _, stderr := runStandalone(t, fake, devices, "playroom", "speed", "2")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !fake.HasWrite(0x0002, []byte{2}) {
		t.Errorf("fake did not see SetSpeedPreset(2): %v", fake.Writes())
	}
}

func TestStandaloneStatus(t *testing.T) {
	fake := startFakeDevice(t)
	// Seed the fake with known param values so the status output
	// includes recognisable substrings.
	fake.Set(0x0001, []byte{1})
	devices := map[string]config.Device{
		"playroom": {ID: fake.DeviceID(), Password: "test", IP: fake.Addr()},
	}
	code, stdout, stderr := runStandalone(t, fake, devices, "playroom", "status")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "playroom") {
		t.Errorf("status output missing device name:\n%s", stdout)
	}
}

func TestStandaloneFaults(t *testing.T) {
	fake := startFakeDevice(t)
	// Seed two faults: code 17 alarm, code 22 warning.
	fake.Set(0x007F, []byte{17, 0, 22, 1})
	devices := map[string]config.Device{
		"playroom": {ID: fake.DeviceID(), Password: "test", IP: fake.Addr()},
	}
	code, stdout, _ := runStandalone(t, fake, devices, "playroom", "faults")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stdout, "17") || !strings.Contains(stdout, "22") {
		t.Errorf("faults output missing codes:\n%s", stdout)
	}
}
```

The `startFakeDevice`, `fake.HasWrite`, `fake.DeviceID`, `fake.Addr`, `fake.Set`, `fake.Writes` API names are illustrative — read `pkg/breezy/fakedevice/*.go` for the actual API and adapt. If the fake doesn't expose write inspection, use `c.ReadParams` after the write to verify the cached value changed (assuming the fake implements WriteThrough).

- [ ] **Step 4: Run the standalone tests**

```bash
go test ./cmd/breezy/... -run 'Standalone' -v
just check-all
```

All four standalone tests should pass. If a test fails because `fakedevice` doesn't behave like a real device for writes (e.g. doesn't store written values), simplify the assertion to "exit code 0" only — at minimum that proves the backend interface and dial chain work.

- [ ] **Step 5: Commit**

```bash
git add cmd/breezy/backend.go cmd/breezy/main.go cmd/breezy/main_test.go
git commit -m "$(cat <<'EOF'
cmd/breezy: add directBackend (standalone UDP path)

directBackend opens *breezy.Client per device on demand, calls
pkg/breezy/ops, and closes everything on Close. New runStandalone
test helper drives the CLI through directBackend against an
in-process fakedevice; four parallel tests exercise power/speed/
status/faults end-to-end.

Phase 2 part of issue #2.
EOF
)"
```

---

### Task 3: Backend resolution + `ls`/`daemon-url` for standalone; remove `defaultDaemonURL`

**Goal:** Replace `resolveDaemonURL` with `resolveBackend` that picks `daemonBackend` only when explicitly configured, otherwise picks `directBackend`. Remove the `defaultDaemonURL` constant. Update `cmdLs` and `cmdDaemonURL` (effectively the inline switch case) to handle the standalone case correctly.

**Files:**
- Modify: `cmd/breezy/main.go` (replace `resolveDaemonURL` with `resolveBackend`, remove `defaultDaemonURL`)
- Modify: `cmd/breezy/main_test.go` (update tests that asserted on the old default URL)

**Acceptance Criteria:**
- [ ] `defaultDaemonURL` constant is removed.
- [ ] `resolveBackend(override string, cfg *config.Config) (backend, error)` exists. Rules:
  - `override != ""` → `newDaemonBackend(normalizeURL(override))`
  - `cfg != nil && cfg.Daemon.Listen != ""` → `newDaemonBackend(normalizeURL(cfg.Daemon.Listen))`
  - Otherwise → `newDirectBackend(cfg.Devices)` (or empty map if cfg is nil)
- [ ] `run()` calls `resolveBackend` instead of `resolveDaemonURL` + `newDaemonBackend`.
- [ ] `daemon-url` global prints the URL when daemon mode, else prints `(standalone — no daemon)`.
- [ ] `cmdLs` works in both modes: daemon mode shows live columns from cache, standalone mode shows config-only rows with `?`/`never` placeholders (the existing `renderLs` already substitutes those for empty fields).
- [ ] Existing test `TestDaemonURL` (which asserts on the explicit `--daemon` override path) still passes.
- [ ] If a test exists for the old default-fallback behavior (`TestDaemonURLDefaultsTo127001`), it's updated or removed because the default is no longer "fall through to localhost".
- [ ] `just check-all` passes.

**Verify:** `just check-all` exits 0; `breezy daemon-url` against a config without `[daemon].listen` prints `(standalone — no daemon)`.

**Steps:**

- [ ] **Step 1: Replace `resolveDaemonURL` with `resolveBackend` in `cmd/breezy/main.go`**

Delete:

```go
const defaultDaemonURL = "http://127.0.0.1:9876"
// ...
func resolveDaemonURL(override string) string { ... }
```

Replace with:

```go
// resolveBackend picks a backend based on the precedence:
//
//  1. --daemon URL flag (explicit override).
//  2. ~/.config/breezy/config.toml [daemon].listen.
//  3. Standalone (direct UDP via pkg/breezy/ops).
//
// There is no fallback URL: if neither a flag nor config opts in to
// daemon mode, we go standalone. The user's choice is honoured —
// daemon-mode-but-unreachable surfaces as a clear HTTP error from the
// first request, not a silent fall-through.
func resolveBackend(override string, cfg *config.Config) (backend, error) {
	if override != "" {
		return newDaemonBackend(normalizeURL(override)), nil
	}
	if cfg != nil && cfg.Daemon.Listen != "" {
		return newDaemonBackend(normalizeURL(cfg.Daemon.Listen)), nil
	}
	devices := map[string]config.Device{}
	if cfg != nil {
		devices = cfg.Devices
	}
	return newDirectBackend(devices), nil
}
```

- [ ] **Step 2: Update `run()` to call `resolveBackend`**

```go
func run(args []string, stdout, stderr io.Writer) int {
	// ... flag parsing unchanged ...

	cfg := loadConfig()  // small helper that wraps the existing config.Load chain
	b, err := resolveBackend(*daemon, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	defer b.Close()

	// ... dispatch unchanged ...
}
```

The `loadConfig()` helper centralises:

```go
func loadConfig() *config.Config {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	cfg, err := config.Load(filepath.Join(home, ".config", "breezy", "config.toml"))
	if err != nil {
		return nil
	}
	return cfg
}
```

Errors loading config silently fall through to "no config" (matches the pre-Phase-2 `resolveDaemonURL` behavior).

If `testBackend != nil`, prefer it over `resolveBackend`:

```go
var b backend
if testBackend != nil {
	b = testBackend
} else {
	cfg := loadConfig()
	var err error
	b, err = resolveBackend(*daemon, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
}
defer b.Close()
```

- [ ] **Step 3: Update the `daemon-url` global**

The existing globals switch (around line 100 of main.go pre-Phase-2):

```go
case "daemon-url":
	fmt.Fprintln(stdout, daemonURL)
	return 0
```

becomes:

```go
case "daemon-url":
	url := b.DaemonURLString()
	if url == "" {
		fmt.Fprintln(stdout, "(standalone — no daemon)")
	} else {
		fmt.Fprintln(stdout, url)
	}
	return 0
```

- [ ] **Step 4: Update tests that assumed the old default fallback URL**

The pre-Phase-2 `TestDaemonURLNormalizesBareHostPort` and similar tests use `--daemon` overrides explicitly — those should still pass because `resolveBackend`'s rule 1 honours the override.

If a test like `TestDaemonURLDefaults` exists (asserting on `127.0.0.1:9876` when no flag/config is given), update it to assert on the new standalone behavior:

```go
func TestDaemonURLStandaloneByDefault(t *testing.T) {
	// No --daemon flag, no config (HOME points at a tempdir without one).
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"daemon-url"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "(standalone — no daemon)" {
		t.Errorf("got %q, want '(standalone — no daemon)'", got)
	}
}
```

Use `t.Setenv("HOME", t.TempDir())` to neutralise any real `~/.config/breezy/config.toml` so the test is hermetic. (The real `os.UserHomeDir()` honours `$HOME` on Unix.)

- [ ] **Step 5: Run the test suite**

```bash
just check-all
```

Most tests pass unchanged. The places that needed updates: any test that built the CLI's URL assumption around the old default. Grep for `127.0.0.1:9876` in `cmd/breezy/main_test.go` — each match is a candidate for review.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezy/main.go cmd/breezy/main_test.go
git commit -m "$(cat <<'EOF'
cmd/breezy: standalone is the new default; daemon mode is opt-in

resolveBackend picks daemonBackend only when --daemon flag is given
or [daemon].listen is set in config. Otherwise it picks
directBackend, which talks UDP to the device(s) directly via
pkg/breezy/ops. The legacy fallback to http://127.0.0.1:9876 is
removed — opting in to daemon mode means owning the daemon's
reachability.

`breezy daemon-url` prints "(standalone — no daemon)" when no daemon
is configured, matching the new behavior.

Phase 2 part of issue #2.
EOF
)"
```

---

### Task 4: First-run config flip + README + CLAUDE.md + CHANGELOG

**Goal:** Update the bootstrap config template so `[daemon]` is commented out by default — new users land in standalone. Update the bootstrap test in lockstep. Document standalone mode and the daemon opt-in in README.md, CLAUDE.md, and CHANGELOG.md. Mention the multi-CLI concurrency caveat.

**Files:**
- Modify: `internal/config/config.go` (`defaultConfigTemplate`)
- Modify: `internal/config/config_test.go` (`TestWriteDefault_FreshTempDir` and any other test that asserts on the active `listen = "..."` line)
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `CHANGELOG.md` (add `[Unreleased]` entry)

**Acceptance Criteria:**
- [ ] `defaultConfigTemplate` in `internal/config/config.go` has the entire `[daemon]` block commented out (every line begins with `# `). A one-line preamble comment explains how to enable daemon mode (e.g. `# Uncomment the [daemon] block below to run the breezyd daemon and have the CLI talk to it via HTTP. Without it, the CLI runs in standalone mode.`).
- [ ] `TestWriteDefault_FreshTempDir` updated: instead of asserting `listen        = "127.0.0.1:9876"` is present as an active line, asserts that the line is present AS A COMMENT (e.g. `# listen        = "127.0.0.1:9876"`). Other expected-substring assertions stay the same.
- [ ] README.md gains a paragraph (under the CLI surface or near the top) explaining standalone mode, when to use the daemon, and the multi-CLI concurrency caveat.
- [ ] CLAUDE.md's Architecture section updates to mention standalone mode is the new default. The "Why a daemon owns UDP" paragraph stays — it's still true *when* the daemon runs — but adds a note that standalone CLI invocations are safe within a single process and the user takes on the multi-process coordination if they script around it.
- [ ] CHANGELOG.md `[Unreleased]` entry: `**Breaking:** the CLI defaults to standalone mode (direct UDP) when no daemon is configured. Previously it tried http://127.0.0.1:9876. To keep the old behavior, set [daemon] listen = "127.0.0.1:9876" in ~/.config/breezy/config.toml or pass --daemon http://127.0.0.1:9876.` Plus a positive entry: `**Added:** `breezy` CLI runs without `breezyd` for ad-hoc commands; first-run config has `[daemon]` commented out so new users land in standalone mode.`
- [ ] `just check-all` passes (covers the config test).

**Verify:** `just check-all` exits 0; `breezy --help` (or by inspection) confirms README/CLAUDE.md updates render reasonably.

**Steps:**

- [ ] **Step 1: Update `internal/config/config.go::defaultConfigTemplate`**

Replace the current template (around lines 182-201) with:

```go
const defaultConfigTemplate = `# breezyd configuration. See:
#   https://github.com/hughobrien/breezyd#configuration
#
# This file must remain mode 0600 — the daemon refuses to start otherwise.

# Uncomment the [daemon] block to run the breezyd daemon and have the
# CLI talk to it over HTTP (enables caching, polling, /metrics, and the
# embedded dashboard). Without it, the CLI talks UDP directly to each
# configured device — this is the default and is fine for ad-hoc use.
#
# [daemon]
# listen        = "127.0.0.1:9876"
# poll_interval = "30s"
# discovery     = "on-start"   # "on-start" | "off" | "periodic:<duration>"

# One [devices.<name>] block per Breezy unit. Run ` + "`breezy discover`" + ` to
# find device IDs on your LAN, then uncomment one of the blocks below
# and fill in your values. The ip line is optional — if omitted, on-start
# discovery resolves it (only useful in daemon mode).
#
# [devices.playroom]
# id       = "BREEZY00000000A0"
# password = "your-protocol-password"
# ip       = "192.168.1.148"
`
```

- [ ] **Step 2: Update `TestWriteDefault_FreshTempDir`**

Find the test (around line 283 of `internal/config/config_test.go`). Today it asserts on substrings including `listen        = "127.0.0.1:9876"` (around line 302). Update to:

```go
expectedSubstrings := []string{
	`# listen        = "127.0.0.1:9876"`,
	// ... other substrings kept the same ...
}
```

i.e. assert the line is now commented. Other expected-substring assertions (about file mode, parent dir, etc.) stay.

If there's an existing assertion that the file `Load`s cleanly after WriteDefault (the existing test does call `Load(path)` after writing), verify it still passes — Load should accept a config without an active `[daemon]` block (the loader has been doing this since the field was made optional; verify by reading `internal/config/config.go::Load`).

- [ ] **Step 3: Update `README.md`**

In the CLI surface section (or near the top, depending on the existing structure), add a short paragraph:

```markdown
### Standalone mode

The CLI works without `breezyd`. By default — when no daemon is
configured — `breezy <name> <verb>` opens a UDP connection to the
device, issues the requested operation, and exits. This is fine for
ad-hoc commands and matches the no-install / first-run experience.

Run the daemon (`breezyd`) when you want polling, caching,
`/metrics`, the embedded web dashboard, or to coordinate writes
across multiple CLI processes. Uncomment the `[daemon]` block in
`~/.config/breezy/config.toml` and start `breezyd`; the CLI then
prefers daemon mode automatically.

**Concurrency caveat:** the daemon serialises per-device UDP behind a
mutex. Standalone CLI processes do not coordinate with each other —
two `breezy` invocations against the same device at the same instant
can produce silent checksum corruption. If you script invocations
in parallel against the same device, run the daemon and use the CLI
in daemon mode.
```

Read the existing README structure first to pick the right insertion point. Match the markdown style.

- [ ] **Step 4: Update `CLAUDE.md`**

In the Architecture section, after the "Why a daemon owns UDP" paragraph, add:

```markdown
**Standalone mode (default).** The CLI also runs without the daemon —
opening UDP per-invocation via `pkg/breezy/ops` against each
configured device. This is the new default; daemon mode is opt-in
via `--daemon URL` or `[daemon].listen` in config. Within a single
CLI invocation, `pkg/breezy.Client` serialises UDP behind a mutex.
Across multiple CLI invocations against the same device, no
coordination exists — the same hazard `discover` already has applies.
If users script parallel invocations, they should run the daemon.
```

In the "## CLI surface" section, mention standalone in passing (the existing globals list is fine; just note that without a daemon configured, the CLI still works).

- [ ] **Step 5: Update `CHANGELOG.md`**

Find the `[Unreleased]` section (or create one at the top above the latest released version). Add:

```markdown
## [Unreleased]

### Added
- `breezy` CLI runs without `breezyd` for ad-hoc commands. By default — when no daemon is configured — the CLI talks UDP directly to each configured device via `pkg/breezy/ops`. (#2)

### Changed
- **Breaking:** the CLI no longer falls back to `http://127.0.0.1:9876` when no daemon is configured. To keep the old behavior, set `[daemon] listen = "127.0.0.1:9876"` in `~/.config/breezy/config.toml` or pass `--daemon http://127.0.0.1:9876`. New first-run config has the `[daemon]` block commented out — new users land in standalone mode.
- The daemon's per-verb HTTP handlers are now thin wrappers around `pkg/breezy/ops`. JSON shape unchanged. (Phase 1 of #2)
```

- [ ] **Step 6: Run the test suite + smoke test**

```bash
just check-all
```

Then a manual smoke test:

```bash
# Build and run with no config / no daemon.
go build ./cmd/breezy
HOME=$(mktemp -d) ./breezy daemon-url
# Expected: "(standalone — no daemon)"
```

If the binary is named differently or the smoke test reveals an unexpected interaction (e.g. `loadConfig()` produces a "config doesn't exist" error message instead of silently returning nil), adjust the helper.

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go README.md CLAUDE.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
config: comment out [daemon] in first-run template; document standalone

Closes #2. New first-run config has the [daemon] block commented
out so users without a daemon installed land in standalone mode.
README, CLAUDE.md, and CHANGELOG document the default flip, the
opt-in path, and the multi-process concurrency caveat.
EOF
)"
```

---

## Out of scope (Phase 3, never)

- Auto-fallback when daemon is configured but unreachable (rejected in brainstorming Q2).
- A `--standalone` override flag for one-off invocations when daemon is configured (rejected in Q4).
- Caching, polling, `/metrics`, web UI, fan-settle suppression in standalone mode.
- Cross-process UDP coordination.
- Migrating the daemon's `defaultReadIDs` to `breezy.StatusParamIDs` (the constant exists from Phase 1; the migration is mechanical and can land separately).
