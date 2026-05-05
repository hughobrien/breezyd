// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"strings"
	"unicode"

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
	displayName := titleCaseName(name)
	info := accessory.Info{
		Name:         displayName,
		SerialNumber: deviceID,
		Manufacturer: "Vents",
		Model:        "Twinfresh Breezy 160",
	}
	base := accessory.New(info, accessory.TypeAirPurifier)

	a := &Accessory{
		A:  base,
		IP: ip,
		// Info.Name keeps the original config-key form so internal callers
		// (metric labels, log lines, the daemon's device map) see what the
		// operator typed. Only HAP-visible labels are title-cased.
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
	attachName(a.AirPurifier.S, displayName)

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

// titleCaseName converts a config-key device name like "playroom" or
// "guest_room" into a display label like "Playroom" or "Guest Room".
// Underscores and hyphens become spaces; the first letter of each word
// is uppercased. Already-capital letters are left alone, so a key like
// "GuestRoom" round-trips unchanged.
func titleCaseName(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	var b strings.Builder
	capNext := true
	for _, r := range s {
		if r == ' ' {
			capNext = true
			b.WriteRune(r)
			continue
		}
		if capNext {
			b.WriteRune(unicode.ToUpper(r))
			capNext = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// attachName adds Name + ConfiguredName characteristics to a service so
// iOS Home shows the supplied label rather than the generic service-type
// fallback ("Switch", "Switch 2", …).
//
// Name (UUID 0x23) is the HAP spec's mandatory identifier when an
// accessory has multiple services of the same type; ConfiguredName
// (UUID 0xE3) is the user-editable label iOS Home actually displays in
// the per-service rows. Setting both means iOS shows the right label by
// default AND lets the user rename in Home settings.
func attachName(s *service.S, name string) {
	n := characteristic.NewName()
	n.SetValue(name)
	s.AddC(n.C)

	cn := characteristic.NewConfiguredName()
	cn.SetValue(name)
	s.AddC(cn.C)
}

func newNamedSwitch(name string) *service.Switch {
	sw := service.NewSwitch()
	attachName(sw.S, name)
	return sw
}

func newNamedTemp(name string) *TemperatureSensor {
	ts := service.NewTemperatureSensor()
	// Expand the default [0, 100] range to [-40, 85] so outdoor and exhaust
	// temperatures below 0°C (common in winter) are not clamped by the library.
	ts.CurrentTemperature.SetMinValue(-40)
	ts.CurrentTemperature.SetMaxValue(85)
	attachName(ts.S, name)
	return &TemperatureSensor{TemperatureSensor: ts, Name: name}
}
