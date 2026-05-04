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
	"strings"
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
	resp := h.buildSnapshot(name, cfg, snap)
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

	writes := []breezy.ParamWrite{{ID: id, Value: val}}
	if err := h.doWrite(r.Context(), name, writes); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.recordWrite(name, writes)
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
	val := byte(0)
	if *body.On {
		val = 1
	}
	writes := []breezy.ParamWrite{{ID: 0x0001, Value: []byte{val}}}
	if err := h.doWrite(r.Context(), name, writes); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.recordWrite(name, writes)
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
		writes := []breezy.ParamWrite{{ID: 0x0002, Value: []byte{val}}}
		if err := h.doWrite(r.Context(), name, writes); err != nil {
			writeErr(w, classifyClientErr(err), err.Error())
			return
		}
		h.recordWrite(name, writes)

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
		h.recordWrite(name, writes)

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
	writes := []breezy.ParamWrite{{ID: 0x00B7, Value: []byte{val}}}
	if err := h.doWrite(r.Context(), name, writes); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.recordWrite(name, writes)
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
	writes := []breezy.ParamWrite{{ID: 0x0068, Value: []byte{val}}}
	if err := h.doWrite(r.Context(), name, writes); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.recordWrite(name, writes)
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
	// poller; recordWrite still updates the cache and any test hook.
	h.recordWrite(name, writes)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
