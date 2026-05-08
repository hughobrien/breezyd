// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

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
//  2. Sends the current card for every configured device (initial state).
//  3. Subscribes to PushHub and forwards events until the client
//     disconnects.
//  4. Emits a comment line every keepaliveInterval while idle.
//
// Reconnects re-trigger the initial-state pass — the dashboard self-heals
// without Last-Event-ID resume.
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

	for _, view := range h.collectViews(r) {
		if err := sse.PatchElementTempl(
			templates.DeviceCard(view),
			datastar.WithSelectorf(`.card[data-device=%q]`, view.Name),
			datastar.WithModeOuter(),
		); err != nil {
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
			if err := sse.PatchElements(
				ev.HTML,
				datastar.WithSelectorf(`.card[data-device=%q]`, ev.DeviceName),
				datastar.WithModeOuter(),
			); err != nil {
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
