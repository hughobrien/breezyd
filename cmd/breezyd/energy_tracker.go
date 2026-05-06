// SPDX-License-Identifier: GPL-3.0-or-later

// EnergyTracker accumulates per-device energy accounting across daemon restarts.
// It holds both rolling today counters (reset on calendar-date rollover) and
// lifetime counters (monotonically increasing).  State is persisted to a JSON
// file in StateDir so counters survive daemon restarts.  The tracker is safe
// for concurrent use; all public methods and Tick (Task 3) hold mu.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// persistedEnergy is the on-disk JSON shape for the energy state file.
type persistedEnergy struct {
	TodayDate           string  `json:"today_date"`
	MonthStart          string  `json:"month_start"`
	HeatingTodayKWh     float64 `json:"heating_today_kwh"`
	CoolingTodayKWh     float64 `json:"cooling_today_kwh"`
	ConsumedTodayKWh    float64 `json:"consumed_today_kwh"`
	HeatingMonthKWh     float64 `json:"heating_month_kwh"`
	CoolingMonthKWh     float64 `json:"cooling_month_kwh"`
	ConsumedMonthKWh    float64 `json:"consumed_month_kwh"`
	HeatingLifetimeKWh  float64 `json:"heating_lifetime_kwh"`
	CoolingLifetimeKWh  float64 `json:"cooling_lifetime_kwh"`
	ConsumedLifetimeKWh float64 `json:"consumed_lifetime_kwh"`
	LastUpdated         string  `json:"last_updated"`
}

// EnergyTracker accumulates heating, cooling, and consumed energy for one
// device.  It is populated by Tick (Task 3) and read via Snapshot.
type EnergyTracker struct {
	// Device is the device name; used to build the state-file path.
	Device string
	// StateDir is the directory where the state file is persisted.
	StateDir string

	// Fields below are guarded by mu. Read only via Snapshot() (which
	// takes mu); write only from Tick() / Load() / save() while mu is
	// held. External callers must not touch them directly — the
	// race detector will catch violations once Tick lands in Task 3.
	mu sync.Mutex

	// Today counters (reset on calendar-date rollover).
	HeatingTodayKWh  float64
	CoolingTodayKWh  float64
	ConsumedTodayKWh float64

	// Month counters (reset on calendar-month rollover, local TZ).
	HeatingMonthKWh  float64
	CoolingMonthKWh  float64
	ConsumedMonthKWh float64

	// Lifetime counters (monotonically increasing).
	HeatingLifetimeKWh  float64
	CoolingLifetimeKWh  float64
	ConsumedLifetimeKWh float64

	// InstantW is the signed recovered-heat power (W) for the most recent Tick.
	// Positive = net heat delivered; negative = net cooling.  Not persisted.
	InstantW float64
	// ConsumedW is the total fan electric draw (W) for the most recent Tick.
	// Always non-negative.  Not persisted.
	ConsumedW float64

	// Today is the YYYY-MM-DD date (system local TZ) of the current rolling day.
	Today string
	// MonthStart is the YYYY-MM month (system local TZ) of the current month counters.
	MonthStart string
	// LastTick is the wall-clock time of the most recent Tick call.
	LastTick time.Time
	// Error is non-empty when the device's UnitType has no calibration data.
	Error string
}

// statePath returns the path of the JSON state file for this device.
func (e *EnergyTracker) statePath() string {
	return filepath.Join(e.StateDir, fmt.Sprintf("energy_%s.json", e.Device))
}

// Load reads the persisted state file and restores counters. A missing
// file starts fresh; a corrupt file is warned and discarded. On any
// successful or fresh load, LastTick is zeroed so the first Tick (Task 3)
// primes without accumulating a spurious interval. If the persisted
// today_date differs from today, the today counters are zeroed (lifetime
// carries over). All errors are handled internally — the caller has
// nothing to check, hence no return value.
func (e *EnergyTracker) Load() {
	today := time.Now().Local().Format("2006-01-02")

	data, err := os.ReadFile(e.statePath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("energy: failed to read state file; starting fresh",
				"device", e.Device, "err", err)
		}
		e.Today = today
		e.LastTick = time.Time{}
		return
	}

	var p persistedEnergy
	if err := json.Unmarshal(data, &p); err != nil {
		slog.Warn("energy: failed to unmarshal state file; starting fresh",
			"device", e.Device, "err", err)
		e.Today = today
		e.LastTick = time.Time{}
		return
	}

	// Restore lifetime counters unconditionally.
	e.HeatingLifetimeKWh = p.HeatingLifetimeKWh
	e.CoolingLifetimeKWh = p.CoolingLifetimeKWh
	e.ConsumedLifetimeKWh = p.ConsumedLifetimeKWh

	// Restore today counters only if the stored date matches today.
	if p.TodayDate == today {
		e.HeatingTodayKWh = p.HeatingTodayKWh
		e.CoolingTodayKWh = p.CoolingTodayKWh
		e.ConsumedTodayKWh = p.ConsumedTodayKWh
		e.Today = p.TodayDate
	} else {
		// Calendar rollover: zero today counters, carry lifetime forward.
		e.HeatingTodayKWh = 0
		e.CoolingTodayKWh = 0
		e.ConsumedTodayKWh = 0
		e.Today = today
	}

	// Restore month counters only if the stored month matches this month.
	thisMonth := time.Now().Local().Format("2006-01")
	if p.MonthStart == thisMonth {
		e.HeatingMonthKWh = p.HeatingMonthKWh
		e.CoolingMonthKWh = p.CoolingMonthKWh
		e.ConsumedMonthKWh = p.ConsumedMonthKWh
		e.MonthStart = p.MonthStart
	} else {
		// Month rollover: zero month counters; persisted file with no
		// month_start (e.g. from before this version) lands here too.
		e.HeatingMonthKWh = 0
		e.CoolingMonthKWh = 0
		e.ConsumedMonthKWh = 0
		e.MonthStart = thisMonth
	}

	// Always zero LastTick so the first Tick after Load primes without
	// accumulating a spurious interval from the previous daemon run.
	e.LastTick = time.Time{}
}

// save writes the current state atomically to statePath via a sibling temp
// file + rename.  The caller must hold mu or ensure exclusive access.
func (e *EnergyTracker) save() error {
	p := persistedEnergy{
		TodayDate:           e.Today,
		MonthStart:          e.MonthStart,
		HeatingTodayKWh:     e.HeatingTodayKWh,
		CoolingTodayKWh:     e.CoolingTodayKWh,
		ConsumedTodayKWh:    e.ConsumedTodayKWh,
		HeatingMonthKWh:     e.HeatingMonthKWh,
		CoolingMonthKWh:     e.CoolingMonthKWh,
		ConsumedMonthKWh:    e.ConsumedMonthKWh,
		HeatingLifetimeKWh:  e.HeatingLifetimeKWh,
		CoolingLifetimeKWh:  e.CoolingLifetimeKWh,
		ConsumedLifetimeKWh: e.ConsumedLifetimeKWh,
		LastUpdated:         time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("energy: marshal: %w", err)
	}

	tmpPath := e.statePath() + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o600); err != nil {
		return fmt.Errorf("energy: write temp: %w", err)
	}
	if err := os.Rename(tmpPath, e.statePath()); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("energy: rename temp: %w", err)
	}
	return nil
}

// Snapshot returns a value copy of the current energy state.  Supported is
// true when Error is empty (i.e. the unit type has calibration data).
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
		HeatingMonthKWh:     e.HeatingMonthKWh,
		CoolingMonthKWh:     e.CoolingMonthKWh,
		ConsumedMonthKWh:    e.ConsumedMonthKWh,
		HeatingLifetimeKWh:  e.HeatingLifetimeKWh,
		CoolingLifetimeKWh:  e.CoolingLifetimeKWh,
		ConsumedLifetimeKWh: e.ConsumedLifetimeKWh,
		Error:               e.Error,
	}
}

// dtCap bounds how much wall time a single tick can claim. A long pause
// (network out, daemon paused, sleep/resume) would otherwise produce a
// runaway accumulation jump from the elapsed wall clock.
const dtCap = 300 * time.Second

// Tick processes one poll's worth of values into the accumulator. The
// poller calls this after each successful poll. Holds mu for the whole
// duration so concurrent Snapshot() calls from the HTTP path see a
// consistent view.
//
// Logic:
//  1. Date rollover (zero today counters when local date changes).
//  2. First-tick priming: if LastTick was zero, set it and return without accumulating.
//  3. Compute dt; cap at dtCap; clamp negative to zero.
//  4. Resolve UnitType (param 0x00B9). Missing or unsupported → set Error and skip math.
//  5. Regen-only gate (airflow_mode 0x00B7 must equal 1 = regeneration).
//  6. Read inputs: supply pct + extract pct (CommandedFanPct), supply temp (0x0020), outdoor temp (0x001F).
//  7. Compute recovered W (avg of supply+extract pcts as airflow proxy) and consumed W (sum of per-fan electric draws).
//  8. Accumulate |W| × dt / 3.6e6 into right counter (heating if Δ>0, cooling if Δ<0); accumulate consumed always.
//  9. save() the new state.
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
		if err := e.save(); err != nil {
			slog.Warn("energy: rollover save failed", "device", e.Device, "err", err)
		}
	}
	// Month rollover, parallel to date rollover. Crossing a month boundary
	// also crosses a day boundary, so this fires after the date branch
	// (saving twice in that rare case is harmless).
	thisMonth := now.Local().Format("2006-01")
	if e.MonthStart != thisMonth {
		e.HeatingMonthKWh = 0
		e.CoolingMonthKWh = 0
		e.ConsumedMonthKWh = 0
		e.MonthStart = thisMonth
		if err := e.save(); err != nil {
			slog.Warn("energy: month rollover save failed", "device", e.Device, "err", err)
		}
	}

	// Compute dt and advance LastTick. First-tick after Load (LastTick
	// zero) primes without accumulating; otherwise dt is bounded by dtCap
	// and clamped non-negative.
	prev := e.LastTick
	e.LastTick = now
	if prev.IsZero() {
		return
	}
	dt := now.Sub(prev)
	if dt < 0 {
		return
	}
	if dt > dtCap {
		dt = dtCap
	}

	unitType, ok := breezy.Uint16At(values, 0x00B9)
	if !ok {
		e.Error = "device_type (0x00B9) not yet read"
		e.InstantW = 0
		e.ConsumedW = 0
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

	mode, ok := breezy.Uint8At(values, 0x00B7)
	if !ok || mode != 1 { // 1 = regeneration (wire value of 0xB7)
		e.InstantW = 0
		e.ConsumedW = 0
		return
	}

	supplyPct, ok1 := breezy.CommandedFanPct(values, true)
	extractPct, ok2 := breezy.CommandedFanPct(values, false)
	supplyC, ok3 := readTempC(values, 0x0020)
	outdoorC, ok4 := readTempC(values, 0x001F)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		e.InstantW = 0
		e.ConsumedW = 0
		return
	}

	// Recovered uses the average of the two fan pcts as the airflow proxy.
	// Integer truncation here loses ≤0.5pp on asymmetric pairs (e.g. 70+99
	// → avg 84 rather than 84.5), well below the ~10% calibration-curve
	// uncertainty that already dominates the W estimate.
	avgPct := (supplyPct + extractPct) / 2
	w, _ := breezy.ComputeWatts(unitType, avgPct, supplyC-outdoorC)
	e.InstantW = w

	// Consumed: per-fan sum. Both fans run in regen — different pcts
	// (e.g. preset3 = 70/100) yield different draws.
	supplyFanW, _ := breezy.ComputeFanWatts(unitType, supplyPct)
	extractFanW, _ := breezy.ComputeFanWatts(unitType, extractPct)
	e.ConsumedW = supplyFanW + extractFanW

	dtSec := dt.Seconds()
	deltaRecovered := math.Abs(w) * dtSec / 3.6e6
	deltaConsumed := e.ConsumedW * dtSec / 3.6e6

	if w > 0 {
		e.HeatingTodayKWh += deltaRecovered
		e.HeatingMonthKWh += deltaRecovered
		e.HeatingLifetimeKWh += deltaRecovered
	} else if w < 0 {
		e.CoolingTodayKWh += deltaRecovered
		e.CoolingMonthKWh += deltaRecovered
		e.CoolingLifetimeKWh += deltaRecovered
	}
	e.ConsumedTodayKWh += deltaConsumed
	e.ConsumedMonthKWh += deltaConsumed
	e.ConsumedLifetimeKWh += deltaConsumed

	if err := e.save(); err != nil {
		slog.Warn("energy: save failed", "device", e.Device, "err", err)
	}
}

// readTempC pulls a 2-byte signed temperature in tenths of a degree
// from the values map. Returns (0, false) when the value is missing or
// hits the sensor sentinel (|v| >= 10000 = ±1000 °C).
func readTempC(values map[breezy.ParamID][]byte, id breezy.ParamID) (float64, bool) {
	v, ok := breezy.Int16At(values, id)
	if !ok {
		return 0, false
	}
	if v >= 10000 || v <= -10000 {
		return 0, false
	}
	return float64(v) / 10.0, true
}
