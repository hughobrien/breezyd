// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
)

// stubComponent renders a fixed string; used as a render-closure stand-in
// so PushHub tests don't drag in the full templ machinery.
func stubComponent(s string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := w.Write([]byte(s))
		return err
	})
}

// newTestHub returns a PushHub whose render closure produces a stable
// per-device marker so tests can assert which device a fan-out targets.
func newTestHub(t *testing.T) *PushHub {
	t.Helper()
	return NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
		return stubComponent(`<div data-device="` + name + `"></div>`), nil
	})
}

func TestPushHub_SubscribeUnsubscribeRoundTrip(t *testing.T) {
	hub := newTestHub(t)
	sub := hub.Subscribe()
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	hub.Unsubscribe(sub)

	select {
	case _, ok := <-sub.Events:
		if ok {
			t.Error("expected closed channel after Unsubscribe")
		}
	case <-time.After(50 * time.Millisecond):
		t.Error("Unsubscribe did not close events channel")
	}
}

func TestPushHub_NotifyFansOut(t *testing.T) {
	hub := newTestHub(t)
	subs := make([]*Subscriber, 3)
	for i := range subs {
		subs[i] = hub.Subscribe()
	}

	hub.Notify("bedroom", Snapshot{})

	for i, sub := range subs {
		select {
		case ev := <-sub.Events:
			if ev.DeviceName != "bedroom" {
				t.Errorf("subscriber %d: device %q, want %q", i, ev.DeviceName, "bedroom")
			}
			if !strings.Contains(ev.HTML, `data-device="bedroom"`) {
				t.Errorf("subscriber %d: HTML missing device marker: %q", i, ev.HTML)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("subscriber %d: did not receive event", i)
		}
	}
}

func TestPushHub_DropsOldestOnFullBuffer(t *testing.T) {
	hub := newTestHub(t)
	sub := hub.Subscribe()
	for i := 0; i < pushHubBufferSize+4; i++ {
		hub.Notify("bedroom", Snapshot{})
	}
	count := 0
	for {
		select {
		case <-sub.Events:
			count++
		case <-time.After(50 * time.Millisecond):
			if count != pushHubBufferSize {
				t.Errorf("got %d events, want %d (buffer size)", count, pushHubBufferSize)
			}
			return
		}
	}
}

func TestPushHub_ConcurrentNotifyAndUnsubscribe(t *testing.T) {
	hub := newTestHub(t)
	const n = 50
	subs := make([]*Subscriber, n)
	for i := range subs {
		subs[i] = hub.Subscribe()
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			hub.Notify("bedroom", Snapshot{})
		}
	}()

	go func() {
		defer wg.Done()
		for _, sub := range subs {
			hub.Unsubscribe(sub)
		}
	}()

	wg.Wait()
	// Surviving without panicking on closed-channel sends is the assertion.
}

func TestPushHub_RenderErrorIsTolerated(t *testing.T) {
	var renderErrCount atomic.Int32
	hub := NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
		if name == "broken" {
			renderErrCount.Add(1)
			return nil, errors.New("render failed")
		}
		return stubComponent(`<div data-device="` + name + `"></div>`), nil
	})
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	hub.Notify("broken", Snapshot{})
	hub.Notify("bedroom", Snapshot{})

	select {
	case ev := <-sub.Events:
		if ev.DeviceName != "bedroom" {
			t.Errorf("got %q, want bedroom (broken render should be skipped)", ev.DeviceName)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected one successful event after the render error")
	}

	if got := renderErrCount.Load(); got != 1 {
		t.Errorf("renderErrCount: got %d, want 1", got)
	}
}

func TestPushHub_UnsubscribeIsIdempotent(t *testing.T) {
	hub := newTestHub(t)
	sub := hub.Subscribe()
	hub.Unsubscribe(sub)
	// Second call must not panic on already-closed channel.
	hub.Unsubscribe(sub)
}
