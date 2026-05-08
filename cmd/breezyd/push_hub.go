// SPDX-License-Identifier: GPL-3.0-or-later

// PushHub fans out per-device snapshot updates to subscribed SSE clients.
// Producers (the poller's OnPoll hook, action handlers' post-write paths)
// call Notify(name, snap). The hub renders the templ DeviceCard via the
// injected render closure, then queues a PushEvent — carrying the device
// name and the rendered HTML — onto every subscriber's buffered channel.
// The /ui/sse handler drains one such channel and turns each event into a
// `datastar-patch-elements` SSE event via the datastar Go SDK.
//
// Backpressure: subscriber channels are bounded (16). When a subscriber
// is too slow to drain, the oldest event is discarded — pushed events
// are full-card snapshots, so the latest supersedes prior ones and a
// dropped event is never user-visible.
package main

import (
	"bytes"
	"context"
	"sync"

	"github.com/a-h/templ"
)

// pushHubBufferSize is the per-subscriber event channel capacity.
// Sized to absorb a brief stall (e.g., a paused tab catching up) without
// dropping events from a steady-state poll cadence.
const pushHubBufferSize = 16

// PushEvent is one fan-out unit: a device name and the pre-rendered card
// HTML. The SSE handler builds the wire-format event from it, so multiple
// subscribers share a single render.
type PushEvent struct {
	DeviceName string
	HTML       string
}

// Subscriber holds a single SSE client's connection state inside the hub.
// Events is closed when the subscriber is removed.
type Subscriber struct {
	Events chan PushEvent
}

// PushNotifier is the producer-side interface — the poller and action
// handlers depend on this rather than on *PushHub so tests can swap in
// a fake.
type PushNotifier interface {
	Notify(name string, snap Snapshot)
}

// PushHub is the per-process fan-out registry.
type PushHub struct {
	render func(name string, snap Snapshot) (templ.Component, error)

	mu     sync.Mutex
	subs   map[*Subscriber]struct{}
	closed map[*Subscriber]struct{}
}

// NewPushHub constructs an empty hub. render produces the templ
// component to broadcast for each Notify; injection lets tests swap in a
// stub component without setting up the full templ machinery.
func NewPushHub(render func(name string, snap Snapshot) (templ.Component, error)) *PushHub {
	return &PushHub{
		render: render,
		subs:   make(map[*Subscriber]struct{}),
		closed: make(map[*Subscriber]struct{}),
	}
}

// Subscribe registers a new client and returns its handle. Caller is
// responsible for draining sub.Events and calling Unsubscribe when done.
func (h *PushHub) Subscribe() *Subscriber {
	sub := &Subscriber{Events: make(chan PushEvent, pushHubBufferSize)}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

// Unsubscribe removes a subscriber and closes its events channel. Safe
// to call multiple times — the second call is a no-op.
func (h *PushHub) Unsubscribe(sub *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[sub]; !ok {
		return
	}
	delete(h.subs, sub)
	h.closed[sub] = struct{}{}
	close(sub.Events)
}

// Notify renders the card for (name, snap) and enqueues the resulting
// event on every subscriber. Render errors are silently dropped — the
// next successful poll re-renders, and noisy logging from the poller
// hot path costs more than it saves.
func (h *PushHub) Notify(name string, snap Snapshot) {
	cmp, err := h.render(name, snap)
	if err != nil {
		return
	}
	var buf bytes.Buffer
	if err := cmp.Render(context.Background(), &buf); err != nil {
		return
	}
	ev := PushEvent{DeviceName: name, HTML: buf.String()}

	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		// Drop-oldest under pressure: peek for room, otherwise discard one
		// event from the head and retry. Holds h.mu throughout, so no
		// other goroutine can close the channel mid-send.
		select {
		case sub.Events <- ev:
		default:
			select {
			case <-sub.Events:
			default:
			}
			select {
			case sub.Events <- ev:
			default:
			}
		}
	}
}
