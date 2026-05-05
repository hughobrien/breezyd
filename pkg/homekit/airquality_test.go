// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"
)

func TestAirQuality_BoundaryBuckets(t *testing.T) {
	cases := []struct {
		voc  int
		want AirQualityLevel
	}{
		{-1, AirQualityUnknown},
		{0, AirQualityExcellent},
		{50, AirQualityExcellent},
		{51, AirQualityGood},
		{100, AirQualityGood},
		{101, AirQualityFair},
		{150, AirQualityFair},
		{151, AirQualityInferior},
		{200, AirQualityInferior},
		{201, AirQualityPoor},
		{500, AirQualityPoor},
		{10000, AirQualityPoor},
	}
	for _, tc := range cases {
		if got := AirQuality(tc.voc); got != tc.want {
			t.Errorf("AirQuality(%d) = %v, want %v", tc.voc, got, tc.want)
		}
	}
}
