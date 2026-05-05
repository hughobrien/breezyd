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
