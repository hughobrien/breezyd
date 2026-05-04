// HTTP API for the breezy daemon. Routes the endpoints listed in the
// design spec onto the State cache (for reads) and a per-call breezy
// client (for writes and the raw passthrough). All routes return JSON.
//
// Errors are emitted as {"error": "<message>", "code": "<snake_case>"}
// with HTTP statuses chosen per code:
//
//	not_found           404
//	bad_request         400
//	read_only           403
//	device_unreachable  502 (UDP timeout/checksum)
//	auth_failed         502 (ErrAuth from the device)
//	internal            500
//
// The handler uses Go 1.22's enhanced ServeMux patterns for path-segment
// captures, so each handler is a plain http.HandlerFunc with parameters
// extracted via r.PathValue(...).
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hughobrien/twinfresh/pkg/breezy"
)

// HandlerClient is the subset of breezy.Client the HTTP handler needs.
// Tests inject a stub or dial the in-process fakedevice via a real
// *breezy.Client; production code uses breezy.NewClient.
type HandlerClient interface {
	ReadParams(ctx context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error)
	WriteParams(ctx context.Context, writes []breezy.ParamWrite) error
	Close() error
}

// DeviceConfig is the subset of per-device configuration the daemon needs to
// build a HandlerClient.
type DeviceConfig struct {
	ID       string // 16-byte FDFD/02 device ID
	Password string // <= 8-byte protocol password
	IP       string // host[:port]; default port is 4000 if absent
}

// Handler implements the daemon's HTTP API.
type Handler struct {
	// State is the in-memory cache of the most recent poll for each device.
	// Cache-driven endpoints (full snapshot, firmware, efficiency, faults)
	// read here. Writes do not update the cache directly — the next poll
	// picks up the change.
	State *State
	// Devices is the per-name configuration. Routes that target a device
	// 404 if name isn't a key here.
	Devices map[string]DeviceConfig
	// Pollers, if non-nil, is consulted for NoticeWrite on successful
	// writes to fan-affecting params. NoticeFunc takes precedence; this
	// is provided for the production wiring where main.go owns the pollers.
	Pollers map[string]*Poller
	// ClientFactory builds a fresh HandlerClient for one device per
	// request — the daemon doesn't pool because UDP/4000 is request-reply
	// and a connection is just a (deviceID, password, addr) triple.
	ClientFactory func(name string) (HandlerClient, error)
	// NoticeFunc is the notification hook for fan-write settle. Tests
	// inject this directly; production code leaves it nil and Pollers is
	// used instead. Either path is exercised in handleWriteSuccess.
	NoticeFunc func(device string, id breezy.ParamID)

	// mux is built lazily by routes() and cached.
	mux *http.ServeMux
}

// errEnvelope is the JSON shape every error response shares. Error is the
// human-readable message; Code is the stable machine identifier callers
// switch on.
type errEnvelope struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// ServeHTTP dispatches the request through the lazily-built ServeMux.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.mux == nil {
		h.mux = h.routes()
	}
	h.mux.ServeHTTP(w, r)
}

// routes wires the URL pattern → handler mapping. We use Go 1.22 enhanced
// patterns so each {name} / {id} is captured directly via r.PathValue.
func (h *Handler) routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /v1/devices", h.listDevices)
	mux.HandleFunc("GET /v1/devices/{name}", h.getDevice)
	mux.HandleFunc("GET /v1/devices/{name}/firmware", h.getFirmware)
	mux.HandleFunc("GET /v1/devices/{name}/efficiency", h.getEfficiency)
	mux.HandleFunc("GET /v1/devices/{name}/faults", h.getFaults)
	mux.HandleFunc("GET /v1/devices/{name}/params/{id}", h.getParam)

	mux.HandleFunc("POST /v1/devices/{name}/power", h.postPower)
	mux.HandleFunc("POST /v1/devices/{name}/speed", h.postSpeed)
	mux.HandleFunc("POST /v1/devices/{name}/mode", h.postMode)
	mux.HandleFunc("POST /v1/devices/{name}/heater", h.postHeater)
	mux.HandleFunc("POST /v1/devices/{name}/filter/reset", h.postFilterReset)
	mux.HandleFunc("POST /v1/devices/{name}/faults/reset", h.postFaultsReset)
	mux.HandleFunc("POST /v1/devices/{name}/rtc", h.postRTC)
	mux.HandleFunc("POST /v1/devices/{name}/params/{id}", h.postParam)

	return mux
}

// ----------------------------------------------------------------------------
// Generic plumbing
// ----------------------------------------------------------------------------

// writeJSON renders v as JSON with the given status. We don't bother with
// streaming — every response in this API is small.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr emits an error envelope with the right HTTP status for code.
func writeErr(w http.ResponseWriter, code, msg string) {
	status := http.StatusInternalServerError
	switch code {
	case "not_found":
		status = http.StatusNotFound
	case "bad_request":
		status = http.StatusBadRequest
	case "read_only":
		status = http.StatusForbidden
	case "device_unreachable", "auth_failed":
		status = http.StatusBadGateway
	case "internal":
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, errEnvelope{Error: msg, Code: code})
}

// classifyClientErr maps a breezy/UDP error onto a stable error code.
// ErrAuth is the only one of these that is deterministic across retries;
// everything else (timeout, checksum, dial failure) we lump under
// device_unreachable.
func classifyClientErr(err error) string {
	if errors.Is(err, breezy.ErrAuth) {
		return "auth_failed"
	}
	if errors.Is(err, breezy.ErrChecksum) {
		return "device_unreachable"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "device_unreachable"
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return "device_unreachable"
	}
	return "internal"
}

// requireDevice looks up name in h.Devices and writes a 404 if missing.
// Returns the config and ok=true on success.
func (h *Handler) requireDevice(w http.ResponseWriter, name string) (DeviceConfig, bool) {
	d, ok := h.Devices[name]
	if !ok {
		writeErr(w, "not_found", fmt.Sprintf("device %q not configured", name))
		return DeviceConfig{}, false
	}
	return d, true
}

// dial constructs a HandlerClient for name via the configured factory.
// Errors from the factory propagate as 500/internal; transport-level
// errors come from individual Read/Write calls.
func (h *Handler) dial(name string) (HandlerClient, error) {
	if h.ClientFactory == nil {
		return nil, errors.New("server: ClientFactory not configured")
	}
	return h.ClientFactory(name)
}

// notice triggers fan-write settle suppression on the relevant poller after
// a successful write. Either NoticeFunc (for tests) or Pollers (for prod)
// can supply it; both are tolerated.
func (h *Handler) notice(name string, id breezy.ParamID) {
	if h.NoticeFunc != nil {
		h.NoticeFunc(name, id)
	}
	if p, ok := h.Pollers[name]; ok && p != nil {
		p.NoticeWrite(id)
	}
}

// readBody fully reads and unmarshals the request body into v. Returns
// false (and writes a 400) when the body is missing or malformed.
func readBody(w http.ResponseWriter, r *http.Request, v any) bool {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, "bad_request", fmt.Sprintf("read body: %v", err))
		return false
	}
	if len(body) == 0 {
		// Treat empty body as the zero value for v — caller must validate.
		return true
	}
	if err := json.Unmarshal(body, v); err != nil {
		writeErr(w, "bad_request", fmt.Sprintf("parse JSON: %v", err))
		return false
	}
	return true
}

// parseParamID accepts "0001", "0x0001", "1", "0xB7" — anything strconv
// can interpret as a uint16.
func parseParamID(s string) (breezy.ParamID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty param id")
	}
	// strconv.ParseUint with base=0 honours the 0x prefix; without one it's
	// decimal. We want hex-by-default for readability, so explicitly try
	// base=16 first when there's no prefix.
	if !strings.HasPrefix(strings.ToLower(s), "0x") {
		// try hex
		if n, err := strconv.ParseUint(s, 16, 16); err == nil {
			return breezy.ParamID(n), nil
		}
	}
	n, err := strconv.ParseUint(s, 0, 16)
	if err != nil {
		return 0, fmt.Errorf("parse param id %q: %w", s, err)
	}
	return breezy.ParamID(n), nil
}

// ----------------------------------------------------------------------------
// Handlers
// ----------------------------------------------------------------------------

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// listDevices returns a one-line-per-device summary drawn from the cache
// (no UDP). Devices missing from the cache are still listed with
// last_poll == nil.
func (h *Handler) listDevices(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Name     string `json:"name"`
		ID       string `json:"id"`
		IP       string `json:"ip"`
		LastPoll string `json:"last_poll,omitempty"`
		Power    *bool  `json:"power,omitempty"`
		Mode     string `json:"airflow_mode,omitempty"`
		Reachable bool  `json:"reachable"`
	}
	out := []entry{}
	for name, cfg := range h.Devices {
		e := entry{Name: name, ID: cfg.ID, IP: cfg.IP, Reachable: true}
		if snap, ok := h.State.Get(name); ok {
			if !snap.LastPoll.IsZero() {
				e.LastPoll = snap.LastPoll.UTC().Format(time.RFC3339)
			}
			if snap.LastErr != nil {
				e.Reachable = false
			}
			if v, ok := snap.Values[0x0001]; ok && len(v) == 1 {
				on := v[0] != 0
				e.Power = &on
			}
			if v, ok := snap.Values[0x00B7]; ok && len(v) == 1 {
				e.Mode = airflowModeName(v[0])
			}
			if e.IP == "" {
				e.IP = snap.IP
			}
		}
		out = append(out, e)
	}
	// Sort for determinism.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Name > out[j].Name; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// getDevice renders the structured snapshot defined in the spec. Each known
// param is decoded via the registry; unknown bytes go into the "raw" map
// only when explicitly relevant (we omit by default to keep the doc compact).
func (h *Handler) getDevice(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, ok := h.requireDevice(w, name)
	if !ok {
		return
	}
	snap, _ := h.State.Get(name)
	resp := h.buildSnapshot(name, cfg, snap)
	writeJSON(w, http.StatusOK, resp)
}

// SnapshotResponse mirrors the spec's example JSON. Each block is a
// map[string]any so we don't have to enumerate every possible field
// statically — empty/missing values just don't appear.
type SnapshotResponse struct {
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

// buildSnapshot decodes the cached parameter bytes into the spec's
// structured shape. Decode failures fall back to a zero/missing value
// rather than 500 — an unfamiliar device firmware shouldn't take the
// whole API down.
func (h *Handler) buildSnapshot(name string, cfg DeviceConfig, snap Snapshot) SnapshotResponse {
	resp := SnapshotResponse{
		Name:       name,
		ID:         cfg.ID,
		IP:         cfg.IP,
		Configured: map[string]any{},
		Live:       map[string]any{},
		Sensors:    map[string]any{},
		Service:    map[string]any{},
	}
	if !snap.LastPoll.IsZero() {
		resp.LastPoll = snap.LastPoll.UTC().Format(time.RFC3339)
	}
	if snap.IP != "" {
		resp.IP = snap.IP
	}

	// Configured: what the user set.
	if b, ok := uint8At(snap, 0x0001); ok {
		resp.Configured["power"] = b == 1
	}
	if b, ok := uint8At(snap, 0x0002); ok {
		switch b {
		case 0xFF:
			resp.Configured["speed_mode"] = "manual"
		case 1, 2, 3:
			resp.Configured["speed_mode"] = fmt.Sprintf("preset%d", b)
		default:
			resp.Configured["speed_mode"] = fmt.Sprintf("unknown(%d)", b)
		}
	}
	if b, ok := uint8At(snap, 0x0044); ok {
		resp.Configured["manual_pct"] = int(b)
	}
	if b, ok := uint8At(snap, 0x00B7); ok {
		resp.Configured["airflow_mode"] = airflowModeName(b)
	}
	if b, ok := uint8At(snap, 0x0068); ok {
		resp.Configured["heater_enabled"] = b == 1
	}
	if v, ok := uint16At(snap, 0x001A); ok {
		resp.Configured["co2_threshold_ppm"] = int(v)
	}
	if b, ok := uint8At(snap, 0x0019); ok {
		resp.Configured["humidity_threshold_pct"] = int(b)
	}
	if v, ok := uint16At(snap, 0x031F); ok {
		resp.Configured["voc_threshold_index"] = int(v)
	}

	// Live: the device's actual current behavior.
	if v, ok := uint16At(snap, 0x004A); ok {
		resp.Live["fan_supply_rpm"] = int(v)
	}
	if v, ok := uint16At(snap, 0x004B); ok {
		resp.Live["fan_extract_rpm"] = int(v)
	}
	if b, ok := uint8At(snap, 0x0081); ok {
		resp.Live["heater_running"] = b == 1
	}
	resp.Live["in_user_control"] = computeInUserControl(snap)
	resp.Live["sensor_alerts"] = decodeAlerts(snap)
	if b, ok := uint8At(snap, 0x0007); ok {
		resp.Live["special_mode"] = specialModeName(b)
	}
	if raw, ok := snap.Values[0x000B]; ok && len(raw) == 3 {
		// 3-byte time_of_day = [sec, min, hr]
		secs := int(raw[2])*3600 + int(raw[1])*60 + int(raw[0])
		resp.Live["special_mode_remaining_seconds"] = secs
	}

	// Sensors: live readings.
	if b, ok := uint8At(snap, 0x0025); ok {
		resp.Sensors["humidity_pct"] = int(b)
	}
	if v, ok := uint16At(snap, 0x0027); ok {
		resp.Sensors["eco2_ppm"] = int(v)
	}
	if v, ok := uint16At(snap, 0x0320); ok {
		resp.Sensors["voc_index"] = int(v)
	}
	for _, t := range []struct {
		id   breezy.ParamID
		name string
	}{
		{0x001F, "temp_outdoor_c"},
		{0x0020, "temp_supply_c"},
		{0x0021, "temp_exhaust_inlet_c"},
		{0x0022, "temp_exhaust_outlet_c"},
	} {
		if v, ok := int16At(snap, t.id); ok {
			// -32768 / 32767 are sentinels; surface them as null.
			if v == -32768 || v == 32767 {
				continue
			}
			resp.Sensors[t.name] = float64(v) / 10.0
		}
	}
	if b, ok := uint8At(snap, 0x0129); ok {
		resp.Sensors["recovery_efficiency_pct"] = int(b)
	}

	// Service: filter, motor, RTC battery, faults.
	if b, ok := uint8At(snap, 0x0088); ok {
		if b == 0 {
			resp.Service["filter_status"] = "clean"
		} else {
			resp.Service["filter_status"] = "soiled"
		}
	}
	if raw, ok := snap.Values[0x0064]; ok && len(raw) == 4 {
		// remaining_time = [min, hr, day_lo, day_hi]
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		resp.Service["filter_remaining_seconds"] = secs
	}
	if raw, ok := snap.Values[0x007E]; ok && len(raw) == 4 {
		days := int(raw[2]) | int(raw[3])<<8
		secs := days*86400 + int(raw[1])*3600 + int(raw[0])*60
		resp.Service["motor_lifetime_seconds"] = secs
	}
	if v, ok := uint16At(snap, 0x0024); ok {
		resp.Service["rtc_battery_volts"] = float64(v) / 1000.0
	}
	if b, ok := uint8At(snap, 0x0083); ok {
		resp.Service["fault_level"] = faultLevelName(b)
	}
	if b, ok := uint8At(snap, 0x030B); ok {
		resp.Service["frost_protection_active"] = b == 1
	}

	// Firmware.
	if raw, ok := snap.Values[0x0086]; ok && len(raw) == 6 {
		fw := map[string]any{
			"version": fmt.Sprintf("%d.%02d", raw[0], raw[1]),
		}
		year := int(uint16(raw[4]) | uint16(raw[5])<<8)
		fw["build_date"] = fmt.Sprintf("%04d-%02d-%02d", year, raw[3], raw[2])
		resp.Firmware = fw
	}
	return resp
}

// computeInUserControl returns true when the device is doing what the
// user asked: no special-mode timer is active (0x07 == 0), the heater is
// off (0x68 == 0), AND no sensor-alert byte is set (0x84 all-zero).
//
// Any one of those being non-zero means a firmware-driven override is in
// effect — the fan/heater state may differ from the configured values,
// and the CLI should warn the user.
func computeInUserControl(snap Snapshot) bool {
	if b, ok := uint8At(snap, 0x0007); ok && b != 0 {
		return false
	}
	if b, ok := uint8At(snap, 0x0068); ok && b != 0 {
		return false
	}
	if raw, ok := snap.Values[0x0084]; ok {
		for _, b := range raw {
			if b != 0 {
				return false
			}
		}
	}
	return true
}

// decodeAlerts surfaces the per-sensor over-threshold flags from 0x84 as
// a small map. Missing 0x84 yields all-false (cache miss is conservatively
// "no known alert").
func decodeAlerts(snap Snapshot) map[string]any {
	out := map[string]any{"humidity": false, "co2": false, "voc": false}
	raw, ok := snap.Values[0x0084]
	if !ok || len(raw) < 5 {
		return out
	}
	out["humidity"] = raw[0] != 0
	out["co2"] = raw[1] != 0
	out["voc"] = raw[4] != 0
	return out
}

// uint8At returns the single byte stored at id, or (0,false) if the value
// is missing or wrong-sized.
func uint8At(snap Snapshot, id breezy.ParamID) (uint8, bool) {
	raw, ok := snap.Values[id]
	if !ok || len(raw) != 1 {
		return 0, false
	}
	return raw[0], true
}

// uint16At returns the LE 2-byte value at id.
func uint16At(snap Snapshot, id breezy.ParamID) (uint16, bool) {
	raw, ok := snap.Values[id]
	if !ok || len(raw) != 2 {
		return 0, false
	}
	return binary.LittleEndian.Uint16(raw), true
}

// int16At returns the LE 2-byte signed value at id.
func int16At(snap Snapshot, id breezy.ParamID) (int16, bool) {
	v, ok := uint16At(snap, id)
	if !ok {
		return 0, false
	}
	return int16(v), true
}

// airflowModeName decodes 0xB7. Anything outside 0..3 falls through to a
// debug-only string so we don't lose data on a future firmware addition.
func airflowModeName(b uint8) string {
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

// specialModeName decodes 0x07 (0=off, 1=night, 2=turbo).
func specialModeName(b uint8) string {
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

// faultLevelName decodes 0x83 (0=none, 1=alarm, 2=warning).
func faultLevelName(b uint8) string {
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

// ----------------------------------------------------------------------------
// /firmware, /efficiency, /faults
// ----------------------------------------------------------------------------

func (h *Handler) getFirmware(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	snap, _ := h.State.Get(name)
	raw, ok := snap.Values[0x0086]
	if !ok || len(raw) != 6 {
		writeErr(w, "not_found", "firmware metadata not in cache yet")
		return
	}
	year := int(uint16(raw[4]) | uint16(raw[5])<<8)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    fmt.Sprintf("%d.%02d", raw[0], raw[1]),
		"build_date": fmt.Sprintf("%04d-%02d-%02d", year, raw[3], raw[2]),
	})
}

func (h *Handler) getEfficiency(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	snap, _ := h.State.Get(name)
	b, ok := uint8At(snap, 0x0129)
	if !ok {
		writeErr(w, "not_found", "efficiency reading not in cache yet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_efficiency_pct": int(b)})
}

// getFaults decodes 0x7F: a variable-length list of (code, kind) byte pairs.
// kind: 0=alarm, 1=warning. An empty list (no faults) returns an empty array.
func (h *Handler) getFaults(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	snap, _ := h.State.Get(name)
	raw, ok := snap.Values[0x007F]
	out := []map[string]any{}
	if ok {
		// Pairs of (code, kind). An odd trailing byte is ignored.
		for i := 0; i+1 < len(raw); i += 2 {
			kind := "alarm"
			if raw[i+1] == 1 {
				kind = "warning"
			} else if raw[i+1] != 0 {
				kind = fmt.Sprintf("unknown(%d)", raw[i+1])
			}
			out = append(out, map[string]any{
				"code": int(raw[i]),
				"kind": kind,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"faults": out})
}

// ----------------------------------------------------------------------------
// /params/{id}: raw read + write
// ----------------------------------------------------------------------------

// getParam issues a fresh UDP read, bypassing the cache. The result is the
// hex of the LE bytes the device returned, plus the registry name when known.
func (h *Handler) getParam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	id, err := parseParamID(r.PathValue("id"))
	if err != nil {
		writeErr(w, "bad_request", err.Error())
		return
	}

	client, err := h.dial(name)
	if err != nil {
		writeErr(w, "internal", err.Error())
		return
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	out, err := client.ReadParams(ctx, []breezy.ParamID{id})
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	val, ok := out[id]
	if !ok {
		writeErr(w, "not_found", fmt.Sprintf("device replied 'unsupported' for param 0x%04X", uint16(id)))
		return
	}
	resp := map[string]any{
		"id":  fmt.Sprintf("0x%04X", uint16(id)),
		"hex": hex.EncodeToString(val),
	}
	if p, ok := breezy.LookupByID(id); ok {
		resp["name"] = p.Name
		resp["type"] = p.Type.String()
		// Best-effort decode for human consumption; raw hex is always
		// available so we never fail the request on a decode error.
		if v, decErr := p.Decode(val); decErr == nil {
			resp["value"] = v.String()
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// postParam writes raw LE bytes (hex-encoded) to a parameter. The registry
// is consulted to refuse writes to read-only params with a 403/read_only.
// Unknown params are *allowed* — the caller is signalling they know what
// they're doing.
func (h *Handler) postParam(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	id, err := parseParamID(r.PathValue("id"))
	if err != nil {
		writeErr(w, "bad_request", err.Error())
		return
	}

	if p, ok := breezy.LookupByID(id); ok && !p.Caps.CanWrite() {
		writeErr(w, "read_only", fmt.Sprintf("param %s (0x%04X) is read-only", p.Name, uint16(id)))
		return
	}

	var body struct {
		Hex string `json:"hex"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.Hex == "" {
		writeErr(w, "bad_request", "missing 'hex' field")
		return
	}
	val, err := hex.DecodeString(body.Hex)
	if err != nil {
		writeErr(w, "bad_request", fmt.Sprintf("decode hex: %v", err))
		return
	}

	if err := h.doWrite(r.Context(), name, []breezy.ParamWrite{{ID: id, Value: val}}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.notice(name, id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// doWrite is the common path for every write-issuing handler. It opens a
// fresh client, issues the WriteParams, closes the client.
func (h *Handler) doWrite(ctx context.Context, name string, writes []breezy.ParamWrite) error {
	client, err := h.dial(name)
	if err != nil {
		// Surface factory errors as a generic error; classifyClientErr will
		// route to "internal" since they aren't net.Error.
		return err
	}
	defer client.Close()

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return client.WriteParams(cctx, writes)
}

// ----------------------------------------------------------------------------
// POST /power, /speed, /mode, /heater, /filter/reset, /faults/reset
// ----------------------------------------------------------------------------

func (h *Handler) postPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.On == nil {
		writeErr(w, "bad_request", "missing 'on' field (true/false)")
		return
	}
	val := byte(0)
	if *body.On {
		val = 1
	}
	if err := h.doWrite(r.Context(), name, []breezy.ParamWrite{{ID: 0x0001, Value: []byte{val}}}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.notice(name, 0x0001)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// postSpeed accepts {"preset":1..3} OR {"manual": 10..100}. Manual writes
// also flip 0x02 to 0xFF so the firmware honours the percentage.
//
// The handler returns immediately; fan-RPM/sensor reads are suppressed for
// ~12 s by the poller's NoticeWrite mechanism, not by us blocking here.
func (h *Handler) postSpeed(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		Preset *int `json:"preset"`
		Manual *int `json:"manual"`
	}
	if !readBody(w, r, &body) {
		return
	}

	switch {
	case body.Preset != nil:
		if *body.Preset < 1 || *body.Preset > 3 {
			writeErr(w, "bad_request", "preset must be 1, 2, or 3")
			return
		}
		val := byte(*body.Preset)
		if err := h.doWrite(r.Context(), name, []breezy.ParamWrite{{ID: 0x0002, Value: []byte{val}}}); err != nil {
			writeErr(w, classifyClientErr(err), err.Error())
			return
		}
		h.notice(name, 0x0002)

	case body.Manual != nil:
		if *body.Manual < 10 || *body.Manual > 100 {
			writeErr(w, "bad_request", "manual percent must be ≥ 10 (firmware floor) and ≤ 100")
			return
		}
		// Single packet: 0x44 = pct, 0x02 = 0xFF. Order matters per the
		// vendor manual — set the percentage first so the firmware doesn't
		// briefly use a stale value when interpreting the manual flag.
		writes := []breezy.ParamWrite{
			{ID: 0x0044, Value: []byte{byte(*body.Manual)}},
			{ID: 0x0002, Value: []byte{0xFF}},
		}
		if err := h.doWrite(r.Context(), name, writes); err != nil {
			writeErr(w, classifyClientErr(err), err.Error())
			return
		}
		h.notice(name, 0x0044)
		h.notice(name, 0x0002)

	default:
		writeErr(w, "bad_request", "set either 'preset' (1-3) or 'manual' (10-100)")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) postMode(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		Mode string `json:"mode"`
	}
	if !readBody(w, r, &body) {
		return
	}
	var val byte
	switch strings.ToLower(body.Mode) {
	case "ventilation":
		val = 0
	case "regeneration":
		val = 1
	case "supply":
		val = 2
	case "extract":
		val = 3
	default:
		writeErr(w, "bad_request", "mode must be one of: ventilation, regeneration, supply, extract")
		return
	}
	if err := h.doWrite(r.Context(), name, []breezy.ParamWrite{{ID: 0x00B7, Value: []byte{val}}}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.notice(name, 0x00B7)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) postHeater(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.On == nil {
		writeErr(w, "bad_request", "missing 'on' field (true/false)")
		return
	}
	val := byte(0)
	if *body.On {
		val = 1
	}
	if err := h.doWrite(r.Context(), name, []breezy.ParamWrite{{ID: 0x0068, Value: []byte{val}}}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.notice(name, 0x0068)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) postFilterReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	if err := h.doWrite(r.Context(), name, []breezy.ParamWrite{{ID: 0x0065, Value: []byte{1}}}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.notice(name, 0x0065)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) postFaultsReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	if err := h.doWrite(r.Context(), name, []breezy.ParamWrite{{ID: 0x0080, Value: []byte{1}}}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.notice(name, 0x0080)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// postRTC sets both 0x6F (3-byte sec/min/hr) and 0x70 (4-byte day/dow/month/year)
// in a single write packet. Day-of-week is computed from the parsed time
// using the convention 1=Monday..7=Sunday (matches the vendor manual).
func (h *Handler) postRTC(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		Time string `json:"time"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.Time == "" {
		writeErr(w, "bad_request", "missing 'time' field (RFC3339)")
		return
	}
	t, err := time.Parse(time.RFC3339, body.Time)
	if err != nil {
		writeErr(w, "bad_request", fmt.Sprintf("parse time %q: %v", body.Time, err))
		return
	}

	// 0x6F: [sec, min, hr]
	timeBytes := []byte{byte(t.Second()), byte(t.Minute()), byte(t.Hour())}
	// 0x70: [day, day_of_week, month, year-2000]
	// time.Weekday: Sunday=0..Saturday=6; we want Mon=1..Sun=7.
	dow := int(t.Weekday())
	if dow == 0 {
		dow = 7
	}
	year := t.Year() - 2000
	if year < 0 || year > 255 {
		writeErr(w, "bad_request", fmt.Sprintf("year %d out of range for RTC", t.Year()))
		return
	}
	dateBytes := []byte{byte(t.Day()), byte(dow), byte(t.Month()), byte(year)}

	writes := []breezy.ParamWrite{
		{ID: 0x006F, Value: timeBytes},
		{ID: 0x0070, Value: dateBytes},
	}
	if err := h.doWrite(r.Context(), name, writes); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	// Neither RTC param is in fanWriteIDs, so notice() is a no-op for the
	// poller, but we still record the writes for any test/debug hook.
	h.notice(name, 0x006F)
	h.notice(name, 0x0070)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
