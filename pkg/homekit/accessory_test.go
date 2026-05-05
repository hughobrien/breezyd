// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"
)

func TestNewBreezyAccessory_ServiceShape(t *testing.T) {
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	if a == nil {
		t.Fatal("NewBreezyAccessory returned nil")
	}

	// Identity.
	if a.Info.Name != "playroom" {
		t.Errorf("Name = %q", a.Info.Name)
	}
	if a.Info.SerialNumber != "BREEZY00000000A0" {
		t.Errorf("SerialNumber = %q", a.Info.SerialNumber)
	}
	if a.Info.Manufacturer != "Vents" {
		t.Errorf("Manufacturer = %q", a.Info.Manufacturer)
	}
	if a.Info.Model != "Twinfresh Breezy 160" {
		t.Errorf("Model = %q", a.Info.Model)
	}

	// Required services exist.
	if a.AirPurifier == nil {
		t.Error("AirPurifier service missing")
	}
	if a.SupplyOnly == nil || a.ExtractOnly == nil {
		t.Error("airflow Switch services missing")
	}
	if a.Humidity == nil || a.CO2 == nil || a.AirQualitySvc == nil {
		t.Error("sensor services missing")
	}
	for _, ts := range []*TemperatureSensor{a.TempOutdoor, a.TempSupply, a.TempExhaustIn, a.TempExhaustOut} {
		if ts == nil {
			t.Error("temperature sensor missing")
		}
	}

	// Optional characteristics that Task 2 (sync.go) requires typed handles for.
	if a.RotationSpeed == nil {
		t.Error("RotationSpeed characteristic missing")
	}
	if a.VOCDensity == nil {
		t.Error("VOCDensity characteristic missing")
	}
	if a.CarbonDioxideLevel == nil {
		t.Error("CarbonDioxideLevel characteristic missing")
	}

	// Heater/Night/Turbo control switches.
	if a.Heater == nil || a.Night == nil || a.Turbo == nil {
		t.Error("Heater/Night/Turbo Switch services missing")
	}

	// Filter maintenance + battery service.
	if a.Filter == nil || a.FilterLifeLevel == nil || a.ResetFilter == nil {
		t.Error("FilterMaintenance service or its optional characteristics missing")
	}
	if a.Battery == nil {
		t.Error("BatteryService missing")
	}

	// StatusFault on the AirPurifier.
	if a.StatusFault == nil {
		t.Error("StatusFault characteristic missing")
	}

	// IP must be stored for Task 4's daemon glue.
	if a.IP != "192.168.1.148" {
		t.Errorf("IP = %q, want 192.168.1.148", a.IP)
	}
}

func TestTitleCaseName(t *testing.T) {
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
		if got := titleCaseName(in); got != want {
			t.Errorf("titleCaseName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNewBreezyAccessory_TemperatureSensorNames(t *testing.T) {
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	cases := map[string]*TemperatureSensor{
		"Outdoor":     a.TempOutdoor,
		"Supply":      a.TempSupply,
		"Exhaust In":  a.TempExhaustIn,
		"Exhaust Out": a.TempExhaustOut,
	}
	for want, ts := range cases {
		if ts.Name != want {
			t.Errorf("temp sensor: name = %q, want %q", ts.Name, want)
		}
	}
}
