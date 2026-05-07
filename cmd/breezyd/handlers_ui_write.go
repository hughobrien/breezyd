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
	"net/http"
	"time"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
	"github.com/hughobrien/breezyd/pkg/breezy"
)

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
	view, ok := h.viewFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.DeviceCard(view).Render(r.Context(), w)
}

// uiValidationError renders the DeviceCard with PostError set, status 422.
func (h *Handler) uiValidationError(w http.ResponseWriter, r *http.Request, name, msg string) {
	view, ok := h.viewFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	view.PostError = msg
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = templates.DeviceCard(view).Render(r.Context(), w)
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
