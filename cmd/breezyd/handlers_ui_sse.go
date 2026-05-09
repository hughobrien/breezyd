// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
	"github.com/starfederation/datastar-go/datastar"
)

// keepaliveInterval is how often we emit an SSE comment line so idle
// connections aren't dropped by intermediaries (browsers, NATs, reverse
// proxies). Promoted from a const to a package var so tests can shrink
// it without sleeping for a real interval.
var keepaliveInterval = 30 * time.Second

// getUISSE serves the long-lived push channel. On connect, the handler:
//  1. Clears the response writer's WriteDeadline (the daemon's http.Server
//     enforces a 30s WriteTimeout for slow-loris protection on the JSON
//     API; SSE connections must opt out).
//  2. Detects cold load vs reconnect via the Last-Event-ID header.
//  3. Sends the current card for every configured device (initial state).
//     Cold load uses mode=append against #device-list; reconnect uses
//     mode=outer against .card[data-device=...] to replace in-place.
//  4. Subscribes to PushHub and forwards structured PushEvents until the
//     client disconnects: one datastar-patch-signals then one
//     datastar-patch-elements per BlockPatch.
//  5. Emits a comment line every keepaliveInterval while idle.
func (h *Handler) getUISSE(w http.ResponseWriter, r *http.Request) {
	if h.PushHub == nil {
		http.Error(w, "push hub not configured", http.StatusInternalServerError)
		return
	}
	hub, ok := h.PushHub.(*PushHub)
	if !ok {
		http.Error(w, "push hub of wrong type", http.StatusInternalServerError)
		return
	}

	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		// Some test recorders return ErrNotSupported here; that's fine —
		// real net/http always honours it. Don't 500 on that path.
		slog.Debug("sse: clear write deadline", "err", err)
	}

	sse := datastar.NewSSE(w, r)

	// Reconnect detection: EventSource auto-resends Last-Event-ID after a
	// drop. We don't implement replay — we just use the header's presence
	// as a binary cold-load-vs-reconnect signal so the initial-state pass
	// can avoid duplicating cards on reconnect.
	isReconnect := r.Header.Get("Last-Event-ID") != ""

	for _, view := range h.collectViews() {
		if err := emitInitialCard(sse, view, isReconnect); err != nil {
			slog.Debug("sse initial: patch failed", "err", err, "device", view.Name)
			return
		}
	}

	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				return
			}
			if err := emitPushEvent(sse, ev); err != nil {
				slog.Debug("sse: patch failed", "err", err, "device", ev.DeviceName)
				return
			}
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

// emitInitialCard sends the full card for one device. Cold load uses
// mode=append against #device-list (the card doesn't exist yet);
// reconnect uses mode=outer against .card[data-device=...] to replace
// the existing card in-place without duplicating.
func emitInitialCard(sse *datastar.ServerSentEventGenerator, view ui.DeviceView, isReconnect bool) error {
	eventID := "device:" + view.Name
	if isReconnect {
		return sse.PatchElementTempl(
			templates.DeviceCard(view),
			datastar.WithSelectorf(`.card[data-device=%q]`, view.Name),
			datastar.WithModeOuter(),
			datastar.WithPatchElementsEventID(eventID),
		)
	}
	return sse.PatchElementTempl(
		templates.DeviceCard(view),
		datastar.WithSelector("#device-list"),
		datastar.WithModeAppend(),
		datastar.WithPatchElementsEventID(eventID),
	)
}

// emitPushEvent dispatches one PushEvent: the signals patch first
// (so card-outer reactive bindings update before any block content),
// then one elements patch per block.
func emitPushEvent(sse *datastar.ServerSentEventGenerator, ev PushEvent) error {
	if len(ev.SignalsJSON) > 0 {
		if err := sse.PatchSignals(ev.SignalsJSON,
			datastar.WithPatchSignalsEventID("signals:"+ev.DeviceName),
		); err != nil {
			return err
		}
	}
	eventID := "block:" + ev.DeviceName
	for _, b := range ev.Blocks {
		if err := sse.PatchElements(
			b.HTML,
			datastar.WithSelector(b.Selector),
			datastar.WithModeOuter(),
			datastar.WithPatchElementsEventID(eventID),
		); err != nil {
			return err
		}
	}
	return nil
}
