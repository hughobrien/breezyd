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

	// Initial state appends each card to #device-list. Per-card outer patches
	// can't run until the .card[data-device=...] target exists, which it
	// doesn't on a fresh page load — datastar drops them as
	// PatchElementsNoTargetsFound. Append uses real DOM mutation (appendChild)
	// so datastar's MutationObserver picks up data-on:click etc. on the new
	// nodes. Subsequent per-card updates below use outer mode to replace
	// existing cards in place.
	for _, view := range h.collectViews() {
		if err := sse.PatchElementTempl(
			templates.DeviceCard(view),
			datastar.WithSelector("#device-list"),
			datastar.WithModeAppend(),
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
			// TODO(Task 5): emit signals + per-block patches. For now, send
			// the first block (if any) so existing tests keep working until
			// Task 5 rewrites this handler to drain the structured event properly.
			if len(ev.Blocks) > 0 {
				if err := sse.PatchElements(
					ev.Blocks[0].HTML,
					datastar.WithSelector(ev.Blocks[0].Selector),
					datastar.WithModeOuter(),
				); err != nil {
					slog.Debug("sse: patch failed", "err", err, "device", ev.DeviceName)
					return
				}
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
