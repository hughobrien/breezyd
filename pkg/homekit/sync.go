// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import "github.com/hughobrien/breezyd/pkg/breezy"

// Sync writes the latest values from a breezy.Status snapshot into the
// accessory's characteristics. Missing fields in s are left untouched —
// no SetValue is called, so the characteristic retains whatever value it
// held before. Temperature sentinels (|v| ≥ 1000) are similarly skipped.
//
// Sync is a pure function: no I/O, no goroutine creation. It is called
// from cmd/breezyd/homekit.go after every successful poll. It is safe to
// call concurrently against different accessories, but callers must
// serialise concurrent calls against the same accessory.
func Sync(a *Accessory, s breezy.Status) {
	syncAirPurifier(a, s)
	syncAirflowSwitches(a, s)
	syncSensors(a, s)
}

func syncAirPurifier(a *Accessory, s breezy.Status) {
	power, powerOK := boolField(s.Configured, "power")
	if powerOK {
		val := 0
		if power {
			val = 1
		}
		a.AirPurifier.Active.SetValue(val) //nolint:errcheck

		// CurrentAirPurifierState is gated on the same field — without
		// it we can't honestly say "Inactive", we just don't know.
		//   0 = Inactive (not powered)
		//   1 = Idle     (powered but no fan motion)
		//   2 = Purifying (fan is moving air)
		supplyRPM, _ := intField(s.Live, "fan_supply_rpm")
		extractRPM, _ := intField(s.Live, "fan_extract_rpm")
		switch {
		case !power:
			a.AirPurifier.CurrentAirPurifierState.SetValue(0) //nolint:errcheck
		case supplyRPM > 0 || extractRPM > 0:
			a.AirPurifier.CurrentAirPurifierState.SetValue(2) //nolint:errcheck
		default:
			a.AirPurifier.CurrentAirPurifierState.SetValue(1) //nolint:errcheck
		}
	}

	if pct, ok := intField(s.Configured, "manual_pct"); ok {
		a.RotationSpeed.SetValue(float64(pct))
	}

	if mode, ok := s.Configured["speed_mode"].(string); ok {
		target := 1 // Auto (preset1/2/3)
		if mode == "manual" {
			target = 0 // Manual
		}
		a.AirPurifier.TargetAirPurifierState.SetValue(target) //nolint:errcheck
	}
}

func syncAirflowSwitches(a *Accessory, s breezy.Status) {
	mode, ok := s.Configured["airflow_mode"].(string)
	if !ok {
		return
	}
	a.SupplyOnly.On.SetValue(mode == "supply")
	a.ExtractOnly.On.SetValue(mode == "extract")
}

func syncSensors(a *Accessory, s breezy.Status) {
	// Humidity.
	if v, ok := floatField(s.Sensors, "humidity_pct"); ok {
		a.Humidity.CurrentRelativeHumidity.SetValue(v)
	}

	// CO2: level + threshold-triggered detection.
	co2ppm, hasCO2 := floatField(s.Sensors, "eco2_ppm")
	if hasCO2 {
		a.CarbonDioxideLevel.SetValue(co2ppm)
	}
	co2thresh, hasThresh := floatField(s.Configured, "co2_threshold_ppm")
	if hasCO2 && hasThresh {
		detected := 0
		if co2ppm > co2thresh {
			detected = 1
		}
		a.CO2.CarbonDioxideDetected.SetValue(detected) //nolint:errcheck
	} else if hasCO2 {
		// Threshold not configured; use normal (not-detected) by default.
		a.CO2.CarbonDioxideDetected.SetValue(0) //nolint:errcheck
	}

	// VOC: raw density + AirQuality enum.
	if vocIdx, ok := intField(s.Sensors, "voc_index"); ok {
		a.VOCDensity.SetValue(float64(vocIdx))
		a.AirQualitySvc.AirQuality.SetValue(int(AirQuality(vocIdx))) //nolint:errcheck
	}

	// Temperature sensors: skip missing keys and sentinels (|v| ≥ 1000).
	tempPairs := []struct {
		key string
		ts  *TemperatureSensor
	}{
		{"temp_outdoor_c", a.TempOutdoor},
		{"temp_supply_c", a.TempSupply},
		{"temp_exhaust_inlet_c", a.TempExhaustIn},
		{"temp_exhaust_outlet_c", a.TempExhaustOut},
	}
	for _, p := range tempPairs {
		v, ok := floatField(s.Sensors, p.key)
		if !ok {
			continue
		}
		if v >= 1000.0 || v <= -1000.0 {
			continue // sentinel: no-sensor or short-circuit
		}
		p.ts.CurrentTemperature.SetValue(v)
	}
}

// boolField extracts a boolean from a map[string]any. It handles the native
// bool type only; non-bool values are treated as absent.
func boolField(m map[string]any, key string) (bool, bool) {
	if m == nil {
		return false, false
	}
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// intField extracts an integer from a map[string]any, handling both int
// (returned by BuildStatus) and float64 (JSON-decoded form).
func intField(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case int64:
		return int(n), true
	}
	return 0, false
}

// floatField extracts a float64 from a map[string]any, handling int, int64,
// and float64 source values (BuildStatus emits int for integers, JSON decoding
// emits float64).
func floatField(m map[string]any, key string) (float64, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
