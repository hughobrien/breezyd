// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// newTestAccessory is a shorthand for the standard test accessory.
func newTestAccessory() *Accessory {
	return NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
}

// TestSync_PowerAndSpeed covers Active, RotationSpeed, TargetAirPurifierState,
// and CurrentAirPurifierState (Purifying path).
func TestSync_PowerAndSpeed(t *testing.T) {
	is := is.New(t)
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

	is.Equal(a.AirPurifier.Active.Value(), 1)                  // Active should be 1 (powered)
	is.Equal(a.RotationSpeed.Value(), 30.0)                    // RotationSpeed should be 30
	is.Equal(a.AirPurifier.TargetAirPurifierState.Value(), 0)  // manual → Manual (0)
	is.Equal(a.AirPurifier.CurrentAirPurifierState.Value(), 2) // fans running → Purifying (2)
}

// TestSync_PowerOff verifies Inactive state and Active=0 when power is false.
func TestSync_PowerOff(t *testing.T) {
	is := is.New(t)
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

	is.Equal(a.AirPurifier.Active.Value(), 0)                  // Active should be 0 (off)
	is.Equal(a.AirPurifier.TargetAirPurifierState.Value(), 1)  // preset1 → Auto (1)
	is.Equal(a.AirPurifier.CurrentAirPurifierState.Value(), 0) // not powered → Inactive (0)
}

// TestSync_IdleWhenPoweredNoFan covers the Idle path (powered, zero RPM).
func TestSync_IdleWhenPoweredNoFan(t *testing.T) {
	is := is.New(t)
	a := newTestAccessory()
	s := breezy.Status{
		Configured: map[string]any{"power": true},
		Live: map[string]any{
			"fan_supply_rpm":  0,
			"fan_extract_rpm": 0,
		},
	}
	Sync(a, s)

	is.Equal(a.AirPurifier.CurrentAirPurifierState.Value(), 1) // powered, no fan → Idle (1)
}

// TestSync_PowerFieldAbsentLeavesStateUntouched protects the Sync
// contract: if the power field is missing from a partial snapshot,
// CurrentAirPurifierState must NOT be written. Otherwise a snapshot
// that happened to read fan RPMs but skipped 0x01 would falsely
// flip the iOS Home tile to Inactive.
func TestSync_PowerFieldAbsentLeavesStateUntouched(t *testing.T) {
	is := is.New(t)
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

	is.Equal(a.AirPurifier.CurrentAirPurifierState.Value(), 2) // untouched, was set to Purifying
}

// TestSync_AutoModes verifies preset2/preset3 all map to Auto.
func TestSync_AutoModes(t *testing.T) {
	is := is.New(t)
	for _, mode := range []string{"preset1", "preset2", "preset3"} {
		a := newTestAccessory()
		Sync(a, breezy.Status{
			Configured: map[string]any{
				"power":      true,
				"speed_mode": mode,
			},
		})
		is.Equal(a.AirPurifier.TargetAirPurifierState.Value(), 1) // preset modes → Auto (1)
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
			is := is.New(t)
			a := newTestAccessory()
			Sync(a, breezy.Status{Configured: map[string]any{"airflow_mode": tc.mode}})
			is.Equal(a.SupplyOnly.On.Value(), tc.supply)
			is.Equal(a.ExtractOnly.On.Value(), tc.extract)
		})
	}
}

// TestSync_Humidity covers CurrentRelativeHumidity.
func TestSync_Humidity(t *testing.T) {
	is := is.New(t)
	a := newTestAccessory()
	Sync(a, breezy.Status{Sensors: map[string]any{"humidity_pct": 65}})
	is.Equal(a.Humidity.CurrentRelativeHumidity.Value(), 65.0)
}

// TestSync_CO2Detection covers CarbonDioxideLevel and above/below-threshold detection.
func TestSync_CO2Detection(t *testing.T) {
	is := is.New(t)
	// Above threshold → Detected = 1.
	a := newTestAccessory()
	Sync(a, breezy.Status{
		Configured: map[string]any{"co2_threshold_ppm": 1000},
		Sensors:    map[string]any{"eco2_ppm": 1200},
	})
	is.Equal(a.CarbonDioxideLevel.Value(), 1200.0)
	is.Equal(a.CO2.CarbonDioxideDetected.Value(), 1) // above threshold

	// Below threshold → Detected = 0.
	a2 := newTestAccessory()
	Sync(a2, breezy.Status{
		Configured: map[string]any{"co2_threshold_ppm": 1000},
		Sensors:    map[string]any{"eco2_ppm": 800},
	})
	is.Equal(a2.CO2.CarbonDioxideDetected.Value(), 0) // below threshold
}

// TestSync_VOCToAirQuality covers VOCDensity and the AirQuality enum bucket.
func TestSync_VOCToAirQuality(t *testing.T) {
	is := is.New(t)
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
		is.Equal(a.VOCDensity.Value(), float64(tc.voc))
		is.Equal(a.AirQualitySvc.AirQuality.Value(), int(tc.wantEnum))
	}
}

// TestSync_TemperatureSentinelsSkipped verifies that implausible temperature
// values (|v| ≥ 1000) are not written to the characteristic.
func TestSync_TemperatureSentinelsSkipped(t *testing.T) {
	is := is.New(t)
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

	is.Equal(a.TempOutdoor.CurrentTemperature.Value(), 12.5)
	is.Equal(a.TempSupply.CurrentTemperature.Value(), 18.5) // sentinel, untouched
}

// TestSync_TemperatureValid confirms all four sensors are written when present
// and valid.
func TestSync_TemperatureValid(t *testing.T) {
	is := is.New(t)
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
		is.Equal(got[k], wv)
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
	is := is.New(t)
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

	is.Equal(a.RotationSpeed.Value(), 45.0)                           // RotationSpeed via float64 input
	is.Equal(a.CO2.CarbonDioxideDetected.Value(), 1)                  // 1500 > 1000
	is.Equal(a.AirQualitySvc.AirQuality.Value(), int(AirQualityGood)) // voc=80 → Good
}

func TestSync_RotationSpeedPrefersLiveFanPct(t *testing.T) {
	is := is.New(t)
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
	is.Equal(a.RotationSpeed.Value(), 60.0) // prefers live.fan_supply_pct
}

func TestSync_RotationSpeedFallsBackToManualPct(t *testing.T) {
	is := is.New(t)
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
	is.Equal(a.RotationSpeed.Value(), 35.0) // falls back to manual_pct
}

func TestSync_HeaterSwitch(t *testing.T) {
	is := is.New(t)
	a := newTestAccessory()
	Sync(a, breezy.Status{Configured: map[string]any{"heater_enabled": true}})
	is.True(a.Heater.On.Value()) // Heater.On should be true
	Sync(a, breezy.Status{Configured: map[string]any{"heater_enabled": false}})
	is.True(!a.Heater.On.Value()) // Heater.On should be false
}

func TestSync_TimerSwitches(t *testing.T) {
	is := is.New(t)
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
		is.Equal(a.Night.On.Value(), c.night)
		is.Equal(a.Turbo.On.Value(), c.turbo)
	}
}

func TestSync_FilterMaintenance(t *testing.T) {
	is := is.New(t)
	a := newTestAccessory()

	// Clean filter, 30 of 90 days remaining.
	Sync(a, breezy.Status{Service: map[string]any{
		"filter_status":            "clean",
		"filter_remaining_seconds": 30 * 86400,
		"filter_total_seconds":     90 * 86400,
	}})
	is.Equal(a.Filter.FilterChangeIndication.Value(), 0) // clean
	is.Equal(a.FilterLifeLevel.Value(), 33.0)

	// Soiled filter.
	Sync(a, breezy.Status{Service: map[string]any{"filter_status": "soiled"}})
	is.Equal(a.Filter.FilterChangeIndication.Value(), 1) // soiled
}

func TestSync_BatteryLevelFromVolts(t *testing.T) {
	is := is.New(t)
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
		is.Equal(a.Battery.BatteryLevel.Value(), c.pct)
		is.Equal(a.Battery.StatusLowBattery.Value(), c.low)
	}
}

func TestSync_StatusFault(t *testing.T) {
	is := is.New(t)
	a := newTestAccessory()
	Sync(a, breezy.Status{Service: map[string]any{"fault_level": "none"}})
	is.Equal(a.StatusFault.Value(), 0)
	Sync(a, breezy.Status{Service: map[string]any{"fault_level": "alarm"}})
	is.Equal(a.StatusFault.Value(), 1)
}
