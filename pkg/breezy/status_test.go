// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

func TestBuildStatus_Empty(t *testing.T) {
	is := is.New(t)
	s := BuildStatus(map[ParamID][]byte{}, "playroom", "BREEZYID", "192.168.1.1", nil)
	is.Equal(s.Name, "playroom")
	is.Equal(s.ID, "BREEZYID")
	is.Equal(s.IP, "192.168.1.1")
	is.Equal(s.LastPoll, "")     // LastPoll empty when nil pointer passed
	is.True(s.Configured != nil) // blocks must be non-nil maps even when empty
	is.True(s.Live != nil)
	is.True(s.Sensors != nil)
	is.True(s.Service != nil)
}

func TestBuildStatus_LastPollRendered(t *testing.T) {
	is := is.New(t)
	tt := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	s := BuildStatus(map[ParamID][]byte{}, "n", "i", "ip", &tt)
	is.Equal(s.LastPoll, "2026-05-04T10:00:00Z")
}

func TestBuildStatus_PowerSpeedMode(t *testing.T) {
	is := is.New(t)
	values := map[ParamID][]byte{
		0x0001: {1},
		0x0002: {0xFF},
		0x0044: {30},
		0x00B7: {1},
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	is.Equal(s.Configured["power"], true)
	is.Equal(s.Configured["speed_mode"], "manual")
	is.Equal(s.Configured["manual_pct"], 30)
	is.Equal(s.Configured["airflow_mode"], "regeneration")
}

func TestBuildStatus_FanPct_Manual(t *testing.T) {
	is := is.New(t)
	values := map[ParamID][]byte{
		0x0002: {0xFF},
		0x0044: {30},
		0x004A: {0xD4, 0x14}, // 5332 rpm
		0x004B: {0xE0, 0x14}, // 5344 rpm
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	is.Equal(s.Live["fan_supply_pct"], 30)
	is.Equal(s.Live["fan_extract_pct"], 30)
}

func TestBuildStatus_FanPct_Preset(t *testing.T) {
	is := is.New(t)
	values := map[ParamID][]byte{
		0x0002: {2},          // preset 2
		0x003C: {55},         // preset2_supply_pct
		0x003D: {60},         // preset2_extract_pct
		0x004A: {0x10, 0x0E}, // 3600 rpm
		0x004B: {0x40, 0x0E}, // 3648 rpm
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	is.Equal(s.Live["fan_supply_pct"], 55)  // preset2_supply_pct
	is.Equal(s.Live["fan_extract_pct"], 60) // preset2_extract_pct
}

func TestBuildStatus_FanPct_RPMZeroForcesPctZero(t *testing.T) {
	is := is.New(t)
	values := map[ParamID][]byte{
		0x0002: {0xFF}, // manual
		0x0044: {50},   // commanded 50%
		0x004A: {0, 0}, // supply 0 rpm (e.g. extract-only mode)
		0x004B: {0xD4, 0x14},
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	is.Equal(s.Live["fan_supply_pct"], 0) // 0 rpm forces pct 0
	is.Equal(s.Live["fan_extract_pct"], 50)
}

func TestBuildStatus_FanPct_MissingSpeedMode(t *testing.T) {
	is := is.New(t)
	// No 0x0002 → can't determine commanded pct → field omitted entirely.
	values := map[ParamID][]byte{
		0x004A: {0xD4, 0x14},
		0x004B: {0xE0, 0x14},
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	_, ok := s.Live["fan_supply_pct"]
	is.True(!ok) // fan_supply_pct should be absent when speed_mode unknown
	_, ok = s.Live["fan_extract_pct"]
	is.True(!ok) // fan_extract_pct should be absent when speed_mode unknown
}

func TestBuildStatus_TempSensorSentinels(t *testing.T) {
	is := is.New(t)
	values := map[ParamID][]byte{
		0x001F: {0x00, 0x80},
		0x0020: {0xFF, 0x7F},
		0x0021: {0xC8, 0x00},
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	_, ok := s.Sensors["temp_outdoor_c"]
	is.True(!ok) // temp_outdoor_c should be omitted on sentinel -32768
	_, ok = s.Sensors["temp_supply_c"]
	is.True(!ok) // temp_supply_c should be omitted on sentinel 32767
	is.Equal(s.Sensors["temp_exhaust_inlet_c"].(float64), 20.0)
}

func TestBuildStatus_PresetSpeeds(t *testing.T) {
	values := map[ParamID][]byte{
		0x003A: {30}, 0x003B: {35}, // preset 1
		0x003C: {55}, 0x003D: {60}, // preset 2
		0x003E: {100}, 0x003F: {100}, // preset 3
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	for i, want := range []map[string]any{
		{"supply": 30, "extract": 35},
		{"supply": 55, "extract": 60},
		{"supply": 100, "extract": 100},
	} {
		is := is.New(t)
		key := fmt.Sprintf("preset%d", i+1)
		got, ok := s.Configured[key].(map[string]any)
		is.True(ok) // preset key present and right type
		for k, v := range want {
			is.Equal(got[k], v)
		}
	}
}

func TestBuildStatus_FilterTotalSeconds(t *testing.T) {
	is := is.New(t)
	values := map[ParamID][]byte{
		0x0063: {90, 0},       // 90 days
		0x0064: {0, 0, 30, 0}, // 30 days remaining
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	is.Equal(s.Service["filter_total_seconds"], 90*86400)
	is.Equal(s.Service["filter_remaining_seconds"], 30*86400)
}

func TestBuildStatus_FirmwareBlock(t *testing.T) {
	is := is.New(t)
	values := map[ParamID][]byte{
		0x0086: {1, 5, 0x0F, 0x05, 0xEA, 0x07},
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	is.True(s.Firmware != nil) // Firmware should be set when 0x0086 is 6 bytes
	is.Equal(s.Firmware["version"], "1.05")
	is.Equal(s.Firmware["build_date"], "2026-05-15")
}

func TestBuildStatus_JSONShape(t *testing.T) {
	is := is.New(t)
	s := BuildStatus(map[ParamID][]byte{}, "n", "i", "ip", nil)
	out, err := json.Marshal(s)
	is.NoErr(err)
	for _, key := range []string{`"name"`, `"id"`, `"ip"`, `"configured"`, `"live"`, `"sensors"`, `"service"`} {
		is.True(strings.Contains(string(out), key)) // JSON output contains key
	}
}

func TestBuildStatus_SensorEnabledFlags(t *testing.T) {
	is := is.New(t)
	values := map[ParamID][]byte{
		0x000F: {1}, // humidity sensor enabled
		0x0011: {0}, // co2 sensor disabled
		0x0315: {1}, // voc sensor enabled
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	is.Equal(s.Configured["humidity_sensor_enabled"], true)
	is.Equal(s.Configured["co2_sensor_enabled"], false)
	is.Equal(s.Configured["voc_sensor_enabled"], true)
}

func TestComputeInUserControl(t *testing.T) {
	is := is.New(t)
	is.True(ComputeInUserControl(map[ParamID][]byte{0x0007: {0}}))              // true when 0x07=0, no other signals
	is.True(!ComputeInUserControl(map[ParamID][]byte{0x0007: {1}}))             // false when 0x07=1 (special mode)
	is.True(!ComputeInUserControl(map[ParamID][]byte{0x0084: {1, 0, 0, 0, 0}})) // false when 0x84 has any non-zero byte
	is.True(!ComputeInUserControl(map[ParamID][]byte{0x030B: {1}}))             // false when 0x030B=1 (frost protection)
}

func TestBuildStatusWithEnergy_NilEnergy(t *testing.T) {
	is := is.New(t)
	values := map[ParamID][]byte{
		0x0001: {1},
		0x0002: {0xFF},
	}
	base := BuildStatus(values, "attic", "ID001", "10.0.0.1", nil)
	withNil := BuildStatusWithEnergy(values, "attic", "ID001", "10.0.0.1", nil, nil)
	is.True(reflect.DeepEqual(base, withNil)) // BuildStatusWithEnergy(nil) should equal BuildStatus
}

func TestBuildStatusWithEnergy_PopulatedEnergy(t *testing.T) {
	is := is.New(t)
	energy := &EnergyValues{
		Supported:           true,
		InstantW:            245,
		ConsumedW:           18,
		HeatingTodayKWh:     1.234,
		CoolingTodayKWh:     0,
		ConsumedTodayKWh:    0.045,
		HeatingLifetimeKWh:  234.5,
		CoolingLifetimeKWh:  0,
		ConsumedLifetimeKWh: 12.3,
	}
	s := BuildStatusWithEnergy(map[ParamID][]byte{}, "hall", "ID002", "10.0.0.2", nil, energy)
	raw, ok := s.Service["energy"]
	is.True(ok) // s.Service["energy"] missing
	got, ok := raw.(EnergyValues)
	is.True(ok) // s.Service["energy"] should be EnergyValues
	is.Equal(got.Supported, true)
	is.Equal(got.InstantW, 245.0)
	is.Equal(got.HeatingTodayKWh, 1.234)
}

func TestBuildStatusWithEnergy_ErrorOnUnsupportedModel(t *testing.T) {
	is := is.New(t)
	energy := &EnergyValues{
		Supported: false,
		Error:     "unsupported model: Breezy 200 (type=22) — no airflow calibration",
	}
	s := BuildStatusWithEnergy(map[ParamID][]byte{}, "lounge", "ID003", "10.0.0.3", nil, energy)
	raw, ok := s.Service["energy"]
	is.True(ok) // s.Service["energy"] missing
	got, ok := raw.(EnergyValues)
	is.True(ok) // s.Service["energy"] should be EnergyValues
	is.Equal(got.Supported, false)
	is.True(got.Error != "") // Error should be non-empty
}

func TestBuildStatusWithEnergy_JSONShape(t *testing.T) {
	is := is.New(t)
	energy := &EnergyValues{
		Supported:          true,
		InstantW:           245,
		ConsumedW:          18,
		HeatingTodayKWh:    1.234,
		HeatingLifetimeKWh: 234.5,
	}
	s := BuildStatusWithEnergy(map[ParamID][]byte{}, "study", "ID004", "10.0.0.4", nil, energy)
	out, err := json.Marshal(s)
	is.NoErr(err)
	js := string(out)
	for _, needle := range []string{
		`"energy"`,
		`"instant_w":245`,
		`"heating_today_kwh":1.234`,
		`"heating_lifetime_kwh":234.5`,
		`"supported":true`,
	} {
		is.True(strings.Contains(js, needle)) // JSON output contains needle
	}
}
