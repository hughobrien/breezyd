// SPDX-License-Identifier: GPL-3.0-or-later

// Per-verb command handlers (cmd*) for the breezy CLI. Each one is a
// thin wrapper that calls the appropriate backend method and (for read
// verbs) hands the parsed response off to a renderer in render.go.
//
// main.go owns dispatch and backend construction; this file owns "what
// each verb does". Per-verb handlers take a backend instead of a daemon
// URL so the same handlers work with both daemonBackend (HTTP) and
// directBackend (UDP, Task 2).
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
func cmdStatus(b backend, name string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := b.Status(ctx, name)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	renderStatus(stdout, s)
	return 0
}

// cmdPower posts {"on": <v>} to /power.
func cmdPower(b backend, name string, on bool, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Power(ctx, name, on); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return 0
}

// cmdSpeed parses the local `speed <preset>` / `speed manual:<pct>` form.
// We validate the manual floor (pct >= 10) locally so the user gets a
// clear error before the backend round-trip.
func cmdSpeed(b backend, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: breezy <name> speed <1|2|3|manual:PCT>")
		return 2
	}
	arg := args[0]

	if strings.HasPrefix(arg, "manual:") {
		raw := strings.TrimPrefix(arg, "manual:")
		pct, err := strconv.Atoi(raw)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "speed manual: invalid percentage %q\n", raw)
			return 2
		}
		if pct < 10 {
			_, _ = fmt.Fprintf(stderr, "speed manual: %d%% is below the firmware floor of 10%%\n", pct)
			return 2
		}
		if pct > 100 {
			_, _ = fmt.Fprintf(stderr, "speed manual: %d%% is above 100%%\n", pct)
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := b.SpeedManual(ctx, name, pct); err != nil {
			_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, "ok")
		return 0
	}

	preset, err := strconv.Atoi(arg)
	if err != nil || preset < 1 || preset > 3 {
		_, _ = fmt.Fprintln(stderr, "speed: must be 1, 2, 3, or manual:<10..100>")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.SpeedPreset(ctx, name, preset); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return 0
}

// validModes is the set the daemon will accept; we mirror it locally
// so a typo doesn't waste a round-trip and produce a vaguer error.
var validModes = map[string]bool{
	"ventilation":  true,
	"regeneration": true,
	"supply":       true,
	"extract":      true,
}

func cmdMode(b backend, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: breezy <name> mode <ventilation|regeneration|supply|extract>")
		return 2
	}
	mode := strings.ToLower(args[0])
	if !validModes[mode] {
		_, _ = fmt.Fprintf(stderr, "mode: %q is not one of: ventilation, regeneration, supply, extract\n", args[0])
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Mode(ctx, name, mode); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return 0
}

func cmdHeater(b backend, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: breezy <name> heater <on|off>")
		return 2
	}
	var on bool
	switch strings.ToLower(args[0]) {
	case "on":
		on = true
	case "off":
		on = false
	default:
		_, _ = fmt.Fprintln(stderr, "heater: must be on or off")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Heater(ctx, name, on); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return 0
}

// validTimerModes is the set the daemon will accept; we mirror it locally
// so a typo doesn't waste a round-trip and produce a vaguer error.
var validTimerModes = map[string]bool{
	"off":   true,
	"night": true,
	"turbo": true,
}

func cmdTimer(b backend, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: breezy <name> timer <off|night|turbo>")
		return 2
	}
	mode := strings.ToLower(args[0])
	if !validTimerModes[mode] {
		_, _ = fmt.Fprintf(stderr, "timer: %q is not one of: off, night, turbo\n", args[0])
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Timer(ctx, name, mode); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return 0
}

func cmdResetFilter(b backend, name string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.ResetFilter(ctx, name); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "filter timer reset")
	return 0
}

func cmdResetFaults(b backend, name string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.ResetFaults(ctx, name); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "faults cleared")
	return 0
}

// cmdFaults pretty-prints the backend's faults list. Empty list ->
// "no active faults" (with newline).
func cmdFaults(b backend, name string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	faults, err := b.Faults(ctx, name)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	renderFaults(stdout, faults)
	return 0
}

// cmdFirmware prints "<version>  built <date>" — both fields come
// straight from the backend's firmware response.
func cmdFirmware(b backend, name string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	version, buildDate, err := b.Firmware(ctx, name)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	renderFirmware(stdout, version, buildDate)
	return 0
}

func cmdEfficiency(b backend, name string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pct, err := b.Efficiency(ctx, name)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "recovery efficiency %d%%\n", pct)
	return 0
}

// cmdRtc handles both `rtc` (show) and `rtc set <RFC3339>` (set).
// Showing reads params 0x6F + 0x70 via GetParam; setting calls SetRTC.
func cmdRtc(b backend, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return cmdRtcShow(b, name, stdout, stderr)
	}
	if len(args) == 2 && args[0] == "set" {
		t, err := time.Parse(time.RFC3339, args[1])
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "rtc set: parse %q: %v\n", args[1], err)
			return 2
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := b.SetRTC(ctx, name, t); err != nil {
			_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
			return 1
		}
		_, _ = fmt.Fprintln(stdout, "rtc set")
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "usage: breezy <name> rtc [set <RFC3339>]")
	return 2
}

// cmdRtcShow asks for params 0x6F + 0x70 (live reads; bypasses cache
// in daemon mode) and renders them. We don't synthesize from the
// snapshot because rtc_time/rtc_calendar aren't in defaultReadIDs and
// would always be missing from the cache.
func cmdRtcShow(b backend, name string, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	timeBytes, err := b.GetParam(ctx, name, 0x006F)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: read rtc_time: %s\n", err)
		return 1
	}
	dateBytes, err := b.GetParam(ctx, name, 0x0070)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: read rtc_calendar: %s\n", err)
		return 1
	}
	tv, err := breezy.Param{Type: breezy.TypeTimeOfDay}.Decode(timeBytes)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: decode rtc_time: %v\n", err)
		return 1
	}
	dv, err := breezy.Param{Type: breezy.TypeDate}.Decode(dateBytes)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: decode rtc_calendar: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "%s %s\n", dv.String(), tv.String())
	return 0
}

// cmdGet accepts either a hex id ("0x25", "25") or a registry name
// ("humidity", "co2_threshold"). It resolves names → IDs via the
// pkg/breezy registry and renders the decoded value, falling back to
// raw hex when the type doesn't decode.
func cmdGet(b backend, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: breezy <name> get <param>")
		return 2
	}
	id, p, ok, err := resolveParam(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "get: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rawBytes, herr := b.GetParam(ctx, name, id)
	if herr != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", herr)
		return 1
	}

	// Prefer the registry's typed Decode when the param is known;
	// fall back to raw hex for unknown IDs.
	rawHex := hex.EncodeToString(rawBytes)
	display := rawHex
	if ok {
		if v, dErr := p.Decode(rawBytes); dErr == nil {
			display = v.String()
		}
	}

	idStr := fmt.Sprintf("0x%04X", uint16(id))
	label := idStr
	if ok && p.Name != "" {
		label = fmt.Sprintf("%s (%s)", p.Name, idStr)
	}
	unit := ""
	if ok && p.Unit != "" {
		unit = " " + p.Unit
	}
	_, _ = fmt.Fprintf(stdout, "%s = %s%s\n", label, display, unit)
	return 0
}

// cmdSet posts a hex blob to the backend. We resolve names to IDs the
// same way as get, and short-circuit on read-only params before any
// backend traffic.
func cmdSet(b backend, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "usage: breezy <name> set <param> <hex>")
		return 2
	}
	id, p, ok, err := resolveParam(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "set: %v\n", err)
		return 2
	}
	if ok && !p.Caps.CanWrite() {
		_, _ = fmt.Fprintf(stderr, "set: param %s (0x%04X) is read-only\n", p.Name, uint16(id))
		return 2
	}
	hexStr := strings.TrimPrefix(strings.ToLower(args[1]), "0x")
	valueBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "set: invalid hex %q: %v\n", args[1], err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.SetParam(ctx, name, id, valueBytes); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return 0
}

// resolveParam accepts a hex id ("0x25", "25") OR a registry name
// (case-insensitive) and returns the ID + Param (when known). The
// `known` flag lets the caller skip type-aware behaviour for unknown
// params without failing — the backend will validate.
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

// cmdLs prints a small fixed-width table of every device the backend
// knows about. Columns: name, IP, power, mode, last_poll-relative.
func cmdLs(b backend, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := b.Devices(ctx)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	renderLs(stdout, rows)
	return 0
}

// cmdParam prints the static parameter registry as a wide table. Pure
// metadata read; no backend round-trip. Exit code is always 0 (the
// registry is built into the binary).
func cmdParam(stdout io.Writer) int {
	renderParams(stdout, breezy.AllParams())
	return 0
}

// cmdDiscover queries the LAN for Breezy devices. Two modes plus an
// optional password override:
//
//   - No positional args: UDP broadcast to the limited (255.255.255.255)
//     and subnet-directed broadcast addresses. Bootstrap path.
//   - Positional args: each is a target IP (or host:port); the wildcard
//     discovery request is sent unicast to each. Useful when the LAN
//     drops broadcasts (Wi-Fi AP isolation, mesh hops, NAT) but unicast
//     works — pinging the device works, broadcast doesn't.
//   - -p / --password PASSWORD: send the wildcard request with the
//     supplied password instead of the factory default "1111". The
//     manual claims discovery is unauthenticated, but some firmware
//     versions appear to drop wildcard requests with a password
//     mismatch — pass your real password if discovery returns no
//     responders even though the units are reachable.
//
// In all modes output is one line per responder: "<ip>  id=<id>  type=<n>".
func cmdDiscover(args []string, stdout, stderr io.Writer) int {
	password := breezy.DefaultDiscoveryPassword
	targets := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-p" || a == "--password":
			if i+1 >= len(args) {
				_, _ = fmt.Fprintln(stderr, "discover: -p needs a password value")
				return 2
			}
			password = args[i+1]
			i++
		case strings.HasPrefix(a, "--password="):
			password = strings.TrimPrefix(a, "--password=")
		default:
			targets = append(targets, a)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), discoverTimeout)
	defer cancel()

	var found []breezy.Found
	var err error
	if len(targets) == 0 {
		found, err = breezy.DiscoverWithPassword(ctx, password)
	} else {
		normalised := make([]string, len(targets))
		for i, t := range targets {
			if !strings.Contains(t, ":") {
				t += ":4000"
			}
			normalised[i] = t
		}
		found, err = breezy.DiscoverAtWithPassword(ctx, normalised, password)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "discover: %v\n", err)
		return 1
	}
	if len(found) == 0 {
		_, _ = fmt.Fprintln(stdout, "no Breezy devices found")
		_, _ = fmt.Fprintln(stdout, "  things to check:")
		_, _ = fmt.Fprintln(stdout, "    - UDP/4000 is open on this host (a local firewall drop")
		_, _ = fmt.Fprintln(stdout, "      looks identical to no devices answering)")
		_, _ = fmt.Fprintln(stdout, "    - if your LAN drops UDP broadcasts (Wi-Fi AP isolation,")
		_, _ = fmt.Fprintln(stdout, "      VLANs, mesh hops), pass each device's IP directly:")
		_, _ = fmt.Fprintln(stdout, "      breezy discover 192.168.1.148 192.168.1.152")
		_, _ = fmt.Fprintln(stdout, "    - if your devices use a non-default password, pass")
		_, _ = fmt.Fprintln(stdout, "      -p PASSWORD — some firmware drops mismatched wildcard")
		_, _ = fmt.Fprintln(stdout, "      requests despite the spec saying discovery is unauthenticated")
		return 0
	}
	for _, f := range found {
		_, _ = fmt.Fprintf(stdout, "%s  id=%s  type=%d (%s)\n", f.IP, f.DeviceID, f.UnitType, breezy.UnitTypeName(f.UnitType))
	}
	return 0
}
