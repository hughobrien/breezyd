// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

// airflowConstant is W per (m³/h × K):
//
//	ρ_air (1.2 kg/m³) × c_p (1005 J/(kg·K)) ÷ 3600 s/h ≈ 0.335
const airflowConstant = 0.335

// CalPoint is one calibration sample from the fan-curve table.
// Pct is the commanded fan percentage (0–100); Cmh is the resulting
// airflow in m³/h; FanW is the per-fan electric draw in watts.
type CalPoint struct {
	Pct  int
	Cmh  float64
	FanW float64
}

// modelCurves maps UnitType (parameter 0x00B9) to an ascending slice of
// calibration points.  Points must be sorted by Pct; the interpolation
// functions rely on that ordering.
var modelCurves = map[uint16][]CalPoint{
	17: { // Twinfresh Elite 160 (Breezy 160)
		{Pct: 10, Cmh: 30, FanW: 2},
		{Pct: 50, Cmh: 100, FanW: 9},
		{Pct: 100, Cmh: 160, FanW: 22},
	},
}

// interp is the shared interpolation kernel.  sel picks the quantity to
// interpolate (Cmh or FanW).  pct is clamped to [first.Pct, last.Pct].
func interp(curve []CalPoint, pct int, sel func(CalPoint) float64) float64 {
	if len(curve) == 0 {
		return 0
	}
	if pct <= curve[0].Pct {
		return sel(curve[0])
	}
	last := curve[len(curve)-1]
	if pct >= last.Pct {
		return sel(last)
	}
	// Find the bracketing interval.
	for i := 1; i < len(curve); i++ {
		lo, hi := curve[i-1], curve[i]
		if pct <= hi.Pct {
			t := float64(pct-lo.Pct) / float64(hi.Pct-lo.Pct)
			return sel(lo) + t*(sel(hi)-sel(lo))
		}
	}
	// Unreachable, but keep the compiler happy.
	return sel(last)
}

// interpolateCmh returns the airflow (m³/h) at pct for the given curve.
func interpolateCmh(curve []CalPoint, pct int) float64 {
	return interp(curve, pct, func(p CalPoint) float64 { return p.Cmh })
}

// interpolateWatts returns the per-fan electric draw (W) at pct.
func interpolateWatts(curve []CalPoint, pct int) float64 {
	return interp(curve, pct, func(p CalPoint) float64 { return p.FanW })
}

// ComputeWatts returns the instantaneous heat-transfer power (W) recovered
// or lost through the HRV at the given fan percentage and supply-air
// temperature delta (°C).  The sign tracks supplyDeltaC: positive means
// the HRV is delivering net heat, negative means net cooling.
//
// Returns (0, false) when unitType is not in modelCurves.
func ComputeWatts(unitType uint16, fanPct int, supplyDeltaC float64) (w float64, supported bool) {
	curve, ok := modelCurves[unitType]
	if !ok {
		return 0, false
	}
	cmh := interpolateCmh(curve, fanPct)
	return cmh * airflowConstant * supplyDeltaC, true
}

// ComputeFanWatts returns the electric draw (W) of one fan at the given
// commanded percentage for the given unit type.
//
// Returns (0, false) when unitType is not in modelCurves.
func ComputeFanWatts(unitType uint16, fanPct int) (w float64, supported bool) {
	curve, ok := modelCurves[unitType]
	if !ok {
		return 0, false
	}
	return interpolateWatts(curve, fanPct), true
}

// EnergyValues holds the computed energy accounting for one device. It is
// populated by the daemon's energy tracker (Task 4) and exposed via the
// status JSON; zero values with Supported=false are safe to serialise when
// the unit type has no calibration data.
type EnergyValues struct {
	Supported           bool    `json:"supported"`
	InstantW            float64 `json:"instant_w"`
	ConsumedW           float64 `json:"consumed_w"`
	HeatingTodayKWh     float64 `json:"heating_today_kwh"`
	CoolingTodayKWh     float64 `json:"cooling_today_kwh"`
	ConsumedTodayKWh    float64 `json:"consumed_today_kwh"`
	HeatingMonthKWh     float64 `json:"heating_month_kwh"`
	CoolingMonthKWh     float64 `json:"cooling_month_kwh"`
	ConsumedMonthKWh    float64 `json:"consumed_month_kwh"`
	HeatingLifetimeKWh  float64 `json:"heating_lifetime_kwh"`
	CoolingLifetimeKWh  float64 `json:"cooling_lifetime_kwh"`
	ConsumedLifetimeKWh float64 `json:"consumed_lifetime_kwh"`
	Error               string  `json:"error,omitempty"`
}
