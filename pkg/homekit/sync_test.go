// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// newTestAccessory is a shorthand for the standard test accessory.
func newTestAccessory() *Accessory {
	return NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
}

// TestSync_PowerAndSpeed covers Active, RotationSpeed, TargetAirPurifierState,
// and CurrentAirPurifierState (Purifying path).
func TestSync_PowerAndSpeed(t *testing.T) {
	a := newTestAccessory()
	s := breezy.Status{
		Configured: map[string]any{
			"power":      true,
			"speed_mode": "manual",
			"manual_pct": 30,
		},
		Live: map[string]any{
			"fan_supply_rpm":  1500,
			"fan_extract_rpm": 1450,
		},
	}
	Sync(a, s)

	if v := a.AirPurifier.Active.Value(); v != 1 {
		t.Errorf("Active = %v, want 1 (powered)", v)
	}
	if v := a.RotationSpeed.Value(); v != 30.0 {
		t.Errorf("RotationSpeed = %v, want 30", v)
	}
	// manual → TargetAirPurifierState = 0 (Manual)
	if v := a.AirPurifier.TargetAirPurifierState.Value(); v != 0 {
		t.Errorf("TargetAirPurifierState = %v, want 0 (Manual)", v)
	}
	// fans running → CurrentAirPurifierState = 2 (Purifying)
	if v := a.AirPurifier.CurrentAirPurifierState.Value(); v != 2 {
		t.Errorf("CurrentAirPurifierState = %v, want 2 (Purifying)", v)
	}
}

// TestSync_PowerOff verifies Inactive state and Active=0 when power is false.
func TestSync_PowerOff(t *testing.T) {
	a := newTestAccessory()
	s := breezy.Status{
		Configured: map[string]any{
			"power":      false,
			"speed_mode": "preset1",
			"manual_pct": 50,
		},
		Live: map[string]any{
			"fan_supply_rpm":  0,
			"fan_extract_rpm": 0,
		},
	}
	Sync(a, s)

	if v := a.AirPurifier.Active.Value(); v != 0 {
		t.Errorf("Active = %v, want 0 (off)", v)
	}
	// preset1 → Auto
	if v := a.AirPurifier.TargetAirPurifierState.Value(); v != 1 {
		t.Errorf("TargetAirPurifierState = %v, want 1 (Auto)", v)
	}
	// not powered → Inactive = 0
	if v := a.AirPurifier.CurrentAirPurifierState.Value(); v != 0 {
		t.Errorf("CurrentAirPurifierState = %v, want 0 (Inactive)", v)
	}
}

// TestSync_IdleWhenPoweredNoFan covers the Idle path (powered, zero RPM).
func TestSync_IdleWhenPoweredNoFan(t *testing.T) {
	a := newTestAccessory()
	s := breezy.Status{
		Configured: map[string]any{"power": true},
		Live: map[string]any{
			"fan_supply_rpm":  0,
			"fan_extract_rpm": 0,
		},
	}
	Sync(a, s)

	if v := a.AirPurifier.CurrentAirPurifierState.Value(); v != 1 {
		t.Errorf("CurrentAirPurifierState = %v, want 1 (Idle)", v)
	}
}

// TestSync_PowerFieldAbsentLeavesStateUntouched protects the Sync
// contract: if the power field is missing from a partial snapshot,
// CurrentAirPurifierState must NOT be written. Otherwise a snapshot
// that happened to read fan RPMs but skipped 0x01 would falsely
// flip the iOS Home tile to Inactive.
func TestSync_PowerFieldAbsentLeavesStateUntouched(t *testing.T) {
	a := newTestAccessory()
	// Pre-set the characteristic so we can observe it being preserved.
	_ = a.AirPurifier.CurrentAirPurifierState.SetValue(2) // Purifying
	s := breezy.Status{
		Configured: map[string]any{}, // no "power" key
		Live: map[string]any{
			"fan_supply_rpm":  1500,
			"fan_extract_rpm": 1450,
		},
	}
	Sync(a, s)

	if v := a.AirPurifier.CurrentAirPurifierState.Value(); v != 2 {
		t.Errorf("CurrentAirPurifierState = %v, want 2 (untouched, was set to Purifying)", v)
	}
}

// TestSync_AutoModes verifies preset2/preset3 all map to Auto.
func TestSync_AutoModes(t *testing.T) {
	for _, mode := range []string{"preset1", "preset2", "preset3"} {
		a := newTestAccessory()
		Sync(a, breezy.Status{
			Configured: map[string]any{
				"power":      true,
				"speed_mode": mode,
			},
		})
		if v := a.AirPurifier.TargetAirPurifierState.Value(); v != 1 {
			t.Errorf("mode=%s: TargetAirPurifierState = %v, want 1 (Auto)", mode, v)
		}
	}
}

// TestSync_AirflowModeSwitches covers all four airflow_mode strings.
func TestSync_AirflowModeSwitches(t *testing.T) {
	cases := []struct {
		mode    string
		supply  bool
		extract bool
	}{
		{"ventilation", false, false},
		{"regeneration", false, false},
		{"supply", true, false},
		{"extract", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			a := newTestAccessory()
			Sync(a, breezy.Status{Configured: map[string]any{"airflow_mode": tc.mode}})
			if v := a.SupplyOnly.On.Value(); v != tc.supply {
				t.Errorf("supply mode=%s: Supply.On = %v, want %v", tc.mode, v, tc.supply)
			}
			if v := a.ExtractOnly.On.Value(); v != tc.extract {
				t.Errorf("extract mode=%s: Extract.On = %v, want %v", tc.mode, v, tc.extract)
			}
		})
	}
}

// TestSync_Humidity covers CurrentRelativeHumidity.
func TestSync_Humidity(t *testing.T) {
	a := newTestAccessory()
	Sync(a, breezy.Status{Sensors: map[string]any{"humidity_pct": 65}})
	if v := a.Humidity.CurrentRelativeHumidity.Value(); v != 65.0 {
		t.Errorf("Humidity = %v, want 65", v)
	}
}

// TestSync_CO2Detection covers CarbonDioxideLevel and above/below-threshold detection.
func TestSync_CO2Detection(t *testing.T) {
	// Above threshold → Detected = 1.
	a := newTestAccessory()
	Sync(a, breezy.Status{
		Configured: map[string]any{"co2_threshold_ppm": 1000},
		Sensors:    map[string]any{"eco2_ppm": 1200},
	})
	if v := a.CarbonDioxideLevel.Value(); v != 1200.0 {
		t.Errorf("CarbonDioxideLevel = %v, want 1200", v)
	}
	if v := a.CO2.CarbonDioxideDetected.Value(); v != 1 {
		t.Errorf("CarbonDioxideDetected = %v, want 1 (above threshold)", v)
	}

	// Below threshold → Detected = 0.
	a2 := newTestAccessory()
	Sync(a2, breezy.Status{
		Configured: map[string]any{"co2_threshold_ppm": 1000},
		Sensors:    map[string]any{"eco2_ppm": 800},
	})
	if v := a2.CO2.CarbonDioxideDetected.Value(); v != 0 {
		t.Errorf("CarbonDioxideDetected = %v, want 0 (below threshold)", v)
	}
}

// TestSync_VOCToAirQuality covers VOCDensity and the AirQuality enum bucket.
func TestSync_VOCToAirQuality(t *testing.T) {
	cases := []struct {
		voc      int
		wantEnum AirQualityLevel
	}{
		{25, AirQualityExcellent},
		{75, AirQualityGood},
		{125, AirQualityFair},
		{175, AirQualityInferior},
		{300, AirQualityPoor},
	}
	for _, tc := range cases {
		a := newTestAccessory()
		Sync(a, breezy.Status{Sensors: map[string]any{"voc_index": tc.voc}})
		if v := a.VOCDensity.Value(); v != float64(tc.voc) {
			t.Errorf("voc=%d: VOCDensity = %v, want %v", tc.voc, v, float64(tc.voc))
		}
		if v := a.AirQualitySvc.AirQuality.Value(); v != int(tc.wantEnum) {
			t.Errorf("voc=%d: AirQuality enum = %v, want %v", tc.voc, v, int(tc.wantEnum))
		}
	}
}

// TestSync_TemperatureSentinelsSkipped verifies that implausible temperature
// values (|v| ≥ 1000) are not written to the characteristic.
func TestSync_TemperatureSentinelsSkipped(t *testing.T) {
	a := newTestAccessory()
	// Preset the Outdoor and Supply sensors to known values so we can confirm
	// what changes and what doesn't.
	a.TempOutdoor.CurrentTemperature.SetValue(7.7)
	a.TempSupply.CurrentTemperature.SetValue(18.5)

	Sync(a, breezy.Status{Sensors: map[string]any{
		"temp_outdoor_c": 12.5,   // valid → written
		"temp_supply_c":  1000.0, // sentinel (≥1000) → skipped
		// temp_exhaust_inlet_c absent → skipped
		"temp_exhaust_outlet_c": -1000.0, // sentinel (|v|≥1000) → skipped
	}})

	if v := a.TempOutdoor.CurrentTemperature.Value(); v != 12.5 {
		t.Errorf("Outdoor temp = %v, want 12.5", v)
	}
	// Supply was preset to 18.5 but sentinel was presented → must remain 18.5.
	if v := a.TempSupply.CurrentTemperature.Value(); v != 18.5 {
		t.Errorf("Supply temp = %v, want 18.5 (sentinel, untouched)", v)
	}
}

// TestSync_TemperatureValid confirms all four sensors are written when present
// and valid.
func TestSync_TemperatureValid(t *testing.T) {
	a := newTestAccessory()
	Sync(a, breezy.Status{Sensors: map[string]any{
		"temp_outdoor_c":        5.0,
		"temp_supply_c":         20.0,
		"temp_exhaust_inlet_c":  22.0,
		"temp_exhaust_outlet_c": -5.0,
	}})
	want := map[string]float64{
		"outdoor":    5.0,
		"supply":     20.0,
		"exhaustIn":  22.0,
		"exhaustOut": -5.0,
	}
	got := map[string]float64{
		"outdoor":    a.TempOutdoor.CurrentTemperature.Value(),
		"supply":     a.TempSupply.CurrentTemperature.Value(),
		"exhaustIn":  a.TempExhaustIn.CurrentTemperature.Value(),
		"exhaustOut": a.TempExhaustOut.CurrentTemperature.Value(),
	}
	for k, wv := range want {
		if got[k] != wv {
			t.Errorf("temp[%s] = %v, want %v", k, got[k], wv)
		}
	}
}

// TestSync_MissingFieldsNoPanic ensures Sync with empty/nil maps doesn't panic.
func TestSync_MissingFieldsNoPanic(t *testing.T) {
	a := newTestAccessory()
	// Fully empty status — nothing should panic.
	Sync(a, breezy.Status{})
	// Partial maps — still no panic.
	Sync(a, breezy.Status{
		Configured: map[string]any{"power": true},
		Sensors:    map[string]any{},
	})
}

// TestSync_FloatFieldCoercion verifies that float64-typed values from JSON
// decoding are handled correctly (JSON numbers decode to float64 by default).
func TestSync_FloatFieldCoercion(t *testing.T) {
	a := newTestAccessory()
	s := breezy.Status{
		Configured: map[string]any{
			"power":             true,
			"manual_pct":        float64(45), // JSON-decoded form
			"co2_threshold_ppm": float64(1000),
		},
		Live: map[string]any{
			"fan_supply_rpm":  float64(1200),
			"fan_extract_rpm": float64(0),
		},
		Sensors: map[string]any{
			"eco2_ppm":     float64(1500),
			"voc_index":    float64(80),
			"humidity_pct": float64(55),
		},
	}
	Sync(a, s)

	if v := a.RotationSpeed.Value(); v != 45.0 {
		t.Errorf("RotationSpeed (float64 input) = %v, want 45", v)
	}
	if v := a.CO2.CarbonDioxideDetected.Value(); v != 1 {
		t.Errorf("CarbonDioxideDetected = %v, want 1 (1500 > 1000)", v)
	}
	if v := a.AirQualitySvc.AirQuality.Value(); v != int(AirQualityGood) {
		t.Errorf("AirQuality = %v, want Good (voc=80)", v)
	}
}

func TestSync_RotationSpeedPrefersLiveFanPct(t *testing.T) {
	a := newTestAccessory()
	s := breezy.Status{
		Configured: map[string]any{
			"power":      true,
			"speed_mode": "preset2",
			"manual_pct": 50,
		},
		Live: map[string]any{
			"fan_supply_pct":  60,
			"fan_extract_pct": 65,
		},
	}
	Sync(a, s)
	if v := a.RotationSpeed.Value(); v != 60.0 {
		t.Errorf("RotationSpeed = %v, want 60 (live.fan_supply_pct)", v)
	}
}

func TestSync_RotationSpeedFallsBackToManualPct(t *testing.T) {
	a := newTestAccessory()
	s := breezy.Status{
		Configured: map[string]any{
			"power":      true,
			"speed_mode": "manual",
			"manual_pct": 35,
		},
		Live: map[string]any{},
	}
	Sync(a, s)
	if v := a.RotationSpeed.Value(); v != 35.0 {
		t.Errorf("RotationSpeed = %v, want 35 (fallback to manual_pct)", v)
	}
}

func TestSync_HeaterSwitch(t *testing.T) {
	a := newTestAccessory()
	Sync(a, breezy.Status{Configured: map[string]any{"heater_enabled": true}})
	if !a.Heater.On.Value() {
		t.Error("Heater.On = false, want true")
	}
	Sync(a, breezy.Status{Configured: map[string]any{"heater_enabled": false}})
	if a.Heater.On.Value() {
		t.Error("Heater.On = true, want false")
	}
}

func TestSync_TimerSwitches(t *testing.T) {
	a := newTestAccessory()
	cases := []struct {
		mode  string
		night bool
		turbo bool
	}{
		{"off", false, false},
		{"night", true, false},
		{"turbo", false, true},
	}
	for _, c := range cases {
		Sync(a, breezy.Status{Live: map[string]any{"special_mode": c.mode}})
		if a.Night.On.Value() != c.night {
			t.Errorf("special_mode=%q: Night.On = %v, want %v", c.mode, a.Night.On.Value(), c.night)
		}
		if a.Turbo.On.Value() != c.turbo {
			t.Errorf("special_mode=%q: Turbo.On = %v, want %v", c.mode, a.Turbo.On.Value(), c.turbo)
		}
	}
}

func TestSync_FilterMaintenance(t *testing.T) {
	a := newTestAccessory()

	// Clean filter, 30 of 90 days remaining.
	Sync(a, breezy.Status{Service: map[string]any{
		"filter_status":            "clean",
		"filter_remaining_seconds": 30 * 86400,
		"filter_total_seconds":     90 * 86400,
	}})
	if v := a.Filter.FilterChangeIndication.Value(); v != 0 {
		t.Errorf("FilterChangeIndication = %v, want 0 (clean)", v)
	}
	if v := a.FilterLifeLevel.Value(); v != 33.0 {
		t.Errorf("FilterLifeLevel = %v, want 33", v)
	}

	// Soiled filter.
	Sync(a, breezy.Status{Service: map[string]any{"filter_status": "soiled"}})
	if v := a.Filter.FilterChangeIndication.Value(); v != 1 {
		t.Errorf("FilterChangeIndication = %v, want 1 (soiled)", v)
	}
}

func TestSync_BatteryLevelFromVolts(t *testing.T) {
	cases := []struct {
		volts float64
		pct   int
		low   int
	}{
		{3.0, 100, 0},
		{2.75, 50, 0},
		{2.7, 40, 1}, // borderline-low
		{2.5, 0, 1},
		{2.3, 0, 1},   // clamped
		{3.5, 100, 0}, // clamped
	}
	for _, c := range cases {
		a := newTestAccessory()
		Sync(a, breezy.Status{Service: map[string]any{"rtc_battery_volts": c.volts}})
		if v := a.Battery.BatteryLevel.Value(); v != c.pct {
			t.Errorf("volts=%v: BatteryLevel = %v, want %v", c.volts, v, c.pct)
		}
		if v := a.Battery.StatusLowBattery.Value(); v != c.low {
			t.Errorf("volts=%v: StatusLowBattery = %v, want %v", c.volts, v, c.low)
		}
	}
}

func TestSync_StatusFault(t *testing.T) {
	a := newTestAccessory()
	Sync(a, breezy.Status{Service: map[string]any{"fault_level": "none"}})
	if v := a.StatusFault.Value(); v != 0 {
		t.Errorf("StatusFault (none) = %v, want 0", v)
	}
	Sync(a, breezy.Status{Service: map[string]any{"fault_level": "alarm"}})
	if v := a.StatusFault.Value(); v != 1 {
		t.Errorf("StatusFault (alarm) = %v, want 1", v)
	}
}
