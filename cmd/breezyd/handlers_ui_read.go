// SPDX-License-Identifier: GPL-3.0-or-later

// Read-path UI helpers: viewFor / collectViews assemble DeviceView
// values from the State cache and per-device subsystems (Energy,
// Scheduler) for the SSE handler and the action-error paths. The
// dashboard itself is delivered via /ui/sse — there are no
// HTML-fragment endpoints anymore.
package main

import (
	"sort"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
)

// collectViews returns DeviceViews for ALL configured devices in name
// order, including ones without a successful poll (rendered as
// Unreachable). Iterating the device registry rather than the State
// cache mirrors the /v1/devices JSON shape and ensures misconfigured
// devices (wrong IP, bad password, etc.) surface in the dashboard with
// a placeholder card — the only signal users have that the daemon sees
// them at all.
func (h *Handler) collectViews() []ui.DeviceView {
	if h.Devices == nil {
		return nil
	}
	registry := h.Devices.Snapshot()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)

	views := make([]ui.DeviceView, 0, len(names))
	for _, name := range names {
		if h.State != nil {
			// A Snapshot with no Values means the daemon has tried but
			// never got data (auth fail, UDP timeout, etc.) — render
			// unreachable rather than a half-empty card. Once any params
			// land, fall through to the full layout (which marks
			// staleness on its own).
			if snap, ok := h.State.Get(name); ok && len(snap.Values) > 0 {
				views = append(views, h.buildView(name, snap))
				continue
			}
		}
		views = append(views, h.unreachableView(name, registry[name]))
	}
	return views
}

// unreachableView renders a placeholder DeviceView for a configured
// device that has no Snapshot in the State cache (no successful poll
// yet).
func (h *Handler) unreachableView(name string, cfg DeviceConfig) ui.DeviceView {
	return ui.DeviceView{
		Name:        name,
		IP:          cfg.IP,
		Serial:      cfg.ID,
		Unreachable: true,
	}
}

// viewFor returns the DeviceView for name, or false if no snapshot
// exists. Used by the SSE handler's per-device push path and by the
// PushHub's render closure (where there is no HTTP request in hand).
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

// buildView converts a Snapshot to a DeviceView, augmenting with Energy
// and Schedule data from the per-device subsystems when available.
func (h *Handler) buildView(name string, snap Snapshot) ui.DeviceView {
	v := snapshotToView(name, snap)

	if h.Devices != nil {
		if cfg, ok := h.Devices.Get(name); ok {
			v.Serial = cfg.ID
		}
	}

	if h.Pollers != nil {
		if p, ok := h.Pollers[name]; ok && p != nil && p.Energy != nil {
			ev := p.Energy.Snapshot()
			v.Energy = energyViewFrom(ev)
		}
	}

	if h.Schedulers != nil {
		if sch, ok := h.Schedulers[name]; ok && sch != nil {
			v.Schedule = scheduleViewFrom(sch.Snapshot())
		}
	}

	return v
}
