// SPDX-License-Identifier: GPL-3.0-or-later

// breezy is the operator CLI for the breezyd daemon. It speaks HTTP to
// the daemon for everything except `discover`, which performs a real
// LAN broadcast for first-time setup.
//
// The CLI surface is "subject before verb" so per-device verbs read
// naturally:
//
//	breezy playroom status
//	breezy playroom speed manual:30
//	breezy playroom mode regeneration
//
// Globals (`ls`, `discover`, `daemon-url`) are detected by checking the
// first arg against a small reserved-name set.
//
// Exit codes:
//   - 0 success
//   - 1 HTTP / daemon error (the daemon's {"error","code"} envelope is
//     decoded and rendered as `error: <msg> (<code>)`)
//   - 2 local usage error (bad args, validation failure before HTTP)
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hughobrien/breezyd/internal/config"
	"github.com/hughobrien/breezyd/pkg/breezy"
)

// defaultDaemonURL is the fall-back when no --daemon flag is given and
// no config file exists or the file lacks a [daemon].listen entry.
const defaultDaemonURL = "http://127.0.0.1:9876"

// httpTimeout bounds every HTTP request the CLI issues. The daemon
// itself bounds its UDP work to ~5 s, so 10 s leaves headroom for
// OS-level retries without making a hung daemon look like a hang.
const httpTimeout = 10 * time.Second

// discoverTimeout bounds the LAN broadcast in `breezy discover`.
const discoverTimeout = 3 * time.Second

// Build metadata. These are populated by goreleaser via -ldflags at build
// time; an unbuilt local binary reports "dev" / "none" / "unknown" so
// `breezy --version` is always meaningful.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It returns the process exit code so
// tests can assert on stdout/stderr without intercepting os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("breezy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	daemon := fs.String("daemon", "", "daemon URL (overrides config)")
	versionFlag := fs.Bool("version", false, "print version information and exit")

	if err := fs.Parse(args); err != nil {
		// flag prints its own message + usage; we just need the right code.
		return 2
	}

	if *versionFlag {
		fmt.Fprintf(stdout, "breezy %s (commit %s, built %s)\n", version, commit, date)
		return 0
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}

	daemonURL := resolveDaemonURL(*daemon)

	// Globals.
	switch rest[0] {
	case "ls":
		return cmdLs(daemonURL, stdout, stderr)
	case "discover":
		return cmdDiscover(stdout, stderr)
	case "daemon-url":
		fmt.Fprintln(stdout, daemonURL)
		return 0
	case "version":
		fmt.Fprintf(stdout, "breezy %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}

	// Per-device verbs: `breezy <name> <verb> [args...]`.
	if len(rest) < 2 {
		fmt.Fprintln(stderr, "usage: breezy <name> <verb> [args]")
		return 2
	}

	name, verb, vargs := rest[0], rest[1], rest[2:]

	switch verb {
	case "status":
		return cmdStatus(daemonURL, name, stdout, stderr)
	case "on":
		return cmdPower(daemonURL, name, true, stdout, stderr)
	case "off":
		return cmdPower(daemonURL, name, false, stdout, stderr)
	case "speed":
		return cmdSpeed(daemonURL, name, vargs, stdout, stderr)
	case "mode":
		return cmdMode(daemonURL, name, vargs, stdout, stderr)
	case "heater":
		return cmdHeater(daemonURL, name, vargs, stdout, stderr)
	case "reset-filter":
		return cmdResetFilter(daemonURL, name, stdout, stderr)
	case "reset-faults":
		return cmdResetFaults(daemonURL, name, stdout, stderr)
	case "faults":
		return cmdFaults(daemonURL, name, stdout, stderr)
	case "firmware":
		return cmdFirmware(daemonURL, name, stdout, stderr)
	case "efficiency":
		return cmdEfficiency(daemonURL, name, stdout, stderr)
	case "rtc":
		return cmdRtc(daemonURL, name, vargs, stdout, stderr)
	case "get":
		return cmdGet(daemonURL, name, vargs, stdout, stderr)
	case "set":
		return cmdSet(daemonURL, name, vargs, stdout, stderr)
	}

	fmt.Fprintf(stderr, "unknown verb: %s\n", verb)
	return 2
}

const usage = `breezy: control Vents Twinfresh Breezy ERVs via the breezyd daemon

Usage:
  breezy [--daemon URL] <name> <verb> [args]
  breezy [--daemon URL] <global>

Per-device verbs:
  status                show structured snapshot
  on | off              power on/off
  speed <1|2|3>         select preset
  speed manual:<pct>    manual % (10..100)
  mode <ventilation|regeneration|supply|extract>
  heater on|off
  reset-filter          reset filter timer
  reset-faults          clear active faults
  faults                list active faults
  firmware              show firmware version + build date
  efficiency            recovery efficiency %
  rtc                   show device clock
  rtc set <RFC3339>     set device clock
  get <param>           raw read (hex 0x25, "25", or registry name e.g. humidity)
  set <param> <hex>     raw write

Globals:
  ls                    one-line summary of every configured device
  discover              LAN broadcast (bypasses daemon)
  daemon-url            print the URL the CLI would use
`

// resolveDaemonURL chooses the daemon URL according to the precedence:
//
//  1. --daemon flag (override).
//  2. ~/.config/breezy/config.toml [daemon].listen.
//  3. defaultDaemonURL.
//
// Any error reading or parsing the config falls through to the default
// silently — running breezy on a fresh box without a config file should
// still be useful (e.g. `breezy daemon-url`).
func resolveDaemonURL(override string) string {
	if override != "" {
		return normalizeURL(override)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return defaultDaemonURL
	}
	cfg, err := config.Load(filepath.Join(home, ".config", "breezy", "config.toml"))
	if err != nil || cfg == nil || cfg.Daemon.Listen == "" {
		return defaultDaemonURL
	}
	return normalizeURL(cfg.Daemon.Listen)
}

// normalizeURL prepends http:// when the operator wrote bare host:port
// in the config (TOML files commonly do this).
func normalizeURL(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}

// ----------------------------------------------------------------------------
// HTTP plumbing
// ----------------------------------------------------------------------------

// httpJSON issues method url with body (if non-nil) marshalled as JSON,
// reads the entire response, and returns the status + raw bytes. The
// caller decodes success/error envelopes — we don't try to be clever
// about non-2xx here so unit tests can assert on whatever shape comes
// back.
func httpJSON(method, url string, body any) (int, []byte, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, nil, fmt.Errorf("encode body: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, &buf)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, raw, err
	}
	return resp.StatusCode, raw, nil
}

// errEnvelope mirrors the daemon's standard error shape.
type errEnvelope struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// renderErr prints an error to stderr in the canonical
// `error: <msg> (<code>)` form and returns exit code 1. It tolerates
// non-envelope responses (raw HTML, empty body, etc.) by falling back
// to "HTTP <status>".
func renderErr(stderr io.Writer, status int, raw []byte, transportErr error) int {
	if transportErr != nil {
		fmt.Fprintf(stderr, "error: %s\n", transportErr)
		return 1
	}
	var e errEnvelope
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		if e.Code == "" {
			fmt.Fprintf(stderr, "error: %s\n", e.Error)
		} else {
			fmt.Fprintf(stderr, "error: %s (%s)\n", e.Error, e.Code)
		}
		return 1
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		fmt.Fprintf(stderr, "error: HTTP %d\n", status)
	} else {
		fmt.Fprintf(stderr, "error: HTTP %d: %s\n", status, body)
	}
	return 1
}

// doSimple is the common path for verbs that POST a JSON body and
// expect either an `{"ok":true}` ack or an error envelope. On success
// it prints a short "ok" line; on failure it routes through renderErr.
func doSimple(method, url string, body any, ack string, stdout, stderr io.Writer) int {
	status, raw, err := httpJSON(method, url, body)
	if err != nil {
		return renderErr(stderr, status, raw, err)
	}
	if status >= 400 {
		return renderErr(stderr, status, raw, nil)
	}
	fmt.Fprintln(stdout, ack)
	return 0
}

// ----------------------------------------------------------------------------
// Per-device handlers
// ----------------------------------------------------------------------------

// cmdStatus pulls the structured snapshot and renders it in the format
// described in the spec. `live` and `configured` are surfaced
// side-by-side; the sensor-override warning fires when in_user_control
// is explicitly false.
func cmdStatus(daemonURL, name string, stdout, stderr io.Writer) int {
	url := fmt.Sprintf("%s/v1/devices/%s", daemonURL, name)
	status, raw, err := httpJSON(http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return renderErr(stderr, status, raw, err)
	}
	var snap snapshotResp
	if err := json.Unmarshal(raw, &snap); err != nil {
		fmt.Fprintf(stderr, "error: decode snapshot: %v\n", err)
		return 1
	}
	renderSnapshot(stdout, snap)
	return 0
}

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

// renderSnapshot walks the snapshot blocks and produces the
// human-friendly multi-line format. The header line includes firmware
// + last_poll relative; subsequent lines cover power, mode, speed,
// sensors, service. Sensor-override warning is printed on its own line
// just below the header when applicable.
func renderSnapshot(w io.Writer, s snapshotResp) {
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

// cmdPower posts {"on": <v>} to /power.
func cmdPower(daemonURL, name string, on bool, stdout, stderr io.Writer) int {
	url := fmt.Sprintf("%s/v1/devices/%s/power", daemonURL, name)
	body := map[string]any{"on": on}
	ack := "ok"
	return doSimple(http.MethodPost, url, body, ack, stdout, stderr)
}

// cmdSpeed parses the local `speed <preset>` / `speed manual:<pct>` form.
// We validate the manual floor (pct >= 10) locally so the user gets a
// clear error before the daemon round-trip.
func cmdSpeed(daemonURL, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: breezy <name> speed <1|2|3|manual:PCT>")
		return 2
	}
	arg := args[0]
	url := fmt.Sprintf("%s/v1/devices/%s/speed", daemonURL, name)

	if strings.HasPrefix(arg, "manual:") {
		raw := strings.TrimPrefix(arg, "manual:")
		pct, err := strconv.Atoi(raw)
		if err != nil {
			fmt.Fprintf(stderr, "speed manual: invalid percentage %q\n", raw)
			return 2
		}
		if pct < 10 {
			fmt.Fprintf(stderr, "speed manual: %d%% is below the firmware floor of 10%%\n", pct)
			return 2
		}
		if pct > 100 {
			fmt.Fprintf(stderr, "speed manual: %d%% is above 100%%\n", pct)
			return 2
		}
		return doSimple(http.MethodPost, url, map[string]any{"manual": pct}, "ok", stdout, stderr)
	}

	preset, err := strconv.Atoi(arg)
	if err != nil || preset < 1 || preset > 3 {
		fmt.Fprintln(stderr, "speed: must be 1, 2, 3, or manual:<10..100>")
		return 2
	}
	return doSimple(http.MethodPost, url, map[string]any{"preset": preset}, "ok", stdout, stderr)
}

// validModes is the set the daemon will accept; we mirror it locally
// so a typo doesn't waste a round-trip and produce a vaguer error.
var validModes = map[string]bool{
	"ventilation":  true,
	"regeneration": true,
	"supply":       true,
	"extract":      true,
}

func cmdMode(daemonURL, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: breezy <name> mode <ventilation|regeneration|supply|extract>")
		return 2
	}
	mode := strings.ToLower(args[0])
	if !validModes[mode] {
		fmt.Fprintf(stderr, "mode: %q is not one of: ventilation, regeneration, supply, extract\n", args[0])
		return 2
	}
	url := fmt.Sprintf("%s/v1/devices/%s/mode", daemonURL, name)
	return doSimple(http.MethodPost, url, map[string]any{"mode": mode}, "ok", stdout, stderr)
}

func cmdHeater(daemonURL, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: breezy <name> heater <on|off>")
		return 2
	}
	var on bool
	switch strings.ToLower(args[0]) {
	case "on":
		on = true
	case "off":
		on = false
	default:
		fmt.Fprintln(stderr, "heater: must be on or off")
		return 2
	}
	url := fmt.Sprintf("%s/v1/devices/%s/heater", daemonURL, name)
	return doSimple(http.MethodPost, url, map[string]any{"on": on}, "ok", stdout, stderr)
}

func cmdResetFilter(daemonURL, name string, stdout, stderr io.Writer) int {
	url := fmt.Sprintf("%s/v1/devices/%s/filter/reset", daemonURL, name)
	return doSimple(http.MethodPost, url, nil, "filter timer reset", stdout, stderr)
}

func cmdResetFaults(daemonURL, name string, stdout, stderr io.Writer) int {
	url := fmt.Sprintf("%s/v1/devices/%s/faults/reset", daemonURL, name)
	return doSimple(http.MethodPost, url, nil, "faults cleared", stdout, stderr)
}

// cmdFaults pretty-prints the daemon's faults list. Empty list ->
// "no active faults" (with newline).
func cmdFaults(daemonURL, name string, stdout, stderr io.Writer) int {
	url := fmt.Sprintf("%s/v1/devices/%s/faults", daemonURL, name)
	status, raw, err := httpJSON(http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return renderErr(stderr, status, raw, err)
	}
	var resp struct {
		Faults []struct {
			Code int    `json:"code"`
			Kind string `json:"kind"`
		} `json:"faults"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(stderr, "error: decode faults: %v\n", err)
		return 1
	}
	if len(resp.Faults) == 0 {
		fmt.Fprintln(stdout, "no active faults")
		return 0
	}
	for _, f := range resp.Faults {
		fmt.Fprintf(stdout, "  %s: code %d\n", f.Kind, f.Code)
	}
	return 0
}

// cmdFirmware prints "<version>  built <date>" — both fields come
// straight from the daemon's GET /firmware route.
func cmdFirmware(daemonURL, name string, stdout, stderr io.Writer) int {
	url := fmt.Sprintf("%s/v1/devices/%s/firmware", daemonURL, name)
	status, raw, err := httpJSON(http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return renderErr(stderr, status, raw, err)
	}
	var resp struct {
		Version   string `json:"version"`
		BuildDate string `json:"build_date"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(stderr, "error: decode firmware: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "version %s  built %s\n", resp.Version, resp.BuildDate)
	return 0
}

func cmdEfficiency(daemonURL, name string, stdout, stderr io.Writer) int {
	url := fmt.Sprintf("%s/v1/devices/%s/efficiency", daemonURL, name)
	status, raw, err := httpJSON(http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return renderErr(stderr, status, raw, err)
	}
	var resp struct {
		Pct int `json:"recovery_efficiency_pct"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(stderr, "error: decode efficiency: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "recovery efficiency %d%%\n", resp.Pct)
	return 0
}

// cmdRtc handles both `rtc` (show) and `rtc set <RFC3339>` (set).
// Showing reads the cached snapshot's rtc_time/rtc_calendar via
// /params/0x6F + /params/0x70; setting POSTs to /rtc.
func cmdRtc(daemonURL, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return cmdRtcShow(daemonURL, name, stdout, stderr)
	}
	if len(args) == 2 && args[0] == "set" {
		t, err := time.Parse(time.RFC3339, args[1])
		if err != nil {
			fmt.Fprintf(stderr, "rtc set: parse %q: %v\n", args[1], err)
			return 2
		}
		url := fmt.Sprintf("%s/v1/devices/%s/rtc", daemonURL, name)
		body := map[string]any{"time": t.Format(time.RFC3339)}
		return doSimple(http.MethodPost, url, body, "rtc set", stdout, stderr)
	}
	fmt.Fprintln(stderr, "usage: breezy <name> rtc [set <RFC3339>]")
	return 2
}

// cmdRtcShow asks the daemon for params 0x6F + 0x70 (live UDP reads;
// the daemon's /params/{id} bypasses cache) and renders them. We don't
// just synthesize from the snapshot because rtc_time/rtc_calendar
// aren't in defaultReadIDs and would always be missing from the cache.
func cmdRtcShow(daemonURL, name string, stdout, stderr io.Writer) int {
	timeBytes, code, err := fetchParamHex(daemonURL, name, 0x006F)
	if err != nil {
		fmt.Fprintf(stderr, "error: read rtc_time: %s\n", err)
		return code
	}
	dateBytes, code, err := fetchParamHex(daemonURL, name, 0x0070)
	if err != nil {
		fmt.Fprintf(stderr, "error: read rtc_calendar: %s\n", err)
		return code
	}
	tv, err := breezy.Param{Type: breezy.TypeTimeOfDay}.Decode(timeBytes)
	if err != nil {
		fmt.Fprintf(stderr, "error: decode rtc_time: %v\n", err)
		return 1
	}
	dv, err := breezy.Param{Type: breezy.TypeDate}.Decode(dateBytes)
	if err != nil {
		fmt.Fprintf(stderr, "error: decode rtc_calendar: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s %s\n", dv.String(), tv.String())
	return 0
}

// fetchParamHex returns the raw LE bytes of a parameter from the
// daemon's GET /params/{id}. The exit-code-on-error pattern lets the
// caller propagate (1 for daemon error, 2 for usage) without re-parsing.
func fetchParamHex(daemonURL, name string, id breezy.ParamID) ([]byte, int, error) {
	url := fmt.Sprintf("%s/v1/devices/%s/params/0x%04X", daemonURL, name, uint16(id))
	status, raw, err := httpJSON(http.MethodGet, url, nil)
	if err != nil {
		return nil, 1, err
	}
	if status >= 400 {
		var e errEnvelope
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			if e.Code != "" {
				return nil, 1, fmt.Errorf("%s (%s)", e.Error, e.Code)
			}
			return nil, 1, errors.New(e.Error)
		}
		return nil, 1, fmt.Errorf("HTTP %d: %s", status, strings.TrimSpace(string(raw)))
	}
	var resp struct {
		Hex string `json:"hex"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, 1, fmt.Errorf("decode response: %w", err)
	}
	b, err := hex.DecodeString(resp.Hex)
	if err != nil {
		return nil, 1, fmt.Errorf("decode hex %q: %w", resp.Hex, err)
	}
	return b, 0, nil
}

// cmdGet accepts either a hex id ("0x25", "25") or a registry name
// ("humidity", "co2_threshold"). It resolves names → IDs via the
// pkg/breezy registry and renders the decoded value, falling back to
// raw hex when the type doesn't decode.
func cmdGet(daemonURL, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: breezy <name> get <param>")
		return 2
	}
	id, p, ok, err := resolveParam(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "get: %v\n", err)
		return 2
	}
	url := fmt.Sprintf("%s/v1/devices/%s/params/0x%04X", daemonURL, name, uint16(id))
	status, raw, herr := httpJSON(http.MethodGet, url, nil)
	if herr != nil || status >= 400 {
		return renderErr(stderr, status, raw, herr)
	}
	var resp struct {
		ID    string `json:"id"`
		Hex   string `json:"hex"`
		Name  string `json:"name"`
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(stderr, "error: decode get response: %v\n", err)
		return 1
	}

	// Prefer the registry's typed Decode for a cleaner display when we
	// know the param; otherwise fall through to whatever the daemon
	// offered (`resp.Value`/`resp.Hex`).
	display := resp.Value
	if ok && resp.Hex != "" {
		if b, decErr := hex.DecodeString(resp.Hex); decErr == nil {
			if v, dErr := p.Decode(b); dErr == nil {
				display = v.String()
			}
		}
	}
	if display == "" {
		display = resp.Hex
	}

	label := resp.ID
	if resp.Name != "" {
		label = fmt.Sprintf("%s (%s)", resp.Name, resp.ID)
	}
	unit := ""
	if ok && p.Unit != "" {
		unit = " " + p.Unit
	}
	fmt.Fprintf(stdout, "%s = %s%s\n", label, display, unit)
	return 0
}

// cmdSet posts a hex blob to /params/{id}. We resolve names to IDs the
// same way as get, and short-circuit on read-only params before any
// HTTP traffic.
func cmdSet(daemonURL, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: breezy <name> set <param> <hex>")
		return 2
	}
	id, p, ok, err := resolveParam(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "set: %v\n", err)
		return 2
	}
	if ok && !p.Caps.CanWrite() {
		fmt.Fprintf(stderr, "set: param %s (0x%04X) is read-only\n", p.Name, uint16(id))
		return 2
	}
	hexStr := strings.TrimPrefix(strings.ToLower(args[1]), "0x")
	if _, err := hex.DecodeString(hexStr); err != nil {
		fmt.Fprintf(stderr, "set: invalid hex %q: %v\n", args[1], err)
		return 2
	}
	url := fmt.Sprintf("%s/v1/devices/%s/params/0x%04X", daemonURL, name, uint16(id))
	body := map[string]any{"hex": hexStr}
	return doSimple(http.MethodPost, url, body, "ok", stdout, stderr)
}

// resolveParam accepts a hex id ("0x25", "25") OR a registry name
// (case-insensitive) and returns the ID + Param (when known). The
// `known` flag lets the caller skip type-aware behaviour for unknown
// params without failing — the daemon will validate.
func resolveParam(s string) (id breezy.ParamID, p breezy.Param, known bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, breezy.Param{}, false, errors.New("empty param")
	}
	// Try numeric first: hex with or without 0x prefix. If the bare
	// string matches a registry name, prefer the name lookup; that way
	// `breezy get power` works even though "power" coincidentally
	// parses as nothing useful.
	if rp, ok := breezy.LookupByName(s); ok {
		return rp.ID, rp, true, nil
	}
	low := strings.ToLower(s)
	if !strings.HasPrefix(low, "0x") {
		// Try hex without prefix.
		if n, err := strconv.ParseUint(s, 16, 16); err == nil {
			id := breezy.ParamID(n)
			rp, ok := breezy.LookupByID(id)
			return id, rp, ok, nil
		}
	}
	n, err := strconv.ParseUint(s, 0, 16)
	if err != nil {
		return 0, breezy.Param{}, false, fmt.Errorf("unknown param %q (not a registry name or hex id)", s)
	}
	id = breezy.ParamID(n)
	rp, ok := breezy.LookupByID(id)
	return id, rp, ok, nil
}

// ----------------------------------------------------------------------------
// Globals
// ----------------------------------------------------------------------------

// cmdLs prints a small fixed-width table of every device the daemon
// knows about. Columns: name, IP, power, mode, last_poll-relative.
func cmdLs(daemonURL string, stdout, stderr io.Writer) int {
	url := daemonURL + "/v1/devices"
	status, raw, err := httpJSON(http.MethodGet, url, nil)
	if err != nil || status >= 400 {
		return renderErr(stderr, status, raw, err)
	}
	var resp struct {
		Devices []struct {
			Name      string `json:"name"`
			ID        string `json:"id"`
			IP        string `json:"ip"`
			LastPoll  string `json:"last_poll"`
			Power     *bool  `json:"power"`
			Mode      string `json:"airflow_mode"`
			Reachable bool   `json:"reachable"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(stderr, "error: decode device list: %v\n", err)
		return 1
	}
	if len(resp.Devices) == 0 {
		fmt.Fprintln(stdout, "(no devices configured)")
		return 0
	}

	// Sort for stable output.
	sort.Slice(resp.Devices, func(i, j int) bool {
		return resp.Devices[i].Name < resp.Devices[j].Name
	})

	// Compute column widths.
	wName, wIP, wPower, wMode := len("NAME"), len("IP"), len("POWER"), len("MODE")
	rows := make([][]string, 0, len(resp.Devices))
	for _, d := range resp.Devices {
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
		rows = append(rows, row)
	}

	fmt.Fprintf(stdout, "%-*s  %-*s  %-*s  %-*s  %s\n", wName, "NAME", wIP, "IP", wPower, "POWER", wMode, "MODE", "LAST POLL")
	for _, r := range rows {
		fmt.Fprintf(stdout, "%-*s  %-*s  %-*s  %-*s  %s\n", wName, r[0], wIP, r[1], wPower, r[2], wMode, r[3], r[4])
	}
	return 0
}

// cmdDiscover does a real LAN broadcast (no daemon involved). This is
// the bootstrap path: the user runs it once, copies the device IDs
// into config.toml, and from then on lets the daemon manage things.
func cmdDiscover(stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), discoverTimeout)
	defer cancel()
	found, err := breezy.Discover(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "discover: %v\n", err)
		return 1
	}
	if len(found) == 0 {
		fmt.Fprintln(stdout, "no Breezy devices found on the LAN")
		return 0
	}
	for _, f := range found {
		fmt.Fprintf(stdout, "%s  id=%s  type=%d\n", f.IP, f.DeviceID, f.UnitType)
	}
	return 0
}
