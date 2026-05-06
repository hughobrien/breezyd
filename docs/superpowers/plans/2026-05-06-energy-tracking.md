# Energy Recovery Tracking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Track heating-recovered, cooling-recovered, and fan-electric-consumed kWh per device on the daemon side (today + lifetime), gated to regeneration airflow_mode, persisted in `state_dir`, surfaced through `service.energy` on the JSON snapshot, eight Prometheus gauges, and a new collapsed-by-default ENERGY block on the dashboard. Concurrently move the existing override warnings out of the Speed control into a new NOTICE block at the bottom of each card.

The "consumed" counter exists so the user can see the cost alongside the saving — fan motors aren't free, and a true energy figure is recovered minus consumed. Both fans contribute to consumed (each with its own electric draw at its current pct), so asymmetric presets are handled correctly.

**Architecture:** A pure calculator in `pkg/breezy/energy.go` does the airflow interpolation and instantaneous W math (no I/O, no state). A daemon-only `cmd/breezyd/energy_tracker.go` owns the per-device accumulator, JSON persistence, and the `Tick()` step driven by the poller. `pkg/breezy/status.go` gains a sibling `BuildStatusWithEnergy()` so the existing call sites stay untouched. The UI gains two new `<div class="block">` sections at the bottom of each card.

**Tech Stack:** Go 1.x, Vents Twinfresh FDFD/02 protocol, prometheus client_golang, brutella/hap (untouched here), embedded HTML/CSS/JS dashboard, Playwright for UI tests.

---

## File Structure

**Create:**
- `pkg/breezy/energy.go` — airflow calibration table + `ComputeWatts()` (pure)
- `pkg/breezy/energy_test.go`
- `cmd/breezyd/energy_tracker.go` — `EnergyTracker` struct, persistence, `Tick()`
- `cmd/breezyd/energy_tracker_test.go`

**Modify:**
- `pkg/breezy/status.go` — `EnergyValues` type + `BuildStatusWithEnergy()`
- `pkg/breezy/status_test.go` — tests for the new function
- `cmd/breezyd/poller.go` — `Energy *EnergyTracker` field + `Tick` after `pollOnce`
- `cmd/breezyd/main.go` — construct trackers per device from `state_dir`
- `cmd/breezyd/metrics.go` — five new gauges, populated pre-scrape from tracker snapshots
- `cmd/breezyd/handlers_device.go` — read tracker snapshot + call `BuildStatusWithEnergy`
- `cmd/breezyd/ui/index.html` — ENERGY + NOTICE blocks; move `overrideLine` out of Speed
- `tests/ui/dashboard.spec.ts` — new Playwright tests; adjust override-in-Speed assertions

---

## Task 1: Pure energy calculator + calibration table

**Goal:** A self-contained `pkg/breezy/energy.go` that converts (unit type, fan %, supply Δ°C) into instantaneous watts of recovered heat, and (unit type, fan %) into the electric draw of one fan at that speed. No I/O, no state.

**Files:**
- Create: `pkg/breezy/energy.go`
- Create: `pkg/breezy/energy_test.go`

**Acceptance Criteria:**
- [ ] `ComputeWatts(unitType, fanPct, supplyDeltaC)` returns `(W, supported)`. Sign of `W` matches sign of `supplyDeltaC`.
- [ ] `ComputeFanWatts(unitType, fanPct)` returns `(W, supported)`. Single fan's electric draw at that speed; the caller sums supply + extract for total consumption.
- [ ] `Interpolate(curve, fanPct)` linearly interpolates between adjacent points, clamps to the curve's domain.
- [ ] Calibration table includes UnitType 17 (Breezy 160) with three points: fan curve (Cmh) and electric draw (Watts); lookup of any other UnitType returns `(0, false)` from both Compute funcs.
- [ ] All branches covered by `pkg/breezy/energy_test.go`.

**Verify:** `go test ./pkg/breezy/ -run TestEnergy -v` → all pass

**Steps:**

- [ ] **Step 1: Write failing tests**

Create `pkg/breezy/energy_test.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"math"
	"testing"
)

func TestEnergy_Interpolate(t *testing.T) {
	curve := []CalPoint{
		{Pct: 10, Cmh: 30, FanW: 2},
		{Pct: 50, Cmh: 100, FanW: 9},
		{Pct: 100, Cmh: 160, FanW: 22},
	}
	cmhCases := []struct {
		pct  int
		want float64
	}{
		{0, 30},     // below first → clamp
		{10, 30},    // exact first
		{30, 65},    // halfway between 10 and 50: 30 + (100-30)*0.5 = 65
		{50, 100},   // exact mid
		{75, 130},   // halfway between 50 and 100: 100 + (160-100)*0.5 = 130
		{100, 160},  // exact last
		{120, 160},  // above last → clamp
	}
	for _, c := range cmhCases {
		got := interpolateCmh(curve, c.pct)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("interpolateCmh(curve, %d) = %.2f, want %.2f", c.pct, got, c.want)
		}
	}
	wattCases := []struct {
		pct  int
		want float64
	}{
		{10, 2}, {50, 9}, {100, 22}, // exact points
		{30, 5.5},  // halfway between 2 and 9
		{75, 15.5}, // halfway between 9 and 22
	}
	for _, c := range wattCases {
		got := interpolateWatts(curve, c.pct)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("interpolateWatts(curve, %d) = %.2f, want %.2f", c.pct, got, c.want)
		}
	}
}

func TestEnergy_ComputeWatts_Breezy160(t *testing.T) {
	// Breezy 160 at 100% (= 160 m³/h) with supply Δ +5°C:
	// W = 160 * 0.335 * 5 = 268 W
	w, ok := ComputeWatts(17, 100, 5.0)
	if !ok {
		t.Fatalf("expected supported=true for unit 17")
	}
	if math.Abs(w-268.0) > 0.5 {
		t.Errorf("W = %.2f, want ~268", w)
	}
}

func TestEnergy_ComputeWatts_NegativeDelta(t *testing.T) {
	// Cooling: outdoor hotter than supply, Δ negative, W should be negative.
	w, ok := ComputeWatts(17, 50, -3.0)
	if !ok {
		t.Fatal("expected supported=true")
	}
	// 100 m³/h * 0.335 * -3 = -100.5 W
	if math.Abs(w-(-100.5)) > 0.5 {
		t.Errorf("W = %.2f, want ~-100.5", w)
	}
}

func TestEnergy_ComputeWatts_UnsupportedModel(t *testing.T) {
	w, ok := ComputeWatts(99, 50, 5.0)
	if ok {
		t.Errorf("expected supported=false for unknown unit type 99")
	}
	if w != 0 {
		t.Errorf("W = %.2f, want 0 for unsupported model", w)
	}
}

func TestEnergy_ComputeWatts_ZeroFan(t *testing.T) {
	// 0% fan → curve clamps to lowest point (10%, 30 m³/h) — but we keep the
	// math honest. The caller (tracker) is responsible for skipping ticks
	// when the unit isn't actually running.
	w, ok := ComputeWatts(17, 0, 5.0)
	if !ok {
		t.Fatal("expected supported=true")
	}
	// 30 m³/h * 0.335 * 5 = 50.25 W
	if math.Abs(w-50.25) > 0.5 {
		t.Errorf("W = %.2f, want ~50.25 (clamped to curve floor)", w)
	}
}

func TestEnergy_ComputeFanWatts(t *testing.T) {
	cases := []struct {
		pct  int
		want float64
	}{
		{10, 2}, {50, 9}, {100, 22}, // exact points from spec sheet
		{75, 15.5}, // interpolated
	}
	for _, c := range cases {
		w, ok := ComputeFanWatts(17, c.pct)
		if !ok {
			t.Fatalf("expected supported=true at pct=%d", c.pct)
		}
		if math.Abs(w-c.want) > 0.01 {
			t.Errorf("ComputeFanWatts(17, %d) = %.2f, want %.2f", c.pct, w, c.want)
		}
	}
}

func TestEnergy_ComputeFanWatts_UnsupportedModel(t *testing.T) {
	w, ok := ComputeFanWatts(99, 50)
	if ok {
		t.Errorf("expected supported=false for unit 99")
	}
	if w != 0 {
		t.Errorf("W = %.2f, want 0", w)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/breezy/ -run TestEnergy -v`
Expected: FAIL — `Interpolate`, `ComputeWatts`, `AirflowPoint` undefined.

- [ ] **Step 3: Implement the calculator**

Create `pkg/breezy/energy.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// Heat-recovery energy calculation. Converts (unit type, fan %, supply Δ°C)
// to instantaneous watts of heat moved across the heat exchanger.
//
// Pure functions only — no I/O, no global state. The accumulator (today /
// lifetime kWh, persistence, regen gate) lives separately in
// cmd/breezyd/energy_tracker.go. Splitting the math from the bookkeeping
// keeps this file trivially testable and lets the calibration table grow
// as new device models are characterised without daemon plumbing changes.
package breezy

// airflowConstant = ρ_air (1.2 kg/m³) × c_p (1005 J/(kg·K)) ÷ 3600 s/h
//                ≈ 0.335 W per (m³/h × K)
//
// Treats the air as ideal at standard density; good enough for the
// dashboard estimate. The error from real-world density variation
// (altitude, humidity for an HRV) is well below the ±10% airflow-curve
// uncertainty that dominates anyway.
const airflowConstant = 0.335

// CalPoint is one calibration sample for a device model: at fan pct =
// Pct, the unit moves Cmh m³/h and consumes FanW watts of electricity
// per fan. Sourced from the vendor's spec sheet.
type CalPoint struct {
	Pct  int
	Cmh  float64
	FanW float64
}

// modelCurves maps a device's UnitType (param 0x00B9) to its calibration
// curve. Points are vendor spec-sheet readings; the interpolators
// linearly fill the gaps. Add new models here as their datasheets are
// characterised; an unsupported UnitType makes ComputeWatts and
// ComputeFanWatts return supported=false and the daemon surfaces an
// error in service.energy.
//
// Breezy 160 (Twinfresh Elite 160) figures: airflow 30/100/160 m³/h at
// approx 10/50/100% fan; per-fan electric draw 2/9/22 W (taken from the
// product datasheet's "speed I/II/III" power consumption table; both
// fans run in regen so total electric load is roughly double).
var modelCurves = map[uint16][]CalPoint{
	17: {
		{Pct: 10, Cmh: 30, FanW: 2},
		{Pct: 50, Cmh: 100, FanW: 9},
		{Pct: 100, Cmh: 160, FanW: 22},
	},
}

// interpolateCmh returns the airflow at the given fan percent by linear
// interpolation between adjacent points; clamps below/above the curve's
// domain. Curve must be sorted ascending by Pct.
func interpolateCmh(curve []CalPoint, pct int) float64 {
	return interp(curve, pct, func(p CalPoint) float64 { return p.Cmh })
}

// interpolateWatts returns one fan's electric draw at the given fan
// percent, by the same linear-interpolation rule.
func interpolateWatts(curve []CalPoint, pct int) float64 {
	return interp(curve, pct, func(p CalPoint) float64 { return p.FanW })
}

// interp is the shared interpolation kernel; the field selector lets
// us reuse it for both Cmh and FanW without code duplication.
func interp(curve []CalPoint, pct int, sel func(CalPoint) float64) float64 {
	if len(curve) == 0 {
		return 0
	}
	if pct <= curve[0].Pct {
		return sel(curve[0])
	}
	if pct >= curve[len(curve)-1].Pct {
		return sel(curve[len(curve)-1])
	}
	for i := 1; i < len(curve); i++ {
		if pct <= curve[i].Pct {
			lo, hi := curve[i-1], curve[i]
			frac := float64(pct-lo.Pct) / float64(hi.Pct-lo.Pct)
			return sel(lo) + frac*(sel(hi)-sel(lo))
		}
	}
	return sel(curve[len(curve)-1]) // unreachable but keeps the compiler happy
}

// ComputeWatts returns the instantaneous heat-transfer power across the
// HRV's heat exchanger, in watts. Sign tracks supplyDeltaC: positive
// means the unit is heating incoming air (winter), negative means it's
// cooling incoming air (summer). The caller (tracker) uses the magnitude
// for accumulation and the sign to route the energy into the right
// (heating/cooling) counter.
//
// Returns supported=false when the device's UnitType has no calibration
// in modelCurves; W is 0 and the caller should surface an
// unsupported-model error.
func ComputeWatts(unitType uint16, fanPct int, supplyDeltaC float64) (w float64, supported bool) {
	curve, ok := modelCurves[unitType]
	if !ok {
		return 0, false
	}
	return interpolateCmh(curve, fanPct) * airflowConstant * supplyDeltaC, true
}

// ComputeFanWatts returns the electric draw of one fan at the given
// percent. The caller sums supply + extract draws to get total
// electric consumption (both fans run independently in regen mode).
func ComputeFanWatts(unitType uint16, fanPct int) (w float64, supported bool) {
	curve, ok := modelCurves[unitType]
	if !ok {
		return 0, false
	}
	return interpolateWatts(curve, fanPct), true
}

// EnergyValues is the immutable snapshot of an energy tracker. Lives
// here (rather than in cmd/breezyd) so the status builder can reference
// it without an import cycle. Populated by the daemon's tracker.Snapshot()
// and consumed by BuildStatusWithEnergy + the Prometheus metrics layer.
type EnergyValues struct {
	Supported           bool    `json:"supported"`
	InstantW            float64 `json:"instant_w"`             // recovered, signed
	ConsumedW           float64 `json:"consumed_w"`            // both fans' electric draw, magnitude
	HeatingTodayKWh     float64 `json:"heating_today_kwh"`
	CoolingTodayKWh     float64 `json:"cooling_today_kwh"`
	ConsumedTodayKWh    float64 `json:"consumed_today_kwh"`
	HeatingLifetimeKWh  float64 `json:"heating_lifetime_kwh"`
	CoolingLifetimeKWh  float64 `json:"cooling_lifetime_kwh"`
	ConsumedLifetimeKWh float64 `json:"consumed_lifetime_kwh"`
	Error               string  `json:"error,omitempty"`
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/breezy/ -run TestEnergy -v`
Expected: PASS for `TestEnergy_Interpolate`, `TestEnergy_ComputeWatts_Breezy160`, `TestEnergy_ComputeWatts_NegativeDelta`, `TestEnergy_ComputeWatts_UnsupportedModel`, `TestEnergy_ComputeWatts_ZeroFan`.

- [ ] **Step 5: Commit**

```bash
git add pkg/breezy/energy.go pkg/breezy/energy_test.go
git commit -m "energy: pure calculator + Breezy 160 calibration"
```

---

## Task 2: EnergyTracker struct + persistence

**Goal:** A daemon-only struct that holds today/lifetime kWh per device and round-trips through a JSON file in `state_dir`. No `Tick()` yet — that's Task 3.

**Files:**
- Create: `cmd/breezyd/energy_tracker.go`
- Create: `cmd/breezyd/energy_tracker_test.go`

**Acceptance Criteria:**
- [ ] `EnergyTracker` struct holds device name, state-dir path, all six kWh counters (heating today/lifetime, cooling today/lifetime, consumed today/lifetime), today date, last-tick timestamp, error string, instantaneous W (recovered, signed) and instantaneous consumed W, and a mutex.
- [ ] `Load()` reads `<state_dir>/energy_<device>.json`; missing file → fresh state with zeros and today's date; malformed file → log a warning and start fresh.
- [ ] `save()` writes via temp+rename atomically.
- [ ] `Snapshot()` returns an immutable `EnergyValues` struct copy; safe to call concurrently.
- [ ] Round-trip persistence preserves all fields exactly.

**Verify:** `go test ./cmd/breezyd/ -run TestEnergyTracker_Load -v && go test ./cmd/breezyd/ -run TestEnergyTracker_RoundTrip -v` → both pass

**Steps:**

- [ ] **Step 1: Write failing tests**

Create `cmd/breezyd/energy_tracker_test.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnergyTracker_Load_MissingFile(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "playroom", StateDir: dir}
	if err := tr.Load(); err != nil {
		t.Fatalf("Load on missing file should succeed, got %v", err)
	}
	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 || snap.CoolingLifetimeKWh != 0 {
		t.Errorf("expected zero counters on missing file, got %+v", snap)
	}
	if tr.Today != time.Now().Local().Format("2006-01-02") {
		t.Errorf("Today = %q, want today's date", tr.Today)
	}
}

func TestEnergyTracker_Load_MalformedFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "energy_playroom.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr := &EnergyTracker{Device: "playroom", StateDir: dir}
	if err := tr.Load(); err != nil {
		t.Fatalf("Load on malformed file should succeed (start fresh), got %v", err)
	}
	if tr.HeatingLifetimeKWh != 0 {
		t.Errorf("expected fresh state on malformed file")
	}
}

func TestEnergyTracker_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:              "playroom",
		StateDir:            dir,
		HeatingTodayKWh:     1.234,
		CoolingTodayKWh:     0.456,
		ConsumedTodayKWh:    0.123,
		HeatingLifetimeKWh:  234.5,
		CoolingLifetimeKWh:  123.4,
		ConsumedLifetimeKWh: 12.3,
		Today:               "2026-05-06",
	}
	if err := tr.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	tr2 := &EnergyTracker{Device: "playroom", StateDir: dir}
	if err := tr2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	snap := tr2.Snapshot()
	if snap.HeatingTodayKWh != 1.234 ||
		snap.CoolingTodayKWh != 0.456 ||
		snap.ConsumedTodayKWh != 0.123 ||
		snap.HeatingLifetimeKWh != 234.5 ||
		snap.CoolingLifetimeKWh != 123.4 ||
		snap.ConsumedLifetimeKWh != 12.3 {
		t.Errorf("round-trip lost values: %+v", snap)
	}
	if tr2.Today != "2026-05-06" {
		t.Errorf("Today = %q, want 2026-05-06", tr2.Today)
	}
}

func TestEnergyTracker_Snapshot_IsCopy(t *testing.T) {
	tr := &EnergyTracker{
		Device:          "playroom",
		HeatingTodayKWh: 1.0,
	}
	snap := tr.Snapshot()
	tr.HeatingTodayKWh = 999.0
	if snap.HeatingTodayKWh != 1.0 {
		t.Errorf("Snapshot should be a value copy, got %v", snap.HeatingTodayKWh)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/breezyd/ -run TestEnergyTracker -v`
Expected: FAIL — `EnergyTracker`, `Snapshot`, `Load`, `save` undefined.

- [ ] **Step 3: Implement struct + persistence**

Create `cmd/breezyd/energy_tracker.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// Per-device accumulator for HRV heat recovery. Owns persistence to a
// JSON file in state_dir, called from the poller after each successful
// tick. The pure energy math lives in pkg/breezy/energy.go; this file
// is the daemon-side bookkeeping (today/lifetime counters, regen gate,
// dt cap, date rollover).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// EnergyValues lives in pkg/breezy after Task 4 — see that step.

// EnergyTracker accumulates heat-recovery and electric-consumption
// energy for one device. Construct with Device + StateDir set, then
// call Load() once at startup; the poller drives Tick() after each
// successful poll.
type EnergyTracker struct {
	Device   string // device name (e.g. "playroom"); used in the state-file path
	StateDir string // directory for persistence; usually daemon's state_dir

	mu                  sync.Mutex
	HeatingTodayKWh     float64
	CoolingTodayKWh     float64
	ConsumedTodayKWh    float64
	HeatingLifetimeKWh  float64
	CoolingLifetimeKWh  float64
	ConsumedLifetimeKWh float64
	InstantW            float64 // signed recovered W; not persisted
	ConsumedW           float64 // current electric draw of both fans, W; not persisted
	Today               string  // YYYY-MM-DD (system local TZ)
	LastTick            time.Time
	Error               string // non-empty when the device's UnitType isn't calibrated
}

// persistedEnergy is the on-disk JSON shape. InstantW and ConsumedW are
// not persisted — they're recomputed every tick from the snapshot; a
// stale value from a previous process run would lie about live state.
type persistedEnergy struct {
	TodayDate           string  `json:"today_date"`
	HeatingTodayKWh     float64 `json:"heating_today_kwh"`
	CoolingTodayKWh     float64 `json:"cooling_today_kwh"`
	ConsumedTodayKWh    float64 `json:"consumed_today_kwh"`
	HeatingLifetimeKWh  float64 `json:"heating_lifetime_kwh"`
	CoolingLifetimeKWh  float64 `json:"cooling_lifetime_kwh"`
	ConsumedLifetimeKWh float64 `json:"consumed_lifetime_kwh"`
	LastUpdated         string  `json:"last_updated"`
}

func (e *EnergyTracker) statePath() string {
	return filepath.Join(e.StateDir, fmt.Sprintf("energy_%s.json", e.Device))
}

// Load reads the persisted state. A missing or malformed file is treated
// as fresh state — the lifetime accumulator is informational, not
// load-bearing, so we never return an error to the caller.
func (e *EnergyTracker) Load() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	today := time.Now().Local().Format("2006-01-02")
	e.Today = today
	e.LastTick = time.Time{} // first Tick after Load will set this without accumulating

	data, err := os.ReadFile(e.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil // fresh start
	}
	if err != nil {
		slog.Warn("energy: read state file failed; starting fresh",
			"device", e.Device, "err", err)
		return nil
	}

	var p persistedEnergy
	if err := json.Unmarshal(data, &p); err != nil {
		slog.Warn("energy: state file malformed; starting fresh",
			"device", e.Device, "err", err)
		return nil
	}

	e.HeatingLifetimeKWh = p.HeatingLifetimeKWh
	e.CoolingLifetimeKWh = p.CoolingLifetimeKWh
	e.ConsumedLifetimeKWh = p.ConsumedLifetimeKWh
	if p.TodayDate == today {
		e.HeatingTodayKWh = p.HeatingTodayKWh
		e.CoolingTodayKWh = p.CoolingTodayKWh
		e.ConsumedTodayKWh = p.ConsumedTodayKWh
	} // else: silently roll into the new day with zero today counters
	return nil
}

// save writes the current state via temp + atomic rename so a crash
// mid-write can never produce a torn file.
func (e *EnergyTracker) save() error {
	p := persistedEnergy{
		TodayDate:           e.Today,
		HeatingTodayKWh:     e.HeatingTodayKWh,
		CoolingTodayKWh:     e.CoolingTodayKWh,
		ConsumedTodayKWh:    e.ConsumedTodayKWh,
		HeatingLifetimeKWh:  e.HeatingLifetimeKWh,
		CoolingLifetimeKWh:  e.CoolingLifetimeKWh,
		ConsumedLifetimeKWh: e.ConsumedLifetimeKWh,
		LastUpdated:         time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := e.statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.statePath())
}

// Snapshot returns a value copy of the current tracker state. Used by
// BuildStatusWithEnergy and the metrics collector, both of which run on
// the HTTP request path (concurrent with the poller's Tick). Returns
// breezy.EnergyValues (defined in pkg/breezy/energy.go after Task 4) so
// the status builder can consume it without an import cycle.
func (e *EnergyTracker) Snapshot() breezy.EnergyValues {
	e.mu.Lock()
	defer e.mu.Unlock()
	return breezy.EnergyValues{
		Supported:           e.Error == "",
		InstantW:            e.InstantW,
		ConsumedW:           e.ConsumedW,
		HeatingTodayKWh:     e.HeatingTodayKWh,
		CoolingTodayKWh:     e.CoolingTodayKWh,
		ConsumedTodayKWh:    e.ConsumedTodayKWh,
		HeatingLifetimeKWh:  e.HeatingLifetimeKWh,
		CoolingLifetimeKWh:  e.CoolingLifetimeKWh,
		ConsumedLifetimeKWh: e.ConsumedLifetimeKWh,
		Error:               e.Error,
	}
}

// Tick is implemented in Task 3.
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/breezyd/ -run TestEnergyTracker -v`
Expected: PASS for `TestEnergyTracker_Load_MissingFile`, `TestEnergyTracker_Load_MalformedFile`, `TestEnergyTracker_RoundTrip`, `TestEnergyTracker_Snapshot_IsCopy`.

- [ ] **Step 5: Commit**

```bash
git add cmd/breezyd/energy_tracker.go cmd/breezyd/energy_tracker_test.go
git commit -m "energy: per-device tracker with JSON persistence"
```

---

## Task 3: EnergyTracker.Tick() + math integration

**Goal:** The per-poll accumulation step. Runs the regen gate, dt cap, date rollover, math from Task 1, sign-routes into heating/cooling counters, and persists.

**Files:**
- Modify: `cmd/breezyd/energy_tracker.go` (add `Tick()` method, drop the placeholder `var _ = ...` line from Task 2)
- Modify: `cmd/breezyd/energy_tracker_test.go` (add tick tests)

**Acceptance Criteria:**
- [ ] First tick after `Load()` accumulates 0 (LastTick was zero, dt would be massive; we set LastTick = now and skip accumulation).
- [ ] Subsequent ticks add `|W| × dt / 3.6e6` kWh into the heating counter when `supplyΔ > 0`, cooling when `supplyΔ < 0`.
- [ ] Each tick also computes `consumed_W = ComputeFanWatts(supply_pct) + ComputeFanWatts(extract_pct)` and accumulates `consumed_W × dt / 3.6e6` into `ConsumedTodayKWh` and `ConsumedLifetimeKWh`.
- [ ] Tick is a no-op when `airflow_mode != "regeneration"` (no accumulation, but `LastTick` still advances so the next tick's dt is reasonable).
- [ ] Tick is a no-op when any required input is missing/sentinel (LastTick still advances).
- [ ] dt is capped at 300 s; negative dt clamped to 0.
- [ ] Date rollover zeros today counters (heating + cooling + consumed) when `now`'s local date differs from `e.Today`.
- [ ] Unknown UnitType sets `Error` and skips the math (LastTick still advances).
- [ ] Each successful Tick triggers a `save()`.

**Verify:** `go test ./cmd/breezyd/ -run TestEnergyTracker -v` → all pass

**Steps:**

- [ ] **Step 1: Write failing tests**

Add to `cmd/breezyd/energy_tracker_test.go`:

```go
import (
	// existing imports + bytes for raw param values
	"github.com/hughobrien/breezyd/pkg/breezy"
)

// makeRegenSnap builds a values map representing a Breezy 160 in
// manual-mode regeneration at the given fan % and outdoor/supply
// temps. Manual-mode is fine for the basic tests because the tracker
// uses breezy.CommandedFanPct (exported in this task) which falls
// back to manual_pct when speed_mode == "manual". Asymmetric preset
// pcts get their own helper below.
// Temperatures are uint16 little-endian, in tenths of a degree.
func makeRegenSnap(fanPct int, outdoorC, supplyC float64) map[breezy.ParamID][]byte {
	return map[breezy.ParamID][]byte{
		0x00B7: {2},               // airflow_mode = regeneration
		0x0002: {0xFF},            // speed_mode = manual (so manual_pct is the source of truth)
		0x0044: {byte(fanPct)},    // manual_pct
		0x004A: {0xC8, 0x00},      // fan_supply_rpm = 200 (non-zero so live-pct gating doesn't zero it)
		0x004B: {0xC8, 0x00},      // fan_extract_rpm = 200
		0x0020: encTemp(supplyC),  // temp_supply_c
		0x001F: encTemp(outdoorC), // temp_outdoor_c
		0x00B9: {17, 0},           // device_type = Breezy 160
	}
}

// makeRegenSnapAsymmetric exercises the asymmetric-preset path: supply
// runs at one pct (preset's 0x3E), extract at another (0x3F). Both are
// active in regen mode (e.g. preset3 = 70/100 in regen → supply 70 W,
// extract 100 W of air; the test asserts consumed-W routes both fans
// independently through ComputeFanWatts).
func makeRegenSnapAsymmetric(supplyPct, extractPct int, outdoorC, supplyC float64) map[breezy.ParamID][]byte {
	return map[breezy.ParamID][]byte{
		0x00B7: {2},
		0x0002: {3},                 // speed_mode = preset3
		0x003E: {byte(supplyPct)},   // preset3_supply_pct
		0x003F: {byte(extractPct)},  // preset3_extract_pct
		0x004A: {0xC8, 0x00},
		0x004B: {0xC8, 0x00},
		0x0020: encTemp(supplyC),
		0x001F: encTemp(outdoorC),
		0x00B9: {17, 0},
	}
}

func encTemp(c float64) []byte {
	v := int16(c * 10)
	return []byte{byte(v), byte(v >> 8)}
}

func TestEnergyTracker_Tick_FirstTickNoAccumulation(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()

	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	tr.Tick(makeRegenSnap(50, 0, 20), now)
	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("first tick should accumulate 0; got %v", snap.HeatingTodayKWh)
	}
}

func TestEnergyTracker_Tick_HeatingAccumulation(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()
	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	tr.Tick(makeRegenSnap(50, 0, 20), t0) // primes LastTick

	// 5 s later: recovered W = 100 m³/h * 0.335 * 20 = 670 W
	// consumed W = 9 W per fan * 2 fans = 18 W (Breezy 160 mid-speed spec sheet)
	t1 := t0.Add(5 * time.Second)
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap := tr.Snapshot()
	wantHeat := 670.0 * 5.0 / 3.6e6
	wantConsumed := 18.0 * 5.0 / 3.6e6
	if snap.HeatingTodayKWh < wantHeat*0.99 || snap.HeatingTodayKWh > wantHeat*1.01 {
		t.Errorf("heating today = %v, want ~%v", snap.HeatingTodayKWh, wantHeat)
	}
	if snap.ConsumedTodayKWh < wantConsumed*0.99 || snap.ConsumedTodayKWh > wantConsumed*1.01 {
		t.Errorf("consumed today = %v, want ~%v", snap.ConsumedTodayKWh, wantConsumed)
	}
	if snap.CoolingTodayKWh != 0 {
		t.Errorf("cooling today should be 0 for positive Δ, got %v", snap.CoolingTodayKWh)
	}
	if snap.InstantW < 669 || snap.InstantW > 671 {
		t.Errorf("InstantW = %v, want ~670", snap.InstantW)
	}
	if snap.ConsumedW < 17.5 || snap.ConsumedW > 18.5 {
		t.Errorf("ConsumedW = %v, want ~18", snap.ConsumedW)
	}
}

func TestEnergyTracker_Tick_AsymmetricFans(t *testing.T) {
	// preset3 with supply=70, extract=100 in regen: each fan draws its own
	// per-pct power. ComputeFanWatts(70) ≈ 14.2 W; ComputeFanWatts(100) =
	// 22 W. Total consumed ≈ 36.2 W.
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()
	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	tr.Tick(makeRegenSnapAsymmetric(70, 100, 0, 20), t0)
	tr.Tick(makeRegenSnapAsymmetric(70, 100, 0, 20), t0.Add(5*time.Second))
	snap := tr.Snapshot()
	if snap.ConsumedW < 35 || snap.ConsumedW > 37 {
		t.Errorf("ConsumedW = %v, want ~36 for supply=70 + extract=100", snap.ConsumedW)
	}
}

func TestEnergyTracker_Tick_CoolingAccumulation(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()
	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	tr.Tick(makeRegenSnap(50, 30, 25), t0) // outdoor 30, supply 25 → Δ=-5

	t1 := t0.Add(5 * time.Second)
	tr.Tick(makeRegenSnap(50, 30, 25), t1)
	snap := tr.Snapshot()
	// |W| = 100 * 0.335 * 5 = 167.5 W. 167.5 * 5 / 3.6e6 = 2.326e-4 kWh
	want := 167.5 * 5.0 / 3.6e6
	if snap.CoolingTodayKWh < want*0.99 || snap.CoolingTodayKWh > want*1.01 {
		t.Errorf("cooling today = %v, want ~%v", snap.CoolingTodayKWh, want)
	}
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("heating today should be 0 for negative Δ, got %v", snap.HeatingTodayKWh)
	}
	if snap.InstantW > -166 || snap.InstantW < -169 {
		t.Errorf("InstantW = %v, want ~-167.5", snap.InstantW)
	}
}

func TestEnergyTracker_Tick_NonRegenSkipped(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()
	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	tr.Tick(makeRegenSnap(50, 0, 20), t0) // primes

	// Switch to ventilation: airflow_mode = 1
	values := makeRegenSnap(50, 0, 20)
	values[0x00B7] = []byte{1}
	t1 := t0.Add(5 * time.Second)
	tr.Tick(values, t1)
	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("non-regen tick should not accumulate, got %v", snap.HeatingTodayKWh)
	}
	if snap.InstantW != 0 {
		t.Errorf("InstantW should be 0 in non-regen mode, got %v", snap.InstantW)
	}
}

func TestEnergyTracker_Tick_DtCap(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()
	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	tr.Tick(makeRegenSnap(50, 0, 20), t0)

	// 1 hour later — dt should cap at 300 s.
	t1 := t0.Add(1 * time.Hour)
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap := tr.Snapshot()
	want := 670.0 * 300.0 / 3.6e6 // capped at 300 s, not 3600
	if snap.HeatingTodayKWh > want*1.01 {
		t.Errorf("dt should cap at 300 s; got %v kWh, want ≤%v", snap.HeatingTodayKWh, want)
	}
}

func TestEnergyTracker_Tick_NegativeDt(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()
	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	tr.Tick(makeRegenSnap(50, 0, 20), t0)

	// Clock jumped backwards (NTP correction).
	t1 := t0.Add(-30 * time.Second)
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("negative dt should not accumulate; got %v", snap.HeatingTodayKWh)
	}
}

func TestEnergyTracker_Tick_DateRollover(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:             "p",
		StateDir:           dir,
		HeatingTodayKWh:    1.5,
		HeatingLifetimeKWh: 100,
		Today:              "2026-05-05",
	}
	t0 := time.Date(2026, 5, 6, 0, 0, 5, 0, time.Local) // 5 s past midnight
	tr.Tick(makeRegenSnap(50, 0, 20), t0)               // primes
	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("today should reset on date rollover, got %v", snap.HeatingTodayKWh)
	}
	if snap.HeatingLifetimeKWh != 100 {
		t.Errorf("lifetime should survive rollover, got %v", snap.HeatingLifetimeKWh)
	}
	if tr.Today != "2026-05-06" {
		t.Errorf("Today not updated, got %q", tr.Today)
	}
}

func TestEnergyTracker_Tick_UnsupportedModel(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()
	values := makeRegenSnap(50, 0, 20)
	values[0x00B9] = []byte{99, 0} // unknown unit type
	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	tr.Tick(values, t0)
	tr.Tick(values, t0.Add(5*time.Second))
	snap := tr.Snapshot()
	if snap.Supported {
		t.Errorf("expected supported=false for unit 99")
	}
	if snap.Error == "" {
		t.Errorf("expected Error to be populated")
	}
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("no accumulation for unsupported model, got %v", snap.HeatingTodayKWh)
	}
}

func TestEnergyTracker_Tick_PersistsAfterEachTick(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()
	t0 := time.Date(2026, 5, 6, 12, 0, 0, 0, time.Local)
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	tr.Tick(makeRegenSnap(50, 0, 20), t0.Add(5*time.Second))

	// Reload from disk; the second tick's accumulation must be present.
	tr2 := &EnergyTracker{Device: "p", StateDir: dir}
	if err := tr2.Load(); err != nil {
		t.Fatal(err)
	}
	if tr2.HeatingLifetimeKWh == 0 {
		t.Errorf("expected persisted heating accumulation after Tick")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/breezyd/ -run TestEnergyTracker_Tick -v`
Expected: FAIL — Tick is undefined.

- [ ] **Step 3: Export CommandedFanPct so the tracker can reuse it**

In `pkg/breezy/status.go`, find `commandedFanPct` (search `func commandedFanPct`). Rename to exported `CommandedFanPct` (capital C) and update the two call sites in `BuildStatus`. Add a doc comment:

```go
// CommandedFanPct returns the percent setting commanded for one of the
// two fans, picked by isSupply. Resolves the speed_mode register (0x02)
// and reads the right per-mode source: 0x44 (manual_pct) in manual
// mode, 0x3A/3B/3C/3D/3E/3F in presets 1/2/3. Used by BuildStatus's
// live block and by cmd/breezyd's EnergyTracker.
func CommandedFanPct(values map[ParamID][]byte, isSupply bool) (int, bool) {
	// existing body — unchanged
}
```

- [ ] **Step 4: Implement Tick()**

In `cmd/breezyd/energy_tracker.go`, replace the placeholder `// Tick is implemented in Task 3.` line with:

```go
const (
	// dtCap bounds how much wall time a single tick can claim. A long pause
	// (network out, daemon paused, sleep/resume) would otherwise produce a
	// runaway accumulation jump.
	dtCap = 300 * time.Second
)

// Tick processes one poll's worth of values into the accumulator. Called
// by the poller after each successful poll. Safe to call concurrently
// with Snapshot() (mu protects the counters).
func (e *EnergyTracker) Tick(values map[breezy.ParamID][]byte, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Date rollover comes first: even if we skip the math below, the new
	// day's counters should be zero.
	today := now.Local().Format("2006-01-02")
	if e.Today != today {
		e.HeatingTodayKWh = 0
		e.CoolingTodayKWh = 0
		e.ConsumedTodayKWh = 0
		e.Today = today
	}

	// Compute dt and advance LastTick. The "first tick" case (LastTick zero)
	// produces dt > dtCap → we'd cap, but accumulation would still happen.
	// Treat the first tick after Load specifically by skipping accumulation
	// when LastTick is zero, then setting it.
	prev := e.LastTick
	e.LastTick = now
	if prev.IsZero() {
		return // priming tick, no accumulation
	}
	dt := now.Sub(prev)
	if dt < 0 {
		return // clock jumped backwards
	}
	if dt > dtCap {
		dt = dtCap
	}

	// Resolve UnitType. Missing or non-uint16 → unsupported. Once we've
	// seen a UnitType the daemon doesn't recognise, surface the error.
	unitType, ok := uint16At(values, 0x00B9)
	if !ok {
		e.Error = "device_type (0x00B9) not yet read"
		return
	}
	if _, supported := breezy.ComputeWatts(unitType, 0, 0); !supported {
		e.Error = fmt.Sprintf("unsupported model: %s (type=%d) — no airflow calibration",
			breezy.UnitTypeName(unitType), unitType)
		e.InstantW = 0
		e.ConsumedW = 0
		return
	}
	e.Error = "" // calibration found; clear any prior error

	// Regen-only gate.
	mode, ok := uint8At(values, 0x00B7)
	if !ok || mode != 2 { // 2 = regeneration
		e.InstantW = 0
		e.ConsumedW = 0
		return
	}

	// Inputs for the math. Skip if any is missing or a temperature
	// sentinel (|v| ≥ 1000 °C) signals "no sensor". Per-fan pcts come
	// from the same helper BuildStatus uses (commandedFanPct, exported
	// here as CommandedFanPct), so manual / preset / asymmetric cases
	// resolve correctly without us re-implementing the speed-mode logic.
	supplyPct, ok1 := breezy.CommandedFanPct(values, true)
	extractPct, ok2 := breezy.CommandedFanPct(values, false)
	supplyC, ok3 := tempCAt(values, 0x0020)
	outdoorC, ok4 := tempCAt(values, 0x001F)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		e.InstantW = 0
		e.ConsumedW = 0
		return
	}

	// Recovered W uses the average of the two fans' pcts as the airflow
	// proxy. In symmetric modes (most ventilation/regen settings) this
	// equals either fan's pct; in asymmetric presets it averages the two.
	avgPct := (supplyPct + extractPct) / 2
	w, _ := breezy.ComputeWatts(unitType, avgPct, supplyC-outdoorC)
	e.InstantW = w

	// Consumed W sums each fan's draw at its own pct. ComputeFanWatts is
	// guaranteed supported here (we returned earlier on unsupported).
	supplyFanW, _ := breezy.ComputeFanWatts(unitType, supplyPct)
	extractFanW, _ := breezy.ComputeFanWatts(unitType, extractPct)
	e.ConsumedW = supplyFanW + extractFanW

	// Accumulate. dt is a Duration; Seconds() makes the kWh math readable.
	dtSec := dt.Seconds()
	deltaRecovered := absFloat(w) * dtSec / 3.6e6
	deltaConsumed := e.ConsumedW * dtSec / 3.6e6

	if w > 0 {
		e.HeatingTodayKWh += deltaRecovered
		e.HeatingLifetimeKWh += deltaRecovered
	} else if w < 0 {
		e.CoolingTodayKWh += deltaRecovered
		e.CoolingLifetimeKWh += deltaRecovered
	}
	e.ConsumedTodayKWh += deltaConsumed
	e.ConsumedLifetimeKWh += deltaConsumed

	if err := e.save(); err != nil {
		slog.Warn("energy: save failed", "device", e.Device, "err", err)
	}
}

// uint8At reads a single-byte parameter. Returns (0, false) if missing.
func uint8At(values map[breezy.ParamID][]byte, id breezy.ParamID) (byte, bool) {
	b, ok := values[id]
	if !ok || len(b) < 1 {
		return 0, false
	}
	return b[0], true
}

// uint16At reads a little-endian 2-byte parameter.
func uint16At(values map[breezy.ParamID][]byte, id breezy.ParamID) (uint16, bool) {
	b, ok := values[id]
	if !ok || len(b) < 2 {
		return 0, false
	}
	return uint16(b[0]) | uint16(b[1])<<8, true
}

// tempCAt reads a 2-byte signed temperature in tenths of a degree.
// Sentinel values (|v| ≥ 10000 = 1000 °C) signal "no sensor"; we skip
// those so they don't poison the calculation.
func tempCAt(values map[breezy.ParamID][]byte, id breezy.ParamID) (float64, bool) {
	b, ok := values[id]
	if !ok || len(b) < 2 {
		return 0, false
	}
	v := int16(uint16(b[0]) | uint16(b[1])<<8)
	if v >= 10000 || v <= -10000 {
		return 0, false
	}
	return float64(v) / 10.0, true
}

func absFloat(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/breezyd/ ./pkg/breezy/ -run TestEnergyTracker -v`
Expected: PASS for all tracker tests, plus the existing `pkg/breezy` tests still green after the rename.

Run also: `just test-race` (race detector covers the mutex, since Tick can race with Snapshot from the HTTP path).

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/energy_tracker.go cmd/breezyd/energy_tracker_test.go pkg/breezy/status.go
git commit -m "energy: Tick — regen gate, dt cap, recovered + consumed accumulation"
```

---

## Task 4: BuildStatusWithEnergy + EnergyValues exposure

**Goal:** A new `BuildStatusWithEnergy()` sibling of `BuildStatus()` that takes an optional `*EnergyValues` and emits `service.energy` in the JSON snapshot. The existing `BuildStatus` keeps its signature so CLI / tests / callers stay untouched.

**Files:**
- Modify: `pkg/breezy/status.go` — add `EnergyValues` type alias + `BuildStatusWithEnergy()`
- Modify: `pkg/breezy/status_test.go` — tests for the new function

**Acceptance Criteria:**
- [ ] `BuildStatusWithEnergy(values, name, id, ip, lastPoll, energy *EnergyValues)` returns Status; identical to `BuildStatus()` when `energy` is nil.
- [ ] When `energy` is non-nil, `Status.Service["energy"]` contains all six EnergyValues fields with the JSON keys from the spec.
- [ ] `EnergyValues` is defined in `pkg/breezy` (not `cmd/breezyd`) so the daemon package and any future consumers can pass it without an import cycle.

**Verify:** `go test ./pkg/breezy/ -run TestBuildStatusWithEnergy -v` → all pass

**Steps:**

- [ ] **Step 1: Write failing tests**

(`EnergyValues` already lives in `pkg/breezy/energy.go` from Task 1; this task just adds the BuildStatusWithEnergy function that takes `*EnergyValues` and emits `service.energy`.)

Add to `pkg/breezy/status_test.go`:

```go
func TestBuildStatusWithEnergy_NilEnergy(t *testing.T) {
	a := BuildStatus(map[ParamID][]byte{}, "n", "i", "ip", nil)
	b := BuildStatusWithEnergy(map[ParamID][]byte{}, "n", "i", "ip", nil, nil)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("nil energy must produce identical Status; diff:\n%+v\nvs\n%+v", a, b)
	}
}

func TestBuildStatusWithEnergy_PopulatedEnergy(t *testing.T) {
	ev := &EnergyValues{
		Supported:          true,
		InstantW:           245.0,
		HeatingTodayKWh:    1.234,
		CoolingTodayKWh:    0.456,
		HeatingLifetimeKWh: 234.5,
		CoolingLifetimeKWh: 123.4,
	}
	s := BuildStatusWithEnergy(map[ParamID][]byte{}, "n", "i", "ip", nil, ev)
	got, ok := s.Service["energy"].(EnergyValues)
	if !ok {
		t.Fatalf("service.energy missing or wrong type: %T", s.Service["energy"])
	}
	if got.InstantW != 245.0 || got.HeatingTodayKWh != 1.234 {
		t.Errorf("service.energy values not preserved: %+v", got)
	}
	if got.Error != "" {
		t.Errorf("expected empty Error on supported tracker, got %q", got.Error)
	}
}

func TestBuildStatusWithEnergy_ErrorOnUnsupportedModel(t *testing.T) {
	ev := &EnergyValues{
		Supported: false,
		Error:     "unsupported model: Breezy 200 (type=22) — no airflow calibration",
	}
	s := BuildStatusWithEnergy(map[ParamID][]byte{}, "n", "i", "ip", nil, ev)
	got := s.Service["energy"].(EnergyValues)
	if got.Supported {
		t.Errorf("expected Supported=false")
	}
	if got.Error == "" {
		t.Errorf("expected Error to be populated")
	}
}

func TestBuildStatusWithEnergy_JSONShape(t *testing.T) {
	ev := &EnergyValues{
		Supported:          true,
		InstantW:           245.0,
		HeatingTodayKWh:    1.234,
		HeatingLifetimeKWh: 234.5,
	}
	s := BuildStatusWithEnergy(map[ParamID][]byte{}, "n", "i", "ip", nil, ev)
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"energy"`,
		`"instant_w":245`,
		`"heating_today_kwh":1.234`,
		`"heating_lifetime_kwh":234.5`,
		`"supported":true`,
	} {
		if !strings.Contains(string(out), key) {
			t.Errorf("JSON missing %s; got %s", key, out)
		}
	}
}
```

`reflect` import needs to be added to status_test.go. Existing imports already cover the rest (`encoding/json`, `strings`).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/breezy/ -run TestBuildStatusWithEnergy -v`
Expected: FAIL — `BuildStatusWithEnergy` undefined.

- [ ] **Step 3: Implement BuildStatusWithEnergy**

In `pkg/breezy/status.go`, immediately after `BuildStatus`, add:

```go
// BuildStatusWithEnergy is BuildStatus plus an optional energy block.
// When energy is non-nil, the result's Service map gains an "energy"
// key holding the EnergyValues. When nil, the result is identical to
// BuildStatus's. Daemon callers (with a per-device EnergyTracker) call
// this; CLI and standalone-mode callers keep using BuildStatus.
func BuildStatusWithEnergy(values map[ParamID][]byte, name, id, ip string, lastPoll *time.Time, energy *EnergyValues) Status {
	s := BuildStatus(values, name, id, ip, lastPoll)
	if energy != nil {
		s.Service["energy"] = *energy
	}
	return s
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/breezy/ -v`
Expected: PASS for all `TestBuildStatusWithEnergy_*` tests; existing `TestBuildStatus_*` tests still pass.

- [ ] **Step 5: Commit**

```bash
git add pkg/breezy/status.go pkg/breezy/status_test.go
git commit -m "energy: BuildStatusWithEnergy emits service.energy"
```

---

## Task 5: Wire EnergyTracker into Poller and main

**Goal:** Each device's poller owns an `*EnergyTracker`. After a successful pollOnce, the poller calls `Tick(values, time.Now())`. Construction happens in `main.go::startPollers` from each device config + the daemon's `state_dir`.

**Files:**
- Modify: `cmd/breezyd/poller.go` — add `Energy *EnergyTracker` field; call `Energy.Tick(...)` after a successful poll
- Modify: `cmd/breezyd/main.go` — for each device, build a tracker, `Load()` it, attach to the poller

**Acceptance Criteria:**
- [ ] Each Poller has a `*EnergyTracker` field, nil-safe (pre-existing tests that build a Poller without setting it must not panic).
- [ ] After a successful pollOnce, `Energy.Tick(values, time.Now())` runs if `Energy != nil`.
- [ ] On error / no-data poll, Tick is not called (LastTick stays where it was; the next successful poll uses the longer dt, capped).
- [ ] `main.go` constructs one tracker per device and Load()s it before the poller goroutine starts.
- [ ] `state_dir` is the same path the HomeKit bridge already uses (config field).

**Verify:** `go test ./cmd/breezyd/ -run TestPoller_EnergyTickCalled -v` → pass

**Steps:**

- [ ] **Step 1: Write failing test**

Add to `cmd/breezyd/poller_test.go` (or create if missing — search for existing poller tests first with `grep -l "TestPoller" cmd/breezyd/*_test.go`):

```go
func TestPoller_EnergyTickCalled(t *testing.T) {
	// Build a poller with a tracker; run one tick; assert the tracker's
	// LastTick was set (proxy for "Tick was called").
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	if err := tr.Load(); err != nil {
		t.Fatal(err)
	}

	// PollerClient stub returning a regen snapshot.
	cli := &fakePollerClient{
		readResp: map[breezy.ParamID][]byte{
			0x00B7: {2}, 0x00B9: {17, 0},
			0x0044: {50}, 0x004A: {0xC8, 0x00},
			0x0020: {200, 0}, 0x001F: {0, 0},
		},
	}
	p := &Poller{
		Name:    "p",
		Energy:  tr,
		ReadIDs: []breezy.ParamID{0x00B7, 0x00B9, 0x0044, 0x004A, 0x0020, 0x001F},
		State:   NewState(),
	}
	p.dialClient = func(string, string, string) (PollerClient, error) { return cli, nil }
	if err := p.pollOnce(context.Background()); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if tr.LastTick.IsZero() {
		t.Errorf("tracker.LastTick should have been set; Tick was not called")
	}
}
```

If `fakePollerClient` doesn't already exist, use whatever stub the existing poller tests use — search `grep -rn "PollerClient" cmd/breezyd/*_test.go` and reuse the same pattern.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/breezyd/ -run TestPoller_EnergyTickCalled -v`
Expected: FAIL — Poller has no `Energy` field.

- [ ] **Step 3: Add Energy field + Tick call in Poller**

In `cmd/breezyd/poller.go`, find the `Poller` struct (search for `type Poller struct`) and add a field:

```go
type Poller struct {
	// existing fields ...

	// Energy tracker for this device (optional; nil for tests / standalone).
	// When non-nil, Tick is called after each successful pollOnce.
	Energy *EnergyTracker
}
```

In `pollOnce` (or whichever method actually performs the poll and updates State), find the success path — where State is updated with the freshly read values — and append:

```go
if p.Energy != nil {
	p.Energy.Tick(values, time.Now())
}
```

(The exact insertion point depends on the existing function shape; the variable holding the freshly read values is whatever the existing code passes to `BuildStatus` or stores in `State`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/breezyd/ -run TestPoller_EnergyTickCalled -v`
Expected: PASS.

- [ ] **Step 5: Construct trackers in main.go**

In `cmd/breezyd/main.go`, find `startPollers` (line ~261). Before the per-device goroutine starts, for each device, construct a tracker:

```go
// Per-device energy tracker. State persists in cfg.Daemon.StateDir;
// missing or malformed state file → fresh accumulator (logged warning).
tr := &EnergyTracker{
	Device:   name,
	StateDir: cfg.Daemon.StateDir,
}
if err := tr.Load(); err != nil {
	slog.Warn("energy: tracker load failed", "device", name, "err", err)
}
poller.Energy = tr
```

(Adjust to match the existing variable names and config struct paths in `startPollers`. The config field for state_dir is whatever the HomeKit bridge already uses — search `grep -n "StateDir" cmd/breezyd/*.go` and reuse the same path.)

- [ ] **Step 6: Run all tests**

Run: `just check` (lint + fast tests).
Expected: PASS — no regressions in poller, main, or existing tests.

- [ ] **Step 7: Commit**

```bash
git add cmd/breezyd/poller.go cmd/breezyd/main.go cmd/breezyd/poller_test.go
git commit -m "energy: wire tracker into Poller; construct per device in startPollers"
```

---

## Task 6: HTTP integration — pass energy values to BuildStatusWithEnergy

**Goal:** The cache-driven HTTP handlers (`GET /v1/devices/{name}`, `GET /v1/devices`) read the device's tracker snapshot and call `BuildStatusWithEnergy`. Snapshots emitted to clients now include `service.energy`.

**Files:**
- Modify: `cmd/breezyd/handlers_device.go` — replace `BuildStatus` calls with `BuildStatusWithEnergy`, threading the tracker

**Acceptance Criteria:**
- [ ] `getDevice` and `listDevices` use the tracker per device (looked up by name) to build the energy snapshot.
- [ ] When a device has no tracker (shouldn't happen post-Task 5 but defend anyway), nil is passed → identical behaviour to before.
- [ ] Existing tests (`TestHandler_GetDevice` etc.) still pass; new test asserts `service.energy` is in the response when a tracker is present.

**Verify:** `go test ./cmd/breezyd/ -run TestHandler -v` → all pass; new test for energy in response

**Steps:**

- [ ] **Step 1: Write failing test**

Add to `cmd/breezyd/server_test.go`:

```go
func TestHandler_GetDevice_IncludesEnergy(t *testing.T) {
	h, _, _ := newServerHandler(t)
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:             "playroom",
		StateDir:           dir,
		HeatingTodayKWh:    1.234,
		HeatingLifetimeKWh: 234.5,
		Today:              time.Now().Local().Format("2006-01-02"),
	}
	h.Pollers["playroom"] = &Poller{Energy: tr}
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)

	service, _ := resp["service"].(map[string]any)
	energy, ok := service["energy"].(map[string]any)
	if !ok {
		t.Fatalf("service.energy missing; got %v", service)
	}
	if energy["heating_today_kwh"] != 1.234 {
		t.Errorf("heating_today_kwh = %v, want 1.234", energy["heating_today_kwh"])
	}
}
```

If `Pollers` is a different field name on `Handler`, adjust accordingly (search `grep -n "Pollers" cmd/breezyd/server.go`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/breezyd/ -run TestHandler_GetDevice_IncludesEnergy -v`
Expected: FAIL — `service.energy` is missing because handlers still call `BuildStatus`.

- [ ] **Step 3: Update handler to call BuildStatusWithEnergy**

In `cmd/breezyd/handlers_device.go`, find the `getDevice` function (search `func .*getDevice`). Replace the `BuildStatus(...)` call with:

```go
var ev *breezy.EnergyValues
if p, ok := h.Pollers[name]; ok && p.Energy != nil {
	v := p.Energy.Snapshot()
	ev = &v
}
status := breezy.BuildStatusWithEnergy(snap.Values, name, dev.ID, dev.IP, &snap.LastPoll, ev)
```

Apply the same change to `listDevices` if it builds Status per device (it likely does).

- [ ] **Step 4: Run tests**

Run: `go test ./cmd/breezyd/ -run TestHandler -v`
Expected: PASS for all handler tests including the new `IncludesEnergy` one.

- [ ] **Step 5: Commit**

```bash
git add cmd/breezyd/handlers_device.go cmd/breezyd/server_test.go
git commit -m "energy: HTTP handlers include service.energy in snapshots"
```

---

## Task 7: Prometheus gauges

**Goal:** Eight new gauges per supported device, populated pre-scrape from each tracker's `Snapshot()`. Devices with `Error != ""` emit no gauges.

**Files:**
- Modify: `cmd/breezyd/metrics.go` — add gauges + a hook the metrics handler can use to refresh them
- Modify: `cmd/breezyd/main.go` — pass the trackers map into `metricsHandler` so it can refresh per-device

**Acceptance Criteria:**
- [ ] `breezyd_energy_recovered_watts{device=...}` reflects the tracker's `InstantW` (signed).
- [ ] `breezyd_energy_consumed_watts{device=...}` reflects total fan electric draw (magnitude).
- [ ] `breezyd_energy_{heating,cooling,consumed}_today_kwh{device=...}` and the matching `_lifetime_kwh{...}` reflect the corresponding fields.
- [ ] When a tracker has `Error != ""`, no gauges are emitted for that device label (the existing pre-scrape pattern just skips the `WithLabelValues(...).Set(...)` call; previously emitted samples are deleted).

**Verify:** `go test ./cmd/breezyd/ -run TestMetrics -v` → pass; manual `curl http://localhost:8080/metrics | grep energy` shows the gauges

**Steps:**

- [ ] **Step 1: Add gauges to Metrics struct**

In `cmd/breezyd/metrics.go`, find the `Metrics` struct (search `type Metrics struct`). Add five gauge vectors:

```go
type Metrics struct {
	// existing fields...

	EnergyRecoveredWatts     *prometheus.GaugeVec
	EnergyConsumedWatts      *prometheus.GaugeVec
	EnergyHeatingTodayKWh    *prometheus.GaugeVec
	EnergyCoolingTodayKWh    *prometheus.GaugeVec
	EnergyConsumedTodayKWh   *prometheus.GaugeVec
	EnergyHeatingLifetimeKWh *prometheus.GaugeVec
	EnergyCoolingLifetimeKWh *prometheus.GaugeVec
	EnergyConsumedLifetimeKWh *prometheus.GaugeVec
}
```

In `NewMetrics(reg *prometheus.Registry)`, register them:

```go
gauge := func(name, help string) *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: name, Help: help},
		[]string{"device"},
	)
}
m.EnergyRecoveredWatts = gauge("breezyd_energy_recovered_watts",
	"Instantaneous heat-transfer power across the HRV exchanger. Positive = heating recovered (winter), negative = cooling recovered (summer).")
m.EnergyConsumedWatts = gauge("breezyd_energy_consumed_watts",
	"Instantaneous electric draw of both fans combined (magnitude).")
m.EnergyHeatingTodayKWh = gauge("breezyd_energy_heating_today_kwh",
	"Heating energy recovered today (resets at local midnight).")
m.EnergyCoolingTodayKWh = gauge("breezyd_energy_cooling_today_kwh",
	"Cooling energy recovered today (resets at local midnight).")
m.EnergyConsumedTodayKWh = gauge("breezyd_energy_consumed_today_kwh",
	"Electric energy consumed by the fans today (resets at local midnight).")
m.EnergyHeatingLifetimeKWh = gauge("breezyd_energy_heating_lifetime_kwh",
	"Heating energy recovered cumulative (persists across daemon restart).")
m.EnergyCoolingLifetimeKWh = gauge("breezyd_energy_cooling_lifetime_kwh",
	"Cooling energy recovered cumulative (persists across daemon restart).")
m.EnergyConsumedLifetimeKWh = gauge("breezyd_energy_consumed_lifetime_kwh",
	"Electric energy consumed by the fans cumulative (persists across daemon restart).")
reg.MustRegister(
	m.EnergyRecoveredWatts, m.EnergyConsumedWatts,
	m.EnergyHeatingTodayKWh, m.EnergyCoolingTodayKWh, m.EnergyConsumedTodayKWh,
	m.EnergyHeatingLifetimeKWh, m.EnergyCoolingLifetimeKWh, m.EnergyConsumedLifetimeKWh,
)
```

- [ ] **Step 2: Add a refresh helper**

Still in `metrics.go`, add (near other `Record*` helpers):

```go
// SetEnergy updates all eight energy gauges for a device. Skips entirely
// when the tracker reports an unsupported model (Error != "") so we
// don't expose phantom zeros for un-calibrated units.
func (m *Metrics) SetEnergy(device string, ev breezy.EnergyValues) {
	all := []*prometheus.GaugeVec{
		m.EnergyRecoveredWatts, m.EnergyConsumedWatts,
		m.EnergyHeatingTodayKWh, m.EnergyCoolingTodayKWh, m.EnergyConsumedTodayKWh,
		m.EnergyHeatingLifetimeKWh, m.EnergyCoolingLifetimeKWh, m.EnergyConsumedLifetimeKWh,
	}
	if ev.Error != "" {
		// Drop any previously-emitted samples for this device, in case
		// it was supported earlier and isn't now (e.g., calibration
		// table edited live — unlikely but cheap defence).
		for _, g := range all {
			g.DeleteLabelValues(device)
		}
		return
	}
	m.EnergyRecoveredWatts.WithLabelValues(device).Set(ev.InstantW)
	m.EnergyConsumedWatts.WithLabelValues(device).Set(ev.ConsumedW)
	m.EnergyHeatingTodayKWh.WithLabelValues(device).Set(ev.HeatingTodayKWh)
	m.EnergyCoolingTodayKWh.WithLabelValues(device).Set(ev.CoolingTodayKWh)
	m.EnergyConsumedTodayKWh.WithLabelValues(device).Set(ev.ConsumedTodayKWh)
	m.EnergyHeatingLifetimeKWh.WithLabelValues(device).Set(ev.HeatingLifetimeKWh)
	m.EnergyCoolingLifetimeKWh.WithLabelValues(device).Set(ev.CoolingLifetimeKWh)
	m.EnergyConsumedLifetimeKWh.WithLabelValues(device).Set(ev.ConsumedLifetimeKWh)
}
```

- [ ] **Step 3: Refresh gauges from the metrics handler's pre-scrape hook**

In `cmd/breezyd/main.go`, find `metricsHandler` (line ~327) — it already does pre-scrape work for state-driven gauges. Extend it to walk pollers and call `SetEnergy`:

```go
func metricsHandler(reg *prometheus.Registry, m *Metrics, state *State, devices *DeviceRegistry, pollers map[string]*Poller) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).(http.Handler)
	// (preserved existing wrapping; insert pre-scrape walk before promhttp.HandlerFor)
}
```

Actually replace the body's existing pre-scrape walk to also iterate `pollers`:

```go
return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	// existing per-device state walk...
	for name, p := range pollers {
		if p.Energy != nil {
			m.SetEnergy(name, p.Energy.Snapshot())
		}
	}
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(w, r)
})
```

Update the call site at `cmd/breezyd/main.go:158` to pass pollers in:

```go
mux.Handle("/metrics", metricsHandler(reg, metrics, state, devices, pollers))
```

(`pollers` is the map returned from `startPollers` — it's already in scope a few lines later.)

- [ ] **Step 4: Add a test**

Add to `cmd/breezyd/metrics_test.go`:

```go
func TestMetrics_SetEnergy_Supported(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.SetEnergy("playroom", breezy.EnergyValues{
		Supported:           true,
		InstantW:            245,
		ConsumedW:           18,
		HeatingTodayKWh:     1.234,
		ConsumedTodayKWh:    0.123,
		HeatingLifetimeKWh:  234.5,
		ConsumedLifetimeKWh: 12.3,
	})
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"breezyd_energy_recovered_watts":       245,
		"breezyd_energy_consumed_watts":        18,
		"breezyd_energy_heating_today_kwh":     1.234,
		"breezyd_energy_consumed_today_kwh":    0.123,
		"breezyd_energy_heating_lifetime_kwh":  234.5,
		"breezyd_energy_consumed_lifetime_kwh": 12.3,
	}
	for _, fam := range families {
		w, ok := want[fam.GetName()]
		if !ok {
			continue
		}
		got := fam.GetMetric()[0].GetGauge().GetValue()
		if got != w {
			t.Errorf("%s = %v, want %v", fam.GetName(), got, w)
		}
		delete(want, fam.GetName())
	}
	for name := range want {
		t.Errorf("expected metric %s not emitted", name)
	}
}

func TestMetrics_SetEnergy_UnsupportedDropsLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.SetEnergy("playroom", breezy.EnergyValues{Supported: true, InstantW: 245})
	m.SetEnergy("playroom", breezy.EnergyValues{Error: "unsupported"})
	families, _ := reg.Gather()
	for _, fam := range families {
		if fam.GetName() == "breezyd_energy_recovered_watts" && len(fam.GetMetric()) > 0 {
			t.Errorf("expected zero samples after unsupported update; got %d", len(fam.GetMetric()))
		}
	}
}
```

- [ ] **Step 5: Run tests**

Run: `just check`
Expected: PASS — all existing + new tests.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/metrics.go cmd/breezyd/metrics_test.go cmd/breezyd/main.go
git commit -m "energy: five Prometheus gauges per supported device"
```

---

## Task 8: UI — ENERGY block, NOTICE block, move overrideLine

**Goal:** Add ENERGY block at the bottom of each card, collapsed by default (using `<details>` like Device Info). When expanded, show "now: 245 W heating · 18 W consumed" plus a 3x2 today/lifetime grid (heating / cooling / consumed columns × today / lifetime rows). Add NOTICE block below it that absorbs the existing override warnings (currently rendered inside the Speed control). Hide NOTICE entirely when there are no warnings.

**Files:**
- Modify: `cmd/breezyd/ui/index.html` — render two new `<div class="block">` sections; remove `${overrideLine(snap.live)}` from the Speed `.ctrl`
- Modify: `tests/ui/dashboard.spec.ts` — new tests for ENERGY (supported / error states), new tests for NOTICE (visible / hidden); fix any test that checked the override warning's position relative to Speed

**Acceptance Criteria:**
- [ ] ENERGY block renders below CONTROLS as a `<details class="block">` with summary "ENERGY". Default closed (`<details>` without `open` attribute).
- [ ] Expanding the section shows: a "now: 245 W heating · 18 W consumed" line (sign-encoded for cooling, "0 W (not regen)" when no flow), followed by a 3-column sensor grid with two rows — today (heating | cooling | consumed) and lifetime (heating | cooling | consumed).
- [ ] When `service.energy.error` is non-empty, the expanded view shows the error string instead of the values.
- [ ] When `service.energy` is missing entirely, ENERGY block is hidden.
- [ ] NOTICE block renders below ENERGY as a non-collapsible `<div class="block">`. Hidden (no heading, no padding) when no notices apply. Otherwise heading "NOTICE" plus the override warnings (currently sensor-override and timer-active).
- [ ] No `.warn` element rendered inside the Speed `.ctrl` anymore.

**Verify:** `just test-ui` → all pass (45+ tests; 4-5 new for ENERGY/NOTICE)

**Steps:**

- [ ] **Step 1: Write failing tests**

Add to `tests/ui/dashboard.spec.ts`:

```typescript
test("ENERGY block: collapsed by default, expanding shows now-line + 3-col grid", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: {
          supported: true,
          instant_w: 245,
          consumed_w: 18,
          heating_today_kwh: 1.234,
          cooling_today_kwh: 0.456,
          consumed_today_kwh: 0.123,
          heating_lifetime_kwh: 234.5,
          cooling_lifetime_kwh: 123.4,
          consumed_lifetime_kwh: 12.3,
        },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await expect(energy).toBeVisible();
  // Default closed.
  await expect(energy).not.toHaveAttribute("open", "");
  // Expand and assert content.
  await energy.locator("summary").click();
  await expect(energy).toContainText("245 W heating");
  await expect(energy).toContainText("18 W consumed");
  // 3-col grid: 6 cells.
  const cells = energy.locator(".sensor-cell");
  await expect(cells).toHaveCount(6);
  await expect(energy).toContainText("1.23"); // heating today
  await expect(energy).toContainText("0.12"); // consumed today
  await expect(energy).toContainText("12.30"); // consumed lifetime
});

test("ENERGY block: cooling sign + sums in now-line", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: { supported: true, instant_w: -180, consumed_w: 18, heating_today_kwh: 0, cooling_today_kwh: 0.5, consumed_today_kwh: 0.05, heating_lifetime_kwh: 0, cooling_lifetime_kwh: 0, consumed_lifetime_kwh: 0 },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  await expect(energy).toContainText("180 W cooling");
  await expect(energy).toContainText("18 W consumed");
});

test("ENERGY block: not regen → '0 W (not regen)' + 0 W consumed", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: { supported: true, instant_w: 0, consumed_w: 0, heating_today_kwh: 0, cooling_today_kwh: 0, consumed_today_kwh: 0, heating_lifetime_kwh: 0, cooling_lifetime_kwh: 0, consumed_lifetime_kwh: 0 },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  await expect(energy).toContainText("(not regen)");
});

test("ENERGY block: error replaces grid when collapsed and expanded", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: { supported: false, error: "unsupported model: Breezy 200 (type=22) — no airflow calibration" },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  await expect(energy).toContainText("unsupported model");
});

test("NOTICE block: hidden when no warnings", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: { in_user_control: true, sensor_alerts: { humidity: false, co2: false, voc: false }, special_mode: "off" },
    }),
  });
  await expect(page.locator(".card .block", { hasText: "NOTICE" })).toHaveCount(0);
});

test("NOTICE block: shows sensor-override warning", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: { in_user_control: false, sensor_alerts: { humidity: false, co2: true, voc: true } },
    }),
  });
  const notice = page.locator(".card .block", { hasText: "NOTICE" });
  await expect(notice).toBeVisible();
  await expect(notice).toContainText("sensor override");
  // And not in the Speed control:
  await expect(page.locator(".card .ctrl .warn")).toHaveCount(0);
});
```

Adjust the existing `sensor override:` and `timer turbo: ... countdown line` tests to look for `.block` (NOTICE) rather than searching the whole card or the Speed `.ctrl`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `just test-ui` (or scope to `pnpm exec playwright test -g ENERGY`).
Expected: FAIL — ENERGY/NOTICE blocks don't exist yet.

- [ ] **Step 3: Add ENERGY and NOTICE rendering**

In `cmd/breezyd/ui/index.html`, find the renderCard return template. After the `${renderControls(...)}` line, add:

```html
${renderEnergy(name, snap)}
${renderNotice(name, snap)}
```

And add the two functions (next to renderControls or in a logical neighbouring position):

```js
function renderEnergy(name, snap) {
  const ev = snap.service?.energy;
  if (!ev) return "";
  // Collapsible block — default closed via plain <details> (no open attr).
  // Reuses .block / .sensor-grid / .sensor-cell classes for visual
  // consistency with SENSORS.
  const inner = ev.error
    ? `<div class="warn">${esc(ev.error)}</div>`
    : energyBody(ev);
  return `<details class="block energy">
    <summary><h3>ENERGY</h3></summary>
    ${inner}
  </details>`;
}

function energyBody(ev) {
  let recovered;
  if (ev.instant_w > 0) {
    recovered = `${Math.round(ev.instant_w)} W heating`;
  } else if (ev.instant_w < 0) {
    recovered = `${Math.round(-ev.instant_w)} W cooling`;
  } else {
    recovered = `0 W (not regen)`;
  }
  const consumed = `${Math.round(ev.consumed_w ?? 0)} W consumed`;
  const fmtKwh = (v) => `${(v ?? 0).toFixed(2)} kWh`;
  // 3-col grid: heating | cooling | consumed, today on row 1, lifetime row 2.
  return `<div class="row"><span>now: ${esc(recovered)} · ${esc(consumed)}</span></div>
    <div class="sensor-grid energy-grid">
      <div class="sensor-cell"><div class="sensor-label">heating today</div><div>${fmtKwh(ev.heating_today_kwh)}</div></div>
      <div class="sensor-cell"><div class="sensor-label">cooling today</div><div>${fmtKwh(ev.cooling_today_kwh)}</div></div>
      <div class="sensor-cell"><div class="sensor-label">consumed today</div><div>${fmtKwh(ev.consumed_today_kwh)}</div></div>
      <div class="sensor-cell"><div class="sensor-label">heating lifetime</div><div>${fmtKwh(ev.heating_lifetime_kwh)}</div></div>
      <div class="sensor-cell"><div class="sensor-label">cooling lifetime</div><div>${fmtKwh(ev.cooling_lifetime_kwh)}</div></div>
      <div class="sensor-cell"><div class="sensor-label">consumed lifetime</div><div>${fmtKwh(ev.consumed_lifetime_kwh)}</div></div>
    </div>`;
}

function renderNotice(name, snap) {
  const warning = overrideLine(snap.live); // returns "" when no warning applies
  if (!warning) return "";
  return `<div class="block">
    <h3>NOTICE</h3>
    ${warning}
  </div>`;
}
```

Also add CSS to override the Sensors block's 2-col grid for the ENERGY case (3 columns):

```css
.sensor-grid.energy-grid { grid-template-columns: repeat(3, 1fr); }
```

Place near the existing `.sensor-grid` rule.

Then remove the existing `${overrideLine(snap.live)}` call from inside the Speed `.ctrl` block (search for it with `grep -n overrideLine cmd/breezyd/ui/index.html`).

- [ ] **Step 4: Run UI tests**

Run: `just test-ui`
Expected: PASS — all existing + new tests pass.

- [ ] **Step 5: Regenerate screenshots**

Run: `just screenshot`
Expected: PNGs at `tests/ui/screenshots/dashboard-3col.png` and `dashboard-1col.png` updated; visually verify they show the new ENERGY block and (when applicable) NOTICE block.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/ui/index.html tests/ui/dashboard.spec.ts tests/ui/screenshots/
git commit -m "ui: ENERGY block + NOTICE block; move overrideLine out of Speed"
```

---

## Self-review

**Spec coverage check (each spec section → task):**

- Goal / Architecture diagram → Tasks 1–8 collectively
- Energy math (formula, sign convention, fan-electric draw) → Task 1 (pure calc + ComputeFanWatts) + Task 3 (sign routing + consumed accumulation)
- Calibration table (per-UnitType lookup, airflow + fan-watts triple, error on unknown) → Task 1 (CalPoint with Cmh + FanW)
- EnergyTracker (struct, Tick, Load, save, Snapshot) → Tasks 2 (struct + persistence) + 3 (Tick)
- Persistence (JSON file, atomic temp+rename, missing/malformed handling, lifetime survives restart) → Task 2 (six counters now persisted: heating/cooling/consumed × today/lifetime)
- Status snapshot fields (service.energy with all nine fields) → Task 4
- Prometheus (8 gauges, supported gating) → Task 7
- UI: collapsible ENERGY block (default closed; now-line + 3x2 grid; error replaces body) → Task 8
- UI: NOTICE block (overrideLine moved here, hidden when empty) → Task 8
- Failure modes (unknown UnitType, missing temps, stale snapshot, state file corrupt, write fails, clock jump backwards, paused daemon, first tick) → Task 3 (tick-side) + Task 2 (persistence-side)
- BuildStatusWithEnergy (sibling of BuildStatus, no API break) → Task 4

All spec sections accounted for. The "fan-electric consumption" + "collapsed-by-default UI" additions surfaced after spec freeze; both are integrated through the existing tasks rather than a new task because they fit naturally into the same edit windows.

**Placeholder scan:** No "TBD"/"TODO"/"add appropriate" — every step has concrete code or an exact command.

**Type consistency:**
- `EnergyValues` defined in pkg/breezy (Task 1 step 3) with all nine fields — referenced in pkg/breezy/status.go (Task 4) and cmd/breezyd/energy_tracker.go (Task 2 onwards) as `breezy.EnergyValues`.
- Field names match across `EnergyValues` (struct), `persistedEnergy` (on-disk, six numeric fields), `service.energy` (JSON), and the Prometheus gauge names — all use the `{heating,cooling,consumed}_{today,lifetime}_kwh` pattern + `instant_w` / `consumed_w`.
- `ComputeWatts(unitType uint16, fanPct int, supplyDeltaC float64)` and `ComputeFanWatts(unitType uint16, fanPct int)` signatures consistent in Task 1 (impl) and Task 3 (caller).
- `CalPoint{Pct, Cmh, FanW}` is the unified calibration point shape used in Task 1 (table + interp helpers) — no parallel `AirflowPoint` type lingers.
- `Tick(values map[breezy.ParamID][]byte, now time.Time)` consistent in Task 3 (impl) and Task 5 (caller).
- `Snapshot()` returns `breezy.EnergyValues` (Task 2 sets it up; the `breezy.EnergyValues` type is defined in Task 1); consumed by Task 6 (handler) and Task 7 (metrics).
- `CommandedFanPct` (exported in Task 3 step 3, was `commandedFanPct`) is used both inside `BuildStatus` (existing) and inside the tracker's Tick (Task 3 step 4).

No drift detected.

---
