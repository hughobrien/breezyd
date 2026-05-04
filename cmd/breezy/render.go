// SPDX-License-Identifier: GPL-3.0-or-later

// Human-friendly renderers for the breezy CLI's status, ls, faults,
// firmware, and param verbs. Each function takes either a parsed daemon
// response or a typed value from the local registry and writes a
// multi-line block to the given Writer. They are package-private;
// main.go's cmd* functions own the HTTP plumbing and then delegate the
// formatting here.
package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// snapshotResp mirrors the daemon's SnapshotResponse JSON. Each block
// is map[string]any so we don't have to enumerate every possible field
// — the renderer probes for what it knows about and ignores the rest.
type snapshotResp struct {
	Name       string         `json:"name"`
	ID         string         `json:"id"`
	IP         string         `json:"ip"`
	LastPoll   string         `json:"last_poll"`
	Configured map[string]any `json:"configured"`
	Live       map[string]any `json:"live"`
	Sensors    map[string]any `json:"sensors"`
	Service    map[string]any `json:"service"`
	Firmware   map[string]any `json:"firmware"`
}

// renderStatus writes the multi-line per-device snapshot to w. The
// header line includes firmware + last_poll relative; subsequent lines
// cover power, mode, speed, sensors, service. Sensor-override warning
// is printed on its own line just below the header when applicable.
func renderStatus(w io.Writer, s snapshotResp) {
	// Header: "<name> @ <ip>  (firmware X.YY, last poll Ns ago)".
	fwStr := ""
	if v, ok := s.Firmware["version"].(string); ok {
		fwStr = "firmware " + v
	}
	lastStr := ""
	if s.LastPoll != "" {
		if t, err := time.Parse(time.RFC3339, s.LastPoll); err == nil {
			lastStr = "last poll " + humanDuration(time.Since(t)) + " ago"
		}
	}
	suffix := joinNonEmpty(", ", fwStr, lastStr)
	if suffix != "" {
		fmt.Fprintf(w, "%s @ %s  (%s)\n", s.Name, s.IP, suffix)
	} else {
		fmt.Fprintf(w, "%s @ %s\n", s.Name, s.IP)
	}

	// Sensor-override warning: in_user_control is explicitly false.
	if v, ok := s.Live["in_user_control"].(bool); ok && !v {
		alerts := alertSummary(s.Live["sensor_alerts"])
		if alerts != "" {
			fmt.Fprintf(w, "  !! sensor override active (%s) — fan/heater may not match configured values\n", alerts)
		} else {
			fmt.Fprintln(w, "  !! sensor override active — fan/heater may not match configured values")
		}
	}

	// Power.
	if v, ok := boolField(s.Configured, "power"); ok {
		fmt.Fprintf(w, "  power      : %s\n", onOff(v))
	}

	// Mode.
	if v, ok := s.Configured["airflow_mode"].(string); ok {
		fmt.Fprintf(w, "  mode       : %s\n", v)
	}

	// Speed: configured side first, then live RPM if known.
	speedLine := configuredSpeedLine(s.Configured)
	if speedLine != "" {
		live := liveRPMLine(s.Live)
		if live != "" {
			fmt.Fprintf(w, "  speed      : %-15s   (%s)\n", speedLine, live)
		} else {
			fmt.Fprintf(w, "  speed      : %s\n", speedLine)
		}
	}

	// Sensors.
	if line := sensorsLine(s.Sensors); line != "" {
		fmt.Fprintf(w, "  sensors    : %s\n", line)
	}

	// Service: filter status + remaining, motor lifetime.
	if line := serviceLine(s.Service); line != "" {
		fmt.Fprintf(w, "  service    : %s\n", line)
	}

	// Battery (RTC).
	if v, ok := floatField(s.Service, "rtc_battery_volts"); ok {
		fmt.Fprintf(w, "  battery    : RTC %.2f V\n", v)
	}

	// Faults.
	if v, ok := s.Service["fault_level"].(string); ok && v != "none" && v != "" {
		fmt.Fprintf(w, "  faults     : %s (use `breezy %s faults` for detail)\n", v, s.Name)
	}
}

// lsRow is the per-device row for renderLs; built by the cmdLs caller
// from the daemon's GET /v1/devices response.
type lsRow struct {
	Name      string
	ID        string
	IP        string
	LastPoll  string // RFC3339 or "" if never polled
	Power     *bool  // nil = unknown
	Mode      string // empty -> "?"
	Reachable bool
}

// renderLs writes the device-list table to w. Empty input yields the
// "(no devices configured)" line. Output is sorted by name for stability.
func renderLs(w io.Writer, rows []lsRow) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no devices configured)")
		return
	}

	// Sort for stable output.
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })

	// Compute column widths.
	wName, wIP, wPower, wMode := len("NAME"), len("IP"), len("POWER"), len("MODE")
	cells := make([][]string, 0, len(rows))
	for _, d := range rows {
		power := "?"
		if d.Power != nil {
			power = onOff(*d.Power)
		}
		mode := d.Mode
		if mode == "" {
			mode = "?"
		}
		last := "never"
		if d.LastPoll != "" {
			if t, err := time.Parse(time.RFC3339, d.LastPoll); err == nil {
				last = humanDuration(time.Since(t)) + " ago"
			}
		}
		if !d.Reachable {
			last += " (unreachable)"
		}
		row := []string{d.Name, d.IP, power, mode, last}
		if len(d.Name) > wName {
			wName = len(d.Name)
		}
		if len(d.IP) > wIP {
			wIP = len(d.IP)
		}
		if len(power) > wPower {
			wPower = len(power)
		}
		if len(mode) > wMode {
			wMode = len(mode)
		}
		cells = append(cells, row)
	}

	fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %s\n", wName, "NAME", wIP, "IP", wPower, "POWER", wMode, "MODE", "LAST POLL")
	for _, r := range cells {
		fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %s\n", wName, r[0], wIP, r[1], wPower, r[2], wMode, r[3], r[4])
	}
}

// fault is one row of the daemon's faults response.
type fault struct {
	Code int    `json:"code"`
	Kind string `json:"kind"`
}

// renderFaults pretty-prints a faults list. Empty list yields the
// "no active faults" line (with newline).
func renderFaults(w io.Writer, faults []fault) {
	if len(faults) == 0 {
		fmt.Fprintln(w, "no active faults")
		return
	}
	for _, f := range faults {
		fmt.Fprintf(w, "  %s: code %d\n", f.Kind, f.Code)
	}
}

// renderFirmware prints "version <v>  built <date>" — both fields come
// straight from the daemon's GET /firmware route.
func renderFirmware(w io.Writer, version, buildDate string) {
	fmt.Fprintf(w, "version %s  built %s\n", version, buildDate)
}

// ----------------------------------------------------------------------------
// Snapshot renderer helpers
// ----------------------------------------------------------------------------

// configuredSpeedLine renders the user-facing speed setting. When in
// manual mode we surface the percentage; in preset mode we surface the
// preset label.
func configuredSpeedLine(cfg map[string]any) string {
	mode, _ := cfg["speed_mode"].(string)
	switch mode {
	case "manual":
		if pct, ok := intField(cfg, "manual_pct"); ok {
			return fmt.Sprintf("manual %d%%", pct)
		}
		return "manual"
	case "preset1", "preset2", "preset3":
		return mode
	}
	return mode
}

// liveRPMLine renders the live fan RPMs as "supply / extract rpm".
// Empty when neither value is in the cache.
func liveRPMLine(live map[string]any) string {
	supply, sok := intField(live, "fan_supply_rpm")
	extract, eok := intField(live, "fan_extract_rpm")
	switch {
	case sok && eok:
		return fmt.Sprintf("live: %d / %d rpm", supply, extract)
	case sok:
		return fmt.Sprintf("live: supply %d rpm", supply)
	case eok:
		return fmt.Sprintf("live: extract %d rpm", extract)
	}
	return ""
}

// sensorsLine packs RH/eCO2/VOC + outdoor temp + recovery into one line.
func sensorsLine(sensors map[string]any) string {
	if len(sensors) == 0 {
		return ""
	}
	parts := []string{}
	if v, ok := intField(sensors, "humidity_pct"); ok {
		parts = append(parts, fmt.Sprintf("RH=%d%%", v))
	}
	if v, ok := intField(sensors, "eco2_ppm"); ok {
		parts = append(parts, fmt.Sprintf("eCO2=%dppm", v))
	}
	if v, ok := intField(sensors, "voc_index"); ok {
		parts = append(parts, fmt.Sprintf("VOC=%d", v))
	}
	if v, ok := floatField(sensors, "temp_outdoor_c"); ok {
		parts = append(parts, fmt.Sprintf("outdoor=%.1f°C", v))
	}
	if v, ok := intField(sensors, "recovery_efficiency_pct"); ok {
		parts = append(parts, fmt.Sprintf("recovery=%d%%", v))
	}
	return strings.Join(parts, "  ")
}

// serviceLine summarises filter status and motor lifetime in one line.
func serviceLine(svc map[string]any) string {
	parts := []string{}
	if v, ok := svc["filter_status"].(string); ok {
		s := "filter " + v
		if rem, ok := intField(svc, "filter_remaining_seconds"); ok {
			s += " (" + humanRemaining(time.Duration(rem)*time.Second) + " remaining)"
		}
		parts = append(parts, s)
	}
	if v, ok := intField(svc, "motor_lifetime_seconds"); ok {
		parts = append(parts, "motor "+humanRemaining(time.Duration(v)*time.Second)+" lifetime")
	}
	if v, ok := boolField(svc, "frost_protection_active"); ok && v {
		parts = append(parts, "frost protection active")
	}
	return strings.Join(parts, "  ")
}

// alertSummary turns the sensor-alert map into a comma-joined string
// of which thresholds are over.
func alertSummary(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	var parts []string
	for _, k := range []string{"humidity", "co2", "voc"} {
		if b, ok := m[k].(bool); ok && b {
			parts = append(parts, k+" alert")
		}
	}
	return strings.Join(parts, ", ")
}

// boolField, intField, floatField extract typed values from the
// JSON-decoded map[string]any. Numbers come back as float64 from
// encoding/json; we treat anything within int range as an int.
func boolField(m map[string]any, key string) (bool, bool) {
	v, ok := m[key].(bool)
	return v, ok
}
func intField(m map[string]any, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	}
	return 0, false
}
func floatField(m map[string]any, key string) (float64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

// onOff returns "on"/"off" for a bool — purely so the status output
// matches the spec example exactly.
func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// joinNonEmpty joins parts with sep, skipping empty strings. Used by
// the status header so the parenthesis isn't `(, last poll 3s ago)`
// when firmware is missing.
func joinNonEmpty(sep string, parts ...string) string {
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// humanDuration renders a positive duration as a compact "Ns/Nm/Nh"
// form. Negative durations (clock skew between CLI and daemon) clamp
// to "0s" rather than printing "-1s".
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// humanRemaining renders a "filter / motor odometer" duration with day
// granularity once over 24h, otherwise hours+minutes.
func humanRemaining(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int(d / time.Minute)
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// capsString renders a Capabilities bitmask as a fixed-order letter
// concatenation: R, W, I, D. Read-only -> "R", common writable -> "RW",
// fully capable -> "RWID", write-only triggers -> "W".
func capsString(c breezy.Capabilities) string {
	var b strings.Builder
	if c.CanRead() {
		b.WriteByte('R')
	}
	if c.CanWrite() {
		b.WriteByte('W')
	}
	if c.CanInc() {
		b.WriteByte('I')
	}
	if c.CanDec() {
		b.WriteByte('D')
	}
	return b.String()
}

// renderParams writes the parameter-registry table to w. Columns:
// ID (4-digit hex), NAME, TYPE, UNIT (empty -> "-"), CAPS, DESCRIPTION.
// Two-space gutters; last column unpadded. Rows are emitted in input
// order — the caller is expected to sort if it cares (breezy.AllParams
// already returns sorted-by-ID).
func renderParams(w io.Writer, params []breezy.Param) {
	const (
		hID, hName, hType, hUnit, hCaps, hDesc = "ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION"
	)
	wID, wName, wType, wUnit, wCaps := len(hID), len(hName), len(hType), len(hUnit), len(hCaps)

	cells := make([][6]string, 0, len(params))
	for _, p := range params {
		idStr := fmt.Sprintf("0x%04X", uint16(p.ID))
		typeStr := p.Type.String()
		unit := p.Unit
		if unit == "" {
			unit = "-"
		}
		caps := capsString(p.Caps)
		row := [6]string{idStr, p.Name, typeStr, unit, caps, p.Description}
		if len(idStr) > wID {
			wID = len(idStr)
		}
		if len(p.Name) > wName {
			wName = len(p.Name)
		}
		if len(typeStr) > wType {
			wType = len(typeStr)
		}
		if len(unit) > wUnit {
			wUnit = len(unit)
		}
		if len(caps) > wCaps {
			wCaps = len(caps)
		}
		cells = append(cells, row)
	}

	fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
		wID, hID, wName, hName, wType, hType, wUnit, hUnit, wCaps, hCaps, hDesc)
	for _, r := range cells {
		fmt.Fprintf(w, "%-*s  %-*s  %-*s  %-*s  %-*s  %s\n",
			wID, r[0], wName, r[1], wType, r[2], wUnit, r[3], wCaps, r[4], r[5])
	}
}
