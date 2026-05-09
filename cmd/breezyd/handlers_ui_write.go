// SPDX-License-Identifier: GPL-3.0-or-later

// Datastar action endpoints under /ui/devices/{name}/...
// Each action handler:
//  1. Resolves the device (404 if unknown).
//  2. Parses form params (422 + SSE banner on validation failure).
//  3. Calls the existing breezy write path via dialRecording.
//  4. On success: 200 + empty body, plus PushHub.Notify so subscribed
//     /ui/sse streams refresh the card immediately.
//  5. On breezy.ErrAuth: 401 + datastar-patch-elements event into
//     #global-error-banner.
//  6. On other backend error: 502 + datastar-patch-elements event.
//
// Threshold and schedule fragment endpoints still emit HTML; they get
// converted to SSE in Task 5.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/starfederation/datastar-go/datastar"
)

// ---------- schedule editor ----------

// scheduleSelector targets a device card's schedule details element.
func scheduleSelector(name string) string {
	return fmt.Sprintf(`.card[data-device=%q] details.block.schedule`, name)
}

// scheduleReadFrag emits a datastar-patch-elements event with the
// read-variant schedule block, replacing the device's schedule details
// element.
func (h *Handler) scheduleReadFrag(w http.ResponseWriter, r *http.Request, name string) {
	view, ok := h.viewFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	patchFragmentSSE(w, r, scheduleSelector(name), templates.ScheduleBlock(name, view.Schedule, view.Stale))
}

// scheduleEditFrag emits a datastar-patch-elements event with the
// edit-variant schedule block. A non-empty errMsg signals a validation
// failure and produces a 422 response.
func (h *Handler) scheduleEditFrag(w http.ResponseWriter, r *http.Request, name, errMsg string) {
	view, ok := h.viewFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if errMsg != "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}
	patchFragmentSSE(w, r, scheduleSelector(name), templates.ScheduleBlockEdit(name, view.Schedule, view.Stale, errMsg))
}

// getUIScheduleRead serves the read variant of the schedule block.
//
// GET /ui/devices/{name}/schedule
func (h *Handler) getUIScheduleRead(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.viewFor(name); !ok {
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
	if _, ok := h.viewFor(name); !ok {
		http.NotFound(w, r)
		return
	}
	h.scheduleEditFrag(w, r, name, "")
}

// getUIScheduleNewRow appends an empty edit row to the schedule editor
// table body via a datastar-patch-elements event with mode=append.
//
// GET /ui/devices/{name}/schedule/new-row
func (h *Handler) getUIScheduleNewRow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "keep-alive")
	}
	sse := newSSE(w, r)
	empty := ui.ScheduleEntryView{At: "08:00", Action: "regeneration", Pct: 60}
	if err := sse.PatchElementTempl(
		templates.ScheduleEditRow(empty),
		datastar.WithSelectorf(`.card[data-device=%q] tbody.schedule-edit-tbody`, name),
		// Inner-mode + append-style not needed; we instead append by
		// targeting tbody and using mode=append.
		datastar.WithMode(datastar.ElementPatchModeAppend),
	); err != nil {
		slog.Debug("schedule new-row: patch failed", "err", err)
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
			pct = 0 // off rows: pct is the in-band "no value" sentinel
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

// uiWriteError emits a datastar-patch-elements event into
// #global-error-banner with the error message and the matching HTTP
// status (401 for auth, 502 otherwise). Caller should return after this.
func (h *Handler) uiWriteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, breezy.ErrAuth):
		errorBannerSSE(w, r, http.StatusUnauthorized, "device authentication failed")
	default:
		errorBannerSSE(w, r, http.StatusBadGateway, uiBannerMsg(err))
	}
}

// uiBannerMsg renders err as a user-facing banner string. Raw
// `context deadline exceeded` is meaningless to a dashboard user, so
// translate timeout-shaped errors (ctx deadline + net.Error timeouts +
// breezy.ErrTimeout from the memory backend) into a clear "device timeout".
func uiBannerMsg(err error) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "device timeout (no response)"
	}
	if errors.Is(err, breezy.ErrTimeout) {
		return "device timeout (no response)"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "device timeout (no response)"
	}
	return err.Error()
}

// uiValidationError emits a 422 + datastar-patch-elements event with the
// human-readable validation message. The `name` parameter is unused now
// (errors go to a global banner, not a per-card one), but kept for
// symmetry with existing call sites.
func (h *Handler) uiValidationError(w http.ResponseWriter, r *http.Request, _ /*name*/ string, msg string) {
	errorBannerSSE(w, r, http.StatusUnprocessableEntity, msg)
}

// notifyAfterWrite reads the post-write Snapshot from the cache and fans
// it out to /ui/sse subscribers. Called from every successful action
// handler. The Snapshot was just refreshed by the breezy package's
// WriteThrough hook, so subscribers see the new value within one event.
func (h *Handler) notifyAfterWrite(name string) {
	if h.PushHub == nil || h.State == nil {
		return
	}
	if snap, ok := h.State.Get(name); ok {
		h.PushHub.Notify(name, snap)
	}
}

// patchFragmentSSE renders cmp into a datastar-patch-elements event
// against the given selector + mode (typically "outer"). Status is
// implicit 200; for validation failures the caller WriteHeader's the
// error status before calling this. Used by the threshold and schedule
// fragment endpoints (the dashboard's @get / @put expect SSE event
// streams, not raw HTML).
func patchFragmentSSE(w http.ResponseWriter, r *http.Request, selector string, cmp templ.Component) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "keep-alive")
	}
	sse := newSSE(w, r)
	if err := sse.PatchElementTempl(
		cmp,
		datastar.WithSelector(selector),
		datastar.WithModeOuter(),
	); err != nil {
		slog.Debug("patchFragmentSSE: patch failed", "err", err)
	}
}

// errorBannerSSE writes a datastar-patch-elements event targeting
// #global-error-banner. Returns HTTP 200 — datastar's @post drops
// non-2xx response bodies, so we encode the error in the SSE payload
// itself and exit cleanly. The `status` parameter survives only as a
// custom Datastar-Status response header for observability/debugging.
func errorBannerSSE(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Datastar-Status", strconv.Itoa(status))
	w.Header().Set("X-Accel-Buffering", "no")
	if r.ProtoMajor == 1 {
		w.Header().Set("Connection", "keep-alive")
	}
	w.WriteHeader(http.StatusOK)
	sse := newSSE(w, r)
	htmlFragment := `<div class="err-banner" role="alert">` + html.EscapeString(msg) + `</div>`
	if err := sse.PatchElements(
		htmlFragment,
		datastar.WithSelector("#global-error-banner"),
		datastar.WithModeInner(),
	); err != nil {
		slog.Debug("errorBannerSSE: patch failed", "err", err)
	}
}

// decodeJSONBody decodes r.Body into v. Action endpoints under
// /ui/devices/{name}/... receive JSON payloads from datastar's @post
// action helper (default contentType is JSON). On decode failure, emits
// a 422 + #global-error-banner SSE event and returns false.
func (h *Handler) decodeJSONBody(w http.ResponseWriter, r *http.Request, name string, v interface{}) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		h.uiValidationError(w, r, name, "bad JSON body")
		return false
	}
	return true
}

// postUIMode sets the airflow mode.
//
// JSON: {"mode": "ventilation" | "regeneration" | "supply" | "extract"}
func (h *Handler) postUIMode(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if !h.decodeJSONBody(w, r, name, &req) {
		return
	}
	switch req.Mode {
	case "ventilation", "regeneration", "supply", "extract":
		// valid
	default:
		h.uiValidationError(w, r, name, "mode must be one of ventilation/regeneration/supply/extract")
		return
	}

	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.SetMode(ctx, rc, req.Mode)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUIPreset writes the per-preset supply/extract percentages.
//
// JSON: {"preset": 1|2|3, "supply": 10..100, "extract": 10..100}
func (h *Handler) postUIPreset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Preset  int `json:"preset"`
		Supply  int `json:"supply"`
		Extract int `json:"extract"`
	}
	if !h.decodeJSONBody(w, r, name, &req) {
		return
	}
	if req.Preset < 1 || req.Preset > 3 {
		h.uiValidationError(w, r, name, "preset must be 1, 2, or 3")
		return
	}
	if req.Supply < 10 || req.Supply > 100 {
		h.uiValidationError(w, r, name, "supply must be 10..100")
		return
	}
	if req.Extract < 10 || req.Extract > 100 {
		h.uiValidationError(w, r, name, "extract must be 10..100")
		return
	}

	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.SetPresetSpeed(ctx, rc, req.Preset, req.Supply, req.Extract)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUISpeed sets the fan speed (manual percentage or preset).
//
// JSON: {"manual": N} (10..100) XOR {"preset": N} (1..3)
func (h *Handler) postUISpeed(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Manual *int `json:"manual,omitempty"`
		Preset *int `json:"preset,omitempty"`
	}
	if !h.decodeJSONBody(w, r, name, &req) {
		return
	}
	hasManual := req.Manual != nil
	hasPreset := req.Preset != nil
	if hasManual == hasPreset {
		h.uiValidationError(w, r, name, "set exactly one of 'preset' (1-3) or 'manual' (10-100)")
		return
	}
	if hasPreset && (*req.Preset < 1 || *req.Preset > 3) {
		h.uiValidationError(w, r, name, "preset must be 1, 2, or 3")
		return
	}
	if hasManual && (*req.Manual < 10 || *req.Manual > 100) {
		h.uiValidationError(w, r, name, "manual must be 10..100")
		return
	}
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		if hasPreset {
			return breezy.SetSpeedPreset(ctx, rc, *req.Preset)
		}
		return breezy.SetSpeedManual(ctx, rc, *req.Manual)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUIHeater toggles the heater.
//
// JSON: {"on": bool}
func (h *Handler) postUIHeater(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	var req struct {
		On *bool `json:"on"`
	}
	if !h.decodeJSONBody(w, r, name, &req) {
		return
	}
	if req.On == nil {
		h.uiValidationError(w, r, name, "missing 'on' field (true/false)")
		return
	}

	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.SetHeater(ctx, rc, *req.On)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUITimer toggles a special-mode timer.
//
// JSON: {"mode": "off" | "night" | "turbo"}
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
	var req struct {
		Mode string `json:"mode"`
	}
	if !h.decodeJSONBody(w, r, name, &req) {
		return
	}
	switch req.Mode {
	case "off", "night", "turbo":
		// valid
	default:
		h.uiValidationError(w, r, name, "mode must be one of off/night/turbo")
		return
	}

	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.SetTimer(ctx, rc, req.Mode)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUIResetFilter resets the filter-clogged counter. No form body needed.
func (h *Handler) postUIResetFilter(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.ResetFilter(ctx, rc)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUIResetFaults clears the active fault list. No form body needed.
func (h *Handler) postUIResetFaults(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.ResetFaults(ctx, rc)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUIPower toggles a device on/off.
//
// JSON: {"on": bool}
func (h *Handler) postUIPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	var req struct {
		On *bool `json:"on"`
	}
	if !h.decodeJSONBody(w, r, name, &req) {
		return
	}
	if req.On == nil {
		h.uiValidationError(w, r, name, "missing 'on' field (true/false)")
		return
	}

	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.Power(ctx, rc, *req.On)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
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

// patchThresholdCellSSE emits a datastar-patch-elements event for the
// kind's cell. We simplify selector matching by emitting a unique id on
// the cell — see the wrapping <div class="sensor-cell" id=...> below.
func (h *Handler) patchThresholdCellSSE(w http.ResponseWriter, r *http.Request, name, kind string, edit bool) {
	view, ok := h.viewFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var cmp templ.Component
	if edit {
		cmp = renderThresholdEdit(view, kind)
	} else {
		cmp = renderThresholdRead(view, kind)
	}
	if cmp == nil {
		http.NotFound(w, r)
		return
	}
	patchFragmentSSE(w, r,
		fmt.Sprintf(`.card[data-device=%q] [data-threshold-cell=%q]`, name, kind),
		cmp,
	)
}

// getUIThresholdRead serves the read variant for a threshold cell as an
// SSE patch event.
//
// GET /ui/devices/{name}/threshold/{kind}
func (h *Handler) getUIThresholdRead(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	kind := r.PathValue("kind")
	if kind != "humidity" && kind != "co2" && kind != "voc" {
		http.NotFound(w, r)
		return
	}
	h.patchThresholdCellSSE(w, r, name, kind, false)
}

// getUIThresholdEdit serves the edit variant for a threshold cell as an
// SSE patch event.
//
// GET /ui/devices/{name}/threshold/{kind}/edit
func (h *Handler) getUIThresholdEdit(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	kind := r.PathValue("kind")
	if kind != "humidity" && kind != "co2" && kind != "voc" {
		http.NotFound(w, r)
		return
	}
	h.patchThresholdCellSSE(w, r, name, kind, true)
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

	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.SetThresholdConfig(ctx, rc, kind, valuePtr, enabledPtr)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	h.patchThresholdCellSSE(w, r, name, kind, false)
}
