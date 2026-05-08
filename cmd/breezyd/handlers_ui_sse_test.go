// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
)

// newSSETestHandler builds a Handler with one device's snapshot already
// populated, then attaches a PushHub whose render closure produces a
// minimal templ stub keyed on device name. Tests that need richer cards
// can swap in a different render closure on the returned hub.
func newSSETestHandler(t *testing.T, names ...string) *Handler {
	t.Helper()
	h := newUITestHandler(t, names...)
	h.PushHub = NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
		return stubComponent(`<div class="card" data-device="` + name + `"></div>`), nil
	})
	return h
}

// readUntil drains body until needle appears or deadline elapses, then
// returns the accumulated text. The caller is responsible for closing
// body. Designed for SSE bodies where the connection stays open after
// the relevant event arrives.
func readUntil(t *testing.T, body io.Reader, needle string, deadline time.Duration) string {
	t.Helper()
	br := bufio.NewReader(body)
	var sb strings.Builder
	end := time.Now().Add(deadline)
	chunk := make([]byte, 1024)
	for time.Now().Before(end) {
		n, err := br.Read(chunk)
		if n > 0 {
			sb.Write(chunk[:n])
			if strings.Contains(sb.String(), needle) {
				return sb.String()
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return sb.String()
			}
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

	// Wait until the handler has subscribed before triggering Notify —
	// initial-state writes complete before Subscribe(), so a `Notify` fired
	// the instant `bravo` appears could miss the subscribe register.
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

	// Wait for the handler to subscribe — initial-state writes happen before
	// Subscribe(), so observing the alpha card guarantees Subscribe ran too.
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
