// SPDX-License-Identifier: GPL-3.0-or-later

// Schedule HTTP endpoints.
package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// scheduleResponse is the over-wire JSON shape for GET and PUT.
type scheduleResponse struct {
	Enabled   bool            `json:"enabled"`
	Entries   []ScheduleEntry `json:"entries"`
	LastApply *LastApply      `json:"last_apply,omitempty"`
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
	snap := sch.Snapshot()
	writeJSON(w, http.StatusOK, scheduleResponse{
		Enabled:   snap.Enabled,
		Entries:   nilToEmpty(snap.Entries),
		LastApply: snap.LastApply,
	})
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
	snap := sch.Snapshot()
	writeJSON(w, http.StatusOK, scheduleResponse{
		Enabled:   snap.Enabled,
		Entries:   nilToEmpty(snap.Entries),
		LastApply: snap.LastApply,
	})
}

// nilToEmpty makes JSON render `[]` instead of `null` when the schedule
// has no entries.
func nilToEmpty(e []ScheduleEntry) []ScheduleEntry {
	if e == nil {
		return []ScheduleEntry{}
	}
	return e
}
