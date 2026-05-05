// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"github.com/brutella/hap/accessory"
	"github.com/brutella/hap/service"
)

// AccessoryInfo holds the identity fields surfaced in the HomeKit
// AccessoryInformation service.
type AccessoryInfo struct {
	Name         string
	SerialNumber string
	Manufacturer string
	Model        string
}

// TemperatureSensor wraps brutella/hap's service.TemperatureSensor
// with a Name field so the four temperature sensors per Breezy can
// be distinguished in the iOS Home app's UI.
type TemperatureSensor struct {
	*service.TemperatureSensor
	Name string
}

// Accessory is the HomeKit representation of one Breezy unit. It
// wraps a brutella/hap *accessory.A and exposes typed handles to
// each service so daemon-side code (cmd/breezyd/homekit.go) can
// register write callbacks and update characteristics directly.
type Accessory struct {
	*accessory.A

	Info AccessoryInfo

	// Control services.
	AirPurifier *service.AirPurifier
	SupplyOnly  *service.Switch
	ExtractOnly *service.Switch

	// Sensor services.
	Humidity      *service.HumiditySensor
	CO2           *service.CarbonDioxideSensor
	AirQualitySvc *service.AirQualitySensor

	// Temperature sensors (four distinct sensors per unit).
	TempOutdoor    *TemperatureSensor
	TempSupply     *TemperatureSensor
	TempExhaustIn  *TemperatureSensor
	TempExhaustOut *TemperatureSensor
}

// NewBreezyAccessory constructs the full per-Breezy accessory tree.
// Call once per configured device; the daemon glue in
// cmd/breezyd/homekit.go registers the resulting accessory with the
// HAP bridge and wires write callbacks.
func NewBreezyAccessory(name, deviceID, ip string) *Accessory {
	info := accessory.Info{
		Name:         name,
		SerialNumber: deviceID,
		Manufacturer: "Vents",
		Model:        "Twinfresh Breezy 160",
	}
	base := accessory.New(info, accessory.TypeAirPurifier)

	a := &Accessory{
		A: base,
		Info: AccessoryInfo{
			Name:         name,
			SerialNumber: deviceID,
			Manufacturer: "Vents",
			Model:        "Twinfresh Breezy 160",
		},
	}

	a.AirPurifier = service.NewAirPurifier()
	a.SupplyOnly = service.NewSwitch()
	a.ExtractOnly = service.NewSwitch()
	a.Humidity = service.NewHumiditySensor()
	a.CO2 = service.NewCarbonDioxideSensor()
	a.AirQualitySvc = service.NewAirQualitySensor()
	a.TempOutdoor = newNamedTemp("Outdoor")
	a.TempSupply = newNamedTemp("Supply")
	a.TempExhaustIn = newNamedTemp("Exhaust In")
	a.TempExhaustOut = newNamedTemp("Exhaust Out")

	// Attach all services to the underlying accessory. AddS takes
	// *service.S; each typed service embeds *S, so we pass the .S field.
	for _, s := range []*service.S{
		a.AirPurifier.S,
		a.SupplyOnly.S,
		a.ExtractOnly.S,
		a.Humidity.S,
		a.CO2.S,
		a.AirQualitySvc.S,
		a.TempOutdoor.S,
		a.TempSupply.S,
		a.TempExhaustIn.S,
		a.TempExhaustOut.S,
	} {
		base.AddS(s)
	}
	return a
}

func newNamedTemp(name string) *TemperatureSensor {
	ts := service.NewTemperatureSensor()
	return &TemperatureSensor{TemperatureSensor: ts, Name: name}
}
