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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hughobrien/breezyd/internal/config"
	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
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
			var got stub
			srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
			defer srv.Close()

			code, stdout, stderr := runCLI(t, srv, "playroom", tc.verb)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			if got.method != "POST" || got.path != "/v1/devices/playroom/power" {
				t.Fatalf("got %s %s", got.method, got.path)
			}
			gotOn, _ := got.body["on"].(bool)
			if gotOn != tc.wantOn {
				t.Fatalf("body on=%v want %v (body=%v)", gotOn, tc.wantOn, got.body)
			}
			if !strings.Contains(stdout, "ok") {
				t.Fatalf("stdout=%q", stdout)
			}
		})
	}
}

// TestSpeedPreset / TestSpeedManual cover the local validation +
// outgoing body shape for `speed <preset>` and `speed manual:<pct>`.
func TestSpeedPreset(t *testing.T) {
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "speed", "2")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if got.path != "/v1/devices/playroom/speed" {
		t.Fatalf("path=%q", got.path)
	}
	if v, _ := got.body["preset"].(float64); int(v) != 2 {
		t.Fatalf("preset=%v body=%v", got.body["preset"], got.body)
	}
}

func TestSpeedManual(t *testing.T) {
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "speed", "manual:30")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if v, _ := got.body["manual"].(float64); int(v) != 30 {
		t.Fatalf("manual=%v body=%v", got.body["manual"], got.body)
	}
}

// TestSpeedManualBelowFloor enforces the spec's local-rejection rule:
// pct < 10 must fail BEFORE any HTTP request, with exit code 2.
func TestSpeedManualBelowFloor(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "speed", "manual:5")
	if code != 2 {
		t.Fatalf("exit=%d (want 2) stderr=%q", code, stderr)
	}
	if called {
		t.Fatalf("CLI hit daemon despite local floor check")
	}
	if !strings.Contains(stderr, "floor") {
		t.Fatalf("stderr=%q (want mention of floor)", stderr)
	}
}

func TestSpeedBadArg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	code, _, _ := runCLI(t, srv, "playroom", "speed", "9")
	if code != 2 {
		t.Fatalf("exit=%d (want 2)", code)
	}
}

// TestModeValidation exercises the local check against the four
// allowed mode names.
func TestModeValidation(t *testing.T) {
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	for _, m := range []string{"ventilation", "regeneration", "supply", "extract"} {
		got = stub{}
		code, _, stderr := runCLI(t, srv, "playroom", "mode", m)
		if code != 0 {
			t.Fatalf("mode=%s exit=%d stderr=%q", m, code, stderr)
		}
		if got.body["mode"] != m {
			t.Fatalf("body mode=%v want %s", got.body["mode"], m)
		}
	}
	// Bogus.
	code, _, stderr := runCLI(t, srv, "playroom", "mode", "fluff")
	if code != 2 {
		t.Fatalf("bogus mode exit=%d", code)
	}
	if !strings.Contains(stderr, "ventilation") {
		t.Fatalf("stderr=%q (should list valid modes)", stderr)
	}
}

func TestHeater(t *testing.T) {
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "heater", "on")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if got.path != "/v1/devices/playroom/heater" {
		t.Fatalf("path=%s", got.path)
	}
	if got.body["on"] != true {
		t.Fatalf("body on=%v", got.body["on"])
	}
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
			var got stub
			srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
			defer srv.Close()
			code, _, stderr := runCLI(t, srv, "playroom", tc.verb)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr)
			}
			if got.method != "POST" || got.path != tc.wantPath {
				t.Fatalf("got %s %s want POST %s", got.method, got.path, tc.wantPath)
			}
		})
	}
}

// TestFaultsList verifies the empty-list case prints the spec's
// exact fallback string.
func TestFaultsList(t *testing.T) {
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
	code, stdout, stderr := runCLI(t, srv, "playroom", "faults")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "alarm: code 12") {
		t.Fatalf("stdout=%q", stdout)
	}
	if !strings.Contains(stdout, "warning: code 7") {
		t.Fatalf("stdout=%q", stdout)
	}
}

func TestFaultsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"faults": []any{}})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "faults")
	if code != 0 || !strings.Contains(stdout, "no active faults") {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
}

func TestFirmware(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version":    "0.11",
			"build_date": "2025-04-01",
		})
	}))
	defer srv.Close()
	code, stdout, stderr := runCLI(t, srv, "playroom", "firmware")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "0.11") || !strings.Contains(stdout, "2025-04-01") {
		t.Fatalf("stdout=%q", stdout)
	}
}

func TestEfficiency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"recovery_efficiency_pct": 85})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "playroom", "efficiency")
	if code != 0 || !strings.Contains(stdout, "85%") {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
}

// TestStatus exercises the snapshot renderer end-to-end. We feed a
// realistic SnapshotResponse and assert key substrings (rather than a
// full snapshot string) so minor formatting tweaks don't flake.
func TestStatus(t *testing.T) {
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
	code, stdout, stderr := runCLI(t, srv, "playroom", "status")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
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
		if !strings.Contains(stdout, sub) {
			t.Errorf("stdout missing %q\n%s", sub, stdout)
		}
	}
	// No-override case must NOT include the warning line.
	if strings.Contains(stdout, "sensor override") {
		t.Errorf("unexpected override warning in non-override status:\n%s", stdout)
	}
}

// TestStatusSensorOverride ensures the warning fires when the daemon
// reports in_user_control=false, and that the alert summary is included.
func TestStatusSensorOverride(t *testing.T) {
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
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stdout, "sensor override") {
		t.Errorf("missing override warning:\n%s", stdout)
	}
	if !strings.Contains(stdout, "co2") {
		t.Errorf("override warning should mention co2 alert:\n%s", stdout)
	}
}

func TestLs(t *testing.T) {
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
	code, stdout, stderr := runCLI(t, srv, "ls")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	for _, sub := range []string{
		"NAME", "IP", "POWER", "MODE", "LAST POLL",
		"playroom", "office", "regeneration", "ventilation",
	} {
		if !strings.Contains(stdout, sub) {
			t.Errorf("ls stdout missing %q:\n%s", sub, stdout)
		}
	}
}

func TestLsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": []any{}})
	}))
	defer srv.Close()
	code, stdout, _ := runCLI(t, srv, "ls")
	if code != 0 || !strings.Contains(stdout, "no devices") {
		t.Fatalf("exit=%d stdout=%q", code, stdout)
	}
}

func TestDaemonURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--daemon", "http://x:1234", "daemon-url"}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "http://x:1234" {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestDaemonURLNormalizesBareHostPort(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--daemon", "127.0.0.1:9876", "daemon-url"}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "http://127.0.0.1:9876" {
		t.Fatalf("stdout=%q", got)
	}
}

func TestDaemonURLStandaloneByDefault(t *testing.T) {
	// Neutralise any real ~/.config/breezy/config.toml so the test is hermetic.
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"daemon-url"}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(stdout.String())
	if got != "(standalone — no daemon)" {
		t.Errorf("got %q, want '(standalone — no daemon)'", got)
	}
}

// TestErrorEnvelope verifies that {"error","code"} responses are
// rendered in the spec's "error: <msg> (<code>)" form on stderr and
// produce exit code 1.
func TestErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":"device \"playroom\" not configured","code":"not_found"}`))
	}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "status")
	if code != 1 {
		t.Fatalf("exit=%d (want 1) stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "not configured") || !strings.Contains(stderr, "not_found") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestErrorNonEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("kapow"))
	}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "status")
	if code != 1 {
		t.Fatalf("exit=%d", code)
	}
	if !strings.Contains(stderr, "HTTP 500") {
		t.Fatalf("stderr=%q", stderr)
	}
}

// TestGetByName resolves a registry name → ID before issuing the HTTP
// GET, decodes the response hex into the registry's typed value, and
// surfaces the unit.
func TestGetByName(t *testing.T) {
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
	code, stdout, stderr := runCLI(t, srv, "playroom", "get", "humidity")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if got.path != "/v1/devices/playroom/params/0x0025" {
		t.Fatalf("path=%s (CLI must resolve humidity → 0x0025)", got.path)
	}
	if !strings.Contains(stdout, "52") {
		t.Fatalf("stdout=%q (want 52)", stdout)
	}
	if !strings.Contains(stdout, "%") {
		t.Fatalf("stdout=%q (want %% unit from registry)", stdout)
	}
}

func TestGetByHex(t *testing.T) {
	var got stub
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "0x0001", "hex": "01", "name": "power", "type": "uint8",
		})
	}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "get", "0x01")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if got.path != "/v1/devices/playroom/params/0x0001" {
		t.Fatalf("path=%s", got.path)
	}
}

// TestSetRejectReadOnly ensures we don't even round-trip when the user
// targets a read-only param.
func TestSetRejectReadOnly(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hit = true
	}))
	defer srv.Close()
	// 0x0025 = humidity, read-only.
	code, _, stderr := runCLI(t, srv, "playroom", "set", "humidity", "01")
	if code != 2 {
		t.Fatalf("exit=%d (want 2)", code)
	}
	if hit {
		t.Fatalf("CLI hit daemon despite read-only check")
	}
	if !strings.Contains(stderr, "read-only") {
		t.Fatalf("stderr=%q", stderr)
	}
}

func TestSetWritesHex(t *testing.T) {
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	// 0x001A = co2_threshold, writable uint16.
	code, _, stderr := runCLI(t, srv, "playroom", "set", "co2_threshold", "d007")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if got.path != "/v1/devices/playroom/params/0x001A" {
		t.Fatalf("path=%s", got.path)
	}
	if got.body["hex"] != "d007" {
		t.Fatalf("hex=%v", got.body["hex"])
	}
}

func TestSetUnknownParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "set", "nope", "00")
	if code != 2 {
		t.Fatalf("exit=%d (want 2) stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "unknown param") {
		t.Fatalf("stderr=%q", stderr)
	}
}

// TestRtcShow needs two consecutive GETs (0x6F and 0x70) — verify the
// CLI issues both and renders the combined date+time.
func TestRtcShow(t *testing.T) {
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
	code, stdout, stderr := runCLI(t, srv, "playroom", "rtc")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if hits != 2 {
		t.Fatalf("expected 2 GETs, got %d", hits)
	}
	if !strings.Contains(stdout, "2026-05-03") || !strings.Contains(stdout, "13:45:30") {
		t.Fatalf("stdout=%q", stdout)
	}
}

func TestRtcSet(t *testing.T) {
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "rtc", "set", "2026-05-03T13:45:30Z")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if got.path != "/v1/devices/playroom/rtc" {
		t.Fatalf("path=%s", got.path)
	}
	tStr, _ := got.body["time"].(string)
	if !strings.HasPrefix(tStr, "2026-05-03T13:45:30") {
		t.Fatalf("time=%q", tStr)
	}
}

func TestRtcSetBadFormat(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	code, _, stderr := runCLI(t, srv, "playroom", "rtc", "set", "yesterday")
	if code != 2 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
}

// TestUsageNoArgs / TestUnknownVerb cover the two main "exit code 2"
// surfaces: nothing at all, and an unrecognised verb.
func TestUsageNoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr, nil)
	if code != 2 {
		t.Fatalf("exit=%d (want 2)", code)
	}
}

func TestUnknownVerb(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // hermetic: no real ~/.config/breezy/config.toml
	var stdout, stderr bytes.Buffer
	code := run([]string{"playroom", "barbecue"}, &stdout, &stderr, nil)
	if code != 2 {
		t.Fatalf("exit=%d (want 2)", code)
	}
	if !strings.Contains(stderr.String(), "unknown verb") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestParam(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // hermetic: no real ~/.config/breezy/config.toml
	var stdout, stderr bytes.Buffer
	code := run([]string{"param"}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()

	// Header.
	for _, h := range []string{"ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION"} {
		if !strings.Contains(out, h) {
			t.Errorf("missing header %q in output:\n%s", h, out)
		}
	}

	// Spot-check known params.
	for _, sub := range []string{"0x0001", "power", "0x0044", "speed_manual_pct"} {
		if !strings.Contains(out, sub) {
			t.Errorf("missing %q in output:\n%s", sub, out)
		}
	}

	// Row count = registered params + 1 header.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := len(breezy.AllParams()) + 1
	if len(lines) != want {
		t.Errorf("got %d lines, want %d (header + %d params)", len(lines), want, want-1)
	}
}

// Ensure our test stub produces what we think; quick sanity check that
// the recordingHandler unmarshals empty bodies without crashing.
func TestRecordingHandlerNoBody(t *testing.T) {
	var got stub
	srv := httptest.NewServer(recordingHandler(t, &got, 200, map[string]any{"ok": true}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got.method != "GET" {
		t.Fatalf("method=%s", got.method)
	}
	if got.body != nil {
		t.Fatalf("expected nil body, got %v", got.body)
	}
	_ = fmt.Sprintf // placate goimports
}

func TestCapsString(t *testing.T) {
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
		if got := capsString(tc.caps); got != tc.want {
			t.Errorf("capsString(%b) = %q, want %q", tc.caps, got, tc.want)
		}
	}
}

func TestRenderParams(t *testing.T) {
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
		if !strings.Contains(out, sub) {
			t.Errorf("renderParams output missing %q:\n%s", sub, out)
		}
	}

	// Empty Unit must render as "-".
	powerLine := ""
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if strings.Contains(line, "0x0001") {
			powerLine = line
			break
		}
	}
	if powerLine == "" {
		t.Fatalf("no row found for 0x0001 in output:\n%s", out)
	}
	// The UNIT column for power must be the literal "-" (surrounded by spaces).
	if !strings.Contains(powerLine, " -  ") {
		t.Errorf("expected empty Unit rendered as '-' in row, got:\n%s", powerLine)
	}

	// Header line is the first non-empty line.
	firstLine := strings.SplitN(out, "\n", 2)[0]
	wantHeaderOrder := []string{"ID", "NAME", "TYPE", "UNIT", "CAPS", "DESCRIPTION"}
	prev := -1
	for _, h := range wantHeaderOrder {
		idx := strings.Index(firstLine, h)
		if idx < 0 {
			t.Fatalf("header missing %q: %q", h, firstLine)
		}
		if idx <= prev {
			t.Fatalf("header column %q out of order in: %q", h, firstLine)
		}
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
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, _, stderr := runStandalone(t, devices, "playroom", "on")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	// Verify by reading 0x0001 back through a second CLI invocation.
	code, stdout, stderr := runStandalone(t, devices, "playroom", "get", "power")
	if code != 0 {
		t.Fatalf("get exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "= 1") && !strings.Contains(stdout, "1\n") {
		t.Errorf("expected power=1 after Power(on), got: %q", stdout)
	}
}

func TestStandaloneSpeedPreset(t *testing.T) {
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, _, stderr := runStandalone(t, devices, "playroom", "speed", "2")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	code, stdout, stderr := runStandalone(t, devices, "playroom", "get", "speed_mode")
	if code != 0 {
		t.Fatalf("get exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "= 2") {
		t.Errorf("expected speed_mode=2 after speed 2, got: %q", stdout)
	}
}

func TestStandaloneStatus(t *testing.T) {
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, stdout, stderr := runStandalone(t, devices, "playroom", "status")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "playroom") {
		t.Errorf("status output missing device name:\n%s", stdout)
	}
}

func TestStandaloneFaults(t *testing.T) {
	fake := startFakeDevice(t)
	devices := map[string]config.Device{
		"playroom": {ID: standaloneTestDeviceID, Password: standaloneTestPassword, IP: fake.Addr()},
	}
	code, _, stderr := runStandalone(t, devices, "playroom", "faults")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	// Output is either "no active faults" (snapshot has none) or a list.
	// Either is fine — what matters is the call completed without error.
}

// TestDiscover_UnicastTargets exercises the positional-target form
// of `breezy discover`, which sends the wildcard request unicast to
// each given address. This is the workaround for networks that drop
// UDP broadcasts (Wi-Fi AP isolation, mesh hops, VLAN).
func TestDiscover_UnicastTargets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := startFakeDevice(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"discover", fake.Addr()}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	// Discover wildcard reads param 0x7C from the device, which is the
	// device's own configured ID — for the fakedevice that's the value
	// stored in snapshot_148.json (BREEZY-prefix), NOT the ID we passed
	// to fakedevice.NewServer. Just assert SOMETHING came back with the
	// right shape.
	out := stdout.String()
	// The output format is "<ip>  id=<id>  type=<n>"; the IP shown is the
	// reply's source address (the bare IP, no port).
	host, _, _ := strings.Cut(fake.Addr(), ":")
	if !strings.Contains(out, host) || !strings.Contains(out, "id=") || !strings.Contains(out, "type=") {
		t.Errorf("discover output missing host/id/type:\n%s", out)
	}
}

// TestDiscover_UnicastWithPassword: the wildcard request is encoded
// with the supplied password instead of the factory default. The
// fakedevice accepts any password on wildcard discovery, so this
// just confirms the flag is plumbed through end-to-end.
func TestDiscover_UnicastWithPassword(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := startFakeDevice(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"discover", "-p", "testpwd", fake.Addr()}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	host, _, _ := strings.Cut(fake.Addr(), ":")
	if !strings.Contains(stdout.String(), host) || !strings.Contains(stdout.String(), "id=") {
		t.Errorf("expected discover output for fake device, got:\n%s", stdout.String())
	}
}

// TestDiscover_PasswordFlagFormats covers --password=PWD as well.
func TestDiscover_PasswordFlagFormats(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := startFakeDevice(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"discover", "--password=anything", fake.Addr()}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "id=") {
		t.Errorf("expected discover output, got:\n%s", stdout.String())
	}
}

// TestDiscover_PasswordFlagMissingValue: -p with no following arg
// should exit 2 with a usage error.
func TestDiscover_PasswordFlagMissingValue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"discover", "-p"}, &stdout, &stderr, nil)
	if code != 2 {
		t.Fatalf("exit=%d (want 2); stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "needs a password value") {
		t.Errorf("missing usage hint: stderr=%q", stderr.String())
	}
	_ = stdout
}

// TestDiscover_UnicastNoReply: the daemon-side fakedevice isn't
// running, so the unicast target receives no reply within the
// discover timeout. We expect exit 0 and a "no devices replied"
// message tailored to the unicast path (not the broadcast hint).
func TestDiscover_UnicastNoReply(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	// 192.0.2.1 is TEST-NET-1: routable but never answered.
	code := run([]string{"discover", "192.0.2.1"}, &stdout, &stderr, nil)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no Breezy devices replied at the supplied targets") {
		t.Errorf("expected unicast-no-reply message, got:\n%s", stdout.String())
	}
	// The broadcast hint should NOT show in the unicast no-reply path.
	if strings.Contains(stdout.String(), "AP isolation") {
		t.Errorf("broadcast-hint leaked into unicast path:\n%s", stdout.String())
	}
}
