// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"

	"github.com/matryer/is"
)

func TestAirQuality_BoundaryBuckets(t *testing.T) {
	is := is.New(t)
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
		is.Equal(AirQuality(tc.voc), tc.want)
	}
}
