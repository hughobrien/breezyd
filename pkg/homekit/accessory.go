// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"github.com/brutella/hap/accessory"
	"github.com/brutella/hap/characteristic"
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

	// IP is the device's IPv4 address. Stored here so Task 4's daemon
	// glue can find the device without a separate config map lookup.
	IP string

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

	// Optional characteristics that brutella/hap doesn't include in the
	// service struct by default; we add them here so sync.go (Task 2) and
	// the daemon's write callbacks (Task 4) can reach them via typed
	// handles instead of walking the characteristic list.
	RotationSpeed      *characteristic.RotationSpeed
	VOCDensity         *characteristic.VOCDensity
	CarbonDioxideLevel *characteristic.CarbonDioxideLevel
}

// NewBreezyAccessory constructs the full per-Breezy accessory tree.
// ip is the device's IPv4 address, stored on Accessory.IP for use by
// the daemon glue in cmd/breezyd/homekit.go.
// Call once per configured device; the daemon glue registers the
// resulting accessory with the HAP bridge and wires write callbacks.
func NewBreezyAccessory(name, deviceID, ip string) *Accessory {
	info := accessory.Info{
		Name:         name,
		SerialNumber: deviceID,
		Manufacturer: "Vents",
		Model:        "Twinfresh Breezy 160",
	}
	base := accessory.New(info, accessory.TypeAirPurifier)

	a := &Accessory{
		A:  base,
		IP: ip,
		Info: AccessoryInfo{
			Name:         name,
			SerialNumber: deviceID,
			Manufacturer: "Vents",
			Model:        "Twinfresh Breezy 160",
		},
	}

	// AirPurifier with optional RotationSpeed and Name.
	a.AirPurifier = service.NewAirPurifier()
	a.RotationSpeed = characteristic.NewRotationSpeed()
	a.AirPurifier.AddC(a.RotationSpeed.C)
	attachName(a.AirPurifier.S, name)

	// Switches with Name characteristics so iOS Home can distinguish them.
	a.SupplyOnly = newNamedSwitch("Supply Only")
	a.ExtractOnly = newNamedSwitch("Extract Only")

	a.Humidity = service.NewHumiditySensor()

	// CO2 sensor with optional CarbonDioxideLevel.
	a.CO2 = service.NewCarbonDioxideSensor()
	a.CarbonDioxideLevel = characteristic.NewCarbonDioxideLevel()
	a.CO2.AddC(a.CarbonDioxideLevel.C)

	// AirQuality sensor with optional VOCDensity.
	a.AirQualitySvc = service.NewAirQualitySensor()
	a.VOCDensity = characteristic.NewVOCDensity()
	a.AirQualitySvc.AddC(a.VOCDensity.C)

	// Temperature sensors with Name characteristics so iOS Home shows labels.
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

// attachName adds the optional Name characteristic to a service so the
// iOS Home app shows the supplied label rather than an unnamed tile.
func attachName(s *service.S, name string) {
	n := characteristic.NewName()
	n.SetValue(name)
	s.AddC(n.C)
}

func newNamedSwitch(name string) *service.Switch {
	sw := service.NewSwitch()
	attachName(sw.S, name)
	return sw
}

func newNamedTemp(name string) *TemperatureSensor {
	ts := service.NewTemperatureSensor()
	attachName(ts.S, name)
	return &TemperatureSensor{TemperatureSensor: ts, Name: name}
}
