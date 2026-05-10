// SPDX-License-Identifier: GPL-3.0-or-later

// Device-targeted HTTP handlers: GET /v1/devices/{name} and the POST
// endpoints that issue UDP writes (power, speed, mode, heater, RTC,
// raw param read/write). Each handler is a plain http.HandlerFunc that
// reads the {name}/{id} segments via r.PathValue.
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// postV1WriteJSON is the spine shared by every /v1/devices/{name}/...
// write handler. Mirrors postUIWriteJSON for /ui. Resolves the device,
// decodes JSON into a fresh T, runs an optional shape-validator, executes
// the op via doDeviceOp, surfaces errors through writeErr, and on success
// writes {"ok": true}. Pass nil shapeOK if no pre-op shape check is needed.
// When the shape check returns false it MUST have already written its own
// error response via writeErr (or equivalent).
func postV1WriteJSON[T any](
	h *Handler,
	w http.ResponseWriter,
	r *http.Request,
	shapeOK func(req *T, w http.ResponseWriter) bool,
	op func(ctx context.Context, rc *recordingClient, req *T) error,
) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var req T
	if !readBody(w, r, &req) {
		return
	}
	if shapeOK != nil && !shapeOK(&req, w) {
		return
	}
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return op(ctx, rc, &req)
	}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	ip := cfg.IP
	if snap.IP != "" {
		ip = snap.IP
	}
	var lastPoll *time.Time
	if !snap.LastPoll.IsZero() {
		lastPoll = &snap.LastPoll
	}
	var ev *breezy.EnergyValues
	if p, ok := h.Pollers[name]; ok && p != nil && p.Energy != nil {
		v := p.Energy.Snapshot()
		ev = &v
	}
	resp := breezy.BuildStatus(snap.Values, name, cfg.ID, ip, lastPoll)
	if ev != nil {
		resp.Service["energy"] = *ev
	}
	if sch, ok := h.Schedulers[name]; ok && sch != nil {
		resp.Service["schedule"] = scheduleResponseFrom(sch.Snapshot())
	}
	writeJSON(w, http.StatusOK, resp)
}

// ----------------------------------------------------------------------------
// /params/{id}: raw read + write
// ----------------------------------------------------------------------------

// getParam issues a fresh UDP read, bypassing the cache. The result is the
// hex of the LE bytes the device returned, plus the registry name when known.
//
// The {id} segment is parsed as hex (see parseParamID): "0x44", "44", and
// "0044" all mean param 0x0044. Operators almost always use the named
// route via the registry; bare-numeric is for interactive debugging.
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

	var out map[breezy.ParamID][]byte
	if err := h.doDeviceRead(r, name, func(ctx context.Context, c HandlerClient) error {
		var rerr error
		out, rerr = c.ReadParams(ctx, []breezy.ParamID{id})
		return rerr
	}); err != nil {
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

// postParam writes raw LE bytes (hex-encoded) to a parameter. Read-only
// enforcement is performed by breezy.WriteParams at the package layer
// (single source of truth — see pkg/breezy/client.go::WriteParams). When
// the caller targets a read-only ID, WriteParams returns ErrReadOnly and
// classifyClientErr maps that onto the HTTP "read_only" code (-> 403).
//
// Unknown params (not in the registry) are *allowed* — the caller is
// signalling they know what they're doing.
//
// postParam writes raw bytes directly via rc.WriteParams; it does NOT go
// through pkg/breezy/ops (ops are for known-shape writes only).
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
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return rc.WriteParams(ctx, []breezy.ParamWrite{{ID: id, Value: val}})
	}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ----------------------------------------------------------------------------
// POST /power, /speed, /mode, /heater, /rtc
// ----------------------------------------------------------------------------

func (h *Handler) postPower(w http.ResponseWriter, r *http.Request) {
	type req struct {
		On *bool `json:"on"`
	}
	postV1WriteJSON(h, w, r,
		func(q *req, w http.ResponseWriter) bool {
			if q.On == nil {
				writeErr(w, "bad_request", "missing 'on' field (true/false)")
				return false
			}
			return true
		},
		func(ctx context.Context, rc *recordingClient, q *req) error {
			return breezy.Power(ctx, rc, *q.On)
		},
	)
}

// postSpeed accepts {"preset":1..3} OR {"manual": 10..100}. Manual writes
// also flip 0x02 to 0xFF so the firmware honours the percentage.
//
// The handler returns immediately; fan-RPM/sensor reads are suppressed for
// ~12 s by the poller's NoticeWrite mechanism, not by us blocking here.
func (h *Handler) postSpeed(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Preset *int `json:"preset"`
		Manual *int `json:"manual"`
	}
	postV1WriteJSON(h, w, r,
		func(q *req, w http.ResponseWriter) bool {
			if (q.Preset == nil) == (q.Manual == nil) {
				writeErr(w, "bad_request", "set exactly one of 'preset' (1-3) or 'manual' (10-100)")
				return false
			}
			return true
		},
		func(ctx context.Context, rc *recordingClient, q *req) error {
			if q.Preset != nil {
				return breezy.SetSpeedPreset(ctx, rc, *q.Preset)
			}
			return breezy.SetSpeedManual(ctx, rc, *q.Manual)
		},
	)
}

// postPreset writes the per-preset supply/extract percentages for one of
// the three numbered presets. Body: {"preset":1|2|3, "supply":N, "extract":N}.
// Editing the currently-active preset takes effect immediately on the
// running fan — there is no firmware "scratch" preset to stage edits in.
func (h *Handler) postPreset(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Preset  *int `json:"preset"`
		Supply  *int `json:"supply"`
		Extract *int `json:"extract"`
	}
	postV1WriteJSON(h, w, r,
		func(q *req, w http.ResponseWriter) bool {
			if q.Preset == nil || q.Supply == nil || q.Extract == nil {
				writeErr(w, "bad_request", "missing required field(s); send 'preset' (1-3), 'supply' (10-100), 'extract' (10-100)")
				return false
			}
			return true
		},
		func(ctx context.Context, rc *recordingClient, q *req) error {
			return breezy.SetPresetSpeed(ctx, rc, *q.Preset, *q.Supply, *q.Extract)
		},
	)
}

func (h *Handler) postMode(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Mode string `json:"mode"`
	}
	postV1WriteJSON(h, w, r, nil, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.SetMode(ctx, rc, q.Mode)
	})
}

func (h *Handler) postHeater(w http.ResponseWriter, r *http.Request) {
	type req struct {
		On *bool `json:"on"`
	}
	postV1WriteJSON(h, w, r,
		func(q *req, w http.ResponseWriter) bool {
			if q.On == nil {
				writeErr(w, "bad_request", "missing 'on' field (true/false)")
				return false
			}
			return true
		},
		func(ctx context.Context, rc *recordingClient, q *req) error {
			return breezy.SetHeater(ctx, rc, *q.On)
		},
	)
}

// postThreshold writes one or both of: the per-sensor over-threshold
// setpoint (humidity 0x0019, co2 0x001A, voc 0x031F) and the per-sensor
// enable flag (humidity 0x000F, co2 0x0011, voc 0x0315). Body:
// {"kind":"humidity|co2|voc", "value":N?, "enabled":bool?}. At least one
// of value/enabled must be present. Validation lives in
// breezy.SetThresholdConfig.
func (h *Handler) postThreshold(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Kind    string `json:"kind"`
		Value   *int   `json:"value"`
		Enabled *bool  `json:"enabled"`
	}
	postV1WriteJSON(h, w, r,
		func(q *req, w http.ResponseWriter) bool {
			if q.Kind == "" {
				writeErr(w, "bad_request", "missing 'kind' field (humidity|co2|voc)")
				return false
			}
			if q.Value == nil && q.Enabled == nil {
				writeErr(w, "bad_request", "must supply at least one of 'value' or 'enabled'")
				return false
			}
			return true
		},
		func(ctx context.Context, rc *recordingClient, q *req) error {
			return breezy.SetThresholdConfig(ctx, rc, q.Kind, q.Value, q.Enabled)
		},
	)
}

// postTimer activates a special-mode timer (0x0007). Body: {"mode":"off|night|turbo"}.
// Mirrors postHeater's shape; the recording client wraps the write so cache
// update and Poller.NoticeWrite fire automatically.
func (h *Handler) postTimer(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Mode string `json:"mode"`
	}
	postV1WriteJSON(h, w, r,
		func(q *req, w http.ResponseWriter) bool {
			if q.Mode == "" {
				writeErr(w, "bad_request", "missing 'mode' field (off|night|turbo)")
				return false
			}
			return true
		},
		func(ctx context.Context, rc *recordingClient, q *req) error {
			return breezy.SetTimer(ctx, rc, q.Mode)
		},
	)
}

// postRTC sets both 0x6F (3-byte sec/min/hr) and 0x70 (4-byte day/dow/month/year)
// in a single write packet. Day-of-week and year range validation are
// handled by breezy.SetRTC; parse errors from time.Parse are caught here.
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
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.SetRTC(ctx, rc, t)
	}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
