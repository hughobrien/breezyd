// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"math"
	"testing"

	"github.com/matryer/is"
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
			is := is.New(t)
			gotCmh := interpolateCmh(curve, tc.pct)
			is.True(math.Abs(gotCmh-tc.wantCmh) <= 0.001) // interpolateCmh within tolerance
			gotW := interpolateWatts(curve, tc.pct)
			is.True(math.Abs(gotW-tc.wantW) <= 0.001) // interpolateWatts within tolerance
		})
	}
}

func TestEnergy_ComputeWatts_Breezy160(t *testing.T) {
	is := is.New(t)
	w, ok := ComputeWatts(17, 100, 5)
	is.True(ok)               // expected supported=true for unitType 17
	want := 160.0 * 0.335 * 5 // 268 W
	is.True(math.Abs(w-want) <= 0.5)
}

func TestEnergy_ComputeWatts_NegativeDelta(t *testing.T) {
	is := is.New(t)
	w, ok := ComputeWatts(17, 50, -3)
	is.True(ok)                // expected supported=true for unitType 17
	want := 100.0 * 0.335 * -3 // -100.5 W
	is.True(math.Abs(w-want) <= 0.5)
}

func TestEnergy_ComputeWatts_UnsupportedModel(t *testing.T) {
	is := is.New(t)
	w, ok := ComputeWatts(99, 100, 5)
	is.True(!ok) // expected supported=false for unitType 99
	is.Equal(w, 0.0)
}

func TestEnergy_ComputeWatts_ZeroFan(t *testing.T) {
	is := is.New(t)
	// pct=0 clamps to lowest curve point (30 m³/h)
	w, ok := ComputeWatts(17, 0, 5)
	is.True(ok)              // expected supported=true for unitType 17
	want := 30.0 * 0.335 * 5 // 50.25 W
	is.True(math.Abs(w-want) <= 0.5)
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
		is := is.New(t)
		w, ok := ComputeFanWatts(17, tc.pct)
		is.True(ok) // ComputeFanWatts: expected supported=true
		is.True(math.Abs(w-tc.wantW) <= tc.tol)
	}
}

func TestEnergy_ComputeFanWatts_UnsupportedModel(t *testing.T) {
	is := is.New(t)
	w, ok := ComputeFanWatts(99, 50)
	is.True(!ok) // expected supported=false for unitType 99
	is.Equal(w, 0.0)
}

func TestEnergy_Interpolate_EmptyCurve(t *testing.T) {
	is := is.New(t)
	is.Equal(interpolateCmh(nil, 50), 0.0)
	is.Equal(interpolateWatts([]CalPoint{}, 50), 0.0)
}
