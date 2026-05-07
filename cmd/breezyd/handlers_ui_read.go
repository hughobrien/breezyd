// SPDX-License-Identifier: GPL-3.0-or-later

// Read-path UI handlers: GET /ui/devices and GET /ui/devices/{name}/card.
// These render templ components into HTML fragments consumed by htmx.
package main

import (
	"log/slog"
	"net/http"
	"sort"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
	"github.com/hughobrien/breezyd/internal/uistate"
)

func (h *Handler) getUIDeviceList(w http.ResponseWriter, r *http.Request) {
	views := h.collectViews(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := templates.DeviceList(views).Render(r.Context(), w); err != nil {
		slog.Error("render DeviceList", "err", err)
	}
}

func (h *Handler) getUIDeviceCard(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	view, ok := h.viewFor(r, name)
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

// collectViews returns DeviceViews for ALL configured devices in name order,
// including ones without a successful poll (rendered as Unreachable).
//
// Iterating the device registry rather than the State cache mirrors the
// /v1/devices JSON shape and ensures misconfigured devices (wrong IP, bad
// password, etc.) surface in the dashboard with a placeholder card — the
// only signal users have that the daemon sees them at all.
func (h *Handler) collectViews(r *http.Request) []ui.DeviceView {
	if h.Devices == nil {
		return nil
	}
	state := uistate.Parse(r)
	registry := h.Devices.Snapshot()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)

	views := make([]ui.DeviceView, 0, len(names))
	for _, name := range names {
		if h.State != nil {
			// A Snapshot with no Values means the daemon has tried but never
			// got data (auth fail, UDP timeout, etc.) — render as unreachable
			// rather than a half-empty card. Once any params land, fall
			// through to the full layout (which marks staleness on its own).
			if snap, ok := h.State.Get(name); ok && len(snap.Values) > 0 {
				views = append(views, h.buildView(name, snap, state))
				continue
			}
		}
		views = append(views, h.unreachableView(name, registry[name]))
	}
	return views
}

// unreachableView renders a placeholder DeviceView for a configured device
// that has no Snapshot in the State cache (no successful poll yet).
func (h *Handler) unreachableView(name string, cfg DeviceConfig) ui.DeviceView {
	return ui.DeviceView{
		Name:        name,
		IP:          cfg.IP,
		Serial:      cfg.ID,
		Unreachable: true,
	}
}

// viewFor returns the DeviceView for name, or false if no snapshot exists.
func (h *Handler) viewFor(r *http.Request, name string) (ui.DeviceView, bool) {
	if h.State == nil {
		return ui.DeviceView{}, false
	}
	snap, ok := h.State.Get(name)
	if !ok {
		return ui.DeviceView{}, false
	}
	return h.buildView(name, snap, uistate.Parse(r)), true
}

// buildView converts a Snapshot to a DeviceView, augmenting with Energy and
// Schedule data from the per-device subsystems when available.
func (h *Handler) buildView(name string, snap Snapshot, state uistate.State) ui.DeviceView {
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

	v.DetailsOpen = computeDetailsOpen(name, v, state)
	if ps, ok := state.Preset[name]; ok {
		v.EditingPreset = ps.Open
		v.Automode = ps.Automode
		v.MatchSpeeds = ps.Match
	} else {
		def := uistate.DefaultsForDevice(name)
		v.EditingPreset = def.Open
		v.Automode = def.Automode
		v.MatchSpeeds = def.Match
	}
	return v
}

// computeDetailsOpen returns the per-section open state for a device,
// applying cookie state, force-open rules, and section defaults in that
// order. Force-open always wins (NeedsAttention for info; AlertActive
// for sensors; Schedule.Alert for schedule).
func computeDetailsOpen(name string, v ui.DeviceView, state uistate.State) map[string]bool {
	open := map[string]bool{
		"info":     defaultOpen("info"),
		"sensors":  defaultOpen("sensors"),
		"energy":   defaultOpen("energy"),
		"schedule": defaultOpen("schedule"),
	}
	for section := range open {
		id := section + "-" + name
		if val, ok := state.Details[id]; ok {
			open[section] = val
		}
	}
	if v.NeedsAttention {
		open["info"] = true
	}
	if v.Sensors.AlertActive {
		open["sensors"] = true
	}
	if v.Schedule.Alert {
		open["schedule"] = true
	}
	return open
}

// defaultOpen returns the per-section default when the cookie has no
// entry for that section. See the spec's defaultsBySection table.
func defaultOpen(section string) bool {
	switch section {
	case "sensors":
		return true
	default:
		return false
	}
}
