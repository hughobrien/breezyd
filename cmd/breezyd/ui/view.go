// SPDX-License-Identifier: GPL-3.0-or-later

// DeviceView and sub-view types used by all templ templates.  Handlers convert
// the cached Snapshot (raw param bytes) to a DeviceView once per request;
// templates consume it and only it.  The conversion lives in
// cmd/breezyd/ui_view.go (package main) so templates stay importable from
// package ui without an import cycle.
package ui

import "encoding/json"

// DeviceView is the decoded, render-ready form of a device snapshot.
type DeviceView struct {
	Name string
	IP   string
	// Unreachable is true when the device is configured but no successful
	// poll has produced a Snapshot yet (wrong IP, password, network down,
	// firmware off). The card renders a minimal placeholder instead of the
	// full layout when this is set; most other fields are zero values.
	Unreachable bool
	Serial      string // device ID string
	Stale       bool
	LastPollAge string // human-readable "Xs" / "Xm Ys" / "" when fresh
	FanSettling bool

	Power       bool
	AirflowMode string // "regeneration" | "supply" | "extract" | "ventilation"
	SpeedMode   string // "manual" | "preset1" | "preset2" | "preset3"
	ManualPct   int    // 10-100; only meaningful when SpeedMode=="manual"
	Heater      bool

	Preset1 PresetView
	Preset2 PresetView
	Preset3 PresetView

	SpecialMode          string // "off" | "night" | "turbo"
	SpecialModeRemaining string // human-readable, empty when SpecialMode=="off"

	FirmwareVersion string // "X.YY" or "—"
	FirmwareDate    string // "YYYY-MM-DD" or "—"

	FilterStatus    string // "clean" | "soiled" | "—"
	FilterRemaining string // e.g. "42d" or ""
	MotorLifetime   string // e.g. "1234h 56m" or "—"
	RTCBattery      string // e.g. "3.200V" or "—"
	FaultLevel      string // "none" | "alarm" | "warning" | "—"

	// NeedsAttention is true when fault or soiled filter; drives auto-open of
	// the Device Info <details>.
	NeedsAttention bool

	Sensors  SensorsView
	Energy   *EnergyView // nil when no energy tracker
	Schedule ScheduleView
}

// PresetView is the stored supply/extract percentages for one numbered preset.
type PresetView struct {
	Supply  int // 10-100; "-1" sentinel when not yet in cache
	Extract int
}

// SensorsView carries all sensor readings plus threshold/alert state.
type SensorsView struct {
	AlertActive bool // any of humidity/CO2/VOC alerting

	HumidityPct int
	CO2PPM      int
	VOCPPM      int // VOC index (Sensirion 0-500)

	TempOutdoorC      *float64 // nil = missing
	TempSupplyC       *float64
	TempExhaustInletC *float64
	TempExhaustOutC   *float64
	RecoveryPct       *int // nil = missing

	SupplyRPM  *int // nil when fan-settling or missing
	ExtractRPM *int

	// Per-sensor alert flags.
	HumidityAlert bool
	CO2Alert      bool
	VOCAlert      bool

	// Configured thresholds (for the inline editor).
	HumidityThreshold int
	CO2Threshold      int
	VOCThreshold      int

	// Whether each sensor's auto-fan response is enabled.
	HumidityAutoFan bool
	CO2AutoFan      bool
	VOCAutoFan      bool
}

// EnergyView carries energy accounting data ready for templating.
type EnergyView struct {
	Error string // non-empty → show warn div instead of grid

	InstantW  float64
	ConsumedW float64

	HeatingTodayKWh     float64
	CoolingTodayKWh     float64
	ConsumedTodayKWh    float64
	HeatingMonthKWh     float64
	CoolingMonthKWh     float64
	ConsumedMonthKWh    float64
	HeatingLifetimeKWh  float64
	CoolingLifetimeKWh  float64
	ConsumedLifetimeKWh float64
}

// ScheduleView carries the schedule data for the schedule block.
type ScheduleView struct {
	// Present is true when a scheduler is wired for this device.
	Present bool
	Enabled bool
	Entries []ScheduleEntryView
	Alert   bool // last fire failed
	// LastApply is non-nil when there was a recent fire attempt with an error.
	LastApply *LastApplyView
}

// ScheduleEntryView is one row in the schedule editor.
type ScheduleEntryView struct {
	At     string // "HH:MM"
	Action string // "off" | "regeneration" | "ventilation" | "supply" | "extract"
	Pct    int    // 10-100 (ignored for Action=="off")
}

// LastApplyView carries the last-fire outcome for the schedule block.
type LastApplyView struct {
	At      string // "HH:MM"
	Err     string
	Retries int
}

// CardSignals is the per-device datastar signal payload that drives
// card-outer reactive state (stale class, speed-mode and airflow-mode
// data-attrs, "X ago" stale-row, sensors-block alert class). The card's
// HTML is never patched after initial render; signals are.
//
// Each field is a map keyed by device name so that a single
// datastar-patch-signals event scopes updates to exactly one card without
// overwriting sibling cards' values (same pattern as $detailsOpen from
// PR #215). Bindings reference $<signal>.<deviceName> rather than the
// bare $<signal>.
type CardSignals struct {
	Stale        map[string]bool   `json:"stale"`
	SpeedMode    map[string]string `json:"speedMode"`
	AirflowMode  map[string]string `json:"airflowMode"`
	LastPollAge  map[string]string `json:"lastPollAge"`
	SensorsAlert map[string]bool   `json:"sensorsAlert"`
}

// CardSignalsFor extracts CardSignals from a DeviceView.
func CardSignalsFor(v DeviceView) CardSignals {
	return CardSignals{
		Stale:        map[string]bool{v.Name: v.Stale},
		SpeedMode:    map[string]string{v.Name: v.SpeedMode},
		AirflowMode:  map[string]string{v.Name: v.AirflowMode},
		LastPollAge:  map[string]string{v.Name: v.LastPollAge},
		SensorsAlert: map[string]bool{v.Name: v.Sensors.AlertActive},
	}
}

// MarshalCardSignals returns the JSON payload for a PatchSignals event.
func MarshalCardSignals(v DeviceView) ([]byte, error) {
	return json.Marshal(CardSignalsFor(v))
}
