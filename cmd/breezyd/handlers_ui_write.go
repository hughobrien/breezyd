// SPDX-License-Identifier: GPL-3.0-or-later

// HTML-fragment write endpoints under /ui/devices/{name}/...
// Each handler:
//  1. Resolves the device (404 if unknown).
//  2. Parses form params (422 + DeviceCard with PostError on validation failure).
//  3. Calls the existing breezy write path via dialRecording.
//  4. On success: 200 + rendered DeviceCard.
//  5. On breezy.ErrAuth: 401 + error_banner.
//  6. On other backend error: 502 + error_banner.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/a-h/templ"
	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
	"github.com/hughobrien/breezyd/pkg/breezy"
)

// ---------- schedule editor ----------

// scheduleReadFrag renders the read variant of the schedule block as an HTML fragment.
func (h *Handler) scheduleReadFrag(w http.ResponseWriter, r *http.Request, name string) {
	view, ok := h.viewFor(r, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := templates.ScheduleBlock(name, view.Schedule, view.Stale, view.DetailsOpen["schedule"]).Render(r.Context(), w); err != nil {
		slog.Error("render ScheduleBlock read", "err", err)
	}
}

// scheduleEditFrag renders the edit variant of the schedule block as an HTML fragment.
// A non-empty errMsg signals a validation failure and produces a 422 response;
// otherwise the response is the implicit 200 (used for the initial GET).
func (h *Handler) scheduleEditFrag(w http.ResponseWriter, r *http.Request, name, errMsg string) {
	view, ok := h.viewFor(r, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if errMsg != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	if err := templates.ScheduleBlockEdit(name, view.Schedule, view.Stale, errMsg).Render(r.Context(), w); err != nil {
		slog.Error("render ScheduleBlockEdit", "err", err)
	}
}

// getUIScheduleRead serves the read variant of the schedule block.
//
// GET /ui/devices/{name}/schedule
func (h *Handler) getUIScheduleRead(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.viewFor(r, name); !ok {
		http.NotFound(w, r)
		return
	}
	h.scheduleReadFrag(w, r, name)
}

// getUIScheduleEdit serves the edit variant of the schedule block.
//
// GET /ui/devices/{name}/schedule/edit
func (h *Handler) getUIScheduleEdit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.viewFor(r, name); !ok {
		http.NotFound(w, r)
		return
	}
	h.scheduleEditFrag(w, r, name, "")
}

// getUIScheduleNewRow serves a single empty edit row, appended by the + button.
//
// GET /ui/devices/{name}/schedule/new-row
func (h *Handler) getUIScheduleNewRow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	empty := ui.ScheduleEntryView{At: "08:00", Action: "regeneration", Pct: 60}
	if err := templates.ScheduleEditRow(empty).Render(r.Context(), w); err != nil {
		slog.Error("render ScheduleEditRow new", "err", err)
	}
}

// putUISchedule applies enabled+entries from the edit form, returns read variant on success.
//
// Form: enabled=true (checkbox; absent when unchecked), at[]=HH:MM ..., action[]=..., pct[]=N ...
//
// PUT /ui/devices/{name}/schedule
func (h *Handler) putUISchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.scheduleEditFrag(w, r, name, "bad form encoding")
		return
	}

	// Enabled: checkbox sends "true" when checked, absent when unchecked.
	enabled := r.FormValue("enabled") == "true"

	ats := r.Form["at"]
	actions := r.Form["action"]
	pcts := r.Form["pct"]

	// All three slices must be the same length (form rows are parallel arrays).
	n := len(ats)
	if len(actions) != n || len(pcts) != n {
		h.scheduleEditFrag(w, r, name, "malformed form: row fields mismatched")
		return
	}

	entries := make([]ScheduleEntry, 0, n)
	for i := range ats {
		at, err := ParseScheduleTime(ats[i])
		if err != nil {
			h.scheduleEditFrag(w, r, name, fmt.Sprintf("row %d: invalid time %q", i+1, ats[i]))
			return
		}
		action := actions[i]
		switch action {
		case "ventilation", "regeneration", "supply", "extract", "off":
			// valid
		default:
			h.scheduleEditFrag(w, r, name, fmt.Sprintf("row %d: invalid action %q", i+1, action))
			return
		}
		pct := 0
		if _, err := fmt.Sscanf(pcts[i], "%d", &pct); err != nil || pct < 10 || pct > 100 {
			if action != "off" {
				h.scheduleEditFrag(w, r, name, fmt.Sprintf("row %d: pct must be 10–100, got %q", i+1, pcts[i]))
				return
			}
			pct = 10 // off rows: pct is irrelevant, use default
		}
		entries = append(entries, ScheduleEntry{At: at, Action: action, Pct: pct})
	}

	sch, ok := h.Schedulers[name]
	if !ok || sch == nil {
		h.scheduleEditFrag(w, r, name, fmt.Sprintf("device %q has no scheduler wired", name))
		return
	}
	if err := sch.Replace(enabled, entries); err != nil {
		if errors.Is(err, breezy.ErrInvalidArg) {
			h.scheduleEditFrag(w, r, name, err.Error())
			return
		}
		h.uiWriteError(w, r, err)
		return
	}
	h.scheduleReadFrag(w, r, name)
}

// uiWriteError translates a backend write error into an HTTP status + error_banner.
// Caller should return after this returns.
func (h *Handler) uiWriteError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	switch {
	case errors.Is(err, breezy.ErrAuth):
		w.WriteHeader(http.StatusUnauthorized)
		_ = templates.ErrorBanner("device authentication failed").Render(r.Context(), w)
	default:
		w.WriteHeader(http.StatusBadGateway)
		_ = templates.ErrorBanner(err.Error()).Render(r.Context(), w)
	}
}

// uiRenderCard renders the DeviceCard for a successful write. Re-fetches the
// snapshot from cache (the breezy ops will have updated it via WriteThrough).
func (h *Handler) uiRenderCard(w http.ResponseWriter, r *http.Request, name string) {
	view, ok := h.viewFor(r, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.DeviceCard(view).Render(r.Context(), w)
}

// uiValidationError renders the DeviceCard with PostError set, status 422.
func (h *Handler) uiValidationError(w http.ResponseWriter, r *http.Request, name, msg string) {
	view, ok := h.viewFor(r, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	view.PostError = msg
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = templates.DeviceCard(view).Render(r.Context(), w)
}

// postUIPreset writes the per-preset supply/extract percentages.
//
// Form: preset=1|2|3, supply=10..100, extract=10..100
func (h *Handler) postUIPreset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	preset, err := strconv.Atoi(r.FormValue("preset"))
	if err != nil || preset < 1 || preset > 3 {
		h.uiValidationError(w, r, name, "preset must be 1, 2, or 3")
		return
	}
	supply, err := strconv.Atoi(r.FormValue("supply"))
	if err != nil || supply < 10 || supply > 100 {
		h.uiValidationError(w, r, name, "supply must be 10..100")
		return
	}
	extract, err := strconv.Atoi(r.FormValue("extract"))
	if err != nil || extract < 10 || extract > 100 {
		h.uiValidationError(w, r, name, "extract must be 10..100")
		return
	}

	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetPresetSpeed(ctx, rc, preset, supply, extract); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}

// postUIMode sets the airflow mode.
//
// Form: mode=ventilation | regeneration | supply | extract
func (h *Handler) postUIMode(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	mode := r.FormValue("mode")
	switch mode {
	case "ventilation", "regeneration", "supply", "extract":
		// valid
	default:
		h.uiValidationError(w, r, name, "mode must be one of ventilation/regeneration/supply/extract")
		return
	}

	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetMode(ctx, rc, mode); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}

// postUISpeed sets the fan speed (manual percentage or preset).
//
// Form: manual=N (10..100) XOR preset=N (1..3)
func (h *Handler) postUISpeed(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	manualStr := r.FormValue("manual")
	presetStr := r.FormValue("preset")
	hasManual := manualStr != ""
	hasPreset := presetStr != ""
	if hasManual == hasPreset {
		h.uiValidationError(w, r, name, "set exactly one of 'preset' (1-3) or 'manual' (10-100)")
		return
	}

	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var opErr error
	if hasPreset {
		n := 0
		if _, err := fmt.Sscanf(presetStr, "%d", &n); err != nil || n < 1 || n > 3 {
			h.uiValidationError(w, r, name, "preset must be 1, 2, or 3")
			return
		}
		opErr = breezy.SetSpeedPreset(ctx, rc, n)
	} else {
		n := 0
		if _, err := fmt.Sscanf(manualStr, "%d", &n); err != nil || n < 10 || n > 100 {
			h.uiValidationError(w, r, name, "manual must be 10..100")
			return
		}
		opErr = breezy.SetSpeedManual(ctx, rc, n)
	}
	if opErr != nil {
		h.uiWriteError(w, r, opErr)
		return
	}
	h.uiRenderCard(w, r, name)
}

// postUIHeater toggles the heater.
//
// Form: on=true | on=false
func (h *Handler) postUIHeater(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	onStr := r.FormValue("on")
	if onStr != "true" && onStr != "false" {
		h.uiValidationError(w, r, name, "missing or invalid 'on' field (true/false)")
		return
	}
	on := onStr == "true"

	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetHeater(ctx, rc, on); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}

// postUITimer toggles a special-mode timer.
//
// Form: mode=off | night | turbo
//
// The template implements the toggle: if the requested mode matches the
// currently-active special_mode, the button sends mode=off instead.
// This handler does not need to inspect current state.
func (h *Handler) postUITimer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	mode := r.FormValue("mode")
	switch mode {
	case "off", "night", "turbo":
		// valid
	default:
		h.uiValidationError(w, r, name, "mode must be one of off/night/turbo")
		return
	}

	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetTimer(ctx, rc, mode); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}

// postUIResetFilter resets the filter-clogged counter. No form body needed.
func (h *Handler) postUIResetFilter(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.ResetFilter(ctx, rc); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}

// postUIResetFaults clears the active fault list. No form body needed.
func (h *Handler) postUIResetFaults(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.ResetFaults(ctx, rc); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}

// postUIPower toggles a device on/off.
//
// Form: on=true | on=false
func (h *Handler) postUIPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	onStr := r.FormValue("on")
	if onStr != "true" && onStr != "false" {
		h.uiValidationError(w, r, name, "missing or invalid 'on' field (true/false)")
		return
	}
	on := onStr == "true"

	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.Power(ctx, rc, on); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}

// ---------- threshold inline editor ----------

// renderThresholdRead returns the read-variant component for kind.
func renderThresholdRead(v ui.DeviceView, kind string) templ.Component {
	switch kind {
	case "co2":
		return templates.CO2Cell(v.Name, v.Sensors)
	case "voc":
		return templates.VOCCell(v.Name, v.Sensors)
	case "humidity":
		return templates.HumidityCell(v.Name, v.Sensors)
	}
	return nil
}

// renderThresholdEdit returns the edit-variant component for kind.
func renderThresholdEdit(v ui.DeviceView, kind string) templ.Component {
	switch kind {
	case "humidity":
		return templates.SensorThresholdEdit(v.Name, "humidity", "RH", 40, 80, 1,
			v.Sensors.HumidityThreshold, v.Sensors.HumidityAutoFan, false)
	case "co2":
		return templates.SensorThresholdEdit(v.Name, "co2", "eCO₂", 400, 2000, 10,
			v.Sensors.CO2Threshold, v.Sensors.CO2AutoFan, false)
	case "voc":
		return templates.SensorThresholdEdit(v.Name, "voc", "VOC", 50, 250, 1,
			v.Sensors.VOCThreshold, v.Sensors.VOCAutoFan, false)
	}
	return nil
}

// getUIThresholdRead serves the read variant for a threshold cell.
//
// GET /ui/devices/{name}/threshold/{kind}
func (h *Handler) getUIThresholdRead(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	kind := r.PathValue("kind")
	if kind != "humidity" && kind != "co2" && kind != "voc" {
		http.NotFound(w, r)
		return
	}
	view, ok := h.viewFor(r, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := renderThresholdRead(view, kind).Render(r.Context(), w); err != nil {
		slog.Error("render threshold read", "err", err)
	}
}

// getUIThresholdEdit serves the edit variant for a threshold cell.
//
// GET /ui/devices/{name}/threshold/{kind}/edit
func (h *Handler) getUIThresholdEdit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	kind := r.PathValue("kind")
	if kind != "humidity" && kind != "co2" && kind != "voc" {
		http.NotFound(w, r)
		return
	}
	view, ok := h.viewFor(r, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := renderThresholdEdit(view, kind).Render(r.Context(), w); err != nil {
		slog.Error("render threshold edit", "err", err)
	}
}

// putUIThreshold applies a threshold value and/or sensor-enabled flag.
//
// Form: kind=humidity|co2|voc, value=N (optional), enabled=true|false (always sent by the form).
//
// PUT /ui/devices/{name}/threshold
func (h *Handler) putUIThreshold(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	kind := r.FormValue("kind")
	if kind != "humidity" && kind != "co2" && kind != "voc" {
		h.uiValidationError(w, r, name, "invalid 'kind' (humidity|co2|voc)")
		return
	}
	var valuePtr *int
	var enabledPtr *bool
	if vs := r.FormValue("value"); vs != "" {
		v, err := strconv.Atoi(vs)
		if err != nil {
			h.uiValidationError(w, r, name, "invalid 'value' (must be integer)")
			return
		}
		valuePtr = &v
	}
	// The form uses the hidden+checkbox dual-input pattern for enabled:
	// the hidden field always submits "false"; the checkbox adds "true" when
	// checked. Browsers send both values in DOM order, so the LAST value is
	// the authoritative one. r.FormValue returns the FIRST value, which would
	// always read as "false" — use r.Form["enabled"] and read the last entry.
	if es := r.Form["enabled"]; len(es) > 0 {
		last := es[len(es)-1]
		if last != "" {
			e := last == "true"
			enabledPtr = &e
		}
	}
	if valuePtr == nil && enabledPtr == nil {
		h.uiValidationError(w, r, name, "must supply at least one of 'value' or 'enabled'")
		return
	}

	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetThresholdConfig(ctx, rc, kind, valuePtr, enabledPtr); err != nil {
		h.uiWriteError(w, r, err)
		return
	}

	view, ok := h.viewFor(r, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := renderThresholdRead(view, kind).Render(r.Context(), w); err != nil {
		slog.Error("render threshold read after put", "err", err)
	}
}
