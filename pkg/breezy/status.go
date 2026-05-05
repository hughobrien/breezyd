// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"encoding/binary"
	"fmt"
	"time"
)

// Status is the structured per-device snapshot. It is built from raw
// parameter bytes (as returned by Client.ReadParams or stored in the
// daemon's cache) via BuildStatus. The CLI uses it in both standalone
// mode (returned directly by GetStatus) and daemon mode (decoded from
// the daemon's JSON HTTP response). The JSON shape is part of the public
// API of the daemon; do not change tag names or field types without
// considering downstream consumers (including the embedded dashboard).
type Status struct {
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

// BuildStatus decodes raw parameter bytes into the Status shape. Decode
// failures fall back to a missing/zero value rather than producing an
// error — an unfamiliar firmware shouldn't take down the API. lastPoll
// may be nil (e.g. a fresh standalone read with no poll history); when
// non-nil it is rendered in RFC3339 UTC.
func BuildStatus(values map[ParamID][]byte, name, id, ip string, lastPoll *time.Time) Status {
	resp := Status{
		Name:       name,
		ID:         id,
		IP:         ip,
		Configured: map[string]any{},
		Live:       map[string]any{},
		Sensors:    map[string]any{},
		Service:    map[string]any{},
	}
	if lastPoll != nil && !lastPoll.IsZero() {
		resp.LastPoll = lastPoll.UTC().Format(time.RFC3339)
	}

	// Configured: what the user set.
	if b, ok := Uint8At(values, 0x0001); ok {
		resp.Configured["power"] = b == 1
	}
	if b, ok := Uint8At(values, 0x0002); ok {
		switch b {
		case 0xFF:
			resp.Configured["speed_mode"] = "manual"
		case 1, 2, 3:
			resp.Configured["speed_mode"] = fmt.Sprintf("preset%d", b)
		default:
			resp.Configured["speed_mode"] = fmt.Sprintf("unknown(%d)", b)
		}
	}
	if b, ok := Uint8At(values, 0x0044); ok {
		resp.Configured["manual_pct"] = int(b)
	}
	if b, ok := Uint8At(values, 0x00B7); ok {
		resp.Configured["airflow_mode"] = AirflowModeName(b)
	}
	if b, ok := Uint8At(values, 0x0068); ok {
		resp.Configured["heater_enabled"] = b == 1
	}
	if v, ok := Uint16At(values, 0x001A); ok {
		resp.Configured["co2_threshold_ppm"] = int(v)
	}
	if b, ok := Uint8At(values, 0x0019); ok {
		resp.Configured["humidity_threshold_pct"] = int(b)
	}
	if v, ok := Uint16At(values, 0x031F); ok {
		resp.Configured["voc_threshold_index"] = int(v)
	}

	// Live: the device's actual current behavior.
	supplyRPM, supplyRPMOK := Uint16At(values, 0x004A)
	extractRPM, extractRPMOK := Uint16At(values, 0x004B)
	if supplyRPMOK {
		resp.Live["fan_supply_rpm"] = int(supplyRPM)
	}
	if extractRPMOK {
		resp.Live["fan_extract_rpm"] = int(extractRPM)
	}
	// Per-fan commanded percentage. In manual mode both fans share 0x0044;
	// in preset N supply uses 0x3A/3C/3E and extract uses 0x3B/3D/3F. We
	// gate on the live RPM so a stopped fan reads 0% (power off, supply-
	// only / extract-only modes), even when the firmware still has a
	// non-zero commanded value stored.
	if supplyPct, ok := commandedFanPct(values, true); ok {
		if supplyRPMOK && supplyRPM == 0 {
			supplyPct = 0
		}
		resp.Live["fan_supply_pct"] = supplyPct
	}
	if extractPct, ok := commandedFanPct(values, false); ok {
		if extractRPMOK && extractRPM == 0 {
			extractPct = 0
		}
		resp.Live["fan_extract_pct"] = extractPct
	}
	if b, ok := Uint8At(values, 0x0081); ok {
		resp.Live["heater_running"] = b == 1
	}
	resp.Live["in_user_control"] = ComputeInUserControl(values)
	resp.Live["sensor_alerts"] = DecodeAlerts(values)
	if b, ok := Uint8At(values, 0x0007); ok {
		resp.Live["special_mode"] = SpecialModeName(b)
	}
	if raw, ok := values[0x000B]; ok && len(raw) == 3 {
		secs := int(raw[2])*3600 + int(raw[1])*60 + int(raw[0])
		resp.Live["special_mode_remaining_seconds"] = secs
	}

	// Sensors: live readings.
	if b, ok := Uint8At(values, 0x0025); ok {
		resp.Sensors["humidity_pct"] = int(b)
	}
	if v, ok := Uint16At(values, 0x0027); ok {
		resp.Sensors["eco2_ppm"] = int(v)
	}
	if v, ok := Uint16At(values, 0x0320); ok {
		resp.Sensors["voc_index"] = int(v)
	}
	for _, t := range []struct {
		id   ParamID
		name string
	}{
		{0x001F, "temp_outdoor_c"},
		{0x0020, "temp_supply_c"},
		{0x0021, "temp_exhaust_inlet_c"},
		{0x0022, "temp_exhaust_outlet_c"},
	} {
		if v, ok := Int16At(values, t.id); ok {
			if v == -32768 || v == 32767 {
				continue
			}
			resp.Sensors[t.name] = float64(v) / 10.0
		}
	}
	if b, ok := Uint8At(values, 0x0129); ok {
		resp.Sensors["recovery_efficiency_pct"] = int(b)
	}

	// Service: filter, motor, RTC battery, faults.
	if b, ok := Uint8At(values, 0x0088); ok {
		if b == 0 {
			resp.Service["filter_status"] = "clean"
		} else {
			resp.Service["filter_status"] = "soiled"
		}
	}
	if raw, ok := values[0x0064]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		resp.Service["filter_remaining_seconds"] = secs
	}
	if v, ok := Uint16At(values, 0x0063); ok {
		resp.Service["filter_total_seconds"] = int(v) * 86400
	}
	if raw, ok := values[0x007E]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		resp.Service["motor_lifetime_seconds"] = secs
	}
	if v, ok := Uint16At(values, 0x0024); ok {
		resp.Service["rtc_battery_volts"] = float64(v) / 1000.0
	}
	if b, ok := Uint8At(values, 0x0083); ok {
		resp.Service["fault_level"] = FaultLevelName(b)
	}
	if b, ok := Uint8At(values, 0x030B); ok {
		resp.Service["frost_protection_active"] = b == 1
	}

	// Firmware.
	if raw, ok := values[0x0086]; ok && len(raw) == 6 {
		fw := map[string]any{
			"version": fmt.Sprintf("%d.%02d", raw[0], raw[1]),
		}
		year := int(uint16(raw[4]) | uint16(raw[5])<<8)
		fw["build_date"] = fmt.Sprintf("%04d-%02d-%02d", year, raw[3], raw[2])
		resp.Firmware = fw
	}
	return resp
}

// Uint8At returns the single byte stored at id, or (0, false) if the
// value is missing or wrong-sized.
func Uint8At(values map[ParamID][]byte, id ParamID) (uint8, bool) {
	raw, ok := values[id]
	if !ok || len(raw) != 1 {
		return 0, false
	}
	return raw[0], true
}

// Uint16At returns the LE 2-byte value at id.
func Uint16At(values map[ParamID][]byte, id ParamID) (uint16, bool) {
	raw, ok := values[id]
	if !ok || len(raw) != 2 {
		return 0, false
	}
	return binary.LittleEndian.Uint16(raw), true
}

// Int16At returns the LE 2-byte signed value at id.
func Int16At(values map[ParamID][]byte, id ParamID) (int16, bool) {
	v, ok := Uint16At(values, id)
	if !ok {
		return 0, false
	}
	return int16(v), true
}

// ComputeInUserControl returns true when the device is behaving according
// to user configuration, false when a firmware-driven override is in
// effect (sensor alert, special-mode timer, frost protection).
func ComputeInUserControl(values map[ParamID][]byte) bool {
	if b, ok := Uint8At(values, 0x0007); ok && b != 0 {
		return false
	}
	if raw, ok := values[0x0084]; ok {
		for _, b := range raw {
			if b != 0 {
				return false
			}
		}
	}
	if b, ok := Uint8At(values, 0x030B); ok && b == 1 {
		return false
	}
	return true
}

// DecodeAlerts surfaces the per-sensor over-threshold flags from 0x84.
// Missing 0x84 yields all-false (cache miss is conservatively "no alert").
func DecodeAlerts(values map[ParamID][]byte) map[string]any {
	out := map[string]any{"humidity": false, "co2": false, "voc": false}
	raw, ok := values[0x0084]
	if !ok || len(raw) < 5 {
		return out
	}
	out["humidity"] = raw[0] != 0
	out["co2"] = raw[1] != 0
	out["voc"] = raw[4] != 0
	return out
}

// AirflowModeName decodes 0xB7. Anything outside 0..3 falls through to a
// debug-only string so future firmware additions don't lose data.
func AirflowModeName(b uint8) string {
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

// commandedFanPct returns the percentage the firmware is commanding for one
// fan, derived from speed_mode (0x02) and the active source param: 0x44 in
// manual mode, 0x3A/3C/3E for supply at preset 1/2/3, 0x3B/3D/3F for extract.
// Returns (0, false) when speed_mode or the source param is missing or
// outside the recognised set.
func commandedFanPct(values map[ParamID][]byte, supply bool) (int, bool) {
	mode, ok := Uint8At(values, 0x0002)
	if !ok {
		return 0, false
	}
	var src ParamID
	switch mode {
	case 0xFF:
		src = 0x0044
	case 1:
		src = 0x003A
		if !supply {
			src = 0x003B
		}
	case 2:
		src = 0x003C
		if !supply {
			src = 0x003D
		}
	case 3:
		src = 0x003E
		if !supply {
			src = 0x003F
		}
	default:
		return 0, false
	}
	v, ok := Uint8At(values, src)
	if !ok {
		return 0, false
	}
	return int(v), true
}

// SpecialModeName decodes 0x07 (0=off, 1=night, 2=turbo).
func SpecialModeName(b uint8) string {
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

// FaultLevelName decodes 0x83 (0=none, 1=alarm, 2=warning).
func FaultLevelName(b uint8) string {
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
