// SPDX-License-Identifier: GPL-3.0-or-later

// Service-oriented HTTP handlers: cache-driven reads of /firmware,
// /efficiency, /faults, plus the small POST endpoints that reset filter
// counters and clear faults. These handlers do not produce a structured
// snapshot — they each surface a single fact.
package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// ----------------------------------------------------------------------------
// /firmware, /efficiency, /faults
// ----------------------------------------------------------------------------

func (h *Handler) getFirmware(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	snap, _ := h.State.Get(name)
	raw, ok := snap.Values[0x0086]
	if !ok {
		writeErr(w, "not_found", "firmware metadata not in cache yet")
		return
	}
	v, err := (breezy.Param{Type: breezy.TypeFirmwareMeta}).Decode(raw)
	if err != nil {
		writeErr(w, "internal", fmt.Sprintf("decode firmware: %v", err))
		return
	}
	fw, ok := v.(breezy.FirmwareMetaValue)
	if !ok {
		writeErr(w, "internal", fmt.Sprintf("unexpected decoded type %T", v))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    fmt.Sprintf("%d.%02d", fw.Major, fw.Minor),
		"build_date": fw.Date.Format("2006-01-02"),
	})
}

func (h *Handler) getEfficiency(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	snap, _ := h.State.Get(name)
	b, ok := breezy.Uint8At(snap.Values, 0x0129)
	if !ok {
		writeErr(w, "not_found", "efficiency reading not in cache yet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery_efficiency_pct": int(b)})
}

// getFaults decodes 0x7F: a variable-length list of (code, kind) byte pairs.
// kind: 0=alarm, 1=warning. An empty list (no faults) returns an empty array.
func (h *Handler) getFaults(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	snap, _ := h.State.Get(name)
	out := []breezy.FaultCode{}
	if raw, ok := snap.Values[0x007F]; ok {
		for i := 0; i+1 < len(raw); i += 2 {
			var kind string
			switch raw[i+1] {
			case 0:
				kind = "alarm"
			case 1:
				kind = "warning"
			default:
				kind = fmt.Sprintf("unknown(%d)", raw[i+1])
			}
			out = append(out, breezy.FaultCode{Code: int(raw[i]), Kind: kind})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"faults": out})
}

// ----------------------------------------------------------------------------
// /filter/reset, /faults/reset
// ----------------------------------------------------------------------------

func (h *Handler) postFilterReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.ResetFilter(ctx, rc); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) postFaultsReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.ResetFaults(ctx, rc); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
