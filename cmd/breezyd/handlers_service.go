// SPDX-License-Identifier: GPL-3.0-or-later

// Service-oriented HTTP handlers: cache-driven reads of /firmware,
// /efficiency, /faults, plus the small POST endpoints that reset filter
// counters and clear faults. These handlers do not produce a structured
// snapshot — they each surface a single fact.
package main

import (
	"fmt"
	"net/http"

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
	if !ok || len(raw) != 6 {
		writeErr(w, "not_found", "firmware metadata not in cache yet")
		return
	}
	year := int(uint16(raw[4]) | uint16(raw[5])<<8)
	writeJSON(w, http.StatusOK, map[string]any{
		"version":    fmt.Sprintf("%d.%02d", raw[0], raw[1]),
		"build_date": fmt.Sprintf("%04d-%02d-%02d", year, raw[3], raw[2]),
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
	raw, ok := snap.Values[0x007F]
	out := []map[string]any{}
	if ok {
		// Pairs of (code, kind). An odd trailing byte is ignored.
		for i := 0; i+1 < len(raw); i += 2 {
			kind := "alarm"
			if raw[i+1] == 1 {
				kind = "warning"
			} else if raw[i+1] != 0 {
				kind = fmt.Sprintf("unknown(%d)", raw[i+1])
			}
			out = append(out, map[string]any{
				"code": int(raw[i]),
				"kind": kind,
			})
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
	writes := []breezy.ParamWrite{{ID: 0x0065, Value: []byte{1}}}
	if err := h.doWrite(r.Context(), name, writes); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.recordWrite(name, writes)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) postFaultsReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	writes := []breezy.ParamWrite{{ID: 0x0080, Value: []byte{1}}}
	if err := h.doWrite(r.Context(), name, writes); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	h.recordWrite(name, writes)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
