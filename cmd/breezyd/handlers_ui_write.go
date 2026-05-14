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
// The threshold and schedule fragment endpoints emit datastar-patch-elements
// SSE events via patchFragmentSSE (the dashboard's @get / @put expect SSE
// streams, not raw HTML).
package main

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

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

// scheduleAcknowledgeSSE writes a 200 OK SSE response with no
// datastar-patch-elements events — the autosave handler's success
// path. The form's DOM state is already correct on the client (the
// user just typed it); sending back a re-rendered fragment would
// clobber whatever input still has focus. The next regular poll
// cycle refreshes the block once the user collapses or moves on.
//
// Mirrors errorBannerSSE's shape but emits no events. The SSE
// content-type + 200 status is still required so datastar's @put
// fetch action processes the response cleanly rather than treating
// it as an error.
func scheduleAcknowledgeSSE(w http.ResponseWriter, r *http.Request) {
	sse := newSSE(w, r)
	_ = sse // keep the import dependency; emitting no events.
}

// emptyScheduleEntry returns the seed values for a freshly-added edit
// row. Used by both getUIScheduleNewRow (when the user clicks
// "+ add row") and scheduleEditFrag (when entering edit mode with no
// existing entries).
func emptyScheduleEntry() ui.ScheduleEntryView {
	return ui.ScheduleEntryView{At: "08:00", Action: "regeneration", Pct: 60}
}

// getUIScheduleRead serves the always-editable schedule block.
//
// GET /ui/devices/{name}/schedule
func (h *Handler) getUIScheduleRead(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	view, ok := h.viewFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	patchFragmentSSE(w, r, scheduleSelector(name), templates.ScheduleBlock(name, view.Schedule, view.Stale))
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
	sse := newSSE(w, r)
	if err := sse.PatchElementTempl(
		templates.ScheduleRow(name, emptyScheduleEntry()),
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
		errorBannerSSE(w, r, http.StatusUnprocessableEntity, "bad form encoding")
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
		errorBannerSSE(w, r, http.StatusUnprocessableEntity, "malformed form: row fields mismatched")
		return
	}

	entries := make([]ScheduleEntry, 0, n)
	for i := range ats {
		at, err := ParseScheduleTime(ats[i])
		if err != nil {
			errorBannerSSE(w, r, http.StatusUnprocessableEntity, fmt.Sprintf("row %d: invalid time %q", i+1, ats[i]))
			return
		}
		action := actions[i]
		switch action {
		case "ventilation", "regeneration", "supply", "extract", "off":
			// valid
		default:
			errorBannerSSE(w, r, http.StatusUnprocessableEntity, fmt.Sprintf("row %d: invalid action %q", i+1, action))
			return
		}
		pct := 0
		if _, err := fmt.Sscanf(pcts[i], "%d", &pct); err != nil || pct < 10 || pct > 100 {
			if action != "off" {
				errorBannerSSE(w, r, http.StatusUnprocessableEntity, fmt.Sprintf("row %d: pct must be 10–100, got %q", i+1, pcts[i]))
				return
			}
			pct = 0 // off rows: pct is the in-band "no value" sentinel
		}
		entries = append(entries, ScheduleEntry{At: at, Action: action, Pct: pct})
	}

	sch, ok := h.Schedulers[name]
	if !ok || sch == nil {
		errorBannerSSE(w, r, http.StatusUnprocessableEntity, fmt.Sprintf("device %q has no scheduler wired", name))
		return
	}
	if err := sch.Replace(enabled, entries); err != nil {
		if errors.Is(err, breezy.ErrInvalidArg) {
			errorBannerSSE(w, r, http.StatusUnprocessableEntity, err.Error())
			return
		}
		h.uiWriteError(w, r, err)
		return
	}
	scheduleAcknowledgeSSE(w, r)
}

// postUISchedEnabled toggles the enabled bit on a device's schedule
// without touching its entries. Lets the dashboard's inline checkbox
// flip the schedule on/off without entering edit mode. See #27.
//
// POST /ui/devices/{name}/schedule/enabled
func (h *Handler) postUISchedEnabled(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Enabled *bool `json:"enabled"`
	}
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	var body req
	if err := datastar.ReadSignals(r, &body); err != nil {
		h.uiValidationError(w, r, name, "bad JSON body")
		return
	}
	if body.Enabled == nil {
		h.uiValidationError(w, r, name, "missing 'enabled' field (true/false)")
		return
	}
	sch, ok := h.Schedulers[name]
	if !ok || sch == nil {
		h.uiValidationError(w, r, name, "schedule not configured for this device")
		return
	}
	if err := sch.SetEnabled(*body.Enabled); err != nil {
		slog.Error("schedule: SetEnabled failed", "device", name, "err", err)
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// uiWriteError emits a datastar-patch-elements event into
// #global-error-banner with the matching HTTP status. ErrInvalidArg from
// pkg/breezy/ops surfaces as 422 with the op's own message — that's the
// single source of truth for protocol validation. Auth failures are 401;
// other backend errors (timeout, transport, etc.) are 502. Caller should
// return after this.
func (h *Handler) uiWriteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, breezy.ErrAuth):
		errorBannerSSE(w, r, http.StatusUnauthorized, "device authentication failed")
	case errors.Is(err, breezy.ErrInvalidArg):
		errorBannerSSE(w, r, http.StatusUnprocessableEntity, err.Error())
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
// newSSE flushes the response head (status 200 implicit) on construction;
// Datastar-Status must be set BEFORE that flush, so it precedes newSSE.
func errorBannerSSE(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Datastar-Status", strconv.Itoa(status))
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
// action helper (default contentType is JSON); datastar.ReadSignals is
// the SDK-blessed parser for that envelope. On decode failure, emits a
// 422 + #global-error-banner SSE event and returns false.
func (h *Handler) decodeJSONBody(w http.ResponseWriter, r *http.Request, name string, v interface{}) bool {
	if err := datastar.ReadSignals(r, v); err != nil {
		h.uiValidationError(w, r, name, "bad JSON body")
		return false
	}
	return true
}

// postUIWriteJSON is the spine shared by every /ui/devices/{name}/...
// action handler that takes a JSON body. It resolves the device, decodes
// into a fresh T, runs an optional shape-validator (for nil-pointer
// "field required" checks that precede the device round-trip), executes
// the op via doDeviceOp, surfaces errors through uiWriteError, and on
// success notifies subscribers and writes 200. The op closure may return
// breezy.ErrInvalidArg for value-range failures; uiWriteError translates
// that into a 422 banner with the op's own message — the single source
// of truth for protocol validation lives in pkg/breezy/ops.go.
//
// Pass nil for `shapeOK` if no pre-op shape check is needed. When the
// shape check returns false it MUST have already emitted its own error
// response (typically via h.uiValidationError).
func postUIWriteJSON[T any](
	h *Handler,
	w http.ResponseWriter,
	r *http.Request,
	shapeOK func(req *T) bool,
	op func(ctx context.Context, rc *recordingClient, req *T) error,
) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	var req T
	if !h.decodeJSONBody(w, r, name, &req) {
		return
	}
	if shapeOK != nil && !shapeOK(&req) {
		return
	}
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return op(ctx, rc, &req)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUIWriteNoBody is the body-less counterpart used by reset endpoints
// (postUIResetFilter / postUIResetFaults). Same flow without decode + shape.
func postUIWriteNoBody(
	h *Handler,
	w http.ResponseWriter,
	r *http.Request,
	op func(ctx context.Context, rc *recordingClient) error,
) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.doDeviceOp(r, name, op); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUIMode sets the airflow mode.
//
// JSON: {"mode": "ventilation" | "regeneration" | "supply" | "extract"}
func (h *Handler) postUIMode(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Mode string `json:"mode"`
	}
	postUIWriteJSON(h, w, r, nil, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.SetMode(ctx, rc, q.Mode)
	})
}

// postUIPreset writes the per-preset supply/extract percentages.
//
// JSON: {"preset": 1|2|3, "supply": 10..100, "extract": 10..100}
func (h *Handler) postUIPreset(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Preset  int `json:"preset"`
		Supply  int `json:"supply"`
		Extract int `json:"extract"`
	}
	postUIWriteJSON(h, w, r, nil, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.SetPresetSpeed(ctx, rc, q.Preset, q.Supply, q.Extract)
	})
}

// postUISpeed sets the fan speed (manual percentage or preset).
//
// JSON: {"manual": N} (10..100) XOR {"preset": N} (1..3)
func (h *Handler) postUISpeed(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Manual *int `json:"manual,omitempty"`
		Preset *int `json:"preset,omitempty"`
	}
	shape := func(q *req) bool {
		hasManual := q.Manual != nil
		hasPreset := q.Preset != nil
		if hasManual == hasPreset {
			h.uiValidationError(w, r, "", "set exactly one of 'preset' (1-3) or 'manual' (10-100)")
			return false
		}
		return true
	}
	postUIWriteJSON(h, w, r, shape, func(ctx context.Context, rc *recordingClient, q *req) error {
		if q.Preset != nil {
			return breezy.SetSpeedPreset(ctx, rc, *q.Preset)
		}
		return breezy.SetSpeedManual(ctx, rc, *q.Manual)
	})
}

// postUIHeater toggles the heater.
//
// JSON: {"on": bool}
func (h *Handler) postUIHeater(w http.ResponseWriter, r *http.Request) {
	type req struct {
		On *bool `json:"on"`
	}
	shape := func(q *req) bool {
		if q.On == nil {
			h.uiValidationError(w, r, "", "missing 'on' field (true/false)")
			return false
		}
		return true
	}
	postUIWriteJSON(h, w, r, shape, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.SetHeater(ctx, rc, *q.On)
	})
}

// postUITimer toggles a special-mode timer.
//
// JSON: {"mode": "off" | "night" | "turbo"}
//
// The template implements the toggle: if the requested mode matches the
// currently-active special_mode, the button sends mode=off instead.
// This handler does not need to inspect current state.
//
// Activating a timer (mode != off) also writes power=on first, so the
// cache reflects the on-state immediately and the dashboard's power
// button flips with the timer click. The firmware runs the fans on
// timer regardless of 0x0001, but our view-derived Power needs the
// param-side to be consistent for the cache-driven render path.
func (h *Handler) postUITimer(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Mode string `json:"mode"`
	}
	postUIWriteJSON(h, w, r, nil, func(ctx context.Context, rc *recordingClient, q *req) error {
		if strings.ToLower(q.Mode) != "off" {
			if err := breezy.Power(ctx, rc, true); err != nil {
				return err
			}
		}
		return breezy.SetTimer(ctx, rc, q.Mode)
	})
}

// postUITimerDuration writes the configured duration for one of the
// special-mode timers. When the matching timer is currently active,
// the firmware restarts the running countdown to the new total on
// its own — no follow-up 0x0007 write needed (verified against
// firmware 0.11; see the design doc).
//
// JSON: {"mode": "night"|"turbo", "hours": 0..23, "minutes": 0..59}
//
// Hours+minutes must sum to at least 1 minute. All validation errors
// land in #global-error-banner via the SSE error envelope.
func (h *Handler) postUITimerDuration(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Mode    string `json:"mode"`
		Hours   int    `json:"hours"`
		Minutes int    `json:"minutes"`
	}
	shape := func(q *req) bool {
		m := strings.ToLower(q.Mode)
		if m != "night" && m != "turbo" {
			h.uiValidationError(w, r, "", "mode must be 'night' or 'turbo'")
			return false
		}
		if q.Hours < 0 || q.Hours > 23 {
			h.uiValidationError(w, r, "", "hours must be 0..23")
			return false
		}
		if q.Minutes < 0 || q.Minutes > 59 {
			h.uiValidationError(w, r, "", "minutes must be 0..59")
			return false
		}
		if q.Hours == 0 && q.Minutes == 0 {
			h.uiValidationError(w, r, "", "duration must be at least 1 minute")
			return false
		}
		return true
	}
	postUIWriteJSON(h, w, r, shape, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.SetTimerDuration(ctx, rc, q.Mode, q.Hours, q.Minutes)
	})
}

// postUIResetFilter resets the filter-clogged counter. No body.
func (h *Handler) postUIResetFilter(w http.ResponseWriter, r *http.Request) {
	postUIWriteNoBody(h, w, r, func(ctx context.Context, rc *recordingClient) error {
		return breezy.ResetFilter(ctx, rc)
	})
}

// postUIResetFaults clears the active fault list. No body.
func (h *Handler) postUIResetFaults(w http.ResponseWriter, r *http.Request) {
	postUIWriteNoBody(h, w, r, func(ctx context.Context, rc *recordingClient) error {
		return breezy.ResetFaults(ctx, rc)
	})
}

// postUIPower toggles a device on/off.
//
// JSON: {"on": bool}
//
// Powering off implicitly clears the timer (0x0007 → 0); breezy.Power
// emits both writes in one packet to mirror the firmware behavior at
// the cache level. See pkg/breezy/ops.go::Power.
func (h *Handler) postUIPower(w http.ResponseWriter, r *http.Request) {
	type req struct {
		On *bool `json:"on"`
	}
	shape := func(q *req) bool {
		if q.On == nil {
			h.uiValidationError(w, r, "", "missing 'on' field (true/false)")
			return false
		}
		return true
	}
	postUIWriteJSON(h, w, r, shape, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.Power(ctx, rc, *q.On)
	})
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
