// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"

	"github.com/matryer/is"
)

func TestNewBreezyAccessory_ServiceShape(t *testing.T) {
	is := is.New(t)
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	is.True(a != nil) // NewBreezyAccessory returned nil

	// Identity.
	is.Equal(a.Info.Name, "playroom")
	is.Equal(a.Info.SerialNumber, "BREEZY00000000A0")
	is.Equal(a.Info.Manufacturer, "Vents")
	is.Equal(a.Info.Model, "Twinfresh Breezy 160")

	// Required services exist.
	is.True(a.AirPurifier != nil)                                        // AirPurifier service missing
	is.True(a.SupplyOnly != nil && a.ExtractOnly != nil)                 // airflow Switch services missing
	is.True(a.Humidity != nil && a.CO2 != nil && a.AirQualitySvc != nil) // sensor services missing
	for _, ts := range []*TemperatureSensor{a.TempOutdoor, a.TempSupply, a.TempExhaustIn, a.TempExhaustOut} {
		is.True(ts != nil) // temperature sensor missing
	}

	// Optional characteristics that Task 2 (sync.go) requires typed handles for.
	is.True(a.RotationSpeed != nil)      // RotationSpeed characteristic missing
	is.True(a.VOCDensity != nil)         // VOCDensity characteristic missing
	is.True(a.CarbonDioxideLevel != nil) // CarbonDioxideLevel characteristic missing

	// Heater/Night/Turbo control switches.
	is.True(a.Heater != nil && a.Night != nil && a.Turbo != nil) // Heater/Night/Turbo Switch services missing

	// Filter maintenance + battery service.
	is.True(a.Filter != nil && a.FilterLifeLevel != nil && a.ResetFilter != nil) // FilterMaintenance service or its optional characteristics missing
	is.True(a.Battery != nil)                                                    // BatteryService missing

	// StatusFault on the AirPurifier.
	is.True(a.StatusFault != nil) // StatusFault characteristic missing

	// IP must be stored for Task 4's daemon glue.
	is.Equal(a.IP, "192.168.1.148")
}

func TestTitleCaseName(t *testing.T) {
	is := is.New(t)
	cases := map[string]string{
		"playroom":   "Playroom",
		"bedroom":    "Bedroom",
		"office":     "Office",
		"guest_room": "Guest Room",
		"upper-deck": "Upper Deck",
		"GuestRoom":  "GuestRoom", // existing capital preserved
		"":           "",
	}
	for in, want := range cases {
		is.Equal(titleCaseName(in), want)
	}
}

func TestNewBreezyAccessory_TemperatureSensorNames(t *testing.T) {
	is := is.New(t)
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	cases := map[string]*TemperatureSensor{
		"Outdoor":     a.TempOutdoor,
		"Supply":      a.TempSupply,
		"Exhaust In":  a.TempExhaustIn,
		"Exhaust Out": a.TempExhaustOut,
	}
	for want, ts := range cases {
		is.Equal(ts.Name, want)
	}
}
