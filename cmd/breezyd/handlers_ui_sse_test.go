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
	h := newSSETestHandler(t, "alpha", "bravo")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type: %q, want text/event-stream prefix", got)
	}

	body := readUntil(t, resp.Body, `data-device="bravo"`, 2*time.Second)
	if !strings.Contains(body, "event: datastar-patch-elements") {
		t.Errorf("initial state: missing patch-elements event; body=%q", body)
	}
	if !strings.Contains(body, `data-device="alpha"`) {
		t.Errorf("initial state: missing alpha card; body=%q", body)
	}
	if !strings.Contains(body, `data-device="bravo"`) {
		t.Errorf("initial state: missing bravo card; body=%q", body)
	}

	// Subscribe runs before any initial-state writes (post-#75), so by the
	// time we've read the bravo card the subscriber is registered. The
	// waitFor is left in place as a defence against the in-process
	// scheduler reordering — it will resolve essentially immediately.
	hub := h.PushHub.(*PushHub)
	if err := waitFor(1*time.Second, func() bool { return subscriberCount(hub) == 1 }); err != nil {
		t.Fatalf("subscribe never registered: %v", err)
	}

	priorAlpha := strings.Count(body, `data-device="alpha"`)
	// Use a sentinel device name the initial state didn't emit, so any
	// further `data-device="charlie"` proves the push went through.
	h.PushHub.Notify("charlie", Snapshot{})

	body2 := readUntil(t, resp.Body, `data-device="charlie"`, 1*time.Second)
	combined := body + body2
	if !strings.Contains(combined, `data-device="charlie"`) {
		t.Errorf("expected charlie push event; combined=%q", combined)
	}
	if got := strings.Count(combined, `data-device="alpha"`); got != priorAlpha {
		t.Errorf("alpha count drifted unexpectedly: before=%d after=%d", priorAlpha, got)
	}
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
	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()
	hub := h.PushHub.(*PushHub)

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/sse: %v", err)
	}

	// Wait for the handler to subscribe. Post-#75 Subscribe runs before
	// initial-state, so observing the alpha card guarantees Subscribe ran too.
	_ = readUntil(t, resp.Body, `data-device="alpha"`, 1*time.Second)
	// Give the handler one more scheduler tick to reach the subscribe loop.
	time.Sleep(50 * time.Millisecond)
	if got := subscriberCount(hub); got != 1 {
		t.Fatalf("subscriber count after connect: got %d, want 1", got)
	}

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
	t.Errorf("subscriber count after cancel: got %d, want 0", subscriberCount(hub))
}

func TestGetUISSE_KeepaliveOnIdleConnection(t *testing.T) {
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
	if err != nil {
		t.Fatalf("GET /ui/sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, ": keepalive", 1*time.Second)
	if !strings.Contains(body, ": keepalive") {
		t.Errorf("expected keepalive comment within 1s; got body=%q", body)
	}
}

func TestGetUISSE_XAccelBufferingHeader(t *testing.T) {
	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering: %q, want %q", got, "no")
	}
}

func TestGetUISSE_NoPushHub_500(t *testing.T) {
	h := newUITestHandler(t, "alpha")
	// PushHub left nil to force the guard.
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/sse")
	if err != nil {
		t.Fatalf("GET /ui/sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: %d, want 500", resp.StatusCode)
	}
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
	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	// No Last-Event-ID — cold load path.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, `data-device="alpha"`, 2*time.Second)
	if !strings.Contains(body, `selector #device-list`) {
		t.Errorf("cold load: expected mode=append against #device-list; body=%q", body)
	}
	if !strings.Contains(body, `mode append`) {
		t.Errorf("cold load: expected `mode append`; body=%q", body)
	}
}

func TestGetUISSE_ReconnectUsesOuterMode(t *testing.T) {
	h := newSSETestHandler(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/ui/sse", nil)
	req.Header.Set("Last-Event-ID", "device:alpha")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /ui/sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, `data-device="alpha"`, 2*time.Second)
	if !strings.Contains(body, `selector .card[data-device=`) {
		t.Errorf("reconnect: expected mode=outer with .card selector; body=%q", body)
	}
	if strings.Contains(body, `selector #device-list`) {
		t.Errorf("reconnect: should not use append against #device-list; body=%q", body)
	}
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
	if err != nil {
		t.Fatalf("GET /ui/sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body := readUntil(t, resp.Body, `data-device="charlie"`, 2*time.Second)
	if !strings.Contains(body, `data-device="charlie"`) {
		t.Errorf("Notify fired between Subscribe and initial-state was lost: %q", body)
	}
}

func TestGetUISSE_PushEventEmitsSignalsAndBlocks(t *testing.T) {
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
	if err != nil {
		t.Fatalf("GET /ui/sse: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Drain initial state.
	_ = readUntil(t, resp.Body, `data-device="alpha"`, 1*time.Second)

	// Wait for subscriber registration.
	hub := h.PushHub.(*PushHub)
	if err := waitFor(1*time.Second, func() bool { return subscriberCount(hub) == 1 }); err != nil {
		t.Fatalf("subscribe never registered: %v", err)
	}

	h.PushHub.Notify("alpha", Snapshot{})

	body := readUntil(t, resp.Body, "datastar-patch-signals", 1*time.Second)
	if !strings.Contains(body, "event: datastar-patch-signals") {
		t.Errorf("push: expected datastar-patch-signals event; body=%q", body)
	}

	body2 := readUntil(t, resp.Body, "datastar-patch-elements", 1*time.Second)
	combined := body + body2
	if !strings.Contains(combined, "event: datastar-patch-elements") {
		t.Errorf("push: expected datastar-patch-elements event; combined=%q", combined)
	}
}
