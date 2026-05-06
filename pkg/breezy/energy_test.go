// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"math"
	"testing"
)

// seed curve used across energy tests — UnitType 17 (Breezy 160).
var breezy160Curve = modelCurves[17]

func TestEnergy_Interpolate(t *testing.T) {
	curve := breezy160Curve

	tests := []struct {
		name    string
		pct     int
		wantCmh float64
		wantW   float64
	}{
		{"below first (clamp)", 0, 30, 2},
		{"exact first", 10, 30, 2},
		{"halfway 10..50", 30, 65, 5.5},
		{"exact last", 100, 160, 22},
		{"above last (clamp)", 110, 160, 22},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotCmh := interpolateCmh(curve, tc.pct)
			if math.Abs(gotCmh-tc.wantCmh) > 0.001 {
				t.Errorf("interpolateCmh(%d) = %g, want %g", tc.pct, gotCmh, tc.wantCmh)
			}
			gotW := interpolateWatts(curve, tc.pct)
			if math.Abs(gotW-tc.wantW) > 0.001 {
				t.Errorf("interpolateWatts(%d) = %g, want %g", tc.pct, gotW, tc.wantW)
			}
		})
	}
}

func TestEnergy_ComputeWatts_Breezy160(t *testing.T) {
	w, ok := ComputeWatts(17, 100, 5)
	if !ok {
		t.Fatal("expected supported=true for unitType 17")
	}
	want := 160.0 * 0.335 * 5 // 268 W
	if math.Abs(w-want) > 0.5 {
		t.Errorf("ComputeWatts(17,100,5) = %g, want ≈%g", w, want)
	}
}

func TestEnergy_ComputeWatts_NegativeDelta(t *testing.T) {
	w, ok := ComputeWatts(17, 50, -3)
	if !ok {
		t.Fatal("expected supported=true for unitType 17")
	}
	want := 100.0 * 0.335 * -3 // -100.5 W
	if math.Abs(w-want) > 0.5 {
		t.Errorf("ComputeWatts(17,50,-3) = %g, want ≈%g", w, want)
	}
}

func TestEnergy_ComputeWatts_UnsupportedModel(t *testing.T) {
	w, ok := ComputeWatts(99, 100, 5)
	if ok {
		t.Errorf("expected supported=false for unitType 99, got true")
	}
	if w != 0 {
		t.Errorf("expected w=0 for unsupported model, got %g", w)
	}
}

func TestEnergy_ComputeWatts_ZeroFan(t *testing.T) {
	// pct=0 clamps to lowest curve point (30 m³/h)
	w, ok := ComputeWatts(17, 0, 5)
	if !ok {
		t.Fatal("expected supported=true for unitType 17")
	}
	want := 30.0 * 0.335 * 5 // 50.25 W
	if math.Abs(w-want) > 0.5 {
		t.Errorf("ComputeWatts(17,0,5) = %g, want ≈%g", w, want)
	}
}

func TestEnergy_ComputeFanWatts(t *testing.T) {
	tests := []struct {
		pct   int
		wantW float64
		tol   float64
	}{
		{10, 2, 0.001},
		{50, 9, 0.001},
		{100, 22, 0.001},
		{75, 15.5, 0.001}, // halfway between (50,9) and (100,22)
	}

	for _, tc := range tests {
		w, ok := ComputeFanWatts(17, tc.pct)
		if !ok {
			t.Errorf("ComputeFanWatts(17,%d): expected supported=true", tc.pct)
			continue
		}
		if math.Abs(w-tc.wantW) > tc.tol {
			t.Errorf("ComputeFanWatts(17,%d) = %g, want %g", tc.pct, w, tc.wantW)
		}
	}
}

func TestEnergy_ComputeFanWatts_UnsupportedModel(t *testing.T) {
	w, ok := ComputeFanWatts(99, 50)
	if ok {
		t.Errorf("expected supported=false for unitType 99, got true")
	}
	if w != 0 {
		t.Errorf("expected w=0 for unsupported model, got %g", w)
	}
}

func TestEnergy_Interpolate_EmptyCurve(t *testing.T) {
	if got := interpolateCmh(nil, 50); got != 0 {
		t.Errorf("interpolateCmh(nil, 50) = %v, want 0", got)
	}
	if got := interpolateWatts([]CalPoint{}, 50); got != 0 {
		t.Errorf("interpolateWatts(empty, 50) = %v, want 0", got)
	}
}
