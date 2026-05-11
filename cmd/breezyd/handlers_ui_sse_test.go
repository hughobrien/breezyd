// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

// newSSETestHandler builds a Handler with one device's snapshot already
// populated, then attaches a PushHub whose renderBlocks closure produces a
// minimal PushEvent stub keyed on device name. Tests that need richer events
// can swap in a different renderBlocks closure on the returned hub.
func newSSETestHandler(t *testing.T, names ...string) *Handler {
	t.Helper()
	h := newUITestHandler(t, names...)
	h.PushHub = NewPushHub(func(name string, _ Snapshot) (*PushEvent, error) {
		return &PushEvent{
			DeviceName:  name,
			SignalsJSON: []byte(`{"stale":false,"speedMode":"manual","airflowMode":"ventilation","lastPollAge":"","sensorsAlert":false}`),
			Blocks: []BlockPatch{
				{Selector: `.card[data-device="` + name + `"]`, HTML: `<div class="card" data-device="` + name + `"></div>`},
			},
		}, nil
	})
	return h
}

// readUntil drains body until needle appears or deadline elapses, then
// returns the accumulated text. The caller is responsible for closing
// body. Designed for SSE bodies where the connection stays open after
// the relevant event arrives.
//
// Reads body directly without a bufio.Reader: a buffered reader created
// per call would discard its read-ahead on return, so consecutive
// readUntil calls against the same body would lose any bytes the prior
// call's buffer had pulled past the needle.
func readUntil(t *testing.T, body io.Reader, needle string, deadline time.Duration) string {
	t.Helper()
	var sb strings.Builder
	end := time.Now().Add(deadline)
	chunk := make([]byte, 1024)
	for time.Now().Before(end) {
		n, err := body.Read(chunk)
		if n > 0 {
			sb.Write(chunk[:n])
			if strings.Contains(sb.String(), needle) {
				return sb.String()
			}
		}
		if err != nil {
			return sb.String()
		}
	}
	return sb.String()
}

func TestGetUISSE_InitialStateAndPush(t *testing.T) {
	is := is.New(t)
	h := newSSETestHandler(t, "alpha", "bravo")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	is.Equal(resp.StatusCode, http.StatusOK)
	is.True(strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"))

	body := readUntil(t, resp.Body, `data-device="bravo"`, 2*time.Second)
	is.True(strings.Contains(body, "event: datastar-patch-elements")) // initial state emits patch-elements
	is.True(strings.Contains(body, `data-device="alpha"`))            // initial state includes alpha card
	is.True(strings.Contains(body, `data-device="bravo"`))            // initial state includes bravo card

	// Subscribe runs before any initial-state writes (post-#75), so by the
	// time we've read the bravo card the subscriber is registered. The
	// waitFor is left in place as a defence against the in-process
	// scheduler reordering — it will resolve essentially immediately.
	hub := h.PushHub.(*PushHub)
	is.NoErr(waitFor(1*time.Second, func() bool { return subscriberCount(hub) == 1 }))

	priorAlpha := strings.Count(body, `data-device="alpha"`)
	// Use a sentinel device name the initial state didn't emit, so any
	// further `data-device="charlie"` proves the push went through.
	h.PushHub.Notify("charlie", Snapshot{})

	body2 := readUntil(t, resp.Body, `data-device="charlie"`, 1*time.Second)
	combined := body + body2
	is.True(strings.Contains(combined, `data-device="charlie"`))         // push event arrived after Notify
	is.Equal(strings.Count(combined, `data-device="alpha"`), priorAlpha) // alpha cards must not drift
}

// waitFor polls cond until it returns true or deadline elapses. Used to
// bridge the gap between an HTTP request landing and an asynchronous
// goroutine reaching its observable steady state.
func waitFor(deadline time.Duration, cond func() bool) error {
	end := time.Now().Add(deadline)
	for time.Now().Before(end) {
		if cond() {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return errors.New("timeout")
}

func TestGetUISSE_ContextCancelCleansUpSubscriber(t *testing.T) {
	is := is.New(t)
	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()
	hub := h.PushHub.(*PushHub)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)

	// Wait for the handler to subscribe. Post-#75 Subscribe runs before
	// initial-state, so observing the alpha card guarantees Subscribe ran too.
	_ = readUntil(t, resp.Body, `data-device="alpha"`, 1*time.Second)
	// Give the handler one more scheduler tick to reach the subscribe loop.
	time.Sleep(50 * time.Millisecond)
	is.Equal(subscriberCount(hub), 1) // exactly one subscriber after connect

	cancel()
	_ = resp.Body.Close()

	// The handler exits and Unsubscribe runs. Poll briefly because the
	// goroutine teardown is asynchronous from the cancel.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if subscriberCount(hub) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	is.Equal(subscriberCount(hub), 0) // subscriber must drop after cancel
}

func TestGetUISSE_KeepaliveOnIdleConnection(t *testing.T) {
	is := is.New(t)
	orig := keepaliveInterval
	keepaliveInterval = 50 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = orig })

	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, ": keepalive", 1*time.Second)
	is.True(strings.Contains(body, ": keepalive")) // keepalive comment must arrive on idle stream
}

func TestGetUISSE_XAccelBufferingHeader(t *testing.T) {
	is := is.New(t)
	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	is.Equal(resp.Header.Get("X-Accel-Buffering"), "no")
}

func TestGetUISSE_NoPushHub_500(t *testing.T) {
	is := is.New(t)
	h := newUITestHandler(t, "alpha")
	// PushHub left nil to force the guard.
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/sse")
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()
	is.Equal(resp.StatusCode, http.StatusInternalServerError)
}

// subscriberCount peeks the unexported subs map for tests. This is the
// simplest way to assert clean unsubscribe without exposing internals on
// the production type.
func subscriberCount(h *PushHub) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

func TestGetUISSE_ColdLoadUsesAppendMode(t *testing.T) {
	is := is.New(t)
	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	// No Last-Event-ID — cold load path.
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, `data-device="alpha"`, 2*time.Second)
	is.True(strings.Contains(body, `selector #device-list`)) // cold load must target #device-list
	is.True(strings.Contains(body, `mode append`))           // cold load must use append mode
	is.True(strings.Contains(body, `id: device:alpha`))      // initial-card event id must use device:<name> format
}

func TestGetUISSE_ReconnectUsesOuterMode(t *testing.T) {
	is := is.New(t)
	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	req.Header.Set("Last-Event-ID", "device:alpha")
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, `data-device="alpha"`, 2*time.Second)
	is.True(strings.Contains(body, `selector .card[data-device=`)) // reconnect must target .card with outer mode
	is.True(!strings.Contains(body, `selector #device-list`))      // reconnect must not append against #device-list
	is.True(strings.Contains(body, `id: device:alpha`))            // reconnect emits the same device:<name> id format as cold load
}

// TestGetUISSE_NotifyDuringInitialStateLands_Regression75 pins the #75
// fix: a Notify fired in the gap between Subscribe and the start of the
// steady-state drain (i.e. while the initial-state pass is running) must
// not be lost. The pre-fix handler subscribed AFTER the initial-state
// pass, so any Notify in that window would arrive before there was a
// channel to receive it.
//
// uiSSEAfterSubscribe is a test hook that runs immediately after the
// handler subscribes, before the first initial-state write. It deterministically
// drives a Notify into the exact window the bug describes.
func TestGetUISSE_NotifyDuringInitialStateLands_Regression75(t *testing.T) {
	is := is.New(t)
	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	uiSSEAfterSubscribe = func() {
		h.PushHub.Notify("charlie", Snapshot{})
	}
	t.Cleanup(func() { uiSSEAfterSubscribe = nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, `data-device="charlie"`, 2*time.Second)
	is.True(strings.Contains(body, `data-device="charlie"`)) // #75 regression: Notify in Subscribe→initial-state window must land
}

// newSSETestHandlerUnreachable builds a Handler whose registered device
// has NO Snapshot in the cache — h.collectViews() will return it with
// Unreachable=true so the initial-state pass emits the placeholder
// card. PushHub is wired with a passthrough renderer; not exercised
// since these tests don't fire a Notify.
func newSSETestHandlerUnreachable(t *testing.T, name string) *Handler {
	t.Helper()
	devices := map[string]DeviceConfig{
		name: {ID: "BREEZY0000000000", Password: "1111", IP: "10.0.0.1:4000"},
	}
	h := newTestHandler(t, devices,
		withPollers(map[string]*Poller{}),
		withSchedulers(map[string]*Scheduler{}),
	)
	h.PushHub = NewPushHub(func(string, Snapshot) (*PushEvent, error) { return &PushEvent{}, nil })
	return h
}

// TestGetUISSE_ColdLoadUnreachable pins that the cold-load initial-state
// pass emits an unreachable placeholder card via mode=append against
// #device-list when the device has no Snapshot yet. A regression that
// short-circuits emitInitialCard on Unreachable=true would silently
// hide misconfigured devices from the dashboard.
func TestGetUISSE_ColdLoadUnreachable(t *testing.T) {
	is := is.New(t)
	h := newSSETestHandlerUnreachable(t, "ghost")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, `class="card unreachable"`, 2*time.Second)
	is.True(strings.Contains(body, `class="card unreachable"`)) // unreachable placeholder must render
	is.True(strings.Contains(body, `data-device="ghost"`))      // scoped to the configured device
	is.True(strings.Contains(body, `selector #device-list`))    // cold load uses append against #device-list
	is.True(strings.Contains(body, `mode append`))
	is.True(strings.Contains(body, `id: device:ghost`))
}

// TestGetUISSE_ReconnectUnreachable pins that on reconnect (Last-Event-ID
// set) the unreachable placeholder is patched in via mode=outer against
// the existing card, not appended. Without this, the dashboard would
// duplicate the placeholder card on every datastar auto-retry.
func TestGetUISSE_ReconnectUnreachable(t *testing.T) {
	is := is.New(t)
	h := newSSETestHandlerUnreachable(t, "ghost")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	req.Header.Set("Last-Event-ID", "device:ghost")
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, `class="card unreachable"`, 2*time.Second)
	is.True(strings.Contains(body, `class="card unreachable"`))    // unreachable card still renders on reconnect
	is.True(strings.Contains(body, `selector .card[data-device=`)) // reconnect uses outer mode against .card
	is.True(!strings.Contains(body, `selector #device-list`))      // reconnect must not append
}

func TestGetUISSE_PushEventEmitsSignalsAndBlocks(t *testing.T) {
	is := is.New(t)
	orig := keepaliveInterval
	keepaliveInterval = 50 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = orig })

	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	is.NoErr(err)
	defer func() { _ = resp.Body.Close() }()

	// Drain initial state.
	_ = readUntil(t, resp.Body, `data-device="alpha"`, 1*time.Second)

	// Wait for subscriber registration.
	hub := h.PushHub.(*PushHub)
	is.NoErr(waitFor(1*time.Second, func() bool { return subscriberCount(hub) == 1 }))

	h.PushHub.Notify("alpha", Snapshot{})

	// Read until we've seen both event types. readUntil consumes its
	// terminator so reading both events back-to-back lets us index into
	// the combined payload.
	first := readUntil(t, resp.Body, "datastar-patch-signals", 1*time.Second)
	second := readUntil(t, resp.Body, "datastar-patch-elements", 1*time.Second)
	combined := first + second

	is.True(strings.Contains(combined, "event: datastar-patch-signals"))
	is.True(strings.Contains(combined, "event: datastar-patch-elements"))

	// Signals must arrive BEFORE elements: card-outer reactive bindings
	// (`data-class:stale="$stale"` etc.) need the new signal values
	// before any block content patches in, otherwise the freshly-rendered
	// block briefly mounts under the stale outer state and visually
	// flickers.
	signalsIdx := strings.Index(combined, "event: datastar-patch-signals")
	elementsIdx := strings.Index(combined, "event: datastar-patch-elements")
	is.True(signalsIdx >= 0 && elementsIdx >= 0)
	is.True(signalsIdx < elementsIdx) // signals event must precede elements event

	// Event IDs use the spec'd "<scope>:<deviceName>" format so reconnect
	// (Last-Event-ID) and any future per-device replay can address them.
	is.True(strings.Contains(combined, "id: signals:alpha")) // signals event id format
	is.True(strings.Contains(combined, "id: block:alpha"))   // block patch event id format
}
