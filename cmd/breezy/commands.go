// SPDX-License-Identifier: GPL-3.0-or-later

// Per-verb command handlers (cmd*) for the breezy CLI. Each one is a
// thin wrapper that builds the daemon URL, issues an HTTP call via
// httpJSON, and (for read verbs) hands the parsed response off to a
// renderer in render.go.
//
// main.go owns dispatch and HTTP plumbing; this file owns "what each
// verb does" so reading the surface area doesn't require scrolling
// through resolveDaemonURL or doSimple.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

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
	renderStatus(stdout, snap)
	return 0
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
		Faults []fault `json:"faults"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(stderr, "error: decode faults: %v\n", err)
		return 1
	}
	renderFaults(stdout, resp.Faults)
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
	renderFirmware(stdout, resp.Version, resp.BuildDate)
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
	rows := make([]lsRow, 0, len(resp.Devices))
	for _, d := range resp.Devices {
		rows = append(rows, lsRow{
			Name:      d.Name,
			ID:        d.ID,
			IP:        d.IP,
			LastPoll:  d.LastPoll,
			Power:     d.Power,
			Mode:      d.Mode,
			Reachable: d.Reachable,
		})
	}
	renderLs(stdout, rows)
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
