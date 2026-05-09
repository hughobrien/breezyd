// SPDX-License-Identifier: GPL-3.0-or-later

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
//
// This file holds only the Handler/registry plumbing, routing setup, and
// generic envelope helpers. The actual handlers live in:
//   - handlers_device.go  — device-targeted reads/writes (power, speed, params, ...)
//   - handlers_service.go — /firmware, /efficiency, /faults, resets
//   - metrics.go          — Prometheus exporter
//
// Status decode helpers (BuildStatus, Uint8At, etc.) live in pkg/breezy/status.go.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/homekit"
)

// HandlerClient is the subset of breezy.Client the HTTP handler needs.
// Tests inject a stub or dial the in-process fakedevice via a real
// *breezy.Client; production code uses breezy.NewClient.
type HandlerClient interface {
	ReadParams(ctx context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error)
	WriteParams(ctx context.Context, writes []breezy.ParamWrite) error
	// IsLocal reports whether the client is in-process. Forwarded from
	// breezy.DeviceClient so recordingClient can satisfy that interface.
	IsLocal() bool
	Close() error
}

// DeviceConfig is the subset of per-device configuration the daemon needs to
// build a HandlerClient.
type DeviceConfig struct {
	ID       string // 16-byte FDFD/02 device ID
	Password string // <= 8-byte protocol password
	IP       string // host[:port]; default port is 4000 if absent
}

// DeviceRegistry holds the current per-device configuration with safe
// concurrent access. Periodic discovery (when enabled) mutates entries
// while HTTP request goroutines read them; without synchronisation that's
// a data race. The registry's Get/Names methods take an RLock; UpdateIP
// takes the write lock. Tests that built a Handler with a static config
// can call NewDeviceRegistry(map) once and never mutate again.
type DeviceRegistry struct {
	mu      sync.RWMutex
	devices map[string]DeviceConfig
}

// NewDeviceRegistry returns a registry seeded with the given devices.
// The supplied map is copied; the caller is free to mutate it afterward.
func NewDeviceRegistry(devices map[string]DeviceConfig) *DeviceRegistry {
	r := &DeviceRegistry{devices: make(map[string]DeviceConfig, len(devices))}
	for k, v := range devices {
		r.devices[k] = v
	}
	return r
}

// Get returns the configuration for name, or zero+false if absent.
func (r *DeviceRegistry) Get(name string) (DeviceConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[name]
	return d, ok
}

// Names returns a copy of the device names registered, in unsorted order.
// The returned slice is independent of internal state.
func (r *DeviceRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.devices))
	for k := range r.devices {
		out = append(out, k)
	}
	return out
}

// Snapshot returns a deep-ish copy of the entire registry. Useful for
// callers that want to iterate without holding the read lock.
func (r *DeviceRegistry) Snapshot() map[string]DeviceConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]DeviceConfig, len(r.devices))
	for k, v := range r.devices {
		out[k] = v
	}
	return out
}

// Set replaces the entry for name. Used by tests to swap a device's
// password/IP after registry construction.
func (r *DeviceRegistry) Set(name string, d DeviceConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.devices[name] = d
}

// UpdateIP atomically updates the IP for name, leaving ID and Password
// untouched. Returns the previous IP and whether the entry existed.
// Periodic discovery uses this when a device's address changes.
func (r *DeviceRegistry) UpdateIP(name, ip string) (prev string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.devices[name]
	if !ok {
		return "", false
	}
	prev = d.IP
	d.IP = ip
	r.devices[name] = d
	return prev, true
}

// Handler implements the daemon's HTTP API.
type Handler struct {
	// State is the in-memory cache of the most recent poll for each device.
	// Cache-driven endpoints (full snapshot, firmware, efficiency, faults)
	// read here. Writes update the cache via WriteThrough on success.
	State *State
	// Devices is the per-name configuration. Routes that target a device
	// 404 if name isn't a key here. Use a *DeviceRegistry so periodic
	// discovery can update IPs while HTTP requests are in flight.
	Devices *DeviceRegistry
	// Pollers, if non-nil, is consulted for NoticeWrite on successful
	// writes to fan-affecting params. NoticeFunc takes precedence; this
	// is provided for the production wiring where main.go owns the pollers.
	Pollers map[string]*Poller
	// Schedulers are per-device subsystems that fire scheduled
	// Power/Mode/Speed writes. Populated by startPollers.
	Schedulers map[string]*Scheduler
	// ClientFactory builds a fresh HandlerClient for one device per
	// request — the daemon doesn't pool because UDP/4000 is request-reply
	// and a connection is just a (deviceID, password, addr) triple.
	ClientFactory func(name string) (HandlerClient, error)
	// NoticeFunc is the notification hook for fan-write settle. Tests
	// inject this directly; production code leaves it nil and Pollers is
	// used instead. Either path is exercised in handleWriteSuccess.
	NoticeFunc func(device string, id breezy.ParamID)

	// homekitAccessories holds the per-device HomeKit accessory built by
	// StartHomekit. Task 5's SyncHomekit reads this map to push poll
	// results into characteristic values. Nil until StartHomekit runs.
	homekitAccessories map[string]*homekit.Accessory

	// PushHub fans out per-device snapshot updates to /ui/sse subscribers.
	// Nil-tolerant: the JSON API and the in-progress migration paths run
	// fine without it. Populated in main.go once the render closure can
	// be wired to the templ machinery.
	PushHub PushNotifier

	// cachedMux is built lazily by mux() and cached. muxOnce guards the
	// initialisation against a data race on the first concurrent burst
	// of requests — net/http serves each request from its own goroutine,
	// so without sync.Once two requests arriving before the cache is
	// populated would each call mux() and race on the assignment.
	cachedMux *http.ServeMux
	muxOnce   sync.Once
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
	h.muxOnce.Do(func() { h.cachedMux = h.mux() })
	h.cachedMux.ServeHTTP(w, r)
}

// mux wires the URL pattern → handler mapping. We use Go 1.22 enhanced
// patterns so each {name} / {id} is captured directly via r.PathValue.
func (h *Handler) mux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", h.getIndex)
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /v1/devices", h.listDevices)
	mux.HandleFunc("GET /v1/devices/{name}", h.getDevice)
	mux.HandleFunc("GET /v1/devices/{name}/firmware", h.getFirmware)
	mux.HandleFunc("GET /v1/devices/{name}/efficiency", h.getEfficiency)
	mux.HandleFunc("GET /v1/devices/{name}/faults", h.getFaults)
	mux.HandleFunc("GET /v1/devices/{name}/params/{id}", h.getParam)

	mux.HandleFunc("POST /v1/devices/{name}/power", h.postPower)
	mux.HandleFunc("POST /v1/devices/{name}/speed", h.postSpeed)
	mux.HandleFunc("POST /v1/devices/{name}/preset", h.postPreset)
	mux.HandleFunc("POST /v1/devices/{name}/mode", h.postMode)
	mux.HandleFunc("POST /v1/devices/{name}/heater", h.postHeater)
	mux.HandleFunc("POST /v1/devices/{name}/timer", h.postTimer)
	mux.HandleFunc("POST /v1/devices/{name}/threshold", h.postThreshold)
	mux.HandleFunc("POST /v1/devices/{name}/filter/reset", h.postFilterReset)
	mux.HandleFunc("POST /v1/devices/{name}/faults/reset", h.postFaultsReset)
	mux.HandleFunc("POST /v1/devices/{name}/rtc", h.postRTC)
	mux.HandleFunc("POST /v1/devices/{name}/params/{id}", h.postParam)

	mux.HandleFunc("GET /v1/devices/{name}/schedule", h.getSchedule)
	mux.HandleFunc("PUT /v1/devices/{name}/schedule", h.putSchedule)

	mux.HandleFunc("GET /ui/sse", h.getUISSE)
	mux.HandleFunc("POST /ui/devices/{name}/power", h.postUIPower)
	mux.HandleFunc("POST /ui/devices/{name}/mode", h.postUIMode)
	mux.HandleFunc("POST /ui/devices/{name}/preset", h.postUIPreset)
	mux.HandleFunc("POST /ui/devices/{name}/speed", h.postUISpeed)
	mux.HandleFunc("POST /ui/devices/{name}/heater", h.postUIHeater)
	mux.HandleFunc("POST /ui/devices/{name}/timer", h.postUITimer)
	mux.HandleFunc("POST /ui/devices/{name}/reset-filter", h.postUIResetFilter)
	mux.HandleFunc("POST /ui/devices/{name}/reset-faults", h.postUIResetFaults)
	mux.HandleFunc("GET /ui/devices/{name}/threshold/{kind}", h.getUIThresholdRead)
	mux.HandleFunc("GET /ui/devices/{name}/threshold/{kind}/edit", h.getUIThresholdEdit)
	mux.HandleFunc("PUT /ui/devices/{name}/threshold", h.putUIThreshold)
	mux.HandleFunc("GET /ui/devices/{name}/schedule", h.getUIScheduleRead)
	mux.HandleFunc("GET /ui/devices/{name}/schedule/edit", h.getUIScheduleEdit)
	mux.HandleFunc("GET /ui/devices/{name}/schedule/new-row", h.getUIScheduleNewRow)
	mux.HandleFunc("PUT /ui/devices/{name}/schedule", h.putUISchedule)
	mux.HandleFunc("GET /ui/style-"+styleHash+".css", h.getStyle)
	mux.HandleFunc("GET /ui/vendor/{file}", h.getVendor)
	mux.HandleFunc("GET /favicon.svg", h.getFavicon)
	mux.HandleFunc("GET /favicon.ico", h.getFavicon)

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
	if errors.Is(err, breezy.ErrReadOnly) {
		return "read_only"
	}
	if errors.Is(err, breezy.ErrInvalidArg) {
		return "bad_request"
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
	if h.Devices == nil {
		writeErr(w, "not_found", fmt.Sprintf("device %q not configured", name))
		return DeviceConfig{}, false
	}
	d, ok := h.Devices.Get(name)
	if !ok {
		writeErr(w, "not_found", fmt.Sprintf("device %q not configured", name))
		return DeviceConfig{}, false
	}
	return d, true
}

// dial constructs a HandlerClient for name via the configured factory.
// Errors from the factory propagate as 500/internal; transport-level
// errors come from individual Read/Write calls.
//
// dial also acquires the per-device UDP serialisation mutex (held by the
// poller during ticks). The returned unlock MUST be deferred by the caller,
// AND the caller MUST also defer client.Close() — list the defers as
// `defer unlock(); defer client.Close()` so LIFO ordering closes the socket
// BEFORE releasing the mutex (otherwise another acquirer could begin UDP
// traffic while our socket is still draining).
func (h *Handler) dial(name string) (HandlerClient, func(), error) {
	if h.ClientFactory == nil {
		return nil, nil, errors.New("server: ClientFactory not configured")
	}
	unlock := h.lockDevice(name)
	c, err := h.ClientFactory(name)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return c, unlock, nil
}

// lockDevice acquires the per-device UDP serialisation mutex when a poller
// is registered for name. Tests that don't wire up Pollers get a no-op
// unlock (the test's mock client has no real UDP traffic, so serialisation
// is moot).
func (h *Handler) lockDevice(name string) func() {
	if p, ok := h.Pollers[name]; ok && p != nil {
		return p.LockUDP()
	}
	return func() {}
}

// dialRecording returns a recordingClient that wraps h.dial(name)'s
// HandlerClient and fires h.recordWrite(name, writes) on every
// successful write. Handlers that issue writes via pkg/breezy/ops
// should use this instead of h.dial — the wrapper subsumes the
// previous "call h.recordWrite at the end" pattern.
//
// Returns (wrapper, raw, unlock, err). Same defer convention as dial:
// the caller writes `defer unlock(); defer raw.Close()` so the socket is
// closed before the mutex releases.
func (h *Handler) dialRecording(name string) (*recordingClient, HandlerClient, func(), error) {
	raw, unlock, err := h.dial(name)
	if err != nil {
		return nil, nil, nil, err
	}
	return newRecordingClient(raw, func(ws []breezy.ParamWrite) {
		h.recordWrite(name, ws)
	}), raw, unlock, nil
}

// handlerOpTimeout caps a single device dial + op (one UDP
// request/response round trip including any internal retries). This is
// not the 12s fan-settle window — that's a separate, post-write
// behavior that suppresses polled reads, not a deadline on the write
// itself (see poller.go::fanSettleDuration).
const handlerOpTimeout = 5 * time.Second

// doDeviceOp acquires the per-device UDP lock, opens a recording
// client, runs op with a 5s timeout derived from r.Context(), and
// tears everything down (Close before unlock; LIFO defer order)
// before returning. Caller has already validated the device exists
// and any input fields, and is responsible for translating the
// returned error and emitting any success body.
//
// Returns nil on success, the dial error if the client could not be
// opened, or the op's error verbatim — including ctx.DeadlineExceeded
// when the 5s budget elapsed.
func (h *Handler) doDeviceOp(
	r *http.Request,
	name string,
	op func(ctx context.Context, rc *recordingClient) error,
) error {
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		return err
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), handlerOpTimeout)
	defer cancel()
	return op(ctx, rc)
}

// doDeviceRead is the read-only sibling used by getParam. Same shape
// as doDeviceOp but goes through h.dial (no recording wrapper) since
// reads have no writes to record.
func (h *Handler) doDeviceRead(
	r *http.Request,
	name string,
	op func(ctx context.Context, c HandlerClient) error,
) error {
	c, unlock, err := h.dial(name)
	if err != nil {
		return err
	}
	defer unlock()
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), handlerOpTimeout)
	defer cancel()
	return op(ctx, c)
}

// doDeviceOpBackground is the no-request sibling of doDeviceOp, used by
// callers that have no *http.Request to derive a parent context from
// (HomeKit characteristic write callbacks, scheduler-fired writes,
// etc.). The parent context is context.Background(); the same
// handlerOpTimeout caps the op so an unreachable device cannot hang
// the caller's goroutine indefinitely.
func (h *Handler) doDeviceOpBackground(
	name string,
	op func(ctx context.Context, rc *recordingClient) error,
) error {
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		return err
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), handlerOpTimeout)
	defer cancel()
	return op(ctx, rc)
}

// scheduleDial returns a Dial closure compatible with Scheduler.Dial.
// Mirrors dialRecording but does NOT acquire the per-device UDP mutex —
// the Scheduler holds it via Scheduler.LockUDP set to poller.LockUDP, so
// taking it again here would deadlock. Cache write-through and
// NoticeWrite happen via the recordingClient as usual.
func (h *Handler) scheduleDial(name string) func(ctx context.Context) (breezy.DeviceClient, HandlerClient, error) {
	return func(ctx context.Context) (breezy.DeviceClient, HandlerClient, error) {
		if h.ClientFactory == nil {
			return nil, nil, errors.New("server: ClientFactory not configured")
		}
		raw, err := h.ClientFactory(name)
		if err != nil {
			return nil, nil, err
		}
		rc := newRecordingClient(raw, func(ws []breezy.ParamWrite) {
			h.recordWrite(name, ws)
		})
		return rc, raw, nil
	}
}

// notice triggers fan-write settle suppression on the relevant poller after
// a successful write.
//
// In production the Handler has Pollers wired and NoticeFunc nil; tests
// inject NoticeFunc on a Handler whose Pollers is empty. To keep the two
// paths from double-firing on a Handler that happens to have both set
// (uncommon, but possible in a hybrid test), NoticeFunc takes precedence:
// when present, it suppresses the Pollers path so a single notice doesn't
// fire twice.
func (h *Handler) notice(name string, id breezy.ParamID) {
	if h.NoticeFunc != nil {
		h.NoticeFunc(name, id)
		return
	}
	if p, ok := h.Pollers[name]; ok && p != nil {
		p.NoticeWrite(id)
	}
}

// recordWrite is the standard "post-successful-write" hook for handlers:
// it updates the cache (write-through, per the design spec) and notifies
// the poller to suppress fan-RPM reads during settle. Pass every write
// that just succeeded on the wire — the cache mirrors the device.
func (h *Handler) recordWrite(name string, writes []breezy.ParamWrite) {
	if h.State != nil {
		h.State.WriteThrough(name, writes)
	}
	for _, w := range writes {
		h.notice(name, w.ID)
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

// parseParamID interprets a {id} URL segment as a Breezy ParamID.
//
// IMPORTANT — bare numeric strings are interpreted as HEX, not decimal.
// This matches how the protocol and the param-map document address
// parameters (everything in the spec is "0x0044", "0x00B7", etc.) and
// how operators tend to think when copy-pasting from the manual.
//
// Concretely:
//
//	"0x0044"  ->  0x0044  (66)
//	"0044"    ->  0x0044  (66) — bare numeric is hex
//	"44"      ->  0x0044  (66) — STILL HEX; "44" is NOT decimal 44
//	"B7"      ->  0x00B7  (183)
//	"10"      ->  0x0010  (16) — surprising if you expected decimal 10!
//
// To pass a true decimal you must use "0x0A" or the hex-equivalent. The
// CLI and operators almost always use named identifiers (e.g. "power",
// "speed_manual_pct") via /v1/devices/{name}/params/{id} resolved in the
// registry, so the ambiguity rarely bites in practice — but it has to be
// documented so a passing reader doesn't assume "10" means ten.
func parseParamID(s string) (breezy.ParamID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty param id")
	}
	// strconv.ParseUint with base=0 honours the 0x prefix; without one it's
	// decimal. We want hex-by-default for readability (see docstring), so
	// explicitly try base=16 first when there's no prefix.
	if !strings.HasPrefix(strings.ToLower(s), "0x") {
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
// Handlers (small ones that don't justify their own file)
// ----------------------------------------------------------------------------

func (h *Handler) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// listDevices returns a one-line-per-device summary drawn from the cache
// (no UDP). Devices missing from the cache are still listed with
// last_poll == nil.
func (h *Handler) listDevices(w http.ResponseWriter, r *http.Request) {
	type entry struct {
		Name      string `json:"name"`
		ID        string `json:"id"`
		IP        string `json:"ip"`
		LastPoll  string `json:"last_poll,omitempty"`
		Power     *bool  `json:"power,omitempty"`
		Mode      string `json:"airflow_mode,omitempty"`
		Reachable bool   `json:"reachable"`
	}
	out := []entry{}
	var devices map[string]DeviceConfig
	if h.Devices != nil {
		devices = h.Devices.Snapshot()
	}
	for name, cfg := range devices {
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
				e.Mode = breezy.AirflowModeName(v[0])
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
