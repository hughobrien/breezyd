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
	resp := breezy.BuildStatus(snap.Values, name, cfg.ID, ip, lastPoll)
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

	client, unlock, err := h.dial(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
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
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := rc.WriteParams(ctx, []breezy.ParamWrite{{ID: id, Value: val}}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ----------------------------------------------------------------------------
// POST /power, /speed, /mode, /heater, /rtc
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
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.Power(ctx, rc, *body.On); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
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
	if (body.Preset == nil) == (body.Manual == nil) {
		writeErr(w, "bad_request", "set exactly one of 'preset' (1-3) or 'manual' (10-100)")
		return
	}
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var opErr error
	if body.Preset != nil {
		opErr = breezy.SetSpeedPreset(ctx, rc, *body.Preset)
	} else {
		opErr = breezy.SetSpeedManual(ctx, rc, *body.Manual)
	}
	if opErr != nil {
		writeErr(w, classifyClientErr(opErr), opErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// postPreset writes the per-preset supply/extract percentages for one of
// the three numbered presets. Body: {"preset":1|2|3, "supply":N, "extract":N}.
// Editing the currently-active preset takes effect immediately on the
// running fan — there is no firmware "scratch" preset to stage edits in.
func (h *Handler) postPreset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		Preset  *int `json:"preset"`
		Supply  *int `json:"supply"`
		Extract *int `json:"extract"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.Preset == nil || body.Supply == nil || body.Extract == nil {
		writeErr(w, "bad_request", "missing one of 'preset' (1-3), 'supply' (10-100), 'extract' (10-100)")
		return
	}
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetPresetSpeed(ctx, rc, *body.Preset, *body.Supply, *body.Extract); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
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
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetMode(ctx, rc, body.Mode); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
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
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetHeater(ctx, rc, *body.On); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// postThreshold writes one of the three sensor over-threshold setpoints
// (humidity 0x0019, co2 0x001A, voc 0x031F). Body: {"kind":"...", "value":N}.
// Per-kind range and step validation lives in breezy.SetThreshold.
func (h *Handler) postThreshold(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		Kind  string `json:"kind"`
		Value *int   `json:"value"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.Kind == "" {
		writeErr(w, "bad_request", "missing 'kind' field (humidity|co2|voc)")
		return
	}
	if body.Value == nil {
		writeErr(w, "bad_request", "missing 'value' field")
		return
	}
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetThreshold(ctx, rc, body.Kind, *body.Value); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// postTimer activates a special-mode timer (0x0007). Body: {"mode":"off|night|turbo"}.
// Mirrors postHeater's shape; the recording client wraps the write so cache
// update and Poller.NoticeWrite fire automatically.
func (h *Handler) postTimer(w http.ResponseWriter, r *http.Request) {
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
	if body.Mode == "" {
		writeErr(w, "bad_request", "missing 'mode' field (off|night|turbo)")
		return
	}
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetTimer(ctx, rc, body.Mode); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer raw.Close()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetRTC(ctx, rc, t); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
