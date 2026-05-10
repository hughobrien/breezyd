// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// handlerOpt configures a *Handler built by newTestHandler. Tests apply
// these in order via newTestHandler's variadic args.
type handlerOpt func(*Handler)

// newTestHandler builds a *Handler with sensible defaults for unit tests.
// The default ClientFactory returns an error — tests that exercise device
// I/O must pass withClient(...) to inject a stub or fakedevice client.
func newTestHandler(t *testing.T, devices map[string]DeviceConfig, opts ...handlerOpt) *Handler {
	t.Helper()
	h := &Handler{
		Devices:      NewDeviceRegistry(devices),
		State:        NewState(),
		PollInterval: 30 * time.Second,
		ClientFactory: func(name string) (HandlerClient, error) {
			return nil, fmt.Errorf("newTestHandler: no client for %q (pass withClient)", name)
		},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// newTestState builds a *State seeded with the given per-device snapshots.
func newTestState(t *testing.T, snaps map[string]Snapshot) *State {
	t.Helper()
	s := NewState()
	for name, snap := range snaps {
		s.Set(name, snap)
	}
	return s
}

// Option helpers.

func withState(s *State) handlerOpt { return func(h *Handler) { h.State = s } }

func withClient(f func(string) (HandlerClient, error)) handlerOpt {
	return func(h *Handler) { h.ClientFactory = f }
}

func withPushHub(p PushNotifier) handlerOpt { return func(h *Handler) { h.PushHub = p } }

func withPollers(p map[string]*Poller) handlerOpt { return func(h *Handler) { h.Pollers = p } }

func withSchedulers(s map[string]*Scheduler) handlerOpt { return func(h *Handler) { h.Schedulers = s } }

func withNoticeFunc(f func(string, breezy.ParamID)) handlerOpt {
	return func(h *Handler) { h.NoticeFunc = f }
}

func withPollInterval(d time.Duration) handlerOpt {
	return func(h *Handler) { h.PollInterval = d }
}
