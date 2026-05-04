// SPDX-License-Identifier: GPL-3.0-or-later

// Structured snapshot construction for the HTTP API. The decode helpers
// (uint8At/uint16At/int16At) live in decode.go and are shared with the
// metrics exporter; everything that's only used to build the per-device
// JSON snapshot lives here.
package main

import (
	"fmt"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// SnapshotResponse mirrors the spec's example JSON. Each block is a
// map[string]any so we don't have to enumerate every possible field
// statically — empty/missing values just don't appear.
type SnapshotResponse struct {
	Name       string         `json:"name"`
	ID         string         `json:"id"`
	IP         string         `json:"ip"`
	LastPoll   string         `json:"last_poll,omitempty"`
	Configured map[string]any `json:"configured"`
	Live       map[string]any `json:"live"`
	Sensors    map[string]any `json:"sensors"`
	Service    map[string]any `json:"service"`
	Firmware   map[string]any `json:"firmware,omitempty"`
}

// buildSnapshot decodes the cached parameter bytes into the spec's
// structured shape. Decode failures fall back to a zero/missing value
// rather than 500 — an unfamiliar device firmware shouldn't take the
// whole API down.
func (h *Handler) buildSnapshot(name string, cfg DeviceConfig, snap Snapshot) SnapshotResponse {
	resp := SnapshotResponse{
		Name:       name,
		ID:         cfg.ID,
		IP:         cfg.IP,
		Configured: map[string]any{},
		Live:       map[string]any{},
		Sensors:    map[string]any{},
		Service:    map[string]any{},
	}
	if !snap.LastPoll.IsZero() {
		resp.LastPoll = snap.LastPoll.UTC().Format(time.RFC3339)
	}
	if snap.IP != "" {
		resp.IP = snap.IP
	}

	// Configured: what the user set.
	if b, ok := uint8At(snap, 0x0001); ok {
		resp.Configured["power"] = b == 1
	}
	if b, ok := uint8At(snap, 0x0002); ok {
		switch b {
		case 0xFF:
			resp.Configured["speed_mode"] = "manual"
		case 1, 2, 3:
			resp.Configured["speed_mode"] = fmt.Sprintf("preset%d", b)
		default:
			resp.Configured["speed_mode"] = fmt.Sprintf("unknown(%d)", b)
		}
	}
	if b, ok := uint8At(snap, 0x0044); ok {
		resp.Configured["manual_pct"] = int(b)
	}
	if b, ok := uint8At(snap, 0x00B7); ok {
		resp.Configured["airflow_mode"] = airflowModeName(b)
	}
	if b, ok := uint8At(snap, 0x0068); ok {
		resp.Configured["heater_enabled"] = b == 1
	}
	if v, ok := uint16At(snap, 0x001A); ok {
		resp.Configured["co2_threshold_ppm"] = int(v)
	}
	if b, ok := uint8At(snap, 0x0019); ok {
		resp.Configured["humidity_threshold_pct"] = int(b)
	}
	if v, ok := uint16At(snap, 0x031F); ok {
		resp.Configured["voc_threshold_index"] = int(v)
	}

	// Live: the device's actual current behavior.
	if v, ok := uint16At(snap, 0x004A); ok {
		resp.Live["fan_supply_rpm"] = int(v)
	}
	if v, ok := uint16At(snap, 0x004B); ok {
		resp.Live["fan_extract_rpm"] = int(v)
	}
	if b, ok := uint8At(snap, 0x0081); ok {
		resp.Live["heater_running"] = b == 1
	}
	resp.Live["in_user_control"] = computeInUserControl(snap)
	resp.Live["sensor_alerts"] = decodeAlerts(snap)
	if b, ok := uint8At(snap, 0x0007); ok {
		resp.Live["special_mode"] = specialModeName(b)
	}
	if raw, ok := snap.Values[0x000B]; ok && len(raw) == 3 {
		// 3-byte time_of_day = [sec, min, hr]
		secs := int(raw[2])*3600 + int(raw[1])*60 + int(raw[0])
		resp.Live["special_mode_remaining_seconds"] = secs
	}

	// Sensors: live readings.
	if b, ok := uint8At(snap, 0x0025); ok {
		resp.Sensors["humidity_pct"] = int(b)
	}
	if v, ok := uint16At(snap, 0x0027); ok {
		resp.Sensors["eco2_ppm"] = int(v)
	}
	if v, ok := uint16At(snap, 0x0320); ok {
		resp.Sensors["voc_index"] = int(v)
	}
	for _, t := range []struct {
		id   breezy.ParamID
		name string
	}{
		{0x001F, "temp_outdoor_c"},
		{0x0020, "temp_supply_c"},
		{0x0021, "temp_exhaust_inlet_c"},
		{0x0022, "temp_exhaust_outlet_c"},
	} {
		if v, ok := int16At(snap, t.id); ok {
			// -32768 / 32767 are sentinels; surface them as null.
			if v == -32768 || v == 32767 {
				continue
			}
			resp.Sensors[t.name] = float64(v) / 10.0
		}
	}
	if b, ok := uint8At(snap, 0x0129); ok {
		resp.Sensors["recovery_efficiency_pct"] = int(b)
	}

	// Service: filter, motor, RTC battery, faults.
	if b, ok := uint8At(snap, 0x0088); ok {
		if b == 0 {
			resp.Service["filter_status"] = "clean"
		} else {
			resp.Service["filter_status"] = "soiled"
		}
	}
	if raw, ok := snap.Values[0x0064]; ok && len(raw) == 4 {
		// remaining_time = [min, hr, day_lo, day_hi]
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		resp.Service["filter_remaining_seconds"] = secs
	}
	if raw, ok := snap.Values[0x007E]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		resp.Service["motor_lifetime_seconds"] = secs
	}
	if v, ok := uint16At(snap, 0x0024); ok {
		resp.Service["rtc_battery_volts"] = float64(v) / 1000.0
	}
	if b, ok := uint8At(snap, 0x0083); ok {
		resp.Service["fault_level"] = faultLevelName(b)
	}
	if b, ok := uint8At(snap, 0x030B); ok {
		resp.Service["frost_protection_active"] = b == 1
	}

	// Firmware.
	if raw, ok := snap.Values[0x0086]; ok && len(raw) == 6 {
		fw := map[string]any{
			"version": fmt.Sprintf("%d.%02d", raw[0], raw[1]),
		}
		year := int(uint16(raw[4]) | uint16(raw[5])<<8)
		fw["build_date"] = fmt.Sprintf("%04d-%02d-%02d", year, raw[3], raw[2])
		resp.Firmware = fw
	}
	return resp
}

// computeInUserControl returns true when the device is behaving according
// to user configuration, false when a firmware-driven override is in
// effect (sensor alert, special-mode timer, frost protection).
//
// "User in control" means none of these are true:
//   - 0x07 != 0       — a special mode (night/turbo) is running
//   - any byte of 0x84 != 0 — at least one sensor crossed its threshold
//   - 0x030B == 1     — frost protection is energising the heater
//
// Note that 0x0068 (heater_control, the user's own toggle) does NOT
// downgrade in_user_control: the user *asking for* heat is configuration,
// not override. The override signal for heat is 0x030B (frost protection
// engaging the reheater autonomously even when the user has it off).
func computeInUserControl(snap Snapshot) bool {
	if b, ok := uint8At(snap, 0x0007); ok && b != 0 {
		return false
	}
	if raw, ok := snap.Values[0x0084]; ok {
		for _, b := range raw {
			if b != 0 {
				return false
			}
		}
	}
	if b, ok := uint8At(snap, 0x030B); ok && b == 1 {
		return false
	}
	return true
}

// decodeAlerts surfaces the per-sensor over-threshold flags from 0x84 as
// a small map. Missing 0x84 yields all-false (cache miss is conservatively
// "no known alert").
func decodeAlerts(snap Snapshot) map[string]any {
	out := map[string]any{"humidity": false, "co2": false, "voc": false}
	raw, ok := snap.Values[0x0084]
	if !ok || len(raw) < 5 {
		return out
	}
	out["humidity"] = raw[0] != 0
	out["co2"] = raw[1] != 0
	out["voc"] = raw[4] != 0
	return out
}

// airflowModeName decodes 0xB7. Anything outside 0..3 falls through to a
// debug-only string so we don't lose data on a future firmware addition.
func airflowModeName(b uint8) string {
	switch b {
	case 0:
		return "ventilation"
	case 1:
		return "regeneration"
	case 2:
		return "supply"
	case 3:
		return "extract"
	}
	return fmt.Sprintf("unknown(%d)", b)
}

// specialModeName decodes 0x07 (0=off, 1=night, 2=turbo).
func specialModeName(b uint8) string {
	switch b {
	case 0:
		return "off"
	case 1:
		return "night"
	case 2:
		return "turbo"
	}
	return fmt.Sprintf("unknown(%d)", b)
}

// faultLevelName decodes 0x83 (0=none, 1=alarm, 2=warning).
func faultLevelName(b uint8) string {
	switch b {
	case 0:
		return "none"
	case 1:
		return "alarm"
	case 2:
		return "warning"
	}
	return fmt.Sprintf("unknown(%d)", b)
}
