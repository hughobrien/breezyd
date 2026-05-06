// SPDX-License-Identifier: GPL-3.0-or-later

// Schedule HTTP endpoints.
package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// scheduleResponse is the over-wire JSON shape used by both the
// /schedule endpoints and the service.schedule glue on /v1/devices/{name}.
// `alert` is a derived bool the dashboard reads to decide force-expand —
// duplicated here so the UI doesn't have to descend into last_apply.
type scheduleResponse struct {
	Enabled   bool            `json:"enabled"`
	Entries   []ScheduleEntry `json:"entries"`
	Alert     bool            `json:"alert"`
	LastApply *LastApply      `json:"last_apply,omitempty"`
}

// scheduleResponseFrom builds a scheduleResponse from a ScheduleSnapshot,
// computing the derived alert flag.
func scheduleResponseFrom(s ScheduleSnapshot) scheduleResponse {
	return scheduleResponse{
		Enabled:   s.Enabled,
		Entries:   nilToEmpty(s.Entries),
		Alert:     s.LastApply != nil && !s.LastApply.OK,
		LastApply: s.LastApply,
	}
}

// getSchedule renders the in-memory schedule.
func (h *Handler) getSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	sch, ok := h.Schedulers[name]
	if !ok || sch == nil {
		// Shouldn't happen in production (every device gets a Scheduler in
		// startPollers), but tests may construct a Handler without one.
		writeJSON(w, http.StatusOK, scheduleResponse{Enabled: false, Entries: []ScheduleEntry{}})
		return
	}
	writeJSON(w, http.StatusOK, scheduleResponseFrom(sch.Snapshot()))
}

// putSchedule replaces the schedule wholesale. Validation lives in
// Scheduler.Replace; ErrInvalidArg → 400 bad_request.
func (h *Handler) putSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		Enabled bool            `json:"enabled"`
		Entries []ScheduleEntry `json:"entries"`
	}
	if !readBody(w, r, &body) {
		return
	}
	sch, ok := h.Schedulers[name]
	if !ok || sch == nil {
		writeErr(w, "internal", fmt.Sprintf("device %q has no scheduler wired", name))
		return
	}
	if err := sch.Replace(body.Enabled, body.Entries); err != nil {
		if errors.Is(err, breezy.ErrInvalidArg) {
			writeErr(w, "bad_request", err.Error())
			return
		}
		writeErr(w, "internal", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, scheduleResponseFrom(sch.Snapshot()))
}

// nilToEmpty makes JSON render `[]` instead of `null` when the schedule
// has no entries.
func nilToEmpty(e []ScheduleEntry) []ScheduleEntry {
	if e == nil {
		return []ScheduleEntry{}
	}
	return e
}
