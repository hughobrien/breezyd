// SPDX-License-Identifier: GPL-3.0-or-later

// Package homekit builds and updates HomeKit accessories for the
// breezyd daemon. The package has no daemon dependency: it consumes
// typed inputs (breezy.Status, target setpoints) and produces HAP
// accessory state. Daemon glue lives in cmd/breezyd/homekit.go.
package homekit

import "github.com/brutella/hap/characteristic"

// AirQualityLevel is HomeKit's 5-level air-quality enum plus an
// "unknown" sentinel for missing/invalid input. The integer values
// match brutella/hap's characteristic.AirQuality* constants exactly,
// so an AirQualityLevel can be passed directly to
// characteristic.AirQuality.SetValue(int).
type AirQualityLevel int

const (
	AirQualityUnknown   AirQualityLevel = AirQualityLevel(characteristic.AirQualityUnknown)
	AirQualityExcellent AirQualityLevel = AirQualityLevel(characteristic.AirQualityExcellent)
	AirQualityGood      AirQualityLevel = AirQualityLevel(characteristic.AirQualityGood)
	AirQualityFair      AirQualityLevel = AirQualityLevel(characteristic.AirQualityFair)
	AirQualityInferior  AirQualityLevel = AirQualityLevel(characteristic.AirQualityInferior)
	AirQualityPoor      AirQualityLevel = AirQualityLevel(characteristic.AirQualityPoor)
)

// AirQuality maps a VOC index (0..500 typical, 0..1000 firmware max)
// to HomeKit's 5-level enum at boundaries 0/50/100/150/200. Negative
// input returns Unknown.
func AirQuality(vocIndex int) AirQualityLevel {
	switch {
	case vocIndex < 0:
		return AirQualityUnknown
	case vocIndex <= 50:
		return AirQualityExcellent
	case vocIndex <= 100:
		return AirQualityGood
	case vocIndex <= 150:
		return AirQualityFair
	case vocIndex <= 200:
		return AirQualityInferior
	default:
		return AirQualityPoor
	}
}
