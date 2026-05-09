// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matryer/is"
)

// newTestHub returns a PushHub whose renderBlocks closure produces a stable
// per-device marker so tests can assert which device a fan-out targets.
func newTestHub(t *testing.T) *PushHub {
	t.Helper()
	return NewPushHub(func(name string, _ Snapshot) (*PushEvent, error) {
		return &PushEvent{
			DeviceName:  name,
			SignalsJSON: []byte(`{"stale":false}`),
			Blocks: []BlockPatch{
				{Selector: `#stub-` + name, HTML: `<div data-device="` + name + `"></div>`},
			},
		}, nil
	})
}

func TestPushHub_SubscribeUnsubscribeRoundTrip(t *testing.T) {
	is := is.New(t)
	hub := newTestHub(t)
	sub := hub.Subscribe()
	is.True(sub != nil) // Subscribe must not return nil
	hub.Unsubscribe(sub)

	select {
	case _, ok := <-sub.Events:
		is.Equal(ok, false) // events channel must be closed after Unsubscribe
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Unsubscribe did not close events channel")
	}
}

func TestPushHub_NotifyFansOut(t *testing.T) {
	is := is.New(t)
	hub := newTestHub(t)
	subs := make([]*Subscriber, 3)
	for i := range subs {
		subs[i] = hub.Subscribe()
	}

	hub.Notify("bedroom", Snapshot{})

	for _, sub := range subs {
		select {
		case ev := <-sub.Events:
			is.Equal(ev.DeviceName, "bedroom")
			is.True(len(ev.Blocks) > 0) // event must carry at least one block
			is.True(strings.Contains(ev.Blocks[0].HTML, `data-device="bedroom"`))
		case <-time.After(100 * time.Millisecond):
			t.Fatal("subscriber did not receive event")
		}
	}
}

func TestPushHub_DropsOldestOnFullBuffer(t *testing.T) {
	is := is.New(t)
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
			is.Equal(count, pushHubBufferSize) // bounded buffer drops oldest, never grows past cap
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
	is := is.New(t)
	var renderErrCount atomic.Int32
	hub := NewPushHub(func(name string, _ Snapshot) (*PushEvent, error) {
		if name == "broken" {
			renderErrCount.Add(1)
			return nil, errors.New("render failed")
		}
		return &PushEvent{
			DeviceName:  name,
			SignalsJSON: []byte(`{"stale":false}`),
			Blocks: []BlockPatch{
				{Selector: `#stub-` + name, HTML: `<div data-device="` + name + `"></div>`},
			},
		}, nil
	})
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	hub.Notify("broken", Snapshot{})
	hub.Notify("bedroom", Snapshot{})

	select {
	case ev := <-sub.Events:
		is.Equal(ev.DeviceName, "bedroom") // broken render is dropped; only successful events fan out
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected one successful event after the render error")
	}

	is.Equal(renderErrCount.Load(), int32(1)) // exactly one render error observed
}

func TestPushHub_UnsubscribeIsIdempotent(t *testing.T) {
	hub := newTestHub(t)
	sub := hub.Subscribe()
	hub.Unsubscribe(sub)
	// Second call must not panic on already-closed channel.
	hub.Unsubscribe(sub)
}
