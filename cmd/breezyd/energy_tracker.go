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
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// persistedEnergy is the on-disk JSON shape for the energy state file.
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
	// LastTick is the wall-clock time of the most recent Tick call.
	LastTick time.Time
	// Error is non-empty when the device's UnitType has no calibration data.
	Error string
}

// statePath returns the path of the JSON state file for this device.
func (e *EnergyTracker) statePath() string {
	return filepath.Join(e.StateDir, fmt.Sprintf("energy_%s.json", e.Device))
}

// Load reads the persisted state file and restores counters.  It always
// returns nil: a missing file starts fresh; a corrupt file is warned and
// discarded.  On any successful or fresh load, LastTick is zeroed so the
// first Tick (Task 3) primes without accumulating a spurious interval.
// If the persisted today_date differs from today, the today counters are
// zeroed (lifetime carries over).
func (e *EnergyTracker) Load() error {
	today := time.Now().Local().Format("2006-01-02")

	data, err := os.ReadFile(e.statePath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("energy: failed to read state file; starting fresh",
				"device", e.Device, "err", err)
		}
		e.Today = today
		e.LastTick = time.Time{}
		return nil
	}

	var p persistedEnergy
	if err := json.Unmarshal(data, &p); err != nil {
		slog.Warn("energy: failed to unmarshal state file; starting fresh",
			"device", e.Device, "err", err)
		e.Today = today
		e.LastTick = time.Time{}
		return nil
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

	// Always zero LastTick so the first Tick after Load primes without
	// accumulating a spurious interval from the previous daemon run.
	e.LastTick = time.Time{}
	return nil
}

// save writes the current state atomically to statePath via a sibling temp
// file + rename.  The caller must hold mu or ensure exclusive access.
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
		HeatingLifetimeKWh:  e.HeatingLifetimeKWh,
		CoolingLifetimeKWh:  e.CoolingLifetimeKWh,
		ConsumedLifetimeKWh: e.ConsumedLifetimeKWh,
		Error:               e.Error,
	}
}

// Tick is implemented in Task 3.
