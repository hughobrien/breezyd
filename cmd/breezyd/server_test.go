// SPDX-License-Identifier: GPL-3.0-or-later

// Tests for the daemon HTTP API. We exercise the handler via httptest.NewRecorder
// (no real network; the in-process fakedevice still uses UDP, but tests dial
// it directly through the breezy.Client constructed by the production
// clientFactory, or via stub HandlerClients for cases the fakedevice can't
// model — read-only enforcement, NoticeWrite tracking, etc.).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
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
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandler_ListDevices(t *testing.T) {
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Devices) != 1 {
		t.Fatalf("len(devices) = %d, want 1; body=%s", len(resp.Devices), rec.Body.String())
	}
	if resp.Devices[0]["name"] != "playroom" {
		t.Errorf("device name = %v, want playroom", resp.Devices[0]["name"])
	}
}

// ------------------------------------------------------------------------
// Structured snapshot
// ------------------------------------------------------------------------

func TestHandler_GetDevice_StructuredSnapshot(t *testing.T) {
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}

	// Top-level identity.
	if resp["name"] != "playroom" {
		t.Errorf("name = %v, want playroom", resp["name"])
	}
	if resp["id"] != srvDeviceID {
		t.Errorf("id = %v, want %s", resp["id"], srvDeviceID)
	}

	// Block presence and a few decoded fields.
	cfg, _ := resp["configured"].(map[string]any)
	if cfg == nil {
		t.Fatalf("configured block missing: %s", rec.Body.String())
	}
	if cfg["power"] != true {
		t.Errorf("configured.power = %v, want true", cfg["power"])
	}
	if cfg["speed_mode"] != "manual" {
		t.Errorf("configured.speed_mode = %v, want manual", cfg["speed_mode"])
	}
	if v, _ := cfg["manual_pct"].(float64); v != 100 {
		t.Errorf("configured.manual_pct = %v, want 100", cfg["manual_pct"])
	}
	if cfg["airflow_mode"] != "extract" {
		t.Errorf("configured.airflow_mode = %v, want extract", cfg["airflow_mode"])
	}
	if v, _ := cfg["co2_threshold_ppm"].(float64); v != 800 {
		t.Errorf("configured.co2_threshold_ppm = %v, want 800", cfg["co2_threshold_ppm"])
	}

	live, _ := resp["live"].(map[string]any)
	if live == nil {
		t.Fatalf("live block missing")
	}
	if v, _ := live["fan_extract_rpm"].(float64); v != 5400 {
		t.Errorf("live.fan_extract_rpm = %v, want 5400", live["fan_extract_rpm"])
	}
	if live["in_user_control"] != true {
		t.Errorf("live.in_user_control = %v, want true (no timer, no heater, no alerts)", live["in_user_control"])
	}

	sensors, _ := resp["sensors"].(map[string]any)
	if sensors == nil {
		t.Fatalf("sensors block missing")
	}
	if v, _ := sensors["humidity_pct"].(float64); v != 54 {
		t.Errorf("sensors.humidity_pct = %v, want 54", sensors["humidity_pct"])
	}
	if v, _ := sensors["temp_outdoor_c"].(float64); v <= 21.0 || v >= 21.4 {
		t.Errorf("sensors.temp_outdoor_c = %v, want ~21.2", sensors["temp_outdoor_c"])
	}
	if v, _ := sensors["recovery_efficiency_pct"].(float64); v != 85 {
		t.Errorf("sensors.recovery_efficiency_pct = %v, want 85", sensors["recovery_efficiency_pct"])
	}

	service, _ := resp["service"].(map[string]any)
	if service == nil {
		t.Fatalf("service block missing")
	}
	if v, _ := service["rtc_battery_volts"].(float64); v <= 3.30 || v >= 3.40 {
		t.Errorf("service.rtc_battery_volts = %v, want ~3.34", service["rtc_battery_volts"])
	}
	if service["filter_status"] != "clean" {
		t.Errorf("service.filter_status = %v, want clean", service["filter_status"])
	}

	fw, _ := resp["firmware"].(map[string]any)
	if fw == nil {
		t.Fatalf("firmware block missing")
	}
	if fw["version"] != "0.11" {
		t.Errorf("firmware.version = %v, want 0.11", fw["version"])
	}
}

func TestHandler_GetDevice_InUserControlFalseOnAlert(t *testing.T) {
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	// Light up the CO2 alert byte (index 1).
	v[0x0084] = []byte{0x00, 0x01, 0x00, 0x00, 0x00}
	seedSnapshot(t, h, "playroom", v)

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	live := resp["live"].(map[string]any)
	if live["in_user_control"] != false {
		t.Errorf("in_user_control = %v, want false (CO2 alert)", live["in_user_control"])
	}
	alerts, _ := live["sensor_alerts"].(map[string]any)
	if alerts == nil || alerts["co2"] != true {
		t.Errorf("sensor_alerts.co2 = %v, want true; alerts=%+v", alerts["co2"], alerts)
	}
}

// TestHandler_GetDevice_InUserControl_HeaterToggleStillUser confirms that
// the user toggling the heater on is configuration, NOT an override.
// in_user_control should remain true. The override signal for unexpected
// heater behavior is frost-protection (0x030B), tested separately below.
func TestHandler_GetDevice_InUserControl_HeaterToggleStillUser(t *testing.T) {
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x0068] = []byte{0x01} // user asked for heat — that's configuration
	seedSnapshot(t, h, "playroom", v)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	live := resp["live"].(map[string]any)
	if live["in_user_control"] != true {
		t.Errorf("in_user_control = %v, want true (heater_control=1 is user configuration, not override)", live["in_user_control"])
	}
}

func TestHandler_GetDevice_InUserControlFalseOnFrostProtection(t *testing.T) {
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x030B] = []byte{0x01} // frost protection actively running
	seedSnapshot(t, h, "playroom", v)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	live := resp["live"].(map[string]any)
	if live["in_user_control"] != false {
		t.Errorf("in_user_control = %v, want false (frost protection active)", live["in_user_control"])
	}
}

func TestHandler_GetDevice_InUserControlFalseOnTimer(t *testing.T) {
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x0007] = []byte{0x02} // turbo timer
	seedSnapshot(t, h, "playroom", v)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	live := resp["live"].(map[string]any)
	if live["in_user_control"] != false {
		t.Errorf("in_user_control = %v, want false (turbo timer)", live["in_user_control"])
	}
}

func TestHandler_GetDevice_IncludesEnergy(t *testing.T) {
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
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	service, ok := resp["service"].(map[string]any)
	if !ok {
		t.Fatalf("service block missing or wrong type: %v", resp)
	}
	energy, ok := service["energy"].(map[string]any)
	if !ok {
		t.Fatalf("service.energy missing or wrong type: %v", service)
	}
	if energy["heating_today_kwh"] != 1.234 {
		t.Errorf("heating_today_kwh = %v, want 1.234", energy["heating_today_kwh"])
	}
	if energy["heating_month_kwh"] != 30.0 {
		t.Errorf("heating_month_kwh = %v, want 30.0", energy["heating_month_kwh"])
	}
	if energy["heating_lifetime_kwh"] != 234.5 {
		t.Errorf("heating_lifetime_kwh = %v, want 234.5", energy["heating_lifetime_kwh"])
	}
}

func TestHandler_GetDevice_NotFound(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/nope", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Code != "not_found" {
		t.Errorf("code = %q, want not_found", env.Code)
	}
}

// ------------------------------------------------------------------------
// Firmware / efficiency / faults
// ------------------------------------------------------------------------

func TestHandler_Firmware(t *testing.T) {
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/firmware", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["version"] != "0.11" {
		t.Errorf("version = %v, want 0.11", resp["version"])
	}
}

func TestHandler_Firmware_BadBytes(t *testing.T) {
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x0086] = []byte{0x01, 0x02} // wrong length: 2 bytes, decoder wants 6
	seedSnapshot(t, h, "playroom", v)

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/firmware", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_Efficiency(t *testing.T) {
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/efficiency", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if v, _ := resp["recovery_efficiency_pct"].(float64); v != 85 {
		t.Errorf("recovery_efficiency_pct = %v, want 85", resp["recovery_efficiency_pct"])
	}
}

func TestHandler_Faults_Empty(t *testing.T) {
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	v[0x007F] = []byte{} // no faults; encoded as zero-length value
	seedSnapshot(t, h, "playroom", v)

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/faults", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Faults []map[string]any `json:"faults"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Faults) != 0 {
		t.Errorf("len(faults) = %d, want 0", len(resp.Faults))
	}
}

func TestHandler_Faults_TwoCodes(t *testing.T) {
	h, _, _ := newServerHandler(t)
	v := snapshotAllParams(t)
	// 0x7F payload: each entry is (code uint8, kind uint8). 0=alarm, 1=warning.
	v[0x007F] = []byte{0x01, 0x00, 0x05, 0x01}
	seedSnapshot(t, h, "playroom", v)

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/faults", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Faults []map[string]any `json:"faults"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Faults) != 2 {
		t.Fatalf("len(faults) = %d, want 2; body=%s", len(resp.Faults), rec.Body.String())
	}
	if v, _ := resp.Faults[0]["code"].(float64); v != 1 {
		t.Errorf("faults[0].code = %v, want 1", resp.Faults[0]["code"])
	}
	if resp.Faults[0]["kind"] != "alarm" {
		t.Errorf("faults[0].kind = %v, want alarm", resp.Faults[0]["kind"])
	}
	if resp.Faults[1]["kind"] != "warning" {
		t.Errorf("faults[1].kind = %v, want warning", resp.Faults[1]["kind"])
	}
}

// ------------------------------------------------------------------------
// Raw param read (passthrough)
// ------------------------------------------------------------------------

func TestHandler_GetParam_Raw(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0001", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	// Power is on → 0x01 byte.
	if resp["hex"] != "01" {
		t.Errorf("hex = %v, want 01", resp["hex"])
	}
}

// ------------------------------------------------------------------------
// POST /power
// ------------------------------------------------------------------------

func TestHandler_PostPower(t *testing.T) {
	h, _, _ := newServerHandler(t)

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Verify the value was written by reading it back via the cache passthrough.
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0001", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "00" {
		t.Errorf("after power off, hex = %v, want 00", resp["hex"])
	}
}

func TestHandler_PostPower_BadBody(t *testing.T) {
	h, _, _ := newServerHandler(t)
	// Missing "on" key.
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// ------------------------------------------------------------------------
// POST /speed
// ------------------------------------------------------------------------

func TestHandler_PostSpeed_Preset(t *testing.T) {
	h, rp, _ := newServerHandler(t)

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"preset": 2})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Verify 0x02 was set to 2.
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0002", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "02" {
		t.Errorf("speed_mode hex = %v, want 02", resp["hex"])
	}
	// NoticeWrite should have been called for 0x02.
	if !hasNotice(rp.all(), "playroom", 0x0002) {
		t.Errorf("NoticeWrite(0x0002) was not called; got=%+v", rp.all())
	}
}

func TestHandler_PostSpeed_Manual(t *testing.T) {
	h, rp, _ := newServerHandler(t)

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"manual": 30})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 0x44 = 30, 0x02 = 0xFF (manual flag).
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0044", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "1e" {
		t.Errorf("manual hex = %v, want 1e", resp["hex"])
	}
	rec3 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0002", nil)
	var resp2 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp2)
	if resp2["hex"] != "ff" {
		t.Errorf("speed_mode hex after manual = %v, want ff", resp2["hex"])
	}
	notices := rp.all()
	if !hasNotice(notices, "playroom", 0x0044) || !hasNotice(notices, "playroom", 0x0002) {
		t.Errorf("expected notices for 0x44 and 0x02; got=%+v", notices)
	}
}

func TestHandler_PostSpeed_ManualBelowFloor(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"manual": 5})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "10") {
		t.Errorf("error message should mention firmware floor 10; got %s", rec.Body.String())
	}
}

func TestHandler_PostSpeed_BadPreset(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"preset": 7})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PostSpeed_NeitherFieldSet(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PostSpeed_BothFieldsSet(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"preset": 1, "manual": 30})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
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
			h, rp, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/mode", map[string]any{"mode": tc.mode})
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x00B7", nil)
			var resp map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
			if resp["hex"] != tc.expect {
				t.Errorf("0xB7 = %v, want %v", resp["hex"], tc.expect)
			}
			if !hasNotice(rp.all(), "playroom", 0x00B7) {
				t.Errorf("NoticeWrite(0x00B7) was not called")
			}
		})
	}
}

func TestHandler_PostMode_Bad(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/mode", map[string]any{"mode": "moonshot"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PostMode_MissingField(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/mode", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PostPreset(t *testing.T) {
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
		if rec.Code != http.StatusOK {
			t.Fatalf("preset=%d status=%d body=%s", c.preset, rec.Code, rec.Body.String())
		}
		recS := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/"+c.supplyParam, nil)
		var rs map[string]any
		_ = json.Unmarshal(recS.Body.Bytes(), &rs)
		if rs["hex"] != c.supplyHex {
			t.Errorf("preset=%d supply hex = %v, want %s", c.preset, rs["hex"], c.supplyHex)
		}
		recE := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/"+c.extParam, nil)
		var re map[string]any
		_ = json.Unmarshal(recE.Body.Bytes(), &re)
		if re["hex"] != c.extHex {
			t.Errorf("preset=%d extract hex = %v, want %s", c.preset, re["hex"], c.extHex)
		}
		// Both preset writes must trip the fan-settle window — editing the
		// active preset ramps the running fan, so 0x3A/3B/3C/3D/3E/3F must
		// be in fanWriteIDs (poller.go) and notified to the poller.
		notices := rp.all()
		if !hasNotice(notices, "playroom", c.supplyParamID) {
			t.Errorf("preset=%d: NoticeWrite(%#04x) not called; got=%+v", c.preset, c.supplyParamID, notices)
		}
		if !hasNotice(notices, "playroom", c.extParamID) {
			t.Errorf("preset=%d: NoticeWrite(%#04x) not called; got=%+v", c.preset, c.extParamID, notices)
		}
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
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/preset", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

// ------------------------------------------------------------------------
// POST /heater, /filter/reset, /faults/reset
// ------------------------------------------------------------------------

func TestHandler_PostHeater(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/heater", map[string]any{"on": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0068", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "01" {
		t.Errorf("heater_control = %v, want 01", resp["hex"])
	}
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
			h, rp, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/timer", map[string]any{"mode": mode.name})
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0007", nil)
			var resp map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
			if resp["hex"] != mode.hex {
				t.Errorf("timer mode = %v, want %s", resp["hex"], mode.hex)
			}
			if !hasNotice(rp.all(), "playroom", 0x0007) {
				t.Errorf("NoticeWrite(0x0007) was not called for mode=%q", mode.name)
			}
		})
	}
}

func TestHandler_PostTimer_BadMode(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/timer", map[string]any{"mode": "sleep"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env["code"] != "bad_request" {
		t.Errorf("error code = %v, want bad_request", env["code"])
	}
}

func TestHandler_PostTimer_MissingMode(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/timer", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}
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
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold",
				map[string]any{"kind": c.kind, "value": c.value})
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/"+c.id, nil)
			var resp map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
			if resp["hex"] != c.hex {
				t.Errorf("%s threshold = %v, want %s", c.kind, resp["hex"], c.hex)
			}
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
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			var env map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &env)
			if env["code"] != "bad_request" {
				t.Errorf("error code = %v, want bad_request", env["code"])
			}
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
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold",
				map[string]any{"kind": c.kind, "enabled": c.enabled})
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/"+c.paramID, nil)
			var resp map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
			if resp["hex"] != c.paramHex {
				t.Errorf("%s enable = %v, want %s", c.kind, resp["hex"], c.paramHex)
			}
		})
	}
}

func TestHandler_PostThreshold_ValueAndEnabled(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold",
		map[string]any{"kind": "humidity", "value": 65, "enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Both params should reflect the write.
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0019", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "41" {
		t.Errorf("humidity threshold hex = %v, want 41", resp["hex"])
	}
	rec3 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x000F", nil)
	var resp3 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp3)
	if resp3["hex"] != "00" {
		t.Errorf("humidity enable hex = %v, want 00", resp3["hex"])
	}
}

func TestHandler_PostFilterReset(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/filter/reset", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0065", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "01" {
		t.Errorf("0x65 = %v, want 01", resp["hex"])
	}
}

func TestHandler_PostFaultsReset(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/faults/reset", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0080", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "01" {
		t.Errorf("0x80 = %v, want 01", resp["hex"])
	}
}

// ------------------------------------------------------------------------
// POST /rtc
// ------------------------------------------------------------------------

func TestHandler_PostRTC(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/rtc", map[string]any{"time": "2026-05-03T22:36:30Z"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Read 0x6F: should be [sec=30, min=36, hr=22] = 1E 24 16
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x006F", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "1e2416" {
		t.Errorf("0x6F = %v, want 1e2416", resp["hex"])
	}
	// Read 0x70: should be [day=3, dow=7, month=5, year=26] (2026-05-03 is a Sunday)
	rec3 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0070", nil)
	var resp2 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp2)
	if resp2["hex"] != "0307051a" {
		t.Errorf("0x70 = %v, want 0307051a", resp2["hex"])
	}
}

func TestHandler_PostRTC_BadTime(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/rtc", map[string]any{"time": "not-a-time"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// ------------------------------------------------------------------------
// POST /params/{id} raw write
// ------------------------------------------------------------------------

func TestHandler_PostParam_Raw(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/params/0x0019", map[string]any{"hex": "50"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0019", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "50" {
		t.Errorf("0x19 = %v, want 50", resp["hex"])
	}
}

func TestHandler_PostParam_ReadOnly(t *testing.T) {
	h, _, _ := newServerHandler(t)
	// 0x004A (fan_supply_rpm) is read-only.
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/params/0x004A", map[string]any{"hex": "0000"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Code != "read_only" {
		t.Errorf("code = %q, want read_only", env.Code)
	}
}

func TestHandler_PostParam_BadHex(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/params/0x0019", map[string]any{"hex": "ZZ"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// ------------------------------------------------------------------------
// Write-through cache (POST updates cache without waiting for next poll)
// ------------------------------------------------------------------------

func TestHandler_PostPower_WriteThroughVisibleInGetDevice(t *testing.T) {
	h, _, _ := newServerHandler(t)
	// Seed cache with power=off.
	v := snapshotAllParams(t)
	v[0x0001] = []byte{0x00}
	seedSnapshot(t, h, "playroom", v)

	// Confirm the cache currently shows power=off.
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var pre map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &pre)
	if cfg, _ := pre["configured"].(map[string]any); cfg["power"] != false {
		t.Fatalf("pre-write power = %v, want false; body=%s", cfg["power"], rec.Body.String())
	}

	// Issue the write.
	rec2 := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": true})
	if rec2.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", rec2.Code, rec2.Body.String())
	}

	// GET should reflect the new value immediately, without a poll tick.
	rec3 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var post map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &post)
	cfg, _ := post["configured"].(map[string]any)
	if cfg["power"] != true {
		t.Errorf("post-write power = %v, want true (write-through); body=%s", cfg["power"], rec3.Body.String())
	}
}

func TestHandler_PostSpeed_WriteThroughManual(t *testing.T) {
	h, _, _ := newServerHandler(t)
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/speed", map[string]any{"manual": 42})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	cfg, _ := resp["configured"].(map[string]any)
	if v, _ := cfg["manual_pct"].(float64); v != 42 {
		t.Errorf("manual_pct = %v, want 42 (write-through); body=%s", cfg["manual_pct"], rec2.Body.String())
	}
	if cfg["speed_mode"] != "manual" {
		t.Errorf("speed_mode = %v, want manual (write-through); body=%s", cfg["speed_mode"], rec2.Body.String())
	}
}

// ------------------------------------------------------------------------
// Error-path: auth_failed and device_unreachable
// ------------------------------------------------------------------------

func TestHandler_AuthFailed(t *testing.T) {
	h, _, addr := newServerHandler(t)
	h.Devices.Set("playroom", DeviceConfig{ID: srvDeviceID, Password: "WRONG", IP: addr})

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": true})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Code != "auth_failed" {
		t.Errorf("code = %q, want auth_failed", env.Code)
	}
}

func TestHandler_DeviceUnreachable(t *testing.T) {
	h, _, _ := newServerHandler(t)
	// 192.0.2.0/24 is the TEST-NET-1 range — guaranteed unrouteable.
	h.Devices.Set("playroom", DeviceConfig{ID: srvDeviceID, Password: srvPassword, IP: "192.0.2.1:4000"})

	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": true})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	var env errEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Code != "device_unreachable" {
		t.Errorf("code = %q, want device_unreachable", env.Code)
	}
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
			rec := doRequest(t, h, ep.method, ep.path, map[string]any{"on": true})
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404; body=%s", rec.Code, rec.Body.String())
			}
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
	// An ID without "0x" prefix should also be accepted.
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0001", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0xdeadbeef", nil)
	if rec2.Code == http.StatusOK {
		t.Errorf("expected non-OK for too-large id; got %d", rec2.Code)
	}
}

func TestHandler_FactoryError(t *testing.T) {
	h, _, _ := newServerHandler(t)
	wantErr := errors.New("factory boom")
	h.ClientFactory = func(name string) (HandlerClient, error) {
		return nil, wantErr
	}
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/power", map[string]any{"on": true})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500; body=%s", rec.Code, rec.Body.String())
	}
}

func TestErrEnvelope_Shape(t *testing.T) {
	// Catch any future regression that drops required fields.
	want := errEnvelope{Error: "boom", Code: "bad_request"}
	b, _ := json.Marshal(want)
	if !bytes.Contains(b, []byte(`"error":`)) || !bytes.Contains(b, []byte(`"code":`)) {
		t.Errorf("envelope JSON missing fields: %s", b)
	}
}
