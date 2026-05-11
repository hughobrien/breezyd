// SPDX-License-Identifier: GPL-3.0-or-later

// snapshotToView converts a cached Snapshot (raw param bytes) into the
// templ-friendly DeviceView consumed by templates. Mirrors the decode
// patterns the JSON handlers use, but produces a render-ready struct
// instead of marshaling JSON.
package main

import (
	"fmt"
	"time"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
	"github.com/hughobrien/breezyd/pkg/breezy"
)

// defaultStaleWindow is the dashboard's stale threshold when the caller
// can't supply one (zero PollInterval). 90s is what the daemon used as a
// hard-coded literal before the 3×poll-interval derivation landed; keeping
// it as the fallback preserves the prior behavior for tests that build a
// Handler{} without setting PollInterval.
const defaultStaleWindow = 90 * time.Second

// snapshotToView converts a cached Snapshot (raw param bytes) into the
// templ-friendly DeviceView consumed by templates. The Handler calls this
// once per request; templates consume the result exclusively.
//
// staleWindow is the threshold beyond which a Snapshot's LastPoll counts
// as stale. Production wires this from 3× the configured poll interval
// (per SPECIFICATION-web.md "Card states"); tests that pass 0 fall back
// to defaultStaleWindow so existing call sites don't need updating.
//
// Decode patterns mirror BuildStatus in pkg/breezy/status.go. Missing or
// wrong-sized values produce a sensible zero/default rather than errors.
func snapshotToView(name string, snap Snapshot, staleWindow time.Duration) ui.DeviceView {
	if staleWindow <= 0 {
		staleWindow = defaultStaleWindow
	}
	v := ui.DeviceView{
		Name: name,
		IP:   snap.IP,
	}
	if !snap.LastPoll.IsZero() {
		age := time.Since(snap.LastPoll)
		v.LastPollAge = formatAge(age)
		v.Stale = age > staleWindow
	} else {
		v.Stale = true // no poll yet
	}

	v.Sensors = sensorsViewFrom(snap.Values)
	// Schedule and Energy are populated externally by the handler (they require
	// access to the per-device Scheduler and EnergyTracker, which the Snapshot
	// does not contain).
	v.Schedule = ui.ScheduleView{}
	v.Energy = nil

	// Configured: what the user set.
	if b, ok := breezy.Uint8At(snap.Values, 0x0001); ok {
		v.Power = b == 1
	}
	if b, ok := breezy.Uint8At(snap.Values, 0x0002); ok {
		switch b {
		case 0xFF:
			v.SpeedMode = "manual"
		case 1, 2, 3:
			v.SpeedMode = fmt.Sprintf("preset%d", b)
		}
	}
	if b, ok := breezy.Uint8At(snap.Values, 0x0044); ok {
		v.ManualPct = int(b)
	}
	if b, ok := breezy.Uint8At(snap.Values, 0x00B7); ok {
		v.AirflowMode = breezy.AirflowModeName(b)
	}
	if b, ok := breezy.Uint8At(snap.Values, 0x0068); ok {
		v.Heater = b == 1
	}
	if b, ok := breezy.Uint8At(snap.Values, 0x0007); ok {
		v.SpecialMode = breezy.SpecialModeName(b)
	}
	if raw, ok := snap.Values[0x000B]; ok && len(raw) == 3 {
		secs := int(raw[2])*3600 + int(raw[1])*60 + int(raw[0])
		v.SpecialModeRemaining = formatSeconds(secs)
		v.SpecialModeRemainingSeconds = secs
	}
	// Per-mode timer durations (params 0x0302 night, 0x0303 turbo).
	// 2-byte TypeDuration encoding: [min, hr] little-endian. Exposed
	// to the dashboard so the timer-chip click handler can seed the
	// countdown signal optimistically before the next poll catches up.
	if raw, ok := snap.Values[0x0302]; ok && len(raw) == 2 {
		v.NightDurationSeconds = int(raw[1])*3600 + int(raw[0])*60
	}
	if raw, ok := snap.Values[0x0303]; ok && len(raw) == 2 {
		v.TurboDurationSeconds = int(raw[1])*3600 + int(raw[0])*60
	}

	// Per-preset stored supply/extract percentages.
	for i, params := range [][2]breezy.ParamID{
		{0x003A, 0x003B}, // preset 1
		{0x003C, 0x003D}, // preset 2
		{0x003E, 0x003F}, // preset 3
	} {
		supply, supplyOK := breezy.Uint8At(snap.Values, params[0])
		extract, extractOK := breezy.Uint8At(snap.Values, params[1])
		pv := ui.PresetView{Supply: -1, Extract: -1}
		if supplyOK {
			pv.Supply = int(supply)
		}
		if extractOK {
			pv.Extract = int(extract)
		}
		switch i {
		case 0:
			v.Preset1 = pv
		case 1:
			v.Preset2 = pv
		case 2:
			v.Preset3 = pv
		}
	}

	// Firmware.
	if raw, ok := snap.Values[0x0086]; ok && len(raw) == 6 {
		v.FirmwareVersion = fmt.Sprintf("%d.%02d", raw[0], raw[1])
		year := int(uint16(raw[4]) | uint16(raw[5])<<8)
		v.FirmwareDate = fmt.Sprintf("%04d-%02d-%02d", year, raw[3], raw[2])
	} else {
		v.FirmwareVersion = "—"
		v.FirmwareDate = "—"
	}

	// Service: filter, motor, RTC battery, faults.
	if b, ok := breezy.Uint8At(snap.Values, 0x0088); ok {
		if b == 0 {
			v.FilterStatus = "clean"
		} else {
			v.FilterStatus = "soiled"
		}
	} else {
		v.FilterStatus = "—"
	}
	if raw, ok := snap.Values[0x0064]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		if days > 0 {
			v.FilterRemaining = fmt.Sprintf("%dd", days)
		} else if secs > 0 {
			v.FilterRemaining = fmt.Sprintf("%dh", secs/3600)
		}
	}
	if raw, ok := snap.Values[0x007E]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		h := secs / 3600
		m := (secs % 3600) / 60
		v.MotorLifetime = fmt.Sprintf("%dh %dm", h, m)
	} else {
		v.MotorLifetime = "—"
	}
	if val, ok := breezy.Uint16At(snap.Values, 0x0024); ok {
		v.RTCBattery = fmt.Sprintf("%.3fV", float64(val)/1000.0)
	} else {
		v.RTCBattery = "—"
	}
	if b, ok := breezy.Uint8At(snap.Values, 0x0083); ok {
		v.FaultLevel = breezy.FaultLevelName(b)
	} else {
		v.FaultLevel = "—"
	}

	// NeedsAttention: fault or soiled filter.
	v.NeedsAttention = (v.FaultLevel != "" && v.FaultLevel != "none" && v.FaultLevel != "—") ||
		v.FilterStatus == "soiled"

	return v
}

// sensorsViewFrom decodes all sensor-related params from the snapshot.
func sensorsViewFrom(values map[breezy.ParamID][]byte) ui.SensorsView {
	s := ui.SensorsView{}

	if b, ok := breezy.Uint8At(values, 0x0025); ok {
		s.HumidityPct = int(b)
	}
	if v, ok := breezy.Uint16At(values, 0x0027); ok {
		s.CO2PPM = int(v)
	}
	if v, ok := breezy.Uint16At(values, 0x0320); ok {
		s.VOCPPM = int(v)
	}

	// Temperatures (signed int16, /10 = °C; sentinels ±32767/32768 = missing).
	if v, ok := breezy.Int16At(values, 0x001F); ok && v != -32768 && v != 32767 {
		f := float64(v) / 10.0
		s.TempOutdoorC = &f
	}
	if v, ok := breezy.Int16At(values, 0x0020); ok && v != -32768 && v != 32767 {
		f := float64(v) / 10.0
		s.TempSupplyC = &f
	}
	if v, ok := breezy.Int16At(values, 0x0021); ok && v != -32768 && v != 32767 {
		f := float64(v) / 10.0
		s.TempExhaustInletC = &f
	}
	if v, ok := breezy.Int16At(values, 0x0022); ok && v != -32768 && v != 32767 {
		f := float64(v) / 10.0
		s.TempExhaustOutC = &f
	}

	if b, ok := breezy.Uint8At(values, 0x0129); ok {
		n := int(b)
		s.RecoveryPct = &n
	}

	// Fan RPMs (suppress during fan-settle by passing nil when 0 after a write;
	// the snap always carries the poller's most-recent read).
	if v, ok := breezy.Uint16At(values, 0x004A); ok {
		n := int(v)
		s.SupplyRPM = &n
	}
	if v, ok := breezy.Uint16At(values, 0x004B); ok {
		n := int(v)
		s.ExtractRPM = &n
	}

	// Alerts from 0x0084 (5-byte bitmap: [humidity, co2, ?, ?, voc]).
	if raw, ok := values[0x0084]; ok && len(raw) >= 5 {
		s.HumidityAlert = raw[0] != 0
		s.CO2Alert = raw[1] != 0
		s.VOCAlert = raw[4] != 0
	}
	s.AlertActive = s.HumidityAlert || s.CO2Alert || s.VOCAlert

	// Configured thresholds.
	if b, ok := breezy.Uint8At(values, 0x0019); ok {
		s.HumidityThreshold = int(b)
	}
	if v, ok := breezy.Uint16At(values, 0x001A); ok {
		s.CO2Threshold = int(v)
	}
	if v, ok := breezy.Uint16At(values, 0x031F); ok {
		s.VOCThreshold = int(v)
	}

	// Auto-fan enable flags (default-on when missing = true).
	s.HumidityAutoFan = true
	if b, ok := breezy.Uint8At(values, 0x000F); ok {
		s.HumidityAutoFan = b == 1
	}
	s.CO2AutoFan = true
	if b, ok := breezy.Uint8At(values, 0x0011); ok {
		s.CO2AutoFan = b == 1
	}
	s.VOCAutoFan = true
	if b, ok := breezy.Uint8At(values, 0x0315); ok {
		s.VOCAutoFan = b == 1
	}

	return s
}

// energyViewFrom converts an EnergyValues snapshot into an EnergyView.
func energyViewFrom(ev breezy.EnergyValues) *ui.EnergyView {
	return &ui.EnergyView{
		Error:               ev.Error,
		InstantW:            ev.InstantW,
		ConsumedW:           ev.ConsumedW,
		HeatingTodayKWh:     ev.HeatingTodayKWh,
		CoolingTodayKWh:     ev.CoolingTodayKWh,
		ConsumedTodayKWh:    ev.ConsumedTodayKWh,
		HeatingMonthKWh:     ev.HeatingMonthKWh,
		CoolingMonthKWh:     ev.CoolingMonthKWh,
		ConsumedMonthKWh:    ev.ConsumedMonthKWh,
		HeatingLifetimeKWh:  ev.HeatingLifetimeKWh,
		CoolingLifetimeKWh:  ev.CoolingLifetimeKWh,
		ConsumedLifetimeKWh: ev.ConsumedLifetimeKWh,
	}
}

// scheduleViewFrom converts a ScheduleSnapshot into a ScheduleView.
func scheduleViewFrom(ss ScheduleSnapshot) ui.ScheduleView {
	sv := ui.ScheduleView{
		Present: true,
		Enabled: ss.Enabled,
		Alert:   ss.LastApply != nil && !ss.LastApply.OK,
	}
	for _, e := range ss.Entries {
		sv.Entries = append(sv.Entries, ui.ScheduleEntryView{
			At:     e.At.String(),
			Action: e.Action,
			Pct:    e.Pct,
		})
	}
	if ss.LastApply != nil && !ss.LastApply.OK {
		sv.LastApply = &ui.LastApplyView{
			At:      ss.LastApply.At.String(),
			Err:     ss.LastApply.Err,
			Retries: ss.LastApply.Retries,
		}
	}
	return sv
}

// formatAge returns a human-readable duration string similar to the old JS humanAgo.
func formatAge(d time.Duration) string {
	s := int(d.Seconds())
	if s < 0 {
		s = 0
	}
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	m := s / 60
	if m < 60 {
		return fmt.Sprintf("%dm %ds", m, s%60)
	}
	h := m / 60
	return fmt.Sprintf("%dh %dm", h, m%60)
}

// formatSeconds formats a duration in seconds as "Xh Ym".
func formatSeconds(secs int) string {
	if secs <= 0 {
		return ""
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
