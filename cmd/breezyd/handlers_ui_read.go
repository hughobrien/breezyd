// SPDX-License-Identifier: GPL-3.0-or-later

// Read-path UI handlers: GET /ui/devices and GET /ui/devices/{name}/card.
// These render templ components into HTML fragments consumed by htmx.
package main

import (
	"log/slog"
	"net/http"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
)

func (h *Handler) getUIDeviceList(w http.ResponseWriter, r *http.Request) {
	views := h.collectViews()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := templates.DeviceList(views).Render(r.Context(), w); err != nil {
		slog.Error("render DeviceList", "err", err)
	}
}

func (h *Handler) getUIDeviceCard(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	view, ok := h.viewFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := templates.DeviceCard(view).Render(r.Context(), w); err != nil {
		slog.Error("render DeviceCard", "device", name, "err", err)
	}
}

// collectViews returns DeviceViews for all configured devices in name order.
func (h *Handler) collectViews() []ui.DeviceView {
	if h.State == nil {
		return nil
	}
	names := h.State.Devices()
	views := make([]ui.DeviceView, 0, len(names))
	for _, name := range names {
		snap, ok := h.State.Get(name)
		if !ok {
			continue
		}
		views = append(views, h.buildView(name, snap))
	}
	return views
}

// viewFor returns the DeviceView for name, or false if no snapshot exists.
func (h *Handler) viewFor(name string) (ui.DeviceView, bool) {
	if h.State == nil {
		return ui.DeviceView{}, false
	}
	snap, ok := h.State.Get(name)
	if !ok {
		return ui.DeviceView{}, false
	}
	return h.buildView(name, snap), true
}

// buildView converts a Snapshot to a DeviceView, augmenting with Energy and
// Schedule data from the per-device subsystems when available.
func (h *Handler) buildView(name string, snap Snapshot) ui.DeviceView {
	v := snapshotToView(name, snap)

	// Populate Serial from the device registry (Snapshot carries no device ID).
	if h.Devices != nil {
		if cfg, ok := h.Devices.Get(name); ok {
			v.Serial = cfg.ID
		}
	}

	// Augment with energy data from the per-device EnergyTracker.
	if h.Pollers != nil {
		if p, ok := h.Pollers[name]; ok && p != nil && p.Energy != nil {
			ev := p.Energy.Snapshot()
			v.Energy = energyViewFrom(ev)
		}
	}

	// Augment with schedule data from the per-device Scheduler.
	if h.Schedulers != nil {
		if sch, ok := h.Schedulers[name]; ok && sch != nil {
			v.Schedule = scheduleViewFrom(sch.Snapshot())
		}
	}

	return v
}
