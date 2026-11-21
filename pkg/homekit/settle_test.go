// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// During the fan-settle window the poller omits the fan RPMs; a powered unit
// must keep its previous CurrentAirPurifierState instead of flipping to Idle.
func TestSync_MissingRPMsKeepPreviousState(t *testing.T) {
	is := is.New(t)
	a := newTestAccessory()
	Sync(a, breezy.Status{
		Configured: map[string]any{"power": true},
		Live:       map[string]any{"fan_supply_rpm": 1500, "fan_extract_rpm": 1450},
	})
	is.Equal(a.AirPurifier.CurrentAirPurifierState.Value(), 2) // purifying

	Sync(a, breezy.Status{
		Configured: map[string]any{"power": true},
		Live:       map[string]any{},
	})
	is.Equal(a.AirPurifier.CurrentAirPurifierState.Value(), 2) // unchanged, not Idle

	// Explicit zero RPMs still mean Idle.
	Sync(a, breezy.Status{
		Configured: map[string]any{"power": true},
		Live:       map[string]any{"fan_supply_rpm": 0, "fan_extract_rpm": 0},
	})
	is.Equal(a.AirPurifier.CurrentAirPurifierState.Value(), 1)
}

// Air-quality enum is derived from the VOC index, which is also withheld
// during the settle window; an absent index must not touch the value.
func TestSync_MissingVOCKeepsPreviousAirQuality(t *testing.T) {
	is := is.New(t)
	a := newTestAccessory()
	Sync(a, breezy.Status{Sensors: map[string]any{"voc_index": 400}})
	want := a.AirQualitySvc.AirQuality.Value()
	Sync(a, breezy.Status{Sensors: map[string]any{}})
	is.Equal(a.AirQualitySvc.AirQuality.Value(), want)
}

// A running timer means the unit is on even when 0x0001 reads 0 (IR-remote
// turbo); HomeKit must agree with the dashboard.
func TestSync_TimerActiveCountsAsPowered(t *testing.T) {
	is := is.New(t)
	a := newTestAccessory()
	values := map[breezy.ParamID][]byte{0x0001: {0}, 0x0007: {2}}
	s := breezy.BuildStatus(values, "x", "id", "ip", nil)
	Sync(a, s)
	is.Equal(a.AirPurifier.Active.Value(), 1)
}

func TestAccessory_SetModel(t *testing.T) {
	is := is.New(t)
	a := newTestAccessory()
	a.SetModel("Breezy 200")
	is.Equal(a.Info.Model, "Breezy 200")
	is.Equal(a.A.Info.Model.Value(), "Breezy 200")
}
