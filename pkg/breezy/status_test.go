// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildStatus_Empty(t *testing.T) {
	s := BuildStatus(map[ParamID][]byte{}, "playroom", "BREEZYID", "192.168.1.1", nil)
	if s.Name != "playroom" || s.ID != "BREEZYID" || s.IP != "192.168.1.1" {
		t.Errorf("identity fields wrong: %+v", s)
	}
	if s.LastPoll != "" {
		t.Errorf("LastPoll should be empty when nil pointer passed, got %q", s.LastPoll)
	}
	if s.Configured == nil || s.Live == nil || s.Sensors == nil || s.Service == nil {
		t.Errorf("blocks must be non-nil maps even when empty")
	}
}

func TestBuildStatus_LastPollRendered(t *testing.T) {
	tt := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	s := BuildStatus(map[ParamID][]byte{}, "n", "i", "ip", &tt)
	if s.LastPoll != "2026-05-04T10:00:00Z" {
		t.Errorf("LastPoll = %q, want 2026-05-04T10:00:00Z", s.LastPoll)
	}
}

func TestBuildStatus_PowerSpeedMode(t *testing.T) {
	values := map[ParamID][]byte{
		0x0001: {1},
		0x0002: {0xFF},
		0x0044: {30},
		0x00B7: {1},
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	if s.Configured["power"] != true {
		t.Errorf("power: want true, got %v", s.Configured["power"])
	}
	if s.Configured["speed_mode"] != "manual" {
		t.Errorf("speed_mode: want manual, got %v", s.Configured["speed_mode"])
	}
	if s.Configured["manual_pct"] != 30 {
		t.Errorf("manual_pct: want 30, got %v", s.Configured["manual_pct"])
	}
	if s.Configured["airflow_mode"] != "regeneration" {
		t.Errorf("airflow_mode: want regeneration, got %v", s.Configured["airflow_mode"])
	}
}

func TestBuildStatus_TempSensorSentinels(t *testing.T) {
	values := map[ParamID][]byte{
		0x001F: {0x00, 0x80},
		0x0020: {0xFF, 0x7F},
		0x0021: {0xC8, 0x00},
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	if _, ok := s.Sensors["temp_outdoor_c"]; ok {
		t.Errorf("temp_outdoor_c should be omitted on sentinel -32768")
	}
	if _, ok := s.Sensors["temp_supply_c"]; ok {
		t.Errorf("temp_supply_c should be omitted on sentinel 32767")
	}
	if v := s.Sensors["temp_exhaust_inlet_c"].(float64); v != 20.0 {
		t.Errorf("temp_exhaust_inlet_c: want 20.0, got %v", v)
	}
}

func TestBuildStatus_FirmwareBlock(t *testing.T) {
	values := map[ParamID][]byte{
		0x0086: {1, 5, 0x0F, 0x05, 0xEA, 0x07},
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	if s.Firmware == nil {
		t.Fatal("Firmware should be set when 0x0086 is 6 bytes")
	}
	if s.Firmware["version"] != "1.05" {
		t.Errorf("version: want 1.05, got %v", s.Firmware["version"])
	}
	if s.Firmware["build_date"] != "2026-05-15" {
		t.Errorf("build_date: want 2026-05-15, got %v", s.Firmware["build_date"])
	}
}

func TestBuildStatus_JSONShape(t *testing.T) {
	s := BuildStatus(map[ParamID][]byte{}, "n", "i", "ip", nil)
	out, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"name"`, `"id"`, `"ip"`, `"configured"`, `"live"`, `"sensors"`, `"service"`} {
		if !strings.Contains(string(out), key) {
			t.Errorf("JSON output missing key %s: %s", key, out)
		}
	}
}

func TestComputeInUserControl(t *testing.T) {
	if !ComputeInUserControl(map[ParamID][]byte{0x0007: {0}}) {
		t.Error("expected true when 0x07=0, no other signals")
	}
	if ComputeInUserControl(map[ParamID][]byte{0x0007: {1}}) {
		t.Error("expected false when 0x07=1 (special mode)")
	}
	if ComputeInUserControl(map[ParamID][]byte{0x0084: {1, 0, 0, 0, 0}}) {
		t.Error("expected false when 0x84 has any non-zero byte")
	}
	if ComputeInUserControl(map[ParamID][]byte{0x030B: {1}}) {
		t.Error("expected false when 0x030B=1 (frost protection)")
	}
}
