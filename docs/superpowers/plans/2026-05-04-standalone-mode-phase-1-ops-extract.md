# Standalone Mode Phase 1 — Ops Extract & Daemon Refactor

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract per-verb protocol logic from `cmd/breezyd/handlers_*.go` into a new `pkg/breezy/ops.go` library, and migrate the structured-snapshot builder into `pkg/breezy/status.go`. After this phase the daemon's HTTP handlers are thin (parse → call op → cache → respond) and the ops layer has its own test coverage. **No user-visible change** — same HTTP API, same daemon behaviour.

**Architecture:** Six tasks. (1) Move `SnapshotResponse` + `buildSnapshot` + decode helpers + name decoders to `pkg/breezy/status.go` as `Status` + `BuildStatus`. (2-3) Add `pkg/breezy/ops.go` with high-level operations (writes first, reads second), each with `fakedevice` tests. (4) Add `cmd/breezyd/recording_client.go` — wraps `pkg/breezy.Client` and notifies a callback on writes, replacing the per-handler `h.recordWrite(...)` calls. (5-6) Rewrite `handlers_device.go` and `handlers_service.go` to use ops + recording wrapper. Each handler shrinks to ~10 lines.

**Tech Stack:** Go 1.22+, existing `pkg/breezy/fakedevice`, no new external deps.

**Spec:** `docs/superpowers/specs/2026-05-04-standalone-mode-design.md` (Phase 1 section).
**Issue:** [#2 — Standalone Mode](https://github.com/hughobrien/breezyd/issues/2)

---

### Task 1: Move snapshot/status types and helpers to `pkg/breezy/status.go`

**Goal:** Lift `SnapshotResponse` (renamed `Status`), `buildSnapshot` (renamed `BuildStatus`), decode helpers (`uint8At`/`uint16At`/`int16At`), and name decoders (`computeInUserControl`, `decodeAlerts`, `airflowModeName`, `specialModeName`, `faultLevelName`) to `pkg/breezy/status.go` so anything in the module — daemon or, later, CLI — can build a structured snapshot from raw parameter bytes. No behaviour change: the existing JSON shape is preserved exactly.

**Files:**
- Create: `pkg/breezy/status.go` (new file holding the lifted code)
- Create: `pkg/breezy/status_test.go` (extracted from existing daemon tests covering `buildSnapshot`)
- Modify: `cmd/breezyd/snapshot.go` (delete moved code, leave only daemon-specific glue if any)
- Modify: `cmd/breezyd/decode.go` (delete moved helpers; file likely becomes empty and is removed)
- Modify: `cmd/breezyd/handlers_device.go` (`getDevice` now calls `breezy.BuildStatus(...)`)
- Modify: `cmd/breezyd/metrics.go` (call sites for `uint8At`/`uint16At`/`int16At` and `computeInUserControl` adapt to free-function signatures)
- Modify: `cmd/breezyd/server.go` (no change expected, but verify after import surface change)
- Verify: existing tests in `cmd/breezyd/server_test.go`, `cmd/breezyd/metrics_test.go`, plus any `snapshot_test.go` or equivalent — all should keep passing without modification beyond import path adjustments.

**Acceptance Criteria:**
- [ ] `pkg/breezy.Status` struct exists with the same JSON tags as today's `cmd/breezyd.SnapshotResponse` (`name`, `id`, `ip`, `last_poll`, `configured`, `live`, `sensors`, `service`, `firmware`).
- [ ] `pkg/breezy.BuildStatus(values map[ParamID][]byte, name, id, ip string, lastPoll *time.Time) Status` exists and produces JSON byte-equal to the old `Handler.buildSnapshot` for the same inputs.
- [ ] `pkg/breezy.Uint8At`, `pkg/breezy.Uint16At`, `pkg/breezy.Int16At` exist as free functions over `map[ParamID][]byte` (capitalised — they're now exported library helpers).
- [ ] `pkg/breezy.ComputeInUserControl(values map[ParamID][]byte) bool` and `pkg/breezy.DecodeAlerts(values map[ParamID][]byte) map[string]any` exist as free functions.
- [ ] `pkg/breezy.AirflowModeName`, `pkg/breezy.SpecialModeName`, `pkg/breezy.FaultLevelName` exist as exported helpers.
- [ ] `cmd/breezyd/decode.go` is removed (or left containing only daemon-specific helpers that don't move).
- [ ] `cmd/breezyd/snapshot.go` is removed (its contents have moved) OR contains only `cmd/breezyd`-specific things if any remain.
- [ ] `cmd/breezyd/handlers_device.go` `getDevice` calls `breezy.BuildStatus(snap.Values, name, cfg.ID, cfg.IP, lastPollPtr(snap))` where `lastPollPtr` returns `&snap.LastPoll` if non-zero, else nil.
- [ ] `cmd/breezyd/metrics.go` compiles and behaves identically — its `Update(name, id, snap Snapshot)` keeps the `Snapshot` parameter type, just calls `breezy.Uint8At(snap.Values, ...)` etc. internally.
- [ ] `just check-all` passes (lint + race + UI). No tests should need rewriting beyond import-path changes.

**Verify:** `just check-all` exits 0; `go test ./cmd/breezyd/... -v` shows no failures.

**Steps:**

- [ ] **Step 1: Create `pkg/breezy/status.go` with the lifted types and helpers**

The new file should contain:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

// Status is the structured per-device snapshot returned by the daemon's
// HTTP API and consumed by the CLI's renderers. It is built from raw
// parameter bytes (as returned by Client.ReadParams or stored in the
// daemon's cache) via BuildStatus. The JSON shape is part of the public
// API of the daemon; do not change tag names or field types without
// considering downstream consumers (including the embedded dashboard).
import (
	"encoding/binary"
	"fmt"
	"time"
)

type Status struct {
	Name       string         `json:"name"`
	ID         string         `json:"id"`
	IP         string         `json:"ip"`
	LastPoll   string         `json:"last_poll,omitempty"`
	Configured map[string]any `json:"configured"`
	Live       map[string]any `json:"live"`
	Sensors    map[string]any `json:"sensors"`
	Service    map[string]any `json:"service"`
	Firmware   map[string]any `json:"firmware,omitempty"`
}

// BuildStatus decodes raw parameter bytes into the Status shape. Decode
// failures fall back to a missing/zero value rather than producing an
// error — an unfamiliar firmware shouldn't take down the API. lastPoll
// may be nil (e.g. a fresh standalone read with no poll history); when
// non-nil it is rendered in RFC3339 UTC.
func BuildStatus(values map[ParamID][]byte, name, id, ip string, lastPoll *time.Time) Status {
	resp := Status{
		Name:       name,
		ID:         id,
		IP:         ip,
		Configured: map[string]any{},
		Live:       map[string]any{},
		Sensors:    map[string]any{},
		Service:    map[string]any{},
	}
	if lastPoll != nil && !lastPoll.IsZero() {
		resp.LastPoll = lastPoll.UTC().Format(time.RFC3339)
	}

	// Configured: what the user set.
	if b, ok := Uint8At(values, 0x0001); ok {
		resp.Configured["power"] = b == 1
	}
	if b, ok := Uint8At(values, 0x0002); ok {
		switch b {
		case 0xFF:
			resp.Configured["speed_mode"] = "manual"
		case 1, 2, 3:
			resp.Configured["speed_mode"] = fmt.Sprintf("preset%d", b)
		default:
			resp.Configured["speed_mode"] = fmt.Sprintf("unknown(%d)", b)
		}
	}
	if b, ok := Uint8At(values, 0x0044); ok {
		resp.Configured["manual_pct"] = int(b)
	}
	if b, ok := Uint8At(values, 0x00B7); ok {
		resp.Configured["airflow_mode"] = AirflowModeName(b)
	}
	if b, ok := Uint8At(values, 0x0068); ok {
		resp.Configured["heater_enabled"] = b == 1
	}
	if v, ok := Uint16At(values, 0x001A); ok {
		resp.Configured["co2_threshold_ppm"] = int(v)
	}
	if b, ok := Uint8At(values, 0x0019); ok {
		resp.Configured["humidity_threshold_pct"] = int(b)
	}
	if v, ok := Uint16At(values, 0x031F); ok {
		resp.Configured["voc_threshold_index"] = int(v)
	}

	// Live: the device's actual current behavior.
	if v, ok := Uint16At(values, 0x004A); ok {
		resp.Live["fan_supply_rpm"] = int(v)
	}
	if v, ok := Uint16At(values, 0x004B); ok {
		resp.Live["fan_extract_rpm"] = int(v)
	}
	if b, ok := Uint8At(values, 0x0081); ok {
		resp.Live["heater_running"] = b == 1
	}
	resp.Live["in_user_control"] = ComputeInUserControl(values)
	resp.Live["sensor_alerts"] = DecodeAlerts(values)
	if b, ok := Uint8At(values, 0x0007); ok {
		resp.Live["special_mode"] = SpecialModeName(b)
	}
	if raw, ok := values[0x000B]; ok && len(raw) == 3 {
		secs := int(raw[2])*3600 + int(raw[1])*60 + int(raw[0])
		resp.Live["special_mode_remaining_seconds"] = secs
	}

	// Sensors: live readings.
	if b, ok := Uint8At(values, 0x0025); ok {
		resp.Sensors["humidity_pct"] = int(b)
	}
	if v, ok := Uint16At(values, 0x0027); ok {
		resp.Sensors["eco2_ppm"] = int(v)
	}
	if v, ok := Uint16At(values, 0x0320); ok {
		resp.Sensors["voc_index"] = int(v)
	}
	for _, t := range []struct {
		id   ParamID
		name string
	}{
		{0x001F, "temp_outdoor_c"},
		{0x0020, "temp_supply_c"},
		{0x0021, "temp_exhaust_inlet_c"},
		{0x0022, "temp_exhaust_outlet_c"},
	} {
		if v, ok := Int16At(values, t.id); ok {
			if v == -32768 || v == 32767 {
				continue
			}
			resp.Sensors[t.name] = float64(v) / 10.0
		}
	}
	if b, ok := Uint8At(values, 0x0129); ok {
		resp.Sensors["recovery_efficiency_pct"] = int(b)
	}

	// Service: filter, motor, RTC battery, faults.
	if b, ok := Uint8At(values, 0x0088); ok {
		if b == 0 {
			resp.Service["filter_status"] = "clean"
		} else {
			resp.Service["filter_status"] = "soiled"
		}
	}
	if raw, ok := values[0x0064]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		resp.Service["filter_remaining_seconds"] = secs
	}
	if raw, ok := values[0x007E]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		resp.Service["motor_lifetime_seconds"] = secs
	}
	if v, ok := Uint16At(values, 0x0024); ok {
		resp.Service["rtc_battery_volts"] = float64(v) / 1000.0
	}
	if b, ok := Uint8At(values, 0x0083); ok {
		resp.Service["fault_level"] = FaultLevelName(b)
	}
	if b, ok := Uint8At(values, 0x030B); ok {
		resp.Service["frost_protection_active"] = b == 1
	}

	// Firmware.
	if raw, ok := values[0x0086]; ok && len(raw) == 6 {
		fw := map[string]any{
			"version": fmt.Sprintf("%d.%02d", raw[0], raw[1]),
		}
		year := int(uint16(raw[4]) | uint16(raw[5])<<8)
		fw["build_date"] = fmt.Sprintf("%04d-%02d-%02d", year, raw[3], raw[2])
		resp.Firmware = fw
	}
	return resp
}

// Uint8At returns the single byte stored at id, or (0, false) if the
// value is missing or wrong-sized.
func Uint8At(values map[ParamID][]byte, id ParamID) (uint8, bool) {
	raw, ok := values[id]
	if !ok || len(raw) != 1 {
		return 0, false
	}
	return raw[0], true
}

// Uint16At returns the LE 2-byte value at id.
func Uint16At(values map[ParamID][]byte, id ParamID) (uint16, bool) {
	raw, ok := values[id]
	if !ok || len(raw) != 2 {
		return 0, false
	}
	return binary.LittleEndian.Uint16(raw), true
}

// Int16At returns the LE 2-byte signed value at id.
func Int16At(values map[ParamID][]byte, id ParamID) (int16, bool) {
	v, ok := Uint16At(values, id)
	if !ok {
		return 0, false
	}
	return int16(v), true
}

// ComputeInUserControl returns true when the device is behaving according
// to user configuration, false when a firmware-driven override is in
// effect (sensor alert, special-mode timer, frost protection).
func ComputeInUserControl(values map[ParamID][]byte) bool {
	if b, ok := Uint8At(values, 0x0007); ok && b != 0 {
		return false
	}
	if raw, ok := values[0x0084]; ok {
		for _, b := range raw {
			if b != 0 {
				return false
			}
		}
	}
	if b, ok := Uint8At(values, 0x030B); ok && b == 1 {
		return false
	}
	return true
}

// DecodeAlerts surfaces the per-sensor over-threshold flags from 0x84.
// Missing 0x84 yields all-false (cache miss is conservatively "no alert").
func DecodeAlerts(values map[ParamID][]byte) map[string]any {
	out := map[string]any{"humidity": false, "co2": false, "voc": false}
	raw, ok := values[0x0084]
	if !ok || len(raw) < 5 {
		return out
	}
	out["humidity"] = raw[0] != 0
	out["co2"] = raw[1] != 0
	out["voc"] = raw[4] != 0
	return out
}

// AirflowModeName decodes 0xB7. Anything outside 0..3 falls through to a
// debug-only string so future firmware additions don't lose data.
func AirflowModeName(b uint8) string {
	switch b {
	case 0:
		return "ventilation"
	case 1:
		return "regeneration"
	case 2:
		return "supply"
	case 3:
		return "extract"
	}
	return fmt.Sprintf("unknown(%d)", b)
}

// SpecialModeName decodes 0x07 (0=off, 1=night, 2=turbo).
func SpecialModeName(b uint8) string {
	switch b {
	case 0:
		return "off"
	case 1:
		return "night"
	case 2:
		return "turbo"
	}
	return fmt.Sprintf("unknown(%d)", b)
}

// FaultLevelName decodes 0x83 (0=none, 1=alarm, 2=warning).
func FaultLevelName(b uint8) string {
	switch b {
	case 0:
		return "none"
	case 1:
		return "alarm"
	case 2:
		return "warning"
	}
	return fmt.Sprintf("unknown(%d)", b)
}
```

- [ ] **Step 2: Create `pkg/breezy/status_test.go` with focused unit tests**

The existing daemon tests in `cmd/breezyd/server_test.go` cover `buildSnapshot` indirectly via end-to-end HTTP calls. Add direct unit tests for `BuildStatus` that exercise the decode branches:

```go
package breezy

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildStatus_Empty(t *testing.T) {
	s := BuildStatus(map[ParamID][]byte{}, "playroom", "BREEZYID", "192.168.1.1", nil)
	if s.Name != "playroom" || s.ID != "BREEZYID" || s.IP != "192.168.1.1" {
		t.Errorf("identity fields wrong: %+v", s)
	}
	if s.LastPoll != "" {
		t.Errorf("LastPoll should be empty when nil pointer passed, got %q", s.LastPoll)
	}
	// Configured/Live/Sensors/Service/Firmware should be present (non-nil).
	if s.Configured == nil || s.Live == nil || s.Sensors == nil || s.Service == nil {
		t.Errorf("blocks must be non-nil maps even when empty")
	}
}

func TestBuildStatus_LastPollRendered(t *testing.T) {
	tt := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	s := BuildStatus(map[ParamID][]byte{}, "n", "i", "ip", &tt)
	if s.LastPoll != "2026-05-04T10:00:00Z" {
		t.Errorf("LastPoll = %q, want 2026-05-04T10:00:00Z", s.LastPoll)
	}
}

func TestBuildStatus_PowerSpeedMode(t *testing.T) {
	values := map[ParamID][]byte{
		0x0001: {1},      // power on
		0x0002: {0xFF},   // manual mode
		0x0044: {30},     // 30%
		0x00B7: {1},      // regeneration
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	if s.Configured["power"] != true {
		t.Errorf("power: want true, got %v", s.Configured["power"])
	}
	if s.Configured["speed_mode"] != "manual" {
		t.Errorf("speed_mode: want manual, got %v", s.Configured["speed_mode"])
	}
	if s.Configured["manual_pct"] != 30 {
		t.Errorf("manual_pct: want 30, got %v", s.Configured["manual_pct"])
	}
	if s.Configured["airflow_mode"] != "regeneration" {
		t.Errorf("airflow_mode: want regeneration, got %v", s.Configured["airflow_mode"])
	}
}

func TestBuildStatus_TempSensorSentinels(t *testing.T) {
	values := map[ParamID][]byte{
		0x001F: {0x00, 0x80}, // -32768 (no sensor)
		0x0020: {0xFF, 0x7F}, // 32767 (short circuit)
		0x0021: {0xC8, 0x00}, // 200 -> 20.0°C
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	if _, ok := s.Sensors["temp_outdoor_c"]; ok {
		t.Errorf("temp_outdoor_c should be omitted on sentinel -32768")
	}
	if _, ok := s.Sensors["temp_supply_c"]; ok {
		t.Errorf("temp_supply_c should be omitted on sentinel 32767")
	}
	if v := s.Sensors["temp_exhaust_inlet_c"].(float64); v != 20.0 {
		t.Errorf("temp_exhaust_inlet_c: want 20.0, got %v", v)
	}
}

func TestBuildStatus_FirmwareBlock(t *testing.T) {
	// 6-byte firmware: major.minor + build date.
	values := map[ParamID][]byte{
		0x0086: {1, 5, 0x0F, 0x05, 0xEA, 0x07}, // 1.05, 2026-05-15
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	if s.Firmware == nil {
		t.Fatal("Firmware should be set when 0x0086 is 6 bytes")
	}
	if s.Firmware["version"] != "1.05" {
		t.Errorf("version: want 1.05, got %v", s.Firmware["version"])
	}
	if s.Firmware["build_date"] != "2026-05-15" {
		t.Errorf("build_date: want 2026-05-15, got %v", s.Firmware["build_date"])
	}
}

func TestBuildStatus_JSONShape(t *testing.T) {
	// Smoke test: marshal to JSON and confirm the top-level keys are present.
	s := BuildStatus(map[ParamID][]byte{}, "n", "i", "ip", nil)
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"name"`, `"id"`, `"ip"`, `"configured"`, `"live"`, `"sensors"`, `"service"`} {
		if !contains(string(out), key) {
			t.Errorf("JSON output missing key %s: %s", key, out)
		}
	}
}

func TestComputeInUserControl(t *testing.T) {
	// User in control: no special mode, no alerts, no frost.
	if !ComputeInUserControl(map[ParamID][]byte{0x0007: {0}}) {
		t.Error("expected true when 0x07=0, no other signals")
	}
	// Special mode running -> override.
	if ComputeInUserControl(map[ParamID][]byte{0x0007: {1}}) {
		t.Error("expected false when 0x07=1 (special mode)")
	}
	// Sensor alert -> override.
	if ComputeInUserControl(map[ParamID][]byte{0x0084: {1, 0, 0, 0, 0}}) {
		t.Error("expected false when 0x84 has any non-zero byte")
	}
	// Frost protection -> override.
	if ComputeInUserControl(map[ParamID][]byte{0x030B: {1}}) {
		t.Error("expected false when 0x030B=1 (frost protection)")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3: Update daemon call sites and delete the old files**

In `cmd/breezyd/handlers_device.go::getDevice`, change:

```go
resp := h.buildSnapshot(name, cfg, snap)
```

to:

```go
var lastPoll *time.Time
if !snap.LastPoll.IsZero() {
	lastPoll = &snap.LastPoll
}
if snap.IP != "" {
	cfg.IP = snap.IP
}
resp := breezy.BuildStatus(snap.Values, name, cfg.ID, cfg.IP, lastPoll)
```

In `cmd/breezyd/metrics.go`, replace every `uint8At(snap, ...)` with `breezy.Uint8At(snap.Values, ...)`, similarly for `uint16At`/`int16At`, and `computeInUserControl(snap)` with `breezy.ComputeInUserControl(snap.Values)`.

Delete `cmd/breezyd/snapshot.go` (its contents have moved). Delete `cmd/breezyd/decode.go` similarly. Verify with `grep -rn "uint8At\|uint16At\|int16At\|buildSnapshot\|computeInUserControl\|decodeAlerts\|airflowModeName\|specialModeName\|faultLevelName\|SnapshotResponse" cmd/breezyd/` that no daemon-side references remain.

Verify `cmd/breezyd/state.go` `Snapshot` type stays where it is (still the daemon's cache type — separate concern from `pkg/breezy.Status`).

- [ ] **Step 4: Run the test suite to verify behaviour preserved**

Run: `go test ./pkg/breezy/... -run BuildStatus -v` → expect PASS for all new tests.
Run: `go test ./cmd/breezyd/... -v` → expect PASS for all existing tests (server, metrics, poller, state).
Run: `just check-all` → expect exit 0 (lint + race + UI tests).

If any cmd/breezyd test fails: it likely references a moved symbol; update the import path.

- [ ] **Step 5: Commit**

```bash
git add pkg/breezy/status.go pkg/breezy/status_test.go \
        cmd/breezyd/snapshot.go cmd/breezyd/decode.go \
        cmd/breezyd/handlers_device.go cmd/breezyd/metrics.go
git commit -m "$(cat <<'EOF'
pkg/breezy: lift Status + decode helpers into the library

Hoists SnapshotResponse → Status, buildSnapshot → BuildStatus,
the uint*At decode helpers, and the name decoders out of cmd/breezyd
so any caller (daemon or future standalone CLI) can build a
structured snapshot from raw parameter bytes. JSON shape unchanged.

Phase 1 prep for issue #2.
EOF
)"
```

---

### Task 2: Add `pkg/breezy/ops.go` with write operations

**Goal:** Introduce the high-level operations layer with all eight write ops (`Power`, `SetSpeedPreset`, `SetSpeedManual`, `SetMode`, `SetHeater`, `ResetFilter`, `ResetFaults`, `SetRTC`). Each is a small function that validates inputs, builds the right `[]ParamWrite`, and calls the supplied `Client`. Each gets table-driven tests against `pkg/breezy/fakedevice` plus a recording wrapper that captures the writes for assertion.

**Files:**
- Create: `pkg/breezy/ops.go` (new file)
- Create: `pkg/breezy/ops_test.go` (table tests for the eight ops)

**Acceptance Criteria:**
- [ ] `pkg/breezy/ops.go` declares interface `Client { ReadParams(ctx, []ParamID) (map[ParamID][]byte, error); WriteParams(ctx, []ParamWrite) error }` (note: `Close()` is not part of this interface — lifecycle is the caller's concern).
- [ ] All eight write ops exist with the signatures listed in the spec.
- [ ] Each op validates inputs and returns a wrapped error rooted in a new sentinel `ErrInvalidArg` (e.g. `fmt.Errorf("%w: preset must be 1-3, got %d", ErrInvalidArg, preset)`).
- [ ] `SetSpeedManual` issues exactly one write packet containing both `0x0044=pct` and `0x0002=0xFF` in that order — preserving the protocol invariant from `cmd/breezyd/handlers_device.go:222-227`.
- [ ] `SetMode` accepts case-insensitive `"ventilation"`, `"regeneration"`, `"supply"`, `"extract"` and rejects others with `ErrInvalidArg`.
- [ ] `SetRTC(ctx, c, t)` writes both `0x006F` (time of day) and `0x0070` (calendar) in one packet, encoded as the existing `TypeTimeOfDay` and `TypeDate` types.
- [ ] All eight ops have tests verifying both the happy path (the recording wrapper captures the expected `[]ParamWrite`) and the validation failures (return `ErrInvalidArg`).
- [ ] `just check-all` passes.

**Verify:** `go test ./pkg/breezy -run 'TestOps_' -v` → PASS for every op test.

**Steps:**

- [ ] **Step 1: Write `pkg/breezy/ops.go` with the Client interface and write ops**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Client is the minimal subset of *breezy.Client that pkg/breezy/ops
// requires. The concrete *breezy.Client satisfies it; tests, the
// daemon's recording wrapper, and the future standalone CLI backend
// all substitute their own implementations.
type Client interface {
	ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error)
	WriteParams(ctx context.Context, writes []ParamWrite) error
}

// ErrInvalidArg is the sentinel for ops that reject caller-supplied
// arguments before any UDP traffic. Callers can errors.Is against this
// to map to a "bad_request" HTTP status, CLI exit code 2, etc.
var ErrInvalidArg = errors.New("invalid argument")

// Power turns the device on or off (parameter 0x0001).
func Power(ctx context.Context, c Client, on bool) error {
	val := byte(0)
	if on {
		val = 1
	}
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0001, Value: []byte{val}}})
}

// SetSpeedPreset selects a numbered fan preset (1, 2, or 3) via 0x0002.
func SetSpeedPreset(ctx context.Context, c Client, preset int) error {
	if preset < 1 || preset > 3 {
		return fmt.Errorf("%w: preset must be 1-3, got %d", ErrInvalidArg, preset)
	}
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0002, Value: []byte{byte(preset)}}})
}

// SetSpeedManual sets manual fan speed to pct% (10..100) and switches the
// device into manual mode in a single packet. Order matters per the vendor
// manual: write 0x0044 (percentage) BEFORE 0x0002 (manual flag), so the
// firmware doesn't briefly interpret the flag against a stale value.
func SetSpeedManual(ctx context.Context, c Client, pct int) error {
	if pct < 10 || pct > 100 {
		return fmt.Errorf("%w: manual percent must be 10-100, got %d", ErrInvalidArg, pct)
	}
	return c.WriteParams(ctx, []ParamWrite{
		{ID: 0x0044, Value: []byte{byte(pct)}},
		{ID: 0x0002, Value: []byte{0xFF}},
	})
}

// SetMode sets the airflow mode via 0x00B7. Accepts case-insensitive
// "ventilation"/"regeneration"/"supply"/"extract".
func SetMode(ctx context.Context, c Client, mode string) error {
	var val byte
	switch strings.ToLower(mode) {
	case "ventilation":
		val = 0
	case "regeneration":
		val = 1
	case "supply":
		val = 2
	case "extract":
		val = 3
	default:
		return fmt.Errorf("%w: mode must be one of ventilation/regeneration/supply/extract, got %q", ErrInvalidArg, mode)
	}
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x00B7, Value: []byte{val}}})
}

// SetHeater toggles the auxiliary reheater (0x0068). Note the firmware may
// also activate the heater autonomously for frost protection; this op
// only controls the user-facing toggle.
func SetHeater(ctx context.Context, c Client, on bool) error {
	val := byte(0)
	if on {
		val = 1
	}
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0068, Value: []byte{val}}})
}

// ResetFilter writes 1 to 0x0065, resetting the filter-replacement
// countdown back to the configured filter_timeout_days.
func ResetFilter(ctx context.Context, c Client) error {
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0065, Value: []byte{1}}})
}

// ResetFaults writes 1 to 0x0080, clearing the active fault list.
func ResetFaults(ctx context.Context, c Client) error {
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0080, Value: []byte{1}}})
}

// SetRTC sets the device's wall clock and calendar from t. Writes 0x006F
// (time_of_day, [sec, min, hr]) and 0x0070 (date, [day, dow, month, year-2000])
// in one packet. Day-of-week follows ISO-8601 (Monday=1, Sunday=7).
func SetRTC(ctx context.Context, c Client, t time.Time) error {
	tv := TimeOfDayValue{Hour: uint8(t.Hour()), Minute: uint8(t.Minute()), Second: uint8(t.Second())}
	timeBytes, err := encodeValue(TypeTimeOfDay, tv)
	if err != nil {
		return fmt.Errorf("ops.SetRTC: encode time: %w", err)
	}
	dow := uint8(t.Weekday())
	if dow == 0 {
		dow = 7 // Sunday: time.Weekday returns 0; ISO calls it 7.
	}
	dv := DateValue{Day: uint8(t.Day()), DayOfWeek: dow, Month: uint8(t.Month()), Year: uint8(t.Year() - 2000)}
	dateBytes, err := encodeValue(TypeDate, dv)
	if err != nil {
		return fmt.Errorf("ops.SetRTC: encode date: %w", err)
	}
	return c.WriteParams(ctx, []ParamWrite{
		{ID: 0x006F, Value: timeBytes},
		{ID: 0x0070, Value: dateBytes},
	})
}
```

- [ ] **Step 2: Write `pkg/breezy/ops_test.go` with table tests**

The tests use a `recordingClient` struct (defined inside the test file — not exported) that implements `Client` and captures the writes for assertion. Use `pkg/breezy/fakedevice` only for ops where verifying the round-trip matters; for write-only ops the recording client alone is sufficient.

```go
package breezy

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// recordingClient implements Client for tests: it captures writes and
// optionally delegates reads to an inner Client.
type recordingClient struct {
	writes [][]ParamWrite
	reads  func(context.Context, []ParamID) (map[ParamID][]byte, error)
	writeErr error
}

func (r *recordingClient) ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
	if r.reads == nil {
		return map[ParamID][]byte{}, nil
	}
	return r.reads(ctx, ids)
}
func (r *recordingClient) WriteParams(ctx context.Context, ws []ParamWrite) error {
	r.writes = append(r.writes, ws)
	return r.writeErr
}

func TestOps_Power(t *testing.T) {
	c := &recordingClient{}
	if err := Power(context.Background(), c, true); err != nil {
		t.Fatalf("Power(true): %v", err)
	}
	want := []ParamWrite{{ID: 0x0001, Value: []byte{1}}}
	if !reflect.DeepEqual(c.writes[0], want) {
		t.Errorf("got %v, want %v", c.writes[0], want)
	}

	c = &recordingClient{}
	if err := Power(context.Background(), c, false); err != nil {
		t.Fatalf("Power(false): %v", err)
	}
	if c.writes[0][0].Value[0] != 0 {
		t.Errorf("Power(false): want value 0, got %d", c.writes[0][0].Value[0])
	}
}

func TestOps_SetSpeedPreset(t *testing.T) {
	for _, preset := range []int{1, 2, 3} {
		c := &recordingClient{}
		if err := SetSpeedPreset(context.Background(), c, preset); err != nil {
			t.Errorf("SetSpeedPreset(%d): %v", preset, err)
			continue
		}
		got := c.writes[0][0].Value[0]
		if int(got) != preset {
			t.Errorf("preset %d: wrote 0x%02x, want 0x%02x", preset, got, preset)
		}
	}
	for _, bad := range []int{0, 4, -1, 255} {
		c := &recordingClient{}
		err := SetSpeedPreset(context.Background(), c, bad)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("preset %d: expected ErrInvalidArg, got %v", bad, err)
		}
		if len(c.writes) != 0 {
			t.Errorf("preset %d: should not have issued any writes", bad)
		}
	}
}

func TestOps_SetSpeedManual_PacketOrder(t *testing.T) {
	c := &recordingClient{}
	if err := SetSpeedManual(context.Background(), c, 30); err != nil {
		t.Fatalf("SetSpeedManual(30): %v", err)
	}
	if len(c.writes) != 1 {
		t.Fatalf("expected 1 packet, got %d", len(c.writes))
	}
	pkt := c.writes[0]
	if len(pkt) != 2 {
		t.Fatalf("expected 2 writes in packet, got %d", len(pkt))
	}
	// Order is critical: 0x44 must come before 0x02.
	if pkt[0].ID != 0x0044 {
		t.Errorf("first write must be 0x0044 (manual_pct), got 0x%04X", uint16(pkt[0].ID))
	}
	if pkt[0].Value[0] != 30 {
		t.Errorf("manual_pct value: want 30, got %d", pkt[0].Value[0])
	}
	if pkt[1].ID != 0x0002 {
		t.Errorf("second write must be 0x0002 (speed_mode), got 0x%04X", uint16(pkt[1].ID))
	}
	if pkt[1].Value[0] != 0xFF {
		t.Errorf("speed_mode value: want 0xFF (manual flag), got 0x%02X", pkt[1].Value[0])
	}
}

func TestOps_SetSpeedManual_RangeReject(t *testing.T) {
	for _, bad := range []int{0, 5, 9, 101, 200} {
		c := &recordingClient{}
		err := SetSpeedManual(context.Background(), c, bad)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("manual %d: expected ErrInvalidArg, got %v", bad, err)
		}
		if len(c.writes) != 0 {
			t.Errorf("manual %d: should not have issued any writes", bad)
		}
	}
}

func TestOps_SetMode(t *testing.T) {
	cases := map[string]byte{
		"ventilation":  0,
		"regeneration": 1,
		"supply":       2,
		"extract":      3,
		"VENTILATION":  0,
		"Regeneration": 1,
	}
	for in, want := range cases {
		c := &recordingClient{}
		if err := SetMode(context.Background(), c, in); err != nil {
			t.Errorf("SetMode(%q): %v", in, err)
			continue
		}
		got := c.writes[0][0].Value[0]
		if got != want {
			t.Errorf("SetMode(%q): wrote 0x%02X, want 0x%02X", in, got, want)
		}
	}
	c := &recordingClient{}
	err := SetMode(context.Background(), c, "auto")
	if !errors.Is(err, ErrInvalidArg) {
		t.Errorf("SetMode(\"auto\"): expected ErrInvalidArg, got %v", err)
	}
}

func TestOps_SetHeater(t *testing.T) {
	c := &recordingClient{}
	if err := SetHeater(context.Background(), c, true); err != nil {
		t.Fatalf("SetHeater(true): %v", err)
	}
	if c.writes[0][0].ID != 0x0068 || c.writes[0][0].Value[0] != 1 {
		t.Errorf("SetHeater(true): unexpected write %+v", c.writes[0][0])
	}
}

func TestOps_ResetFilter(t *testing.T) {
	c := &recordingClient{}
	if err := ResetFilter(context.Background(), c); err != nil {
		t.Fatalf("ResetFilter: %v", err)
	}
	if c.writes[0][0].ID != 0x0065 || c.writes[0][0].Value[0] != 1 {
		t.Errorf("ResetFilter: unexpected write %+v", c.writes[0][0])
	}
}

func TestOps_ResetFaults(t *testing.T) {
	c := &recordingClient{}
	if err := ResetFaults(context.Background(), c); err != nil {
		t.Fatalf("ResetFaults: %v", err)
	}
	if c.writes[0][0].ID != 0x0080 || c.writes[0][0].Value[0] != 1 {
		t.Errorf("ResetFaults: unexpected write %+v", c.writes[0][0])
	}
}

func TestOps_SetRTC(t *testing.T) {
	c := &recordingClient{}
	t0 := time.Date(2026, 5, 4, 10, 30, 45, 0, time.UTC) // Monday
	if err := SetRTC(context.Background(), c, t0); err != nil {
		t.Fatalf("SetRTC: %v", err)
	}
	if len(c.writes) != 1 || len(c.writes[0]) != 2 {
		t.Fatalf("expected one packet with two writes, got %v", c.writes)
	}
	pkt := c.writes[0]
	if pkt[0].ID != 0x006F {
		t.Errorf("first write must be 0x006F (rtc_time), got 0x%04X", uint16(pkt[0].ID))
	}
	// time_of_day = [sec, min, hr]
	if !reflect.DeepEqual(pkt[0].Value, []byte{45, 30, 10}) {
		t.Errorf("rtc_time bytes: want [45 30 10], got %v", pkt[0].Value)
	}
	if pkt[1].ID != 0x0070 {
		t.Errorf("second write must be 0x0070 (rtc_calendar), got 0x%04X", uint16(pkt[1].ID))
	}
	// date = [day, dow, month, year-2000]; 2026-05-04 is a Monday (dow=1).
	if !reflect.DeepEqual(pkt[1].Value, []byte{4, 1, 5, 26}) {
		t.Errorf("rtc_calendar bytes: want [4 1 5 26], got %v", pkt[1].Value)
	}
}

func TestOps_SetRTC_SundayDoW(t *testing.T) {
	c := &recordingClient{}
	t0 := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC) // Sunday
	if err := SetRTC(context.Background(), c, t0); err != nil {
		t.Fatalf("SetRTC: %v", err)
	}
	dow := c.writes[0][1].Value[1]
	if dow != 7 {
		t.Errorf("Sunday: want dow=7 (ISO), got %d", dow)
	}
}
```

- [ ] **Step 3: Run tests to verify all eight ops work**

Run: `go test ./pkg/breezy -run TestOps_ -v`
Expected: PASS for `TestOps_Power`, `TestOps_SetSpeedPreset`, `TestOps_SetSpeedManual_PacketOrder`, `TestOps_SetSpeedManual_RangeReject`, `TestOps_SetMode`, `TestOps_SetHeater`, `TestOps_ResetFilter`, `TestOps_ResetFaults`, `TestOps_SetRTC`, `TestOps_SetRTC_SundayDoW`.

- [ ] **Step 4: Run lint**

Run: `just lint`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add pkg/breezy/ops.go pkg/breezy/ops_test.go
git commit -m "$(cat <<'EOF'
pkg/breezy: add ops.go with high-level write operations

Power, SetSpeedPreset/Manual, SetMode, SetHeater, ResetFilter,
ResetFaults, SetRTC. Each validates inputs, builds the right
ParamWrite slice, and calls a Client interface. The packet-order
invariant for SetSpeedManual (0x44 before 0x02=0xFF) is preserved
and tested.

Phase 1 prep for issue #2.
EOF
)"
```

---

### Task 3: Add read operations to `pkg/breezy/ops.go`

**Goal:** Add the four read ops (`GetFirmware`, `GetEfficiency`, `GetFaults`, `GetStatus`) plus the `FaultCode` type. Each does a `ReadParams` call and decodes the result. `GetStatus` reads the full set of status parameters and calls `BuildStatus` with the device's name/id/ip and a nil last-poll pointer (standalone has no poll history).

**Files:**
- Modify: `pkg/breezy/ops.go` (append the read ops)
- Modify: `pkg/breezy/ops_test.go` (append tests for the read ops)

**Acceptance Criteria:**
- [ ] `pkg/breezy.FaultCode` is a new struct: `{Code int; Kind string}` where `Kind` is `"alarm"`, `"warning"`, or `"unknown(<n>)"`.
- [ ] `GetFirmware(ctx, c) (FirmwareMetaValue, error)` reads `0x0086` and returns the typed value.
- [ ] `GetEfficiency(ctx, c) (int, error)` reads `0x0129` and returns the percentage.
- [ ] `GetFaults(ctx, c) ([]FaultCode, error)` reads `0x007F`, parses pairs of `(code, kind)`, ignores an odd trailing byte. Returns an empty slice (not nil) when the param is absent or empty.
- [ ] `GetStatus(ctx, c, name, id, ip string) (Status, error)` reads the full set of status params (the same ones the daemon's poller covers, see `cmd/breezyd/poller.go` `defaultReadIDs`) and returns `BuildStatus(values, name, id, ip, nil)`.
- [ ] All four ops have tests using `pkg/breezy/fakedevice` to verify the round-trip decoding.
- [ ] `just check-all` passes.

**Verify:** `go test ./pkg/breezy -run 'TestOps_Get' -v` → PASS.

**Steps:**

- [ ] **Step 1: Locate `defaultReadIDs` to know which params `GetStatus` should read**

Run: `grep -n "defaultReadIDs" cmd/breezyd/poller.go` to find the existing list of status-relevant param IDs. Copy that exact slice into `pkg/breezy/ops.go` under a new exported name `StatusParamIDs`. The daemon's poller can later switch to using this canonical list (out of scope for this task — leave the daemon's copy alone).

- [ ] **Step 2: Append read ops to `pkg/breezy/ops.go`**

```go
// FaultCode is a single entry in the device's active fault list. Code is
// the raw fault number; Kind is "alarm" (level 0), "warning" (level 1),
// or "unknown(<n>)" for unrecognised severity bytes.
type FaultCode struct {
	Code int    `json:"code"`
	Kind string `json:"kind"`
}

// StatusParamIDs is the canonical set of parameter IDs that GetStatus
// reads in one batched ReadParams call. It mirrors the daemon poller's
// defaultReadIDs list (cmd/breezyd/poller.go); keep them in sync until
// the daemon migrates to this constant.
var StatusParamIDs = []ParamID{
	// COPY-PASTE EXACT CONTENTS of defaultReadIDs from cmd/breezyd/poller.go.
	// Do not paraphrase — use the exact byte-for-byte slice from the source
	// of truth, in the same order.
}

// GetFirmware reads 0x0086 and decodes it as a FirmwareMetaValue.
func GetFirmware(ctx context.Context, c Client) (FirmwareMetaValue, error) {
	out, err := c.ReadParams(ctx, []ParamID{0x0086})
	if err != nil {
		return FirmwareMetaValue{}, err
	}
	raw, ok := out[0x0086]
	if !ok {
		return FirmwareMetaValue{}, fmt.Errorf("ops.GetFirmware: device replied unsupported for 0x0086")
	}
	v, err := decodeValue(TypeFirmwareMeta, raw)
	if err != nil {
		return FirmwareMetaValue{}, fmt.Errorf("ops.GetFirmware: %w", err)
	}
	return v.(FirmwareMetaValue), nil
}

// GetEfficiency reads 0x0129 and returns it as an int (0..100).
func GetEfficiency(ctx context.Context, c Client) (int, error) {
	out, err := c.ReadParams(ctx, []ParamID{0x0129})
	if err != nil {
		return 0, err
	}
	raw, ok := out[0x0129]
	if !ok || len(raw) != 1 {
		return 0, fmt.Errorf("ops.GetEfficiency: missing or wrong-sized 0x0129")
	}
	return int(raw[0]), nil
}

// GetFaults reads 0x007F and decodes pairs of (code, kind). An odd
// trailing byte is ignored (matches the daemon's existing parsing).
func GetFaults(ctx context.Context, c Client) ([]FaultCode, error) {
	out, err := c.ReadParams(ctx, []ParamID{0x007F})
	if err != nil {
		return nil, err
	}
	faults := []FaultCode{}
	raw, ok := out[0x007F]
	if !ok {
		return faults, nil
	}
	for i := 0; i+1 < len(raw); i += 2 {
		kind := "alarm"
		switch raw[i+1] {
		case 0:
			kind = "alarm"
		case 1:
			kind = "warning"
		default:
			kind = fmt.Sprintf("unknown(%d)", raw[i+1])
		}
		faults = append(faults, FaultCode{Code: int(raw[i]), Kind: kind})
	}
	return faults, nil
}

// GetStatus issues one batched ReadParams for the canonical status set
// and returns the decoded Status. lastPoll is nil — callers that want a
// timestamp (the daemon, building from a cached snapshot) should call
// BuildStatus directly with their own values + last-poll time.
func GetStatus(ctx context.Context, c Client, name, id, ip string) (Status, error) {
	values, err := c.ReadParams(ctx, StatusParamIDs)
	if err != nil {
		return Status{}, err
	}
	return BuildStatus(values, name, id, ip, nil), nil
}
```

- [ ] **Step 3: Append read-op tests to `pkg/breezy/ops_test.go`**

These tests use `pkg/breezy/fakedevice` to exercise the full read-decode round trip. Read `pkg/breezy/fakedevice/*.go` first to find the constructor name and seeding API — likely `fakedevice.New()` or similar. The pattern below assumes `fakedevice.New(t)` returns a `*fakedevice.Server` with a `Set(id, bytes)` method and exposes the address; adjust to the real API.

```go
func TestOps_GetFirmware(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{
				0x0086: {1, 5, 0x0F, 0x05, 0xEA, 0x07}, // 1.05, 2026-05-15
			}, nil
		},
	}
	fw, err := GetFirmware(context.Background(), c)
	if err != nil {
		t.Fatalf("GetFirmware: %v", err)
	}
	if fw.Major != 1 || fw.Minor != 5 {
		t.Errorf("version: want 1.5, got %d.%d", fw.Major, fw.Minor)
	}
	wantDate := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	if !fw.Date.Equal(wantDate) {
		t.Errorf("date: want %v, got %v", wantDate, fw.Date)
	}
}

func TestOps_GetEfficiency(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{0x0129: {72}}, nil
		},
	}
	got, err := GetEfficiency(context.Background(), c)
	if err != nil {
		t.Fatalf("GetEfficiency: %v", err)
	}
	if got != 72 {
		t.Errorf("efficiency: want 72, got %d", got)
	}
}

func TestOps_GetFaults_PairsAndOddTrailing(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{
				// Three pairs (code, kind) + 1 odd trailing byte that should be ignored.
				0x007F: {17, 0, 22, 1, 99, 5, 0xAA},
			}, nil
		},
	}
	got, err := GetFaults(context.Background(), c)
	if err != nil {
		t.Fatalf("GetFaults: %v", err)
	}
	want := []FaultCode{
		{Code: 17, Kind: "alarm"},
		{Code: 22, Kind: "warning"},
		{Code: 99, Kind: "unknown(5)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOps_GetFaults_Empty(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{0x007F: {}}, nil
		},
	}
	got, err := GetFaults(context.Background(), c)
	if err != nil {
		t.Fatalf("GetFaults: %v", err)
	}
	if got == nil {
		t.Errorf("GetFaults must return [], not nil, on empty fault list")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestOps_GetStatus_RoundTrip(t *testing.T) {
	// Build a recordingClient that returns a small set of known status
	// params; verify GetStatus returns the corresponding Status fields.
	values := map[ParamID][]byte{
		0x0001: {1},      // power on
		0x0002: {0x01},   // preset 1
		0x00B7: {1},      // regeneration
		0x0025: {42},     // humidity 42%
	}
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			out := map[ParamID][]byte{}
			for _, id := range ids {
				if v, ok := values[id]; ok {
					out[id] = v
				}
			}
			return out, nil
		},
	}
	s, err := GetStatus(context.Background(), c, "playroom", "BREEZYID", "192.168.1.1")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if s.Name != "playroom" || s.ID != "BREEZYID" || s.IP != "192.168.1.1" {
		t.Errorf("identity fields wrong: %+v", s)
	}
	if s.Configured["power"] != true {
		t.Errorf("power: want true, got %v", s.Configured["power"])
	}
	if s.Configured["speed_mode"] != "preset1" {
		t.Errorf("speed_mode: want preset1, got %v", s.Configured["speed_mode"])
	}
	if s.Configured["airflow_mode"] != "regeneration" {
		t.Errorf("airflow_mode: want regeneration, got %v", s.Configured["airflow_mode"])
	}
	if s.Sensors["humidity_pct"] != 42 {
		t.Errorf("humidity_pct: want 42, got %v", s.Sensors["humidity_pct"])
	}
	if s.LastPoll != "" {
		t.Errorf("LastPoll must be empty in standalone path, got %q", s.LastPoll)
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/breezy -run 'TestOps_(Get|SetRTC)' -v`
Expected: PASS for all four read ops + the previously-added write ops.

Run: `just check-all`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add pkg/breezy/ops.go pkg/breezy/ops_test.go
git commit -m "$(cat <<'EOF'
pkg/breezy: add ops.go read operations + FaultCode

GetFirmware, GetEfficiency, GetFaults, GetStatus. FaultCode is a new
typed shape — replaces the daemon's anonymous map[string]any. The
canonical StatusParamIDs slice mirrors the poller's defaultReadIDs;
the daemon will migrate to it in a follow-up.

Phase 1 prep for issue #2.
EOF
)"
```

---

### Task 4: Add `cmd/breezyd/recording_client.go`

**Goal:** Introduce a `recordingClient` wrapper around `pkg/breezy.Client` that calls a callback after every successful `WriteParams`. The daemon's `Handler` will use this in Tasks 5 and 6 to replace the explicit `h.recordWrite(name, writes)` calls scattered through every write handler. After this task, the wrapper exists with full unit-test coverage but is not yet wired in.

**Files:**
- Create: `cmd/breezyd/recording_client.go` (new file)
- Create: `cmd/breezyd/recording_client_test.go` (new file with unit tests)

**Acceptance Criteria:**
- [ ] `cmd/breezyd.recordingClient` struct exists with fields `inner breezy.Client` and `record func([]breezy.ParamWrite)`.
- [ ] `recordingClient.ReadParams(ctx, ids)` delegates to `r.inner.ReadParams` directly (no recording, reads aren't written).
- [ ] `recordingClient.WriteParams(ctx, writes)` calls `r.inner.WriteParams` first; if the inner returns nil, calls `r.record(writes)`. If inner returns an error, returns that error and does NOT call `r.record`.
- [ ] `recordingClient` implements both `breezy.Client` (the new `pkg/breezy/ops.Client`) and `cmd/breezyd.HandlerClient` (the existing daemon interface). Since they have the same shape modulo `Close`, the type satisfies whichever the caller needs.
- [ ] Unit tests in `recording_client_test.go` cover: writes succeed → record called; writes fail → record NOT called; reads → record NEVER called; record callback receives the exact slice the caller passed (no copy/mutation issues).
- [ ] `just check` passes.

**Verify:** `go test ./cmd/breezyd -run TestRecordingClient -v` → PASS.

**Steps:**

- [ ] **Step 1: Write `cmd/breezyd/recording_client.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// recordingClient wraps a breezy.Client with a per-write callback. The
// callback fires after every successful WriteParams; failed writes do
// not record. This lets handlers call pkg/breezy/ops without each one
// remembering to invoke h.recordWrite — the wrapper does it.
//
// recordingClient does not embed Close: the underlying client's Close
// (e.g. *breezy.Client.Close) is still available via the inner interface
// when needed. Handlers that close the client should hold onto a
// reference to the inner *breezy.Client (or, in the common case, simply
// `defer raw.Close()` before wrapping).
type recordingClient struct {
	inner  breezy.Client
	record func([]breezy.ParamWrite)
}

// newRecordingClient wraps inner with a write-callback.
func newRecordingClient(inner breezy.Client, record func([]breezy.ParamWrite)) *recordingClient {
	return &recordingClient{inner: inner, record: record}
}

// ReadParams delegates without recording.
func (r *recordingClient) ReadParams(ctx context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
	return r.inner.ReadParams(ctx, ids)
}

// WriteParams writes via the inner client and records the writes on
// success. On error, record is not called.
func (r *recordingClient) WriteParams(ctx context.Context, writes []breezy.ParamWrite) error {
	if err := r.inner.WriteParams(ctx, writes); err != nil {
		return err
	}
	if r.record != nil {
		r.record(writes)
	}
	return nil
}
```

- [ ] **Step 2: Write `cmd/breezyd/recording_client_test.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// stubInner is a breezy.Client where ReadParams and WriteParams are
// configurable per test.
type stubInner struct {
	readResp map[breezy.ParamID][]byte
	readErr  error
	writeErr error
	writeCalls [][]breezy.ParamWrite
}

func (s *stubInner) ReadParams(ctx context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
	return s.readResp, s.readErr
}
func (s *stubInner) WriteParams(ctx context.Context, ws []breezy.ParamWrite) error {
	s.writeCalls = append(s.writeCalls, ws)
	return s.writeErr
}

func TestRecordingClient_WriteSuccessFiresCallback(t *testing.T) {
	inner := &stubInner{}
	var recorded [][]breezy.ParamWrite
	rc := newRecordingClient(inner, func(ws []breezy.ParamWrite) { recorded = append(recorded, ws) })

	ws := []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}}
	if err := rc.WriteParams(context.Background(), ws); err != nil {
		t.Fatalf("WriteParams: %v", err)
	}
	if len(recorded) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(recorded))
	}
	if !reflect.DeepEqual(recorded[0], ws) {
		t.Errorf("callback got %v, want %v", recorded[0], ws)
	}
}

func TestRecordingClient_WriteFailureSuppressesCallback(t *testing.T) {
	inner := &stubInner{writeErr: errors.New("boom")}
	called := false
	rc := newRecordingClient(inner, func([]breezy.ParamWrite) { called = true })

	err := rc.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}})
	if err == nil {
		t.Fatal("expected error from inner, got nil")
	}
	if called {
		t.Error("callback must NOT fire on inner write failure")
	}
}

func TestRecordingClient_ReadDoesNotRecord(t *testing.T) {
	inner := &stubInner{readResp: map[breezy.ParamID][]byte{0x0001: {1}}}
	called := false
	rc := newRecordingClient(inner, func([]breezy.ParamWrite) { called = true })

	if _, err := rc.ReadParams(context.Background(), []breezy.ParamID{0x0001}); err != nil {
		t.Fatalf("ReadParams: %v", err)
	}
	if called {
		t.Error("callback must NEVER fire on reads")
	}
}

func TestRecordingClient_NilCallback(t *testing.T) {
	inner := &stubInner{}
	rc := newRecordingClient(inner, nil)
	if err := rc.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}}); err != nil {
		t.Fatalf("WriteParams with nil callback should succeed silently, got: %v", err)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/breezyd -run TestRecordingClient -v`
Expected: PASS for all four tests.

- [ ] **Step 4: Run check**

Run: `just check`
Expected: exit 0.

- [ ] **Step 5: Commit**

```bash
git add cmd/breezyd/recording_client.go cmd/breezyd/recording_client_test.go
git commit -m "$(cat <<'EOF'
cmd/breezyd: add recordingClient wrapper for writes

Wraps a breezy.Client and fires a per-write callback on success,
suppressing the callback on inner-write errors. Reads pass through
unchanged. Not yet wired — Tasks 5/6 will use this to replace the
explicit h.recordWrite() calls in handlers_*.go.

Phase 1 prep for issue #2.
EOF
)"
```

---

### Task 5: Refactor `cmd/breezyd/handlers_device.go` to use ops + recordingClient

**Goal:** Rewrite each device handler (`postPower`, `postSpeed`, `postMode`, `postHeater`, `postRTC`, `postParam`) to call `pkg/breezy/ops` against a `recordingClient` rather than carrying inline protocol logic. After this task each handler is ~10 lines: parse JSON, map to op, call op, JSON response. `getDevice` and `getParam` (read-only routes) stay as-is — they use the cache or do single-shot reads. All existing tests continue to pass.

**Files:**
- Modify: `cmd/breezyd/handlers_device.go` (rewrite the six write handlers)
- Modify: `cmd/breezyd/server.go` if needed (`HandlerClient` may now alias `breezy.Client` — verify and keep backwards-compatible)
- Verify: `cmd/breezyd/server_test.go` (existing handler tests should still pass)

**Acceptance Criteria:**
- [ ] `postPower` calls `breezy.Power(ctx, recClient, *body.On)`. Handler is ≤ 15 lines.
- [ ] `postSpeed` dispatches on `body.Preset`/`body.Manual` and calls `breezy.SetSpeedPreset` or `breezy.SetSpeedManual`. The `*body.Manual` < 10 / > 100 validation comes from the op (returns `ErrInvalidArg`); the handler maps that to `bad_request`.
- [ ] `postMode` calls `breezy.SetMode(ctx, recClient, body.Mode)`. The valid-mode set check moves into the op; the handler handles `ErrInvalidArg`.
- [ ] `postHeater` calls `breezy.SetHeater(ctx, recClient, *body.On)`.
- [ ] `postRTC` parses RFC3339 then calls `breezy.SetRTC(ctx, recClient, t)`.
- [ ] `postParam` writes raw bytes via `recClient.WriteParams` directly (raw param writes don't go through ops — they're an explicit "I know what I'm doing" path).
- [ ] Each handler obtains its `recClient` via a new helper `h.dialRecording(name)` (see step 1).
- [ ] No handler calls `h.recordWrite(...)` explicitly any more — the wrapper does it.
- [ ] All tests in `cmd/breezyd/server_test.go` pass without modification.
- [ ] `just check-all` passes.

**Verify:** `go test ./cmd/breezyd/... -v` → PASS for all handler tests.

**Steps:**

- [ ] **Step 1: Add `h.dialRecording(name)` helper to `cmd/breezyd/server.go`**

Append after `h.dial`:

```go
// dialRecording returns a recordingClient that wraps h.dial(name)'s
// HandlerClient and fires h.recordWrite(name, writes) on every
// successful write. Handlers that issue writes via pkg/breezy/ops
// should use this instead of h.dial — the wrapper subsumes the
// previous "call h.recordWrite at the end" pattern.
func (h *Handler) dialRecording(name string) (*recordingClient, HandlerClient, error) {
	raw, err := h.dial(name)
	if err != nil {
		return nil, nil, err
	}
	return newRecordingClient(raw, func(ws []breezy.ParamWrite) {
		h.recordWrite(name, ws)
	}), raw, nil
}
```

The function returns both the wrapper (for ops calls) and the raw client (so the handler can `defer raw.Close()`). The wrapper does not need a separate close.

- [ ] **Step 2: Rewrite `postPower`**

Replace the existing `postPower` body with:

```go
func (h *Handler) postPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.On == nil {
		writeErr(w, "bad_request", "missing 'on' field (true/false)")
		return
	}
	rc, raw, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, "internal", err.Error())
		return
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.Power(ctx, rc, *body.On); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

- [ ] **Step 3: Rewrite `postSpeed`**

```go
func (h *Handler) postSpeed(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		Preset *int `json:"preset"`
		Manual *int `json:"manual"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if (body.Preset == nil) == (body.Manual == nil) {
		writeErr(w, "bad_request", "set exactly one of 'preset' (1-3) or 'manual' (10-100)")
		return
	}
	rc, raw, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, "internal", err.Error())
		return
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var opErr error
	if body.Preset != nil {
		opErr = breezy.SetSpeedPreset(ctx, rc, *body.Preset)
	} else {
		opErr = breezy.SetSpeedManual(ctx, rc, *body.Manual)
	}
	if opErr != nil {
		writeErr(w, classifyClientErr(opErr), opErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

- [ ] **Step 4: Update `classifyClientErr` to recognise `breezy.ErrInvalidArg`**

In `cmd/breezyd/server.go` (or wherever `classifyClientErr` is defined), add a case:

```go
if errors.Is(err, breezy.ErrInvalidArg) {
	return "bad_request"
}
```

This makes ops' validation errors land as HTTP 400 with `bad_request` code, matching the previous in-handler validation behavior.

- [ ] **Step 5: Rewrite `postMode`, `postHeater`, `postRTC` analogously**

For each: parse body, get rc, call the corresponding op, render. Drop any explicit validation that's now in the op. Drop any explicit `h.recordWrite`. Each handler ends up at 12-15 lines.

- [ ] **Step 6: Rewrite `postParam`**

Raw-bytes write — does not go through ops:

```go
func (h *Handler) postParam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	id, err := parseParamID(r.PathValue("id"))
	if err != nil {
		writeErr(w, "bad_request", err.Error())
		return
	}
	var body struct {
		Hex string `json:"hex"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.Hex == "" {
		writeErr(w, "bad_request", "missing 'hex' field")
		return
	}
	val, err := hex.DecodeString(body.Hex)
	if err != nil {
		writeErr(w, "bad_request", fmt.Sprintf("decode hex: %v", err))
		return
	}
	rc, raw, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, "internal", err.Error())
		return
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := rc.WriteParams(ctx, []breezy.ParamWrite{{ID: id, Value: val}}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

- [ ] **Step 7: Verify `getDevice` still works**

`getDevice` was already updated in Task 1 to call `breezy.BuildStatus`. Confirm it still compiles and the existing snapshot tests still pass: `go test ./cmd/breezyd -run TestServer.*Snapshot -v` (or the closest match in the existing test file).

- [ ] **Step 8: Run the full suite**

Run: `just check-all`
Expected: exit 0. All existing handler tests pass without modification.

If any tests fail with "didn't expect cache update for X" or similar: the recording wrapper is firing recordWrite where the test mock didn't expect it. Inspect the test — likely it was asserting on the absence of a recordWrite call after a deliberate write failure. Verify `recordingClient` isn't firing on errors (it shouldn't — Task 4 covers that).

- [ ] **Step 9: Commit**

```bash
git add cmd/breezyd/handlers_device.go cmd/breezyd/server.go
git commit -m "$(cat <<'EOF'
cmd/breezyd: refactor device handlers to use pkg/breezy/ops

Each write handler shrinks to ~12 lines: parse, dialRecording, call op.
The recordingClient wrapper subsumes the previous "explicit
h.recordWrite at the end" pattern. classifyClientErr now maps
breezy.ErrInvalidArg → bad_request so ops-side validation lands
as HTTP 400 like the old inline checks did.

Phase 1 part of issue #2.
EOF
)"
```

---

### Task 6: Refactor `cmd/breezyd/handlers_service.go` to use ops + recordingClient

**Goal:** Apply the same treatment to the service handlers. `postFilterReset` and `postFaultsReset` use the corresponding write ops. `getFirmware`, `getEfficiency`, and `getFaults` continue to read from the daemon's cache (NOT through ops — those go through UDP and the daemon doesn't want to bypass its cache for these reads); they only change to use the new typed `FaultCode` shape and the new free-function decode helpers from `pkg/breezy/status.go`. After this task the daemon's per-handler files are clean and Phase 1 is complete.

**Files:**
- Modify: `cmd/breezyd/handlers_service.go`
- Verify: `cmd/breezyd/server_test.go` (existing service-handler tests should still pass)

**Acceptance Criteria:**
- [ ] `postFilterReset` calls `breezy.ResetFilter(ctx, rc)`. ≤ 12 lines total.
- [ ] `postFaultsReset` calls `breezy.ResetFaults(ctx, rc)`. ≤ 12 lines total.
- [ ] `getFirmware` reads `0x0086` from `snap.Values` via `breezy.Param{Type: breezy.TypeFirmwareMeta}.Decode(...)` (or whatever the existing path uses), keeping its current cache-only behavior.
- [ ] `getEfficiency` reads `0x0129` via `breezy.Uint8At(snap.Values, 0x0129)`.
- [ ] `getFaults` parses `snap.Values[0x007F]` into `[]breezy.FaultCode` (the new typed shape) and JSON-marshals that. Existing tests should keep passing because `FaultCode` has matching JSON tags `code`/`kind`.
- [ ] No handler calls `h.recordWrite(...)` explicitly any more.
- [ ] All tests in `cmd/breezyd/server_test.go` pass without modification.
- [ ] `just check-all` passes.

**Verify:** `go test ./cmd/breezyd/... -v` → PASS; `just check-all` → exit 0.

**Steps:**

- [ ] **Step 1: Rewrite `postFilterReset` and `postFaultsReset`**

```go
func (h *Handler) postFilterReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	rc, raw, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, "internal", err.Error())
		return
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.ResetFilter(ctx, rc); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) postFaultsReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	rc, raw, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, "internal", err.Error())
		return
	}
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.ResetFaults(ctx, rc); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

- [ ] **Step 2: Update `getFaults` to emit `[]breezy.FaultCode`**

```go
func (h *Handler) getFaults(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	snap, _ := h.State.Get(name)
	out := []breezy.FaultCode{}
	if raw, ok := snap.Values[0x007F]; ok {
		for i := 0; i+1 < len(raw); i += 2 {
			kind := "alarm"
			switch raw[i+1] {
			case 0:
				kind = "alarm"
			case 1:
				kind = "warning"
			default:
				kind = fmt.Sprintf("unknown(%d)", raw[i+1])
			}
			out = append(out, breezy.FaultCode{Code: int(raw[i]), Kind: kind})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"faults": out})
}
```

(We could call `breezy.GetFaults` here, but that issues a fresh UDP read — the daemon prefers to serve from its cache. Keep the inline parsing, just typed.)

- [ ] **Step 3: Update `getEfficiency` to use `breezy.Uint8At`**

Replace the `uint8At(snap, 0x0129)` call with `breezy.Uint8At(snap.Values, 0x0129)`. The handler logic otherwise stays identical.

- [ ] **Step 4: Update `getFirmware` to decode via the typed helper**

Today's `getFirmware` (`cmd/breezyd/handlers_service.go:20-36`) does inline byte unpacking from `snap.Values[0x0086]`. Replace with:

```go
func (h *Handler) getFirmware(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	snap, _ := h.State.Get(name)
	raw, ok := snap.Values[0x0086]
	if !ok {
		writeErr(w, "not_found", "firmware metadata not in cache yet")
		return
	}
	v, err := (breezy.Param{Type: breezy.TypeFirmwareMeta}).Decode(raw)
	if err != nil {
		writeErr(w, "internal", fmt.Sprintf("decode firmware: %v", err))
		return
	}
	fw := v.(breezy.FirmwareMetaValue)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    fmt.Sprintf("%d.%02d", fw.Major, fw.Minor),
		"build_date": fw.Date.Format("2006-01-02"),
	})
}
```

The JSON output stays byte-identical to before. We do NOT call `breezy.GetFirmware` here — that op issues a fresh UDP read; the daemon prefers cache.

- [ ] **Step 5: Run the full suite**

Run: `just check-all`
Expected: exit 0. All service-handler tests pass without modification because the public JSON shape is unchanged.

- [ ] **Step 6: Confirm no dangling explicit `h.recordWrite` calls remain**

Run: `grep -n "h\.recordWrite\|h\.notice" cmd/breezyd/handlers_*.go`
Expected: zero matches in handlers (the calls now happen inside `recordingClient`'s callback, set up in `h.dialRecording`).

- [ ] **Step 7: Commit**

```bash
git add cmd/breezyd/handlers_service.go
git commit -m "$(cat <<'EOF'
cmd/breezyd: refactor service handlers to use pkg/breezy/ops + Status helpers

postFilterReset and postFaultsReset use breezy.ResetFilter/ResetFaults
via the recording wrapper. Read handlers (getFirmware/getEfficiency/
getFaults) keep their cache-only behavior but adopt breezy.Uint8At
and the typed []breezy.FaultCode shape. JSON responses unchanged.

Closes Phase 1 of issue #2.
EOF
)"
```

---

## Out of scope (Phase 2)

- The CLI's `backend` interface and `directBackend` implementation. Phase 2.
- Removing `defaultDaemonURL` and changing `WriteDefault` to comment out the `[daemon]` block. Phase 2.
- README and CLAUDE.md updates for standalone mode. Phase 2.
- Migrating the daemon's `defaultReadIDs` to `breezy.StatusParamIDs` (the constant exists; the migration is mechanical and can land alongside Phase 2 if it makes sense, or earlier as a one-liner if convenient).
