// SPDX-License-Identifier: GPL-3.0-or-later

// Tests for the breezy CLI. Each verb gets a table case that spins up
// an httptest.Server stubbing the daemon side: we verify the request
// shape the CLI sends (method, path, body), then we verify the CLI's
// stdout/stderr handling for both success and error responses.
//
// We deliberately avoid testing the wire format details for things
// already tested in pkg/breezy (frame codec, registry, discovery).
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hughobrien/breezyd/internal/config"
	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
	"github.com/matryer/is"
)

// stub records what the test server received so the test can assert.
type stub struct {
	method string
	path   string
	body   map[string]any
}

// recordingHandler returns an http.Handler that captures the incoming
// request into *got, then writes status + JSON body back. body may be
// nil to send "{}".
func recordingHandler(t *testing.T, got *stub, status int, respBody any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &got.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if respBody == nil {
			_, _ = w.Write([]byte("{}"))
			return
		}
		_ = json.NewEncoder(w).Encode(respBody)
	})
}

// runCLI runs the CLI against a test server and returns the exit code,
// stdout, and stderr.
func runCLI(t *testing.T, server *httptest.Server, args ...string) (int, string, string) {
	t.Helper()
	full := append([]string{"--daemon", server.URL}, args...)
	var stdout, stderr bytes.Buffer
	code := run(full, &stdout, &stderr, nil)
	return code, stdout.String(), stderr.String()
}

// TestPower covers `breezy <name> on` and `... off`: each must POST
// /v1/devices/<name>/power with `{"on":<bool>}`.
func TestPower(t *testing.T) {
	for _, tc := range []struct {
		verb   string
		wantOn bool
	}{
		{"on", true},
		{"off", false},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			is := is.New(t)
			var got stub
			srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
			defer srv.Close()

			code, stdout, _ := runCLI(t, srv, "playroom", tc.verb)
			is.Equal(code, 0)
			is.Equal(got.method, "POST")
			is.Equal(got.path, "/v1/devices/playroom/power")
			gotOn, _ := got.body["on"].(bool)
			is.Equal(gotOn, tc.wantOn) // body on flag must match verb
			is.True(strings.Contains(stdout, "ok"))
		})
	}
}

// TestSpeedPreset / TestSpeedManual cover the local validation +
// outgoing body shape for `speed <preset>` and `speed manual:<pct>`.
func TestSpeedPreset(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "speed", "2")
	is.Equal(code, 0)
	is.Equal(got.path, "/v1/devices/playroom/speed")
	v, _ := got.body["preset"].(float64)
	is.Equal(int(v), 2) // preset value in body
}

func TestSpeedManual(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "speed", "manual:30")
	is.Equal(code, 0)
	v, _ := got.body["manual"].(float64)
	is.Equal(int(v), 30) // manual pct in body
}

// TestSpeedManualBelowFloor enforces the spec's local-rejection rule:
// pct < 10 must fail BEFORE any HTTP request, with exit code 2.
func TestSpeedManualBelowFloor(t *testing.T) {
	is := is.New(t)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "speed", "manual:5")
	is.Equal(code, 2)                          // local floor check exit
	is.True(!called)                           // CLI must not hit daemon
	is.True(strings.Contains(stderr, "floor")) // stderr should mention floor
}

func TestSpeedBadArg(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "speed", "9")
	is.Equal(code, 2)
}

// TestModeValidation exercises the local check against the four
// allowed mode names.
func TestModeValidation(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	for _, m := range []string{"ventilation", "regeneration", "supply", "extract"} {
		got = stub{}
		code, _, _ := runCLI(t, srv, "playroom", "mode", m)
		is.Equal(code, 0)             // mode m should succeed
		is.Equal(got.body["mode"], m) // body mode must match arg
	}
	// Bogus.
	code, _, stderr := runCLI(t, srv, "playroom", "mode", "fluff")
	is.Equal(code, 2)
	is.True(strings.Contains(stderr, "ventilation")) // stderr should list valid modes
}

func TestTimerValidation(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	for _, m := range []string{"off", "night", "turbo"} {
		got = stub{}
		code, _, _ := runCLI(t, srv, "playroom", "timer", m)
		is.Equal(code, 0)
		is.Equal(got.path, "/v1/devices/playroom/timer")
		is.Equal(got.body["mode"], m)
	}
	// Bogus mode → exit 2, stderr lists valid modes.
	code, _, stderr := runCLI(t, srv, "playroom", "timer", "fluff")
	is.Equal(code, 2)
	is.True(strings.Contains(stderr, "off, night, turbo"))
	// Missing arg → exit 2, usage message.
	code, _, stderr = runCLI(t, srv, "playroom", "timer")
	is.Equal(code, 2)
	is.True(strings.Contains(stderr, "usage:"))
}

func TestHeater(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "heater", "on")
	is.Equal(code, 0)
	is.Equal(got.path, "/v1/devices/playroom/heater")
	is.Equal(got.body["on"], true)

	// `off` is the symmetric happy path.
	got = stub{}
	code, _, _ = runCLI(t, srv, "playroom", "heater", "off")
	is.Equal(code, 0)
	is.Equal(got.body["on"], false)
}

// TestHeater_BadArg pins that an unrecognised heater arg exits 2 with a
// usage-style message and does NOT round-trip to the daemon.
func TestHeater_BadArg(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()

	code, _, stderr := runCLI(t, srv, "playroom", "heater", "junk")
	is.Equal(code, 2)
	is.True(strings.Contains(stderr, "must be on or off")) // validation message
	is.Equal(got.path, "")                                 // must not have round-tripped
}

// TestHeater_MissingArg pins that `breezy <name> heater` (no on/off)
// exits 2 with a usage hint, no daemon round-trip.
func TestHeater_MissingArg(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()

	code, _, stderr := runCLI(t, srv, "playroom", "heater")
	is.Equal(code, 2)
	is.True(strings.Contains(stderr, "usage:")) // usage hint
	is.Equal(got.path, "")                      // no daemon traffic on missing arg
}

// TestModeValidation_CaseInsensitive pins the case-insensitive
// canonicalisation in cmdMode: an uppercase arg lands as the canonical
// lowercase mode in the body sent to the daemon.
func TestModeValidation_CaseInsensitive(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()

	for _, in := range []string{"REGENERATION", "Regeneration", "rEGENERATION"} {
		got = stub{}
		code, _, _ := runCLI(t, srv, "playroom", "mode", in)
		is.Equal(code, 0)
		is.Equal(got.body["mode"], "regeneration") // any case must canonicalise
	}
}

// TestHeater_CaseInsensitive pins the same canonicalisation on cmdHeater.
func TestHeater_CaseInsensitive(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()

	for _, in := range []string{"ON", "On", "oN"} {
		got = stub{}
		code, _, _ := runCLI(t, srv, "playroom", "heater", in)
		is.Equal(code, 0)
		is.Equal(got.body["on"], true)
	}
	for _, in := range []string{"OFF", "Off"} {
		got = stub{}
		code, _, _ := runCLI(t, srv, "playroom", "heater", in)
		is.Equal(code, 0)
		is.Equal(got.body["on"], false)
	}
}

// TestCLI_Threshold drives `breezy <name> threshold <kind> <value>` end-to-end
// through directBackend → fakedevice UDP, then asserts the exact bytes
// landed in the expected param ID and only that ID.
func TestCLI_Threshold(t *testing.T) {
	for _, c := range []struct {
		kind  string
		value string
		hex   string
		id    breezy.ParamID
	}{
		{"humidity", "65", "41", 0x0019},
		{"co2", "1500", "dc05", 0x001A},
		{"voc", "200", "c800", 0x031F},
	} {
		t.Run(c.kind, func(t *testing.T) {
			is := is.New(t)
			fake := startFakeDevice(t)
			devices := map[string]config.Device{
				"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
			}
			code, _, _ := runStandalone(t, devices, "playroom", "threshold", c.kind, c.value)
			is.Equal(code, 0)
			got, ok := fake.Value(c.id)
			is.True(ok)          // value must be written at the expected param id
			is.Equal(got, c.hex) // hex encoding at param id
		})
	}
}

// TestCLI_Threshold_Usage: missing args must exit 2 and print usage,
// with no backend round-trip.
func TestCLI_Threshold_Usage(t *testing.T) {
	is := is.New(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: "127.0.0.1:0"},
	}
	code, _, stderr := runStandalone(t, devices, "playroom", "threshold")
	is.Equal(code, 2) // usage error
	is.True(strings.Contains(stderr, "usage:"))
}

// TestCLI_Threshold_OutOfRange: value beyond the firmware-accepted range
// surfaces from breezy.SetThresholdConfig as ErrInvalidArg, which the CLI
// renders as exit code 1 (backend error), distinct from local usage (2).
func TestCLI_Threshold_OutOfRange(t *testing.T) {
	is := is.New(t)
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, _, _ := runStandalone(t, devices, "playroom", "threshold", "humidity", "90")
	is.Equal(code, 1) // backend-side validation rejection
}

// TestCLI_AutoFan drives `breezy <name> auto-fan <kind> on|off` end-to-end
// and asserts the resulting enable byte at the matching enable-flag param.
func TestCLI_AutoFan(t *testing.T) {
	for _, c := range []struct {
		kind  string
		state string
		hex   string
		id    breezy.ParamID
	}{
		{"humidity", "on", "01", 0x000F},
		{"humidity", "off", "00", 0x000F},
		{"co2", "on", "01", 0x0011},
		{"voc", "off", "00", 0x0315},
	} {
		t.Run(c.kind+"_"+c.state, func(t *testing.T) {
			is := is.New(t)
			fake := startFakeDevice(t)
			devices := map[string]config.Device{
				"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
			}
			code, _, _ := runStandalone(t, devices, "playroom", "auto-fan", c.kind, c.state)
			is.Equal(code, 0)
			got, ok := fake.Value(c.id)
			is.True(ok) // value written at expected param id
			is.Equal(got, c.hex)
		})
	}
}

// TestCLI_AutoFan_BadState: a state that isn't on/off must exit 2 with no
// backend round-trip. We deliberately pass a junk IP — if the dispatch
// logic ever reaches the backend, the test will fail with a UDP error
// instead of the expected usage exit.
func TestCLI_AutoFan_BadState(t *testing.T) {
	is := is.New(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: "127.0.0.1:0"},
	}
	code, _, _ := runStandalone(t, devices, "playroom", "auto-fan", "humidity", "yes")
	is.Equal(code, 2) // usage error
}

// TestCLI_Threshold_Daemon and TestCLI_AutoFan_Daemon assert that the CLI
// uses the right HTTP path + body shape when talking to the daemon. The
// stub records the incoming request so we can verify the contract; the
// directBackend path is covered by the standalone tests above.
func TestCLI_Threshold_Daemon(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "threshold", "co2", "1500")
	is.Equal(code, 0)
	is.Equal(got.method, "POST")
	is.Equal(got.path, "/v1/devices/playroom/threshold")
	is.Equal(got.body["kind"], "co2")
	v, _ := got.body["value"].(float64)
	is.Equal(int(v), 1500)
	// "enabled" key must be ABSENT for threshold-only set; sending it as
	// null/false would let the daemon misread the intent.
	_, present := got.body["enabled"]
	is.True(!present) // enabled key must be absent for threshold-only set
}

func TestCLI_AutoFan_Daemon(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "auto-fan", "humidity", "off")
	is.Equal(code, 0)
	is.Equal(got.method, "POST")
	is.Equal(got.path, "/v1/devices/playroom/threshold")
	is.Equal(got.body["kind"], "humidity")
	is.Equal(got.body["enabled"], false)
	// "value" must be absent for enable-only toggles.
	_, present := got.body["value"]
	is.True(!present) // value key must be absent for auto-fan toggle
}

func TestResetFilterAndFaults(t *testing.T) {
	for _, tc := range []struct {
		verb     string
		wantPath string
	}{
		{"reset-filter", "/v1/devices/playroom/filter/reset"},
		{"reset-faults", "/v1/devices/playroom/faults/reset"},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			is := is.New(t)
			var got stub
			srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
			defer srv.Close()
			code, _, _ := runCLI(t, srv, "playroom", tc.verb)
			is.Equal(code, 0)
			is.Equal(got.method, "POST")
			is.Equal(got.path, tc.wantPath)
		})
	}
}

// TestFaultsList verifies the empty-list case prints the spec's
// exact fallback string.
func TestFaultsList(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"faults": []map[string]any{
				{"code": 12, "kind": "alarm"},
				{"code": 7, "kind": "warning"},
			},
		})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "faults")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "alarm: code 12"))
	is.True(strings.Contains(stdout, "warning: code 7"))
}

func TestFaultsEmpty(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"faults": []any{}})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "faults")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "no active faults"))
}

func TestFirmware(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":    "0.11",
			"build_date": "2025-04-01",
		})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "firmware")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "0.11"))
	is.True(strings.Contains(stdout, "2025-04-01"))
}

func TestEfficiency(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"recovery_efficiency_pct": 85})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "efficiency")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "85%"))
}

// TestStatus exercises the snapshot renderer end-to-end. We feed a
// realistic SnapshotResponse and assert key substrings (rather than a
// full snapshot string) so minor formatting tweaks don't flake.
func TestStatus(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":      "playroom",
			"id":        "0123456789ABCDEF",
			"ip":        "192.168.1.148:4000",
			"last_poll": "2026-05-03T10:00:00Z",
			"configured": map[string]any{
				"power":        true,
				"airflow_mode": "regeneration",
				"speed_mode":   "manual",
				"manual_pct":   30,
			},
			"live": map[string]any{
				"fan_supply_rpm":  5340,
				"fan_extract_rpm": 5400,
				"in_user_control": true,
				"sensor_alerts":   map[string]any{"humidity": false, "co2": false, "voc": false},
			},
			"sensors": map[string]any{
				"humidity_pct":            52,
				"eco2_ppm":                3500,
				"voc_index":               350,
				"temp_outdoor_c":          20.8,
				"recovery_efficiency_pct": 85,
			},
			"service": map[string]any{
				"filter_status":            "clean",
				"filter_remaining_seconds": 89*86400 + 9*3600,
				"motor_lifetime_seconds":   14*3600 + 32*60,
				"rtc_battery_volts":        3.34,
				"fault_level":              "none",
			},
			"firmware": map[string]any{
				"version":    "0.11",
				"build_date": "2025-04-01",
			},
		})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "status")
	is.Equal(code, 0)
	for _, sub := range []string{
		"playroom @ 192.168.1.148",
		"firmware 0.11",
		"power      : on",
		"mode       : regeneration",
		"manual 30%",
		"5340 / 5400 rpm",
		"RH=52%",
		"eCO2=3500ppm",
		"VOC=350",
		"outdoor=20.8",
		"recovery=85%",
		"filter clean",
		"motor",
		"RTC 3.34 V",
	} {
		is.True(strings.Contains(stdout, sub)) // status output must contain expected substring
	}
	// No-override case must NOT include the warning line.
	is.True(!strings.Contains(stdout, "sensor override")) // no override warning when in_user_control=true
}

// TestStatus_FaultsLine pins the spec'd render branch: when
// service.fault_level is non-"none", status output adds a `faults` line
// that points at `breezy <name> faults`. Without it, the user has no
// signal that the unit is in alarm/warning state.
func TestStatus_FaultsLine(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "playroom", "ip": "192.168.1.148:4000",
			"configured": map[string]any{"power": true, "speed_mode": "manual", "manual_pct": 30},
			"live": map[string]any{
				"in_user_control": true,
				"sensor_alerts":   map[string]any{},
			},
			"sensors": map[string]any{},
			"service": map[string]any{
				"fault_level": "warning",
			},
		})
	}))
	defer srv.Close()

	code, stdout, _ := runCLI(t, srv, "playroom", "status")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "faults     : warning"))   // fault level rendered
	is.True(strings.Contains(stdout, "breezy playroom faults")) // pointer to detail verb
}

// TestStatusSensorOverride ensures the warning fires when the daemon
// reports in_user_control=false, and that the alert summary is included.
func TestStatusSensorOverride(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "playroom", "ip": "192.168.1.148:4000",
			"configured": map[string]any{"power": true, "speed_mode": "manual", "manual_pct": 30},
			"live": map[string]any{
				"in_user_control": false,
				"sensor_alerts":   map[string]any{"humidity": false, "co2": true, "voc": false},
			},
			"sensors": map[string]any{},
			"service": map[string]any{},
		})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "status")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "sensor override")) // override warning expected
	is.True(strings.Contains(stdout, "co2"))             // override warning should mention co2 alert
}

func TestLs(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		on := true
		off := false
		_ = json.NewEncoder(w).Encode(map[string]any{
			"devices": []map[string]any{
				{
					"name":         "playroom",
					"id":           "0123456789ABCDEF",
					"ip":           "192.168.1.148:4000",
					"last_poll":    "2026-05-03T10:00:00Z",
					"power":        &on,
					"airflow_mode": "regeneration",
					"reachable":    true,
				},
				{
					"name": "office", "id": "OFFICE0000000001",
					"ip": "192.168.1.149:4000", "power": &off,
					"airflow_mode": "ventilation", "reachable": true,
				},
			},
		})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "ls")
	is.Equal(code, 0)
	for _, sub := range []string{
		"NAME", "IP", "POWER", "MODE", "LAST POLL",
		"playroom", "office", "regeneration", "ventilation",
	} {
		is.True(strings.Contains(stdout, sub)) // ls output must contain substring
	}
}

func TestLsEmpty(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": []any{}})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "ls")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "no devices"))
}

func TestDaemonURL(t *testing.T) {
	is := is.New(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--daemon", "http://x:1234", "daemon-url"}, &stdout, &stderr, nil)
	is.Equal(code, 0) // exit code; stderr=stderr.String() if nonzero
	is.Equal(strings.TrimSpace(stdout.String()), "http://x:1234")
}

func TestDaemonURLNormalizesBareHostPort(t *testing.T) {
	is := is.New(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--daemon", "127.0.0.1:9876", "daemon-url"}, &stdout, &stderr, nil)
	is.Equal(code, 0)
	is.Equal(strings.TrimSpace(stdout.String()), "http://127.0.0.1:9876")
	_ = stderr
}

func TestDaemonURLStandaloneByDefault(t *testing.T) {
	is := is.New(t)
	// Neutralise any real ~/.config/breezy/config.toml so the test is hermetic.
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"daemon-url"}, &stdout, &stderr, nil)
	is.Equal(code, 0)
	is.Equal(strings.TrimSpace(stdout.String()), "(standalone — no daemon)")
	_ = stderr
}

// TestErrorEnvelope verifies that {"error","code"} responses are
// rendered in the spec's "error: <msg> (<code>)" form on stderr and
// produce exit code 1.
func TestErrorEnvelope(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"device \"playroom\" not configured","code":"not_found"}`))
	}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "status")
	is.Equal(code, 1) // backend error exit
	is.True(strings.Contains(stderr, "not configured"))
	is.True(strings.Contains(stderr, "not_found"))
}

func TestErrorNonEnvelope(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("kapow"))
	}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "status")
	is.Equal(code, 1)
	is.True(strings.Contains(stderr, "HTTP 500"))
}

// TestGetByName resolves a registry name → ID before issuing the HTTP
// GET, decodes the response hex into the registry's typed value, and
// surfaces the unit.
func TestGetByName(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":   "0x0025",
			"hex":  "34", // 0x34 == 52
			"name": "humidity",
			"type": "uint8",
		})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "get", "humidity")
	is.Equal(code, 0)
	is.Equal(got.path, "/v1/devices/playroom/params/0x0025") // CLI must resolve humidity → 0x0025
	is.True(strings.Contains(stdout, "52"))                  // decoded value
	is.True(strings.Contains(stdout, "%"))                   // unit from registry
}

// TestGetUnknownID pins the spec'd fallback when an ID isn't in the
// registry: hex bytes replace the typed value, no name prefix. Without
// this, a param outside the registry would surface as a typed-decode
// error or a meaningless empty render.
func TestGetUnknownID(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":  "0xFFFE",
			"hex": "abcd",
			// no "name" / "type" — daemon doesn't know it either.
		})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "get", "0xFFFE")
	is.Equal(code, 0)
	is.Equal(got.path, "/v1/devices/playroom/params/0xFFFE")
	is.True(strings.Contains(stdout, "0xFFFE"))                                               // ID echoed
	is.True(strings.Contains(stdout, "abcd"))                                                 // raw hex bytes
	is.True(!strings.Contains(stdout, "humidity") && !strings.Contains(stdout, "(humidity)")) // no name prefix
}

// TestSet_InvalidHex pins that `set <param> <bad-hex>` exits 2 with
// "invalid hex" on stderr and does NOT round-trip to the daemon. The
// hex.DecodeString check runs locally before any HTTP call.
func TestSet_InvalidHex(t *testing.T) {
	is := is.New(t)
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "set", "co2_threshold", "ZZZZ")
	is.Equal(code, 2)
	is.True(strings.Contains(stderr, "invalid hex"))
	is.True(!hit) // bad hex must not round-trip to the daemon
}

// TestUsageNameNoVerb pins that `breezy <name>` (device-only, no verb)
// exits 2 with the documented usage hint, no daemon traffic.
func TestUsageNameNoVerb(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir()) // hermetic
	var stdout, stderr bytes.Buffer
	code := run([]string{"playroom"}, &stdout, &stderr, nil)
	is.Equal(code, 2)
	is.True(strings.Contains(stderr.String(), "usage: breezy"))
}

func TestGetByHex(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "0x0001", "hex": "01", "name": "power", "type": "uint8",
		})
	}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "get", "0x01")
	is.Equal(code, 0)
	is.Equal(got.path, "/v1/devices/playroom/params/0x0001")
}

// TestSetRejectReadOnly ensures we don't even round-trip when the user
// targets a read-only param.
func TestSetRejectReadOnly(t *testing.T) {
	is := is.New(t)
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit = true
	}))
	defer srv.Close()
	// 0x0025 = humidity, read-only.
	code, _, stderr := runCLI(t, srv, "playroom", "set", "humidity", "01")
	is.Equal(code, 2)                              // local read-only rejection
	is.True(!hit)                                  // CLI must not hit daemon
	is.True(strings.Contains(stderr, "read-only")) // stderr should explain
}

func TestSetWritesHex(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	// 0x001A = co2_threshold, writable uint16.
	code, _, _ := runCLI(t, srv, "playroom", "set", "co2_threshold", "d007")
	is.Equal(code, 0)
	is.Equal(got.path, "/v1/devices/playroom/params/0x001A")
	is.Equal(got.body["hex"], "d007")
}

func TestSetUnknownParam(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "set", "nope", "00")
	is.Equal(code, 2)
	is.True(strings.Contains(stderr, "unknown param"))
}

// TestRtcShow needs two consecutive GETs (0x6F and 0x70) — verify the
// CLI issues both and renders the combined date+time.
func TestRtcShow(t *testing.T) {
	is := is.New(t)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/devices/playroom/params/0x006F":
			// time = 13:45:30 -> [sec, min, hr]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "0x006F", "hex": hex.EncodeToString([]byte{30, 45, 13}), "type": "time_of_day",
			})
		case "/v1/devices/playroom/params/0x0070":
			// 2026-05-03 (Sunday=7) -> [day, dow, month, year-2000]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "0x0070", "hex": hex.EncodeToString([]byte{3, 7, 5, 26}), "type": "date",
			})
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "rtc")
	is.Equal(code, 0)
	is.Equal(hits, 2) // expected 2 GETs (date + time)
	is.True(strings.Contains(stdout, "2026-05-03"))
	is.True(strings.Contains(stdout, "13:45:30"))
}

func TestRtcSet(t *testing.T) {
	is := is.New(t)
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "rtc", "set", "2026-05-03T13:45:30Z")
	is.Equal(code, 0)
	is.Equal(got.path, "/v1/devices/playroom/rtc")
	tStr, _ := got.body["time"].(string)
	is.True(strings.HasPrefix(tStr, "2026-05-03T13:45:30"))
}

func TestRtcSetBadFormat(t *testing.T) {
	is := is.New(t)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "rtc", "set", "yesterday")
	is.Equal(code, 2)
}

// TestVersionVerb pins `breezy version` (the global verb) — exit 0,
// stdout matches the documented `breezy <version> (commit <sha>, built
// <date>)` shape. Build-time-injected ldflags supply the values; the
// dev/none/unknown defaults from main.go's package-level vars stand in
// during tests.
func TestVersionVerb(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir()) // hermetic
	var stdout, stderr bytes.Buffer
	code := run([]string{"version"}, &stdout, &stderr, nil)
	is.Equal(code, 0)
	out := stdout.String()
	is.True(strings.HasPrefix(out, "breezy "))
	is.True(strings.Contains(out, " (commit "))
	is.True(strings.Contains(out, ", built "))
	is.True(strings.HasSuffix(out, ")\n"))
	is.Equal(stderr.String(), "")
}

// TestVersionFlag pins `breezy --version` — same banner shape as the
// version verb, handled inside flag.Parse via the bool flag.
func TestVersionFlag(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"--version"}, &stdout, &stderr, nil)
	is.Equal(code, 0)
	is.True(strings.HasPrefix(stdout.String(), "breezy "))
	is.True(strings.Contains(stdout.String(), "(commit "))
	is.Equal(stderr.String(), "")
}

// TestHelpVerb pins `breezy help` (the global verb form) — exit 0
// with usage on stdout. The flag-style `-h` / `--help` paths exit 2
// today (flag.ErrHelp falls through to `return 2`); see #133 for the
// decide-and-align tracking issue.
func TestHelpVerb(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"help"}, &stdout, &stderr, nil)
	is.Equal(code, 0)
	is.True(strings.Contains(stdout.String(), "Usage:"))
	is.Equal(stderr.String(), "")
}

// TestUsageNoArgs / TestUnknownVerb cover the two main "exit code 2"
// surfaces: nothing at all, and an unrecognised verb.
func TestUsageNoArgs(t *testing.T) {
	is := is.New(t)
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr, nil)
	is.Equal(code, 2)
	_, _ = stdout, stderr
}

func TestUnknownVerb(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir()) // hermetic: no real ~/.config/breezy/config.toml
	var stdout, stderr bytes.Buffer
	code := run([]string{"playroom", "barbecue"}, &stdout, &stderr, nil)
	is.Equal(code, 2)
	is.True(strings.Contains(stderr.String(), "unknown verb"))
	_ = stdout
}

func TestParam(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir()) // hermetic: no real ~/.config/breezy/config.toml
	var stdout, stderr bytes.Buffer
	code := run([]string{"param"}, &stdout, &stderr, nil)
	is.Equal(code, 0) // exit code; stderr if nonzero
	out := stdout.String()

	// Header.
	for _, h := range []string{"ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION"} {
		is.True(strings.Contains(out, h)) // header column expected
	}

	// Spot-check known params.
	for _, sub := range []string{"0x0001", "power", "0x0044", "speed_manual_pct"} {
		is.True(strings.Contains(out, sub)) // expected param row
	}

	// Row count = registered params + 1 header.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := len(breezy.AllParams()) + 1
	is.Equal(len(lines), want) // header + one row per registered param
	_ = stderr
}

func TestCapsString(t *testing.T) {
	is := is.New(t)
	for _, tc := range []struct {
		caps breezy.Capabilities
		want string
	}{
		{breezy.CapRead, "R"},
		{breezy.CapWrite, "W"},
		{breezy.CapReadWrite, "RW"},
		{breezy.CapAll, "RWID"},
		{breezy.CapRead | breezy.CapInc, "RI"},
	} {
		is.Equal(capsString(tc.caps), tc.want) // capsString rendering
	}
}

func TestRenderParams(t *testing.T) {
	is := is.New(t)
	params := []breezy.Param{
		{ID: 0x0001, Name: "power", Type: breezy.TypeUint8, Unit: "", Caps: breezy.CapAll, Description: "Turn on/off"},
		{ID: 0x004A, Name: "fan_supply_rpm", Type: breezy.TypeUint16, Unit: "rpm", Caps: breezy.CapRead, Description: "Live RPM"},
		{ID: 0x0065, Name: "reset_filter_timer", Type: breezy.TypeWriteOnly, Unit: "", Caps: breezy.CapWrite, Description: "Trigger"},
	}
	var buf bytes.Buffer
	renderParams(&buf, params)
	out := buf.String()

	for _, sub := range []string{
		"ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION",
		"0x0001", "power", "uint8", "RWID", "Turn on/off",
		"0x004A", "fan_supply_rpm", "uint16", "rpm", "Live RPM",
		"0x0065", "reset_filter_timer", "write_only", "W", "Trigger",
	} {
		is.True(strings.Contains(out, sub)) // renderParams output must contain substring
	}

	// Empty Unit must render as "-".
	powerLine := ""
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.Contains(line, "0x0001") {
			powerLine = line
			break
		}
	}
	is.True(powerLine != "") // row found for 0x0001
	// The UNIT column for power must be the literal "-" (surrounded by spaces).
	is.True(strings.Contains(powerLine, " -  ")) // empty Unit rendered as '-'

	// Header line is the first non-empty line.
	firstLine := strings.SplitN(out, "\n", 2)[0]
	wantHeaderOrder := []string{"ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION"}
	prev := -1
	for _, h := range wantHeaderOrder {
		idx := strings.Index(firstLine, h)
		is.True(idx >= 0)   // header must include column
		is.True(idx > prev) // header columns must be ordered
		prev = idx
	}
}

// ---------------------------------------------------------------------------
// Standalone (directBackend) tests
// ---------------------------------------------------------------------------

const (
	standaloneTestDeviceID = "TESTID0000000001"
	standaloneTestPassword = "1111"
)

// fakeSnapshotPath returns the absolute path to the in-tree fakedevice
// snapshot used by the daemon's poller tests.
func fakeSnapshotPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../pkg/breezy/fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

// startFakeDevice brings up a fakedevice on an ephemeral UDP port and
// ensures it gets closed via t.Cleanup.
func startFakeDevice(t *testing.T) *fakedevice.Server {
	t.Helper()
	srv, err := fakedevice.NewServer(fakeSnapshotPath(t), standaloneTestDeviceID, standaloneTestPassword)
	if err != nil {
		t.Fatalf("fakedevice.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// runStandalone runs the CLI through a directBackend built from the
// supplied device map. The caller's responsibility is to set each
// device's IP to a fakedevice address.
// Returns (exit code, stdout, stderr).
func runStandalone(t *testing.T, devices map[string]config.Device, args ...string) (int, string, string) {
	t.Helper()
	d := newDirectBackend(devices)
	t.Cleanup(func() { _ = d.Close() })

	var stdout, stderr bytes.Buffer
	code := run(args, &stdout, &stderr, d)
	return code, stdout.String(), stderr.String()
}

func TestStandalonePower(t *testing.T) {
	is := is.New(t)
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, _, _ := runStandalone(t, devices, "playroom", "on")
	is.Equal(code, 0)
	// Verify by reading 0x0001 back through a second CLI invocation.
	code, stdout, _ := runStandalone(t, devices, "playroom", "get", "power")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "= 1") || strings.Contains(stdout, "1\n")) // power=1 after Power(on)
}

func TestStandaloneSpeedPreset(t *testing.T) {
	is := is.New(t)
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, _, _ := runStandalone(t, devices, "playroom", "speed", "2")
	is.Equal(code, 0)
	code, stdout, _ := runStandalone(t, devices, "playroom", "get", "speed_mode")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "= 2")) // speed_mode=2 after speed 2
}

func TestStandaloneStatus(t *testing.T) {
	is := is.New(t)
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, stdout, _ := runStandalone(t, devices, "playroom", "status")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "playroom")) // status output must include device name
}

func TestStandaloneFaults(t *testing.T) {
	is := is.New(t)
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, stdout, _ := runStandalone(t, devices, "playroom", "faults")
	is.Equal(code, 0)
	// snapshot_148.json has no active faults → expect the "no active faults" line.
	is.True(strings.Contains(stdout, "no active faults"))
}

// TestStandaloneFirmware pins firmware reporting through directBackend +
// fakedevice. The directBackend.Firmware path does its own bytes →
// "%d.%02d" + date conversion that has no other test coverage.
func TestStandaloneFirmware(t *testing.T) {
	is := is.New(t)
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, stdout, _ := runStandalone(t, devices, "playroom", "firmware")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "version")) // firmware output must include "version"
	is.True(strings.Contains(stdout, "built"))   // and a build date line
}

// TestStandaloneEfficiency pins efficiency reporting through
// directBackend. The renderer formats the percentage value end-to-end.
// Seeds 0x0129 directly because the canned snapshot doesn't include it.
func TestStandaloneEfficiency(t *testing.T) {
	is := is.New(t)
	fake := startFakeDevice(t)
	fake.SetParamValue(0x0129, []byte{85}) // recovery efficiency = 85%
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, stdout, stderr := runStandalone(t, devices, "playroom", "efficiency")
	if code != 0 {
		t.Fatalf("efficiency exit %d; stderr=%q stdout=%q", code, stderr, stdout)
	}
	is.True(strings.Contains(stdout, "85")) // seeded value
	is.True(strings.Contains(stdout, "%"))  // rendered as a percentage
}

// TestStandaloneLs pins the standalone-mode `ls` rendering: there is no
// daemon cache, so POWER / MODE columns render as `?` and LAST POLL as
// `never`. Without this, a regression to the daemon-mode path (which
// reads from the cache and returns real values) would silently pass
// through with daemon-shaped output in standalone mode.
func TestStandaloneLs(t *testing.T) {
	is := is.New(t)
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, stdout, _ := runStandalone(t, devices, "ls")
	is.Equal(code, 0)
	is.True(strings.Contains(stdout, "playroom")) // device name in listing
	is.True(strings.Contains(stdout, "?"))        // power/mode placeholders
	is.True(strings.Contains(stdout, "never"))    // last-poll placeholder
}

// TestDiscover_UnicastTargets exercises the positional-target form
// of `breezy discover`, which sends the wildcard request unicast to
// each given address. This is the workaround for networks that drop
// UDP broadcasts (Wi-Fi AP isolation, mesh hops, VLAN).
func TestDiscover_UnicastTargets(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir())
	fake := startFakeDevice(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"discover", fake.Addr()}, &stdout, &stderr, nil)
	is.Equal(code, 0)
	// Discover wildcard reads param 0x7C from the device, which is the
	// device's own configured ID — for the fakedevice that's the value
	// stored in snapshot_148.json (BREEZY-prefix), NOT the ID we passed
	// to fakedevice.NewServer. Just assert SOMETHING came back with the
	// right shape.
	out := stdout.String()
	// The output format is "<ip>  id=<id>  type=<n>"; the IP shown is the
	// reply's source address (the bare IP, no port).
	host, _, _ := strings.Cut(fake.Addr(), ":")
	is.True(strings.Contains(out, host))
	is.True(strings.Contains(out, "id="))
	is.True(strings.Contains(out, "type="))
	_ = stderr
}

// TestDiscover_UnicastWithPassword: the wildcard request is encoded
// with the supplied password instead of the factory default. The
// fakedevice accepts any password on wildcard discovery, so this
// just confirms the flag is plumbed through end-to-end.
func TestDiscover_UnicastWithPassword(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir())
	fake := startFakeDevice(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"discover", "-p", "testpwd", fake.Addr()}, &stdout, &stderr, nil)
	is.Equal(code, 0)
	host, _, _ := strings.Cut(fake.Addr(), ":")
	is.True(strings.Contains(stdout.String(), host))
	is.True(strings.Contains(stdout.String(), "id="))
	_ = stderr
}

// TestDiscover_PasswordFlagFormats covers --password=PWD as well.
func TestDiscover_PasswordFlagFormats(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir())
	fake := startFakeDevice(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"discover", "--password=anything", fake.Addr()}, &stdout, &stderr, nil)
	is.Equal(code, 0)
	is.True(strings.Contains(stdout.String(), "id="))
	_ = stderr
}

// TestDiscover_PasswordFlagMissingValue: -p with no following arg
// should exit 2 with a usage error.
func TestDiscover_PasswordFlagMissingValue(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"discover", "-p"}, &stdout, &stderr, nil)
	is.Equal(code, 2) // usage error
	is.True(strings.Contains(stderr.String(), "needs a password value"))
	_ = stdout
}

// TestDiscover_UnicastNoReply: the daemon-side fakedevice isn't
// running, so the unicast target receives no reply within the
// discover timeout. We expect exit 0 and the unified guidance block.
func TestDiscover_UnicastNoReply(t *testing.T) {
	is := is.New(t)
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	// 192.0.2.1 is TEST-NET-1: routable but never answered.
	code := run([]string{"discover", "192.0.2.1"}, &stdout, &stderr, nil)
	is.Equal(code, 0)
	is.True(strings.Contains(stdout.String(), "no Breezy devices found"))
	is.True(strings.Contains(stdout.String(), "things to check:"))
	_ = stderr
}
