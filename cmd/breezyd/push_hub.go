// SPDX-License-Identifier: GPL-3.0-or-later

// PushHub fans out per-device snapshot updates to subscribed SSE clients.
// Producers (the poller's OnPoll hook, action handlers' post-write paths)
// call Notify(name, snap). The hub renders a structured PushEvent — one
// signal payload (JSON for datastar-patch-signals) plus a list of block
// patches (each rendered templ component plus the selector to target) —
// then queues it onto every subscriber's bounded channel. The /ui/sse
// handler drains the channel and turns each event into one
// datastar-patch-signals event followed by N datastar-patch-elements
// events.
//
// Backpressure: subscriber channels are bounded (16). When a subscriber
// is too slow to drain, the oldest event is discarded — pushed events
// are full-card snapshots, so the latest supersedes prior ones and a
// dropped event is never user-visible.
package main

import (
	"sync"
)

// pushHubBufferSize is the per-subscriber event channel capacity.
// Sized to absorb a brief stall (e.g., a paused tab catching up) without
// dropping events from a steady-state poll cadence.
const pushHubBufferSize = 16

// PushEvent is one fan-out unit: one device's signal payload plus a list
// of block patches. The SSE handler emits one signals event followed by
// one elements event per block.
type PushEvent struct {
	DeviceName  string
	SignalsJSON []byte
	Blocks      []BlockPatch
}

// BlockPatch is one (selector, html) pair: a single
// datastar-patch-elements event with mode=outer.
type BlockPatch struct {
	Selector string
	HTML     string
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
	renderBlocks func(name string, snap Snapshot) (*PushEvent, error)

	mu     sync.Mutex
	subs   map[*Subscriber]struct{}
	closed map[*Subscriber]struct{}
}

// NewPushHub constructs an empty hub. renderBlocks produces the
// structured per-device event payload — signals plus block patches —
// for each Notify; injection lets tests swap in a stub builder.
func NewPushHub(renderBlocks func(name string, snap Snapshot) (*PushEvent, error)) *PushHub {
	return &PushHub{
		renderBlocks: renderBlocks,
		subs:         make(map[*Subscriber]struct{}),
		closed:       make(map[*Subscriber]struct{}),
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

// Notify renders the event payload for (name, snap) and enqueues the
// resulting event on every subscriber. Render errors are silently
// dropped — the next successful poll re-renders, and noisy logging
// from the poller hot path costs more than it saves.
func (h *PushHub) Notify(name string, snap Snapshot) {
	ev, err := h.renderBlocks(name, snap)
	if err != nil || ev == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		select {
		case sub.Events <- *ev:
		default:
			select {
			case <-sub.Events:
			default:
			}
			select {
			case sub.Events <- *ev:
			default:
			}
		}
	}
}
