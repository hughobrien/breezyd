// SPDX-License-Identifier: GPL-3.0-or-later

// Tests for the daemon HTTP API. We exercise the handler via httptest.NewRecorder
// (no real network; the in-process fakedevice still uses UDP, but tests dial
// it directly through the breezy.Client constructed by the production
// clientFactory, or via stub HandlerClients for cases the fakedevice can't
// model — read-only enforcement, NoticeWrite tracking, etc.).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
	"github.com/matryer/is"
)

const (
	srvDeviceID = "TESTID0000000001"
	srvPassword = "1111"
)

func srvSnapshotPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../pkg/breezy/fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

// newServerFakeDevice spins up an in-process UDP fakedevice and registers
// cleanup. Returns the listener address.
func newServerFakeDevice(t *testing.T) string {
	t.Helper()
	srv, err := fakedevice.NewServer(srvSnapshotPath(t), srvDeviceID, srvPassword)
	if err != nil {
		t.Fatalf("fakedevice.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv.Addr()
}

// newServerHandler builds a Handler whose clientFactory dials the fakedevice
// using a real breezy.Client. The returned handler is wired with one device
// named "playroom" and a poller stub that records NoticeWrite calls.
func newServerHandler(t *testing.T) (*Handler, *recordingPoller, string) {
	t.Helper()
	addr := newServerFakeDevice(t)

	state := NewState()
	rp := newRecordingPoller()

	h := &Handler{
		State: state,
		Devices: NewDeviceRegistry(map[string]DeviceConfig{
			"playroom": {ID: srvDeviceID, Password: srvPassword, IP: addr},
		}),
		Pollers: map[string]*Poller{
			// We replace the real poller with a stub via Notice(); see below.
		},
		// NoticeFunc lets tests hook the per-device NoticeWrite without
		// going through a real *Poller (which we don't need to drive here).
		NoticeFunc: rp.Notice,
	}
	h.ClientFactory = func(name string) (HandlerClient, error) {
		d, ok := h.Devices.Get(name)
		if !ok {
			return nil, fmt.Errorf("unknown device %q", name)
		}
		return breezy.NewClient(d.IP, d.ID, d.Password,
			breezy.WithRetries(0), breezy.WithTimeout(500*time.Millisecond))
	}
	return h, rp, addr
}

// recordingPoller records the (device, paramID) pairs reported via NoticeWrite.
type recordingPoller struct {
	mu      sync.Mutex
	notices []recordedNotice
}

type recordedNotice struct {
	Device string
	ID     breezy.ParamID
}

func newRecordingPoller() *recordingPoller {
	return &recordingPoller{}
}

func (r *recordingPoller) Notice(device string, id breezy.ParamID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notices = append(r.notices, recordedNotice{Device: device, ID: id})
}

func (r *recordingPoller) all() []recordedNotice {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedNotice, len(r.notices))
	copy(out, r.notices)
	return out
}

// doRequest issues a request through the handler and returns the recorder.
func doRequest(t *testing.T, h http.Handler, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// seedSnapshot stuffs a Snapshot into State["playroom"] for the cache-driven
// endpoints that don't go to the device on every call.
func seedSnapshot(t *testing.T, h *Handler, name string, values map[breezy.ParamID][]byte) {
	t.Helper()
	d, _ := h.Devices.Get(name)
	h.State.Set(name, Snapshot{
		IP:       d.IP,
		Values:   values,
		LastPoll: time.Date(2026, 5, 3, 22, 36, 0, 0, time.UTC),
	})
}

// snapshotAllParams seeds the cache from the same JSON snapshot the fakedevice
// uses, so the structured GET /v1/devices/{name} test exercises real values.
func snapshotAllParams(t *testing.T) map[breezy.ParamID][]byte {
	t.Helper()
	// Hand-built from snapshot_148.json — the fields we exercise are stable.
	return map[breezy.ParamID][]byte{
		0x0001: {0x01},                               // power on
		0x0002: {0xFF},                               // manual mode
		0x0007: {0x00},                               // timer off
		0x000B: {0x10, 0x06, 0x00},                   // 06:16:??? remaining
		0x0019: {0x46},                               // humidity threshold = 70
		0x001A: {0x20, 0x03},                         // co2 threshold = 800
		0x001F: {0xD4, 0x00},                         // outdoor 21.2C
		0x0020: {0xD7, 0x00},                         // supply 21.5C
		0x0021: {0xCE, 0x00},                         // exhaust inlet 20.6C
		0x0022: {0xD3, 0x00},                         // exhaust outlet 21.1C
		0x0024: {0x16, 0x0D},                         // 3350 mV
		0x0025: {0x36},                               // humidity 54%
		0x0027: {0x97, 0x04},                         // eCO2 1175 ppm
		0x0044: {0x64},                               // manual 100%
		0x004A: {0x00, 0x00},                         // supply RPM 0
		0x004B: {0x18, 0x15},                         // extract RPM 5400
		0x0064: {0x1D, 0x0D, 0x59, 0x00},             // filter remaining
		0x0068: {0x00},                               // heater off
		0x0081: {0x00},                               // heater_running 0
		0x0083: {0x00},                               // fault_indicator none
		0x0084: {0x00, 0x00, 0x00, 0x00, 0x00},       // no alerts
		0x0086: {0x00, 0x0B, 0x15, 0x03, 0xE9, 0x07}, // 0.11, 2025-03-21
		0x0088: {0x00},                               // filter clean
		0x00B7: {0x03},                               // mode = extract (3)
		0x031F: {0x96, 0x00},                         // VOC threshold 150
		0x0320: {0x5E, 0x01},                         // VOC index 350
		0x0129: {0x55},                               // recovery efficiency 85%
		0x030B: {0x00},                               // frost protection inactive
		0x007E: {0x1E, 0x0A, 0x00, 0x00},             // motor running hours
	}
}

// ------------------------------------------------------------------------
// Healthz + listing
// ------------------------------------------------------------------------

func TestHandler_Healthz(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/healthz", nil)
	is.Equal(rec.Code, http.StatusOK)

	// Body must be the documented {"ok": true} shape.
	var body map[string]any
	is.NoErr(json.Unmarshal(rec.Body.Bytes(), &body))
	is.Equal(body["ok"], true)
}

// TestHandler_RootAnchor_DoesNotCatchAPITypos pins the load-bearing `{$}`
// anchor on the page-shell route. Without it, Go 1.22's mux treats `GET /`
// as a prefix match catching every unmatched URL, silently turning API
// typos and unknown routes into HTML responses instead of 404s.
func TestHandler_RootAnchor_DoesNotCatchAPITypos(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)

	// Page shell at /: HTML 200.
	rec := doRequest(t, h, http.MethodGet, "/", nil)
	is.Equal(rec.Code, http.StatusOK)
	is.True(strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html")) // shell is HTML

	// API-typo path under /v1/...: 404, NOT HTML.
	rec = doRequest(t, h, http.MethodGet, "/v1/devices/zzz/typo", nil)
	is.Equal(rec.Code, http.StatusNotFound)
	is.True(!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html")) // API-typo must not return HTML

	// Random unmatched path: also 404, also not HTML.
	rec = doRequest(t, h, http.MethodGet, "/totally-not-a-route", nil)
	is.Equal(rec.Code, http.StatusNotFound)
	is.True(!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html"))
}

func TestHandler_ListDevices(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices", nil)
	is.Equal(rec.Code, http.StatusOK)
	var resp struct {
		Devices []map[string]any `json:"devices"`
	}
	is.NoErr(json.Unmarshal(rec.Body.Bytes(), &resp))
	is.Equal(len(resp.Devices), 1)
	is.Equal(resp.Devices[0]["name"], "playroom")
}

// ------------------------------------------------------------------------
// Structured snapshot
// ------------------------------------------------------------------------

func TestHandler_GetDevice_StructuredSnapshot(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	is.Equal(rec.Code, http.StatusOK)
	var resp map[string]any
	is.NoErr(json.Unmarshal(rec.Body.Bytes(), &resp))

	// Top-level identity.
	is.Equal(resp["name"], "playroom")
	is.Equal(resp["id"], srvDeviceID)

	// Block presence and a few decoded fields.
	cfg, _ := resp["configured"].(map[string]any)
	is.True(cfg != nil) // configured block must be present
	is.Equal(cfg["power"], true)
	is.Equal(cfg["speed_mode"], "manual")
	v, _ := cfg["manual_pct"].(float64)
	is.Equal(v, float64(100))
	is.Equal(cfg["airflow_mode"], "extract")
	v, _ = cfg["co2_threshold_ppm"].(float64)
	is.Equal(v, float64(800))

	live, _ := resp["live"].(map[string]any)
	is.True(live != nil) // live block must be present
	v, _ = live["fan_extract_rpm"].(float64)
	is.Equal(v, float64(5400))
	is.Equal(live["in_user_control"], true) // no timer, no heater, no alerts

	sensors, _ := resp["sensors"].(map[string]any)
	is.True(sensors != nil) // sensors block must be present
	v, _ = sensors["humidity_pct"].(float64)
	is.Equal(v, float64(54))
	v, _ = sensors["temp_outdoor_c"].(float64)
	is.True(v > 21.0 && v < 21.4) // temp_outdoor_c ~ 21.2
	v, _ = sensors["recovery_efficiency_pct"].(float64)
	is.Equal(v, float64(85))

	service, _ := resp["service"].(map[string]any)
	is.True(service != nil) // service block must be present
	v, _ = service["rtc_battery_volts"].(float64)
	is.True(v > 3.30 && v < 3.40) // rtc_battery_volts ~ 3.34
	is.Equal(service["filter_status"], "clean")

	fw, _ := resp["firmware"].(map[string]any)
	is.True(fw != nil) // firmware block must be present
	is.Equal(fw["version"], "0.11")
}

func TestHandler_GetDevice_InUserControlFalseOnAlert(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	// Light up the CO2 alert byte (index 1).
	v[0x0084] = []byte{0x00, 0x01, 0x00, 0x00, 0x00}
	seedSnapshot(t, h, "playroom", v)

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	is.Equal(rec.Code, http.StatusOK)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	live := resp["live"].(map[string]any)
	is.Equal(live["in_user_control"], false) // CO2 alert removes user control
	alerts, _ := live["sensor_alerts"].(map[string]any)
	is.True(alerts != nil)        // sensor_alerts map must be present
	is.Equal(alerts["co2"], true) // CO2 alert flag must be set
}

// TestHandler_GetDevice_InUserControl_HeaterToggleStillUser confirms that
// the user toggling the heater on is configuration, NOT an override.
// in_user_control should remain true. The override signal for unexpected
// heater behavior is frost-protection (0x030B), tested separately below.
func TestHandler_GetDevice_InUserControl_HeaterToggleStillUser(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x0068] = []byte{0x01} // user asked for heat — that's configuration
	seedSnapshot(t, h, "playroom", v)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	live := resp["live"].(map[string]any)
	is.Equal(live["in_user_control"], true) // heater_control=1 is user configuration, not override
}

func TestHandler_GetDevice_InUserControlFalseOnFrostProtection(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x030B] = []byte{0x01} // frost protection actively running
	seedSnapshot(t, h, "playroom", v)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	live := resp["live"].(map[string]any)
	is.Equal(live["in_user_control"], false) // frost protection takes user out of control
}

func TestHandler_GetDevice_InUserControlFalseOnTimer(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x0007] = []byte{0x02} // turbo timer
	seedSnapshot(t, h, "playroom", v)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	live := resp["live"].(map[string]any)
	is.Equal(live["in_user_control"], false) // turbo timer takes user out of control
}

func TestHandler_GetDevice_IncludesEnergy(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	dir := t.TempDir()
	today := time.Now().Local().Format("2006-01-02")
	thisMonth := time.Now().Local().Format("2006-01")
	tr := &EnergyTracker{
		Device:             "playroom",
		StateDir:           dir,
		HeatingTodayKWh:    1.234,
		HeatingMonthKWh:    30.0,
		HeatingLifetimeKWh: 234.5,
		Today:              today,
		MonthStart:         thisMonth,
	}
	if h.Pollers == nil {
		h.Pollers = map[string]*Poller{}
	}
	h.Pollers["playroom"] = &Poller{Energy: tr}
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	is.Equal(rec.Code, http.StatusOK)
	var resp map[string]any
	is.NoErr(json.Unmarshal(rec.Body.Bytes(), &resp))
	service, ok := resp["service"].(map[string]any)
	is.True(ok) // service block present and is an object
	energy, ok := service["energy"].(map[string]any)
	is.True(ok) // service.energy present and is an object
	is.Equal(energy["heating_today_kwh"], 1.234)
	is.Equal(energy["heating_month_kwh"], 30.0)
	is.Equal(energy["heating_lifetime_kwh"], 234.5)
}

func TestHandler_GetDevice_NotFound(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/nope", nil)
	is.Equal(rec.Code, http.StatusNotFound)
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	is.Equal(env.Code, "not_found")
}

// ------------------------------------------------------------------------
// Firmware / efficiency / faults
// ------------------------------------------------------------------------

func TestHandler_Firmware(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/firmware", nil)
	is.Equal(rec.Code, http.StatusOK)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	is.Equal(resp["version"], "0.11")
}

func TestHandler_Firmware_BadBytes(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x0086] = []byte{0x01, 0x02} // wrong length: 2 bytes, decoder wants 6
	seedSnapshot(t, h, "playroom", v)

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/firmware", nil)
	is.Equal(rec.Code, http.StatusInternalServerError) // bad-length payload must surface as 500
}

func TestHandler_Efficiency(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/efficiency", nil)
	is.Equal(rec.Code, http.StatusOK)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	v, _ := resp["recovery_efficiency_pct"].(float64)
	is.Equal(v, float64(85))
}

func TestHandler_Faults_Empty(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x007F] = []byte{} // no faults; encoded as zero-length value
	seedSnapshot(t, h, "playroom", v)

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/faults", nil)
	is.Equal(rec.Code, http.StatusOK)
	var resp struct {
		Faults []map[string]any `json:"faults"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	is.Equal(len(resp.Faults), 0)
}

func TestHandler_Faults_TwoCodes(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	// 0x7F payload: each entry is (code uint8, kind uint8). 0=alarm, 1=warning.
	v[0x007F] = []byte{0x01, 0x00, 0x05, 0x01}
	seedSnapshot(t, h, "playroom", v)

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/faults", nil)
	is.Equal(rec.Code, http.StatusOK)
	var resp struct {
		Faults []map[string]any `json:"faults"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	is.Equal(len(resp.Faults), 2)
	v0, _ := resp.Faults[0]["code"].(float64)
	is.Equal(v0, float64(1))
	is.Equal(resp.Faults[0]["kind"], "alarm")
	is.Equal(resp.Faults[1]["kind"], "warning")
}

// ------------------------------------------------------------------------
// Raw param read (passthrough)
// ------------------------------------------------------------------------

func TestHandler_GetParam_Raw(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0001", nil)
	is.Equal(rec.Code, http.StatusOK)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// Power is on → 0x01 byte.
	is.Equal(resp["hex"], "01")
}

// ------------------------------------------------------------------------
// POST /power
// ------------------------------------------------------------------------

func TestHandler_PostPower(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": false})
	is.Equal(rec.Code, http.StatusOK)
	// Verify the value was written by reading it back via the cache passthrough.
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0001", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	is.Equal(resp["hex"], "00") // power off bytes through to 0x01
}

func TestHandler_PostPower_BadBody(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	// Missing "on" key.
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{})
	is.Equal(rec.Code, http.StatusBadRequest)
}

// ------------------------------------------------------------------------
// POST /speed
// ------------------------------------------------------------------------

func TestHandler_PostSpeed_Preset(t *testing.T) {
	is := is.New(t)
	h, rp, _ := newServerHandler(t)

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"preset": 2})
	is.Equal(rec.Code, http.StatusOK)
	// Verify 0x02 was set to 2.
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0002", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	is.Equal(resp["hex"], "02") // preset 2 lands at 0x02
	// NoticeWrite should have been called for 0x02.
	is.True(hasNotice(rp.all(), "playroom", 0x0002)) // NoticeWrite(0x0002) must fire
}

func TestHandler_PostSpeed_Manual(t *testing.T) {
	is := is.New(t)
	h, rp, _ := newServerHandler(t)

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"manual": 30})
	is.Equal(rec.Code, http.StatusOK)
	// 0x44 = 30, 0x02 = 0xFF (manual flag).
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0044", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	is.Equal(resp["hex"], "1e") // manual=30 lands at 0x44
	rec3 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0002", nil)
	var resp2 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp2)
	is.Equal(resp2["hex"], "ff") // manual flag set on 0x02
	notices := rp.all()
	is.True(hasNotice(notices, "playroom", 0x0044)) // NoticeWrite(0x0044) must fire
	is.True(hasNotice(notices, "playroom", 0x0002)) // NoticeWrite(0x0002) must fire
}

func TestHandler_PostSpeed_ManualBelowFloor(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"manual": 5})
	is.Equal(rec.Code, http.StatusBadRequest)
	is.True(strings.Contains(rec.Body.String(), "10")) // error message should mention firmware floor 10
}

func TestHandler_PostSpeed_BadPreset(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"preset": 7})
	is.Equal(rec.Code, http.StatusBadRequest)
}

func TestHandler_PostSpeed_NeitherFieldSet(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{})
	is.Equal(rec.Code, http.StatusBadRequest)
}

func TestHandler_PostSpeed_BothFieldsSet(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"preset": 1, "manual": 30})
	is.Equal(rec.Code, http.StatusBadRequest)
}

// ------------------------------------------------------------------------
// POST /mode
// ------------------------------------------------------------------------

func TestHandler_PostMode(t *testing.T) {
	cases := []struct {
		mode   string
		expect string
	}{
		{"ventilation", "00"},
		{"regeneration", "01"},
		{"supply", "02"},
		{"extract", "03"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			is := is.New(t)
			h, rp, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/mode", map[string]any{"mode": tc.mode})
			is.Equal(rec.Code, http.StatusOK)
			rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x00B7", nil)
			var resp map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
			is.Equal(resp["hex"], tc.expect)                 // 0xB7 must reflect the requested mode
			is.True(hasNotice(rp.all(), "playroom", 0x00B7)) // NoticeWrite(0x00B7) must fire
		})
	}
}

func TestHandler_PostMode_Bad(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/mode", map[string]any{"mode": "moonshot"})
	is.Equal(rec.Code, http.StatusBadRequest)
}

func TestHandler_PostMode_MissingField(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/mode", map[string]any{})
	is.Equal(rec.Code, http.StatusBadRequest)
}

func TestHandler_PostPreset(t *testing.T) {
	is := is.New(t)
	for _, c := range []struct {
		preset                    int
		supply, extract           int
		supplyParam, extParam     string
		supplyHex, extHex         string
		supplyParamID, extParamID breezy.ParamID
	}{
		{1, 30, 35, "0x003A", "0x003B", "1e", "23", 0x003A, 0x003B},
		{2, 55, 60, "0x003C", "0x003D", "37", "3c", 0x003C, 0x003D},
		{3, 100, 100, "0x003E", "0x003F", "64", "64", 0x003E, 0x003F},
	} {
		h, rp, _ := newServerHandler(t)
		rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/preset", map[string]any{
			"preset": c.preset, "supply": c.supply, "extract": c.extract,
		})
		is.Equal(rec.Code, http.StatusOK) // each preset POST must succeed
		recS := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/"+c.supplyParam, nil)
		var rs map[string]any
		_ = json.Unmarshal(recS.Body.Bytes(), &rs)
		is.Equal(rs["hex"], c.supplyHex) // supply param matches expected hex
		recE := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/"+c.extParam, nil)
		var re map[string]any
		_ = json.Unmarshal(recE.Body.Bytes(), &re)
		is.Equal(re["hex"], c.extHex) // extract param matches expected hex
		// Both preset writes must trip the fan-settle window — editing the
		// active preset ramps the running fan, so 0x3A/3B/3C/3D/3E/3F must
		// be in fanWriteIDs (poller.go) and notified to the poller.
		notices := rp.all()
		is.True(hasNotice(notices, "playroom", c.supplyParamID)) // supply NoticeWrite must fire
		is.True(hasNotice(notices, "playroom", c.extParamID))    // extract NoticeWrite must fire
	}
}

func TestHandler_PostPreset_BadInputs(t *testing.T) {
	for name, body := range map[string]map[string]any{
		"missing preset":      {"supply": 50, "extract": 50},
		"missing supply":      {"preset": 1, "extract": 50},
		"missing extract":     {"preset": 1, "supply": 50},
		"preset out of range": {"preset": 4, "supply": 50, "extract": 50},
		"supply too low":      {"preset": 1, "supply": 5, "extract": 50},
		"extract too high":    {"preset": 1, "supply": 50, "extract": 150},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/preset", body)
			is.Equal(rec.Code, http.StatusBadRequest)
		})
	}
}

// ------------------------------------------------------------------------
// POST /heater, /filter/reset, /faults/reset
// ------------------------------------------------------------------------

func TestHandler_PostHeater(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/heater", map[string]any{"on": true})
	is.Equal(rec.Code, http.StatusOK)
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0068", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	is.Equal(resp["hex"], "01") // heater_control byte must be 01
}

func TestHandler_PostTimer(t *testing.T) {
	for _, mode := range []struct {
		name string
		hex  string
	}{
		{"off", "00"},
		{"night", "01"},
		{"turbo", "02"},
	} {
		t.Run(mode.name, func(t *testing.T) {
			is := is.New(t)
			h, rp, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/timer", map[string]any{"mode": mode.name})
			is.Equal(rec.Code, http.StatusOK)
			rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0007", nil)
			var resp map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
			is.Equal(resp["hex"], mode.hex)                  // timer mode lands at 0x07
			is.True(hasNotice(rp.all(), "playroom", 0x0007)) // NoticeWrite(0x0007) must fire
		})
	}
}

func TestHandler_PostTimer_BadMode(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/timer", map[string]any{"mode": "sleep"})
	is.Equal(rec.Code, http.StatusBadRequest)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	is.Equal(env["code"], "bad_request")
}

func TestHandler_PostTimer_MissingMode(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/timer", map[string]any{})
	is.Equal(rec.Code, http.StatusBadRequest)
}

func TestHandler_PostThreshold(t *testing.T) {
	for _, c := range []struct {
		kind  string
		value int
		id    string
		hex   string
	}{
		{"humidity", 65, "0x0019", "41"},
		{"co2", 1500, "0x001A", "dc05"},
		{"voc", 200, "0x031F", "c800"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			is := is.New(t)
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold",
				map[string]any{"kind": c.kind, "value": c.value})
			is.Equal(rec.Code, http.StatusOK)
			rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/"+c.id, nil)
			var resp map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
			is.Equal(resp["hex"], c.hex) // threshold value lands at expected hex
		})
	}
}

func TestHandler_PostThreshold_BadInputs(t *testing.T) {
	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"missing kind", map[string]any{"value": 50}},
		{"missing both value and enabled", map[string]any{"kind": "humidity"}},
		{"unknown kind", map[string]any{"kind": "temperature", "value": 50}},
		{"out of range humidity", map[string]any{"kind": "humidity", "value": 90}},
		{"co2 not multiple of 10", map[string]any{"kind": "co2", "value": 1505}},
		{"out of range voc", map[string]any{"kind": "voc", "value": 300}},
	} {
		t.Run(c.name, func(t *testing.T) {
			is := is.New(t)
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold", c.body)
			is.Equal(rec.Code, http.StatusBadRequest)
			var env map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &env)
			is.Equal(env["code"], "bad_request")
		})
	}
}

func TestHandler_PostThreshold_EnabledOnly(t *testing.T) {
	for _, c := range []struct {
		kind     string
		enabled  bool
		paramHex string // expected hex byte at the corresponding enable param
		paramID  string
	}{
		{"humidity", true, "01", "0x000F"},
		{"co2", false, "00", "0x0011"},
		{"voc", true, "01", "0x0315"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			is := is.New(t)
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold",
				map[string]any{"kind": c.kind, "enabled": c.enabled})
			is.Equal(rec.Code, http.StatusOK)
			rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/"+c.paramID, nil)
			var resp map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
			is.Equal(resp["hex"], c.paramHex) // enable byte lands at expected hex
		})
	}
}

// The three Releases* tests below verify that the per-device UDP
// mutex is released on every exit path of doDeviceOp / doDeviceRead.
// They do NOT verify that the underlying socket Close runs BEFORE
// the unlock — that LIFO defer ordering is documented on h.dial
// (see server.go) but not directly observable from outside the
// helpers. If you ever flip the two defer lines in doDeviceOp, all
// three tests still pass; rely on code review for that invariant.
func TestDoDeviceOp_ReleasesLockOnSuccess(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	// newServerHandler wires NoticeFunc but leaves Pollers empty; install a
	// real Poller so LockUDP returns a real mutex (the no-op fallback in
	// lockDevice would mask a leak).
	h.Pollers = map[string]*Poller{"playroom": {Name: "playroom"}}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	called := false
	err := h.doDeviceOp(req, "playroom", func(ctx context.Context, rc *recordingClient) error {
		called = true
		return nil
	})
	is.NoErr(err)
	is.True(called) // op closure must be invoked
	// Lock must be released — taking it again must not block.
	done := make(chan struct{})
	go func() {
		unlock := h.Pollers["playroom"].LockUDP()
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lock not released after successful op")
	}
}

func TestDoDeviceOp_ReleasesLockOnError(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	h.Pollers = map[string]*Poller{"playroom": {Name: "playroom"}}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	want := errors.New("op exploded")
	got := h.doDeviceOp(req, "playroom", func(ctx context.Context, rc *recordingClient) error {
		return want
	})
	is.True(errors.Is(got, want)) // op error must propagate unwrapped
	done := make(chan struct{})
	go func() {
		unlock := h.Pollers["playroom"].LockUDP()
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lock not released after errored op")
	}
}

func TestDoDeviceRead_ReleasesLockOnError(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	h.Pollers = map[string]*Poller{"playroom": {Name: "playroom"}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	want := errors.New("read failed")
	got := h.doDeviceRead(req, "playroom", func(ctx context.Context, c HandlerClient) error {
		return want
	})
	is.True(errors.Is(got, want)) // read error must propagate unwrapped
	done := make(chan struct{})
	go func() {
		unlock := h.Pollers["playroom"].LockUDP()
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lock not released after errored read")
	}
}

func TestHandler_PostThreshold_ValueAndEnabled(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold",
		map[string]any{"kind": "humidity", "value": 65, "enabled": false})
	is.Equal(rec.Code, http.StatusOK)
	// Both params should reflect the write.
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0019", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	is.Equal(resp["hex"], "41") // humidity threshold lands at 0x19
	rec3 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x000F", nil)
	var resp3 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp3)
	is.Equal(resp3["hex"], "00") // humidity enable lands at 0x0F
}

func TestHandler_PostFilterReset(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/filter/reset", nil)
	is.Equal(rec.Code, http.StatusOK)
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0065", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	is.Equal(resp["hex"], "01") // filter reset lands at 0x65
}

func TestHandler_PostFaultsReset(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/faults/reset", nil)
	is.Equal(rec.Code, http.StatusOK)
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0080", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	is.Equal(resp["hex"], "01") // faults reset lands at 0x80
}

// ------------------------------------------------------------------------
// POST /rtc
// ------------------------------------------------------------------------

func TestHandler_PostRTC(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/rtc", map[string]any{"time": "2026-05-03T22:36:30Z"})
	is.Equal(rec.Code, http.StatusOK)
	// Read 0x6F: should be [sec=30, min=36, hr=22] = 1E 24 16
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x006F", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	is.Equal(resp["hex"], "1e2416") // RTC time bytes [sec, min, hr]
	// Read 0x70: should be [day=3, dow=7, month=5, year=26] (2026-05-03 is a Sunday)
	rec3 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0070", nil)
	var resp2 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp2)
	is.Equal(resp2["hex"], "0307051a") // RTC date bytes [day, dow, month, year]
}

func TestHandler_PostRTC_BadTime(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/rtc", map[string]any{"time": "not-a-time"})
	is.Equal(rec.Code, http.StatusBadRequest)
}

// ------------------------------------------------------------------------
// POST /params/{id} raw write
// ------------------------------------------------------------------------

func TestHandler_PostParam_Raw(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/params/0x0019", map[string]any{"hex": "50"})
	is.Equal(rec.Code, http.StatusOK)
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0019", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	is.Equal(resp["hex"], "50") // raw write must round-trip through cache
}

func TestHandler_PostParam_ReadOnly(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	// 0x004A (fan_supply_rpm) is read-only.
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/params/0x004A", map[string]any{"hex": "0000"})
	is.Equal(rec.Code, http.StatusForbidden)
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	is.Equal(env.Code, "read_only")
}

func TestHandler_PostParam_BadHex(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/params/0x0019", map[string]any{"hex": "ZZ"})
	is.Equal(rec.Code, http.StatusBadRequest)
}

// ------------------------------------------------------------------------
// Write-through cache (POST updates cache without waiting for next poll)
// ------------------------------------------------------------------------

func TestHandler_PostPower_WriteThroughVisibleInGetDevice(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	// Seed cache with power=off.
	v := snapshotAllParams(t)
	v[0x0001] = []byte{0x00}
	seedSnapshot(t, h, "playroom", v)

	// Confirm the cache currently shows power=off.
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var pre map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &pre)
	cfg, _ := pre["configured"].(map[string]any)
	is.Equal(cfg["power"], false) // pre-write cache reflects seeded power=off

	// Issue the write.
	rec2 := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": true})
	is.Equal(rec2.Code, http.StatusOK)

	// GET should reflect the new value immediately, without a poll tick.
	rec3 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var post map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &post)
	cfg2, _ := post["configured"].(map[string]any)
	is.Equal(cfg2["power"], true) // write-through: GET reflects POST without a poll tick
}

func TestHandler_PostSpeed_WriteThroughManual(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"manual": 42})
	is.Equal(rec.Code, http.StatusOK)

	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	cfg, _ := resp["configured"].(map[string]any)
	v, _ := cfg["manual_pct"].(float64)
	is.Equal(v, float64(42))              // write-through: manual_pct reflects POST
	is.Equal(cfg["speed_mode"], "manual") // write-through: speed_mode flips to manual
}

// ------------------------------------------------------------------------
// Error-path: auth_failed and device_unreachable
// ------------------------------------------------------------------------

func TestHandler_AuthFailed(t *testing.T) {
	is := is.New(t)
	h, _, addr := newServerHandler(t)
	h.Devices.Set("playroom", DeviceConfig{ID: srvDeviceID, Password: "WRONG", IP: addr})

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": true})
	is.Equal(rec.Code, http.StatusBadGateway)
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	is.Equal(env.Code, "auth_failed")
}

func TestHandler_DeviceUnreachable(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	// 192.0.2.0/24 is the TEST-NET-1 range — guaranteed unrouteable.
	h.Devices.Set("playroom", DeviceConfig{ID: srvDeviceID, Password: srvPassword, IP: "192.0.2.1:4000"})

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": true})
	is.Equal(rec.Code, http.StatusBadGateway)
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	is.Equal(env.Code, "device_unreachable")
}

// ------------------------------------------------------------------------
// 404 on unknown device for various endpoints
// ------------------------------------------------------------------------

func TestHandler_NotFound_OnUnknownDevice(t *testing.T) {
	h, _, _ := newServerHandler(t)
	endpoints := []struct {
		method, path string
	}{
		{http.MethodGet, "/v1/devices/zzz"},
		{http.MethodGet, "/v1/devices/zzz/firmware"},
		{http.MethodGet, "/v1/devices/zzz/efficiency"},
		{http.MethodGet, "/v1/devices/zzz/faults"},
		{http.MethodGet, "/v1/devices/zzz/params/0x0001"},
		{http.MethodPost, "/v1/devices/zzz/power"},
		{http.MethodPost, "/v1/devices/zzz/speed"},
	}
	for _, ep := range endpoints {
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			is := is.New(t)
			rec := doRequest(t, h, ep.method, ep.path, map[string]any{"on": true})
			is.Equal(rec.Code, http.StatusNotFound)
		})
	}
}

// ------------------------------------------------------------------------
// Helpers
// ------------------------------------------------------------------------

func hasNotice(notices []recordedNotice, dev string, id breezy.ParamID) bool {
	for _, n := range notices {
		if n.Device == dev && n.ID == id {
			return true
		}
	}
	return false
}

func TestHandler_ParamGet_RawHexParse(t *testing.T) {
	is := is.New(t)
	// An ID without "0x" prefix should also be accepted.
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0001", nil)
	is.Equal(rec.Code, http.StatusOK)
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0xdeadbeef", nil)
	is.True(rec2.Code != http.StatusOK) // too-large id must not be accepted
}

func TestHandler_FactoryError(t *testing.T) {
	is := is.New(t)
	h, _, _ := newServerHandler(t)
	wantErr := errors.New("factory boom")
	h.ClientFactory = func(name string) (HandlerClient, error) {
		return nil, wantErr
	}
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": true})
	is.Equal(rec.Code, http.StatusInternalServerError)
}

func TestErrEnvelope_Shape(t *testing.T) {
	is := is.New(t)
	// Catch any future regression that drops required fields.
	want := errEnvelope{Error: "boom", Code: "bad_request"}
	b, _ := json.Marshal(want)
	is.True(bytes.Contains(b, []byte(`"error":`))) // envelope JSON must include "error" field
	is.True(bytes.Contains(b, []byte(`"code":`)))  // envelope JSON must include "code" field
}

// ------------------------------------------------------------------------
// Schedule handler helpers and tests
// ------------------------------------------------------------------------

// newServerHandlerWithSchedule extends newServerHandler with a per-device
// Scheduler wired into Handler.Schedulers, plus the stateDir that backs
// it. Used by schedule HTTP handler tests so they can verify persistence
// by reading the file directly.
func newServerHandlerWithSchedule(t *testing.T) (h *Handler, rp *recordingPoller, addr, stateDir string) {
	t.Helper()
	h, rp, addr = newServerHandler(t)
	stateDir = t.TempDir()
	sch := &Scheduler{Device: "playroom", StateDir: stateDir}
	sch.Load() // empty initial state
	h.Schedulers = map[string]*Scheduler{"playroom": sch}
	return h, rp, addr, stateDir
}

func TestHandler_GetSchedule_Empty(t *testing.T) {
	is := is.New(t)
	h, _, _, _ := newServerHandlerWithSchedule(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/schedule", nil)
	is.Equal(rec.Code, http.StatusOK)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	is.Equal(body["enabled"], false) // unconfigured scheduler is disabled
	entries, _ := body["entries"].([]any)
	is.Equal(len(entries), 0) // unconfigured scheduler has no entries
}

func TestHandler_PutSchedule_Roundtrip(t *testing.T) {
	is := is.New(t)
	h, _, _, _ := newServerHandlerWithSchedule(t)
	put := map[string]any{
		"enabled": true,
		"entries": []map[string]any{
			{"at": "08:00", "action": "regeneration", "pct": 60},
			{"at": "22:00", "action": "off", "pct": 60},
		},
	}
	rec := doRequest(t, h, http.MethodPut, "/v1/devices/playroom/schedule", put)
	is.Equal(rec.Code, http.StatusOK)
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/schedule", nil)
	var got map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &got)
	is.Equal(got["enabled"], true) // enabled flag round-trips
	entries, _ := got["entries"].([]any)
	is.Equal(len(entries), 2) // both entries round-trip
}

func TestHandler_PutSchedule_Validation(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{"bad action", map[string]any{"enabled": true, "entries": []map[string]any{{"at": "08:00", "action": "boost", "pct": 60}}}},
		{"low pct", map[string]any{"enabled": true, "entries": []map[string]any{{"at": "08:00", "action": "regeneration", "pct": 5}}}},
		{"high pct", map[string]any{"enabled": true, "entries": []map[string]any{{"at": "08:00", "action": "regeneration", "pct": 101}}}},
		{"bad at", map[string]any{"enabled": true, "entries": []map[string]any{{"at": "08:60", "action": "regeneration", "pct": 60}}}},
		{"duplicate at", map[string]any{"enabled": true, "entries": []map[string]any{
			{"at": "10:00", "action": "regeneration", "pct": 60},
			{"at": "10:00", "action": "off", "pct": 60},
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			is := is.New(t)
			h, _, _, _ := newServerHandlerWithSchedule(t)
			rec := doRequest(t, h, http.MethodPut, "/v1/devices/playroom/schedule", c.body)
			is.Equal(rec.Code, http.StatusBadRequest)
		})
	}
}

func TestHandler_GetDevice_IncludesSchedule(t *testing.T) {
	is := is.New(t)
	h, _, _, _ := newServerHandlerWithSchedule(t)
	put := map[string]any{
		"enabled": true,
		"entries": []map[string]any{{"at": "08:00", "action": "regeneration", "pct": 60}},
	}
	rec := doRequest(t, h, http.MethodPut, "/v1/devices/playroom/schedule", put)
	is.Equal(rec.Code, http.StatusOK) // seed PUT must succeed
	rec = doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	is.Equal(rec.Code, http.StatusOK)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	svc, _ := body["service"].(map[string]any)
	sched, _ := svc["schedule"].(map[string]any)
	is.True(sched != nil) // service.schedule must be present
	is.Equal(sched["enabled"], true)
	entries, _ := sched["entries"].([]any)
	is.Equal(len(entries), 1)
	_, ok := sched["alert"]
	is.True(ok) // service.schedule.alert key must be present
	_, present := sched["last_apply"]
	is.True(!present) // last_apply must be absent before any fire
}

func TestHandler_PutSchedule_Persists(t *testing.T) {
	is := is.New(t)
	h, _, _, stateDir := newServerHandlerWithSchedule(t)
	put := map[string]any{
		"enabled": true,
		"entries": []map[string]any{{"at": "08:00", "action": "regeneration", "pct": 60}},
	}
	rec := doRequest(t, h, http.MethodPut, "/v1/devices/playroom/schedule", put)
	is.Equal(rec.Code, http.StatusOK)
	data, err := os.ReadFile(filepath.Join(stateDir, "schedule_playroom.json"))
	is.NoErr(err)
	is.True(strings.Contains(string(data), `"action":"regeneration"`)) // schedule file must contain the entry
}
