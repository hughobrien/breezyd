# Datastar Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace htmx + the cookie-based UI-state machinery with datastar (htmx + Alpine in one library) and SSE-driven push updates from the daemon poller.

**Architecture:** New `cmd/breezyd/push_hub.go` owns subscriber set + fan-out. New `GET /ui/sse` long-lived handler subscribes a browser and streams updates. Existing poller's `OnPoll` hook gets composed (`SyncHomekit` + `PushHub.Notify`). Action handlers stop returning HTML — they call `Notify` and return 200 (or an SSE error envelope on failure). Templates carry datastar `data-*` attributes; client UI state lives in `data-signals` per card; alert state becomes a CSS class (no force-open).

**Tech Stack:** Go 1.26, templ codegen, datastar 1.x via `github.com/starfederation/datastar/sdk/go`, vendored `datastar.min.js`. Playwright for end-to-end. NixOS module update for nginx.

**Spec:** `docs/superpowers/specs/2026-05-08-datastar-migration-design.md`.

---

## File Structure

**New files:**
- `cmd/breezyd/push_hub.go` — `PushHub` type with `Notify(name, snap)`, `Subscribe()`, `Unsubscribe(s)`, `WriteCard(sse, view)`. ~80 lines.
- `cmd/breezyd/push_hub_test.go` — fan-out, drop-oldest, concurrent subscribe/unsubscribe.
- `cmd/breezyd/handlers_ui_sse.go` — `getUISSE` handler.
- `cmd/breezyd/handlers_ui_sse_test.go` — initial state, push delivery, context-cancel cleanup.
- `cmd/breezyd/ui/vendor/datastar-1.0.0-RC.11.min.js` — vendored. Pin a specific version like the existing htmx vendor.

**Modified files:**
- `cmd/breezyd/server.go` — register `GET /ui/sse`, drop `GET /ui/devices` and `GET /ui/devices/{name}/card` routes once the cutover lands.
- `cmd/breezyd/poller.go` — no signature change; the existing `OnPoll` hook is the integration point.
- `cmd/breezyd/main.go` — compose `handler.SyncHomekit + handler.PushHub.Notify` into the `onPoll` callback passed to `startPollers`. Construct `PushHub` and inject the templ-render closure.
- `cmd/breezyd/handlers_ui_write.go` — every action handler: drop HTML body on success, call `h.PushHub.Notify`, 200 OK empty. 4xx/5xx return SSE-format error envelope targeting `#global-error-banner`.
- `cmd/breezyd/handlers_ui_read.go` — drop `getUIDeviceList`, `getUIDeviceCard`, `computeDetailsOpen`, `defaultOpen`. `viewFor` simplifies (no more `uistate.Parse` threading).
- `cmd/breezyd/ui_assets.go` — vendor entry for datastar replaces htmx entries.
- `cmd/breezyd/ui/view.go` — drop `DetailsOpen`, `EditingPreset`, `Automode`, `MatchSpeeds` from `DeviceView`.
- `cmd/breezyd/ui_view.go` — no longer references the dropped fields (was not setting them anyway, but verify no stale code).
- `cmd/breezyd/ui/templates/layout.templ` — replace inline JS with single datastar `<script>`, body gets `data-signals` + `data-on-load="@get('/ui/sse')"`, theme-picker IIFE stays.
- `cmd/breezyd/ui/templates/device_card.templ` — `data-signals` seed via `initialCardSignals(v)`, `data-bind:open` on each `<details>`, `templ.KV("alert", v.NeedsAttention)` on the device-info details.
- `cmd/breezyd/ui/templates/sensors_block.templ`, `energy_block.templ`, `schedule_block.templ` — `data-bind:open` reads card-level signal, `templ.KV("alert", ...)` for headings.
- `cmd/breezyd/ui/templates/controls_block.templ` — `data-on-click="$editor = …; @post(…)"` replaces `hx-post`/`hx-vals`. `data-show="$editor.device === '…' && $editor.preset === N"` replaces conditional `hidden` on preset panels. Sliders use `data-on-change` with inline expressions.
- `cmd/breezyd/ui/style.css` — `.block.alert > summary > h3` color rule + `⚠` pseudo-element. Drop `.preset-editor[hidden]` (datastar's `data-show` handles visibility).
- `cmd/breezyd/ui/templates/render_test.go` + `testdata/golden_*.html` — regenerated for datastar attribute syntax. Editor-state-variant golden goes away.
- `tests/ui/dashboard.spec.ts` — drop cookie-based tests, replace 5s-poll waits with SSE-driven waits, add reconnect smoke + cross-tab synchronization.
- `tests/ui/screenshot.ts` — adapt initial-paint wait.
- `nix/module.nix` — auto-configured nginx location adds `proxy_buffering off`, `proxy_cache off`, `proxy_http_version 1.1`, `proxy_set_header Connection ""`, `proxy_read_timeout 1d`.
- `flake.nix` — `vendorHash` recomputes (project rule already covers this).
- `go.mod` / `go.sum` — `github.com/starfederation/datastar/sdk/go` added.
- `CLAUDE.md` and `CHANGELOG.md` — describe the architecture change.

**Deleted files:**
- `internal/uistate/state.go`
- `internal/uistate/state_test.go`
- `cmd/breezyd/ui/vendor/htmx-2.0.4.min.js`
- `cmd/breezyd/ui/vendor/htmx-response-targets-2.0.4.min.js`

---

## Task 1: Vendor datastar SDK + JS

**Goal:** Add the Go SDK to `go.mod`, vendor `datastar.min.js`, register the vendor entry in `ui_assets.go`. Build still uses htmx for the dashboard — datastar is loaded but unused. This is a non-functional preparatory change.

**Files:**
- Modify: `go.mod`, `go.sum`
- Create: `cmd/breezyd/ui/vendor/datastar-1.0.0-RC.11.min.js`
- Modify: `cmd/breezyd/ui_assets.go`
- Modify: `flake.nix` (`vendorHash`)

**Acceptance Criteria:**
- [ ] `go build ./...` clean.
- [ ] `cmd/breezyd/ui_assets.go` exposes `datastarVersion` and `datastarHash` constants alongside the existing `htmxVersion` ones.
- [ ] `GET /ui/vendor/datastar-<version>.min.js` returns the vendored file with `Cache-Control: public, max-age=31536000, immutable`.
- [ ] `flake.nix`'s `vendorHash` is updated; `nix-check` passes.

**Verify:** `just check && curl -sI http://localhost:8080/ui/vendor/datastar-1.0.0-RC.11.min.js` (against a running daemon) → 200 OK with the immutable cache header.

**Steps:**

- [ ] **Step 1: Add the SDK dependency**

```bash
go get github.com/starfederation/datastar/sdk/go@v1.0.0-RC.11
go mod tidy
```

- [ ] **Step 2: Download the vendored JS**

The matching client file is published in the same release. Download the minified browser bundle:

```bash
mkdir -p cmd/breezyd/ui/vendor
curl -fL -o cmd/breezyd/ui/vendor/datastar-1.0.0-RC.11.min.js \
  https://github.com/starfederation/datastar/releases/download/v1.0.0-RC.11/datastar.min.js
```

If the release URL has a different shape, check the datastar GitHub releases page and adjust. The downloaded file should be under 25 KB. Verify the SHA256 and pin it in a comment at the top of `ui_assets.go` so future updates are deliberate.

- [ ] **Step 3: Register the vendor file**

Read `cmd/breezyd/ui_assets.go` to see the existing pattern (htmx is registered there). Add a parallel entry. The shape should resemble:

```go
const (
	htmxVersion = "2.0.4"

	datastarVersion = "1.0.0-RC.11"
)

//go:embed vendor/datastar-1.0.0-RC.11.min.js
var datastarJS []byte
```

The file-serving handler reads the path parameter and matches against the embedded blobs. Confirm the existing `getVendor` handler already routes by filename and add `datastar-...min.js` to the match set if needed.

- [ ] **Step 4: Update `flake.nix`**

The Go module hash needs recomputation after `go.sum` changes. Build the package; Nix prints the expected hash:

```bash
nix build 2>&1 | grep -A1 'specified:\|got:'
```

Replace the `vendorHash` value in `flake.nix` with the printed `got:` hash.

- [ ] **Step 5: Verify**

```bash
just check
just nix-check
```

Both clean.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum cmd/breezyd/ui/vendor/datastar-1.0.0-RC.11.min.js \
        cmd/breezyd/ui_assets.go flake.nix
git commit -m "ui: vendor datastar SDK + minified JS"
```

---

## Task 2: PushHub

**Goal:** New `PushHub` type managing subscriber fan-out. Renders templ components and queues `datastar-merge-fragments` events onto each subscriber's buffered channel. Drops oldest event when a subscriber is slow. Pure type, not yet wired anywhere.

**Files:**
- Create: `cmd/breezyd/push_hub.go`
- Create: `cmd/breezyd/push_hub_test.go`

**Acceptance Criteria:**
- [ ] `NewPushHub(render func(name string, snap Snapshot) (templ.Component, error)) *PushHub` constructor.
- [ ] `Subscribe() *Subscriber` returns a subscriber with a buffered events channel (size 16).
- [ ] `Unsubscribe(s *Subscriber)` removes the subscriber and closes its events channel.
- [ ] `Notify(name string, snap Snapshot)` renders the card via the injected closure, builds an event, and fans it out. If a subscriber's channel is full, drop the oldest event and enqueue the new one.
- [ ] `WriteCard(ctx, sse, view)` is a public helper used by the SSE handler to render a card directly to a stream during initial state.
- [ ] Concurrent `Notify` + `Unsubscribe` is safe (no panic on closed channel send).

**Verify:** `go test ./cmd/breezyd/ -run TestPushHub -v` → all pass.

**Steps:**

- [ ] **Step 1: Write the failing tests**

Create `cmd/breezyd/push_hub_test.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
)

// stubComponent renders a fixed string; used as a render-closure stand-in.
func stubComponent(s string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := w.Write([]byte(s))
		return err
	})
}

func newTestHub(t *testing.T) *PushHub {
	t.Helper()
	return NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
		return stubComponent("<div data-device=\"" + name + "\"></div>"), nil
	})
}

func TestPushHub_SubscribeUnsubscribeRoundTrip(t *testing.T) {
	hub := newTestHub(t)
	sub := hub.Subscribe()
	if sub == nil {
		t.Fatal("Subscribe returned nil")
	}
	hub.Unsubscribe(sub)
	// After Unsubscribe, the channel should be closed.
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
			if !bytes.Contains(ev, []byte(`data-device="bedroom"`)) {
				t.Errorf("subscriber %d: event missing device marker: %s", i, ev)
			}
		case <-time.After(100 * time.Millisecond):
			t.Errorf("subscriber %d: did not receive event", i)
		}
	}
}

func TestPushHub_DropsOldestOnFullBuffer(t *testing.T) {
	hub := newTestHub(t)
	sub := hub.Subscribe()
	// Buffer size is 16; emit 20 notifies without draining.
	for i := 0; i < 20; i++ {
		hub.Notify("bedroom", Snapshot{})
	}
	// Drain: should get exactly 16 events (oldest 4 dropped).
	count := 0
	for {
		select {
		case <-sub.Events:
			count++
		case <-time.After(50 * time.Millisecond):
			if count != 16 {
				t.Errorf("got %d events, want 16 (buffer size)", count)
			}
			return
		}
	}
}

func TestPushHub_ConcurrentNotifyAndUnsubscribe(t *testing.T) {
	hub := newTestHub(t)
	subs := make([]*Subscriber, 50)
	for i := range subs {
		subs[i] = hub.Subscribe()
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Goroutine 1: hammer Notify.
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			hub.Notify("bedroom", Snapshot{})
		}
	}()

	// Goroutine 2: unsubscribe everyone.
	go func() {
		defer wg.Done()
		for _, sub := range subs {
			hub.Unsubscribe(sub)
		}
	}()

	wg.Wait()
	// If we got here without panicking on closed-channel sends, the test passes.
}

func TestPushHub_RenderErrorDoesNotPoisonHub(t *testing.T) {
	var renderErrCount atomic.Int32
	hub := NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
		if name == "broken" {
			renderErrCount.Add(1)
			return nil, errors.New("render failed")
		}
		return stubComponent("<div data-device=\"" + name + "\"></div>"), nil
	})
	sub := hub.Subscribe()
	defer hub.Unsubscribe(sub)

	hub.Notify("broken", Snapshot{}) // produces no event
	hub.Notify("bedroom", Snapshot{})

	select {
	case ev := <-sub.Events:
		if !bytes.Contains(ev, []byte(`data-device="bedroom"`)) {
			t.Errorf("expected bedroom event, got %s", ev)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("expected one successful event after the render error")
	}

	if renderErrCount.Load() != 1 {
		t.Errorf("renderErrCount: got %d, want 1", renderErrCount.Load())
	}
}
```

- [ ] **Step 2: Run tests, verify they fail to compile**

```bash
go test ./cmd/breezyd/ -run TestPushHub -v
```

Expected: compile error — `PushHub`, `Subscriber`, `NewPushHub` undefined.

- [ ] **Step 3: Implement `cmd/breezyd/push_hub.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// PushHub fans out per-device snapshot updates to subscribed SSE clients.
// Producers (the poller's OnPoll hook, action handlers' post-write paths)
// call Notify(name, snap). The hub renders the templ DeviceCard via the
// injected render closure and queues a datastar-merge-fragments event
// onto every subscriber's buffered channel. The /ui/sse handler reads
// from one such channel and writes events to its long-lived response.
//
// Backpressure: subscriber channels are bounded (16). When a subscriber
// is too slow to drain, the oldest event is discarded — pushed events
// are full-state snapshots, so the latest supersedes prior ones and a
// dropped event is never user-visible.
package main

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/a-h/templ"
)

// pushHubBufferSize is the per-subscriber event channel capacity.
// Sized to absorb a brief stall (e.g., a paused tab catching up) without
// dropping events from a steady-state poll cadence.
const pushHubBufferSize = 16

// Subscriber holds a single SSE client's connection state inside the hub.
// Events is the rendered SSE event body for each merge.
type Subscriber struct {
	Events chan []byte
}

// PushHub is the per-process fan-out registry.
type PushHub struct {
	render func(name string, snap Snapshot) (templ.Component, error)

	mu   sync.Mutex
	subs map[*Subscriber]struct{}
}

// NewPushHub constructs an empty hub. render is called for each Notify
// to produce the fragment to broadcast; injection allows tests to swap
// in a stub component without setting up the full templ machinery.
func NewPushHub(render func(name string, snap Snapshot) (templ.Component, error)) *PushHub {
	return &PushHub{
		render: render,
		subs:   make(map[*Subscriber]struct{}),
	}
}

// Subscribe registers a new client and returns its subscriber handle.
// The caller is responsible for draining sub.Events and calling
// Unsubscribe when done.
func (h *PushHub) Subscribe() *Subscriber {
	sub := &Subscriber{Events: make(chan []byte, pushHubBufferSize)}
	h.mu.Lock()
	h.subs[sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

// Unsubscribe removes a subscriber from the hub and closes its events
// channel. Safe to call multiple times.
func (h *PushHub) Unsubscribe(sub *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[sub]; !ok {
		return
	}
	delete(h.subs, sub)
	close(sub.Events)
}

// Notify renders the card for (name, snap) and enqueues the resulting
// SSE event onto every subscriber. A render error is logged via the
// caller's slog (we have no logger here; future audit if events go
// silent) and the notify becomes a no-op.
func (h *PushHub) Notify(name string, snap Snapshot) {
	cmp, err := h.render(name, snap)
	if err != nil {
		return
	}
	body, err := buildMergeFragmentEvent(name, cmp)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subs {
		select {
		case sub.Events <- body:
		default:
			// Buffer full: drop oldest, retry. The cap-then-retry pattern
			// keeps semantics simple — the subscriber lags by at most
			// `pushHubBufferSize` events, never more.
			select {
			case <-sub.Events:
			default:
			}
			select {
			case sub.Events <- body:
			default:
				// Subscriber drained between our peek and send. Skip; next
				// notify will succeed.
			}
		}
	}
}

// WriteCard renders one DeviceView's card and writes a
// datastar-merge-fragments event directly to w. Used by the /ui/sse
// handler's initial-state loop where the snapshot is already in hand
// and we want to bypass the subscriber channel.
func (h *PushHub) WriteCard(ctx context.Context, w fmt.Stringer /*sse*/, name string, cmp templ.Component) error {
	// Implementation in Task 3 alongside the SSE handler. Stub here so the
	// type is exported now; real body uses the datastar SDK.
	return nil
}

// buildMergeFragmentEvent renders cmp and wraps it in the SSE wire format
// for a datastar-merge-fragments event targeting `.card[data-device=NAME]`
// with mergeMode=outer. Format follows the datastar protocol — multiple
// `data:` lines for each field, blank line terminator.
func buildMergeFragmentEvent(name string, cmp templ.Component) ([]byte, error) {
	var html bytes.Buffer
	if err := cmp.Render(context.Background(), &html); err != nil {
		return nil, err
	}
	var ev bytes.Buffer
	fmt.Fprint(&ev, "event: datastar-merge-fragments\n")
	fmt.Fprintf(&ev, "data: selector .card[data-device=%q]\n", name)
	fmt.Fprint(&ev, "data: mergeMode outer\n")
	// fragments may contain newlines; each line gets its own `data:` prefix.
	for _, line := range bytes.Split(html.Bytes(), []byte("\n")) {
		fmt.Fprint(&ev, "data: fragments ")
		ev.Write(line)
		ev.WriteByte('\n')
	}
	ev.WriteByte('\n')
	return ev.Bytes(), nil
}
```

Note: `WriteCard` is a stub here — Task 3 (the SSE handler) wires the real body using the datastar SDK. We declare the method now so the interface is stable across tasks, and it's called from the handler loop.

The `Events chan []byte` carries pre-rendered event bytes. This avoids re-rendering per subscriber and keeps the hot path under one mutex.

- [ ] **Step 4: Run tests, verify they pass**

```bash
just generate  # no template changes, but harmless
go test ./cmd/breezyd/ -run TestPushHub -v
```

All five tests pass.

- [ ] **Step 5: Run the broader suite to catch unintended interactions**

```bash
just check
```

Clean.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/push_hub.go cmd/breezyd/push_hub_test.go
git commit -m "ui: add PushHub for SSE fan-out"
```

---

## Task 3: getUISSE handler + onPoll wiring

**Goal:** New `GET /ui/sse` endpoint that subscribes a browser, sends initial-state cards for every configured device, then streams updates from the `PushHub` until the client disconnects. The poller's existing `OnPoll` hook is composed (`SyncHomekit` + `PushHub.Notify`) so every successful poll fans out to subscribers. Dashboard still uses htmx — `/ui/sse` exists in parallel and isn't yet consumed.

**Files:**
- Create: `cmd/breezyd/handlers_ui_sse.go`
- Create: `cmd/breezyd/handlers_ui_sse_test.go`
- Modify: `cmd/breezyd/server.go`
- Modify: `cmd/breezyd/main.go`
- Modify: `cmd/breezyd/push_hub.go` (real `WriteCard` body using datastar SDK)

**Acceptance Criteria:**
- [ ] `GET /ui/sse` returns 200 with `Content-Type: text/event-stream` and emits one `datastar-merge-fragments` event per configured device on connect.
- [ ] Subsequent `Notify` calls produce events on the open connection.
- [ ] Closing the connection (or canceling the request context) unsubscribes cleanly — the hub's subscriber set becomes empty.
- [ ] Heartbeat: every 30 s, a comment-line event (`: keepalive\n\n`) is written.
- [ ] `Handler` struct has a `PushHub *PushHub` field; `main.go` constructs one and composes its `Notify` with `SyncHomekit` in the `onPoll` callback.
- [ ] Dashboard still works as before (htmx polling remains) — this task is purely additive.

**Verify:** `go test ./cmd/breezyd/ -run TestGetUISSE -v && just check` → all pass; `curl -sN http://localhost:8080/ui/sse | head -30` (against a running daemon) shows `event: datastar-merge-fragments` lines.

**Steps:**

- [ ] **Step 1: Replace `WriteCard`'s stub with the real implementation**

In `cmd/breezyd/push_hub.go`, replace the stub `WriteCard` with:

```go
// WriteCard renders cmp and writes a datastar-merge-fragments event to w
// targeting the card matching `name`. Used by the /ui/sse initial-state
// loop where the snapshot is already in hand and we bypass the
// subscriber channel.
func (h *PushHub) WriteCard(ctx context.Context, w io.Writer, name string, cmp templ.Component) error {
	body, err := buildMergeFragmentEventFor(name, cmp, ctx)
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

// buildMergeFragmentEventFor is the ctx-aware variant used by WriteCard.
// buildMergeFragmentEvent (used by Notify) ignores ctx because Notify is
// called from non-request paths (the poller's OnPoll).
func buildMergeFragmentEventFor(name string, cmp templ.Component, ctx context.Context) ([]byte, error) {
	var html bytes.Buffer
	if err := cmp.Render(ctx, &html); err != nil {
		return nil, err
	}
	var ev bytes.Buffer
	fmt.Fprint(&ev, "event: datastar-merge-fragments\n")
	fmt.Fprintf(&ev, "data: selector .card[data-device=%q]\n", name)
	fmt.Fprint(&ev, "data: mergeMode outer\n")
	for _, line := range bytes.Split(html.Bytes(), []byte("\n")) {
		fmt.Fprint(&ev, "data: fragments ")
		ev.Write(line)
		ev.WriteByte('\n')
	}
	ev.WriteByte('\n')
	return ev.Bytes(), nil
}
```

Add `"io"` to the import list and remove the stub. Update `Notify` to call `buildMergeFragmentEventFor(name, cmp, context.Background())` so there's only one renderer.

- [ ] **Step 2: Add `PushHub` to `Handler`**

Edit `cmd/breezyd/server.go` around line 142 (the `Handler` struct):

```go
type Handler struct {
	State      *State
	Devices    *DeviceRegistry
	Pollers    map[string]*Poller
	Schedulers map[string]*Scheduler
	ClientFactory func(name string) (HandlerClient, error)
	NoticeFunc func(device string, id breezy.ParamID)

	// PushHub fans out per-device snapshot updates to /ui/sse subscribers.
	// Populated in main.go.
	PushHub *PushHub

	homekitAccessories map[string]*homekit.Accessory
	cachedMux *http.ServeMux
	muxOnce   sync.Once
}
```

- [ ] **Step 3: Construct PushHub in `main.go` and compose onPoll**

Edit `cmd/breezyd/main.go` around line 132 where `handler := &Handler{...}` is built. Add the hub construction before the handler, then assign it:

```go
hub := NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
	view := handler.viewFor(name) // see step 4 — viewFor needs to return without an error
	if view == nil {
		return nil, fmt.Errorf("no snapshot for %s", name)
	}
	return templates.DeviceCard(*view), nil
})

handler := &Handler{
	State:         state,
	Devices:       devices,
	ClientFactory: makeClientFactory(devices),
	PushHub:       hub,
}
```

Order matters: `handler` is referenced from inside the closure passed to `NewPushHub`. We have a circular initialization: handler depends on hub, hub's render closure depends on handler. Resolve by using a pointer-to-pointer pattern OR by constructing in two phases. The cleanest fix:

```go
handler := &Handler{
	State:         state,
	Devices:       devices,
	ClientFactory: makeClientFactory(devices),
}
handler.PushHub = NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
	view, ok := handler.viewFor(name)
	if !ok {
		return nil, fmt.Errorf("no snapshot for %s", name)
	}
	return templates.DeviceCard(view), nil
})
```

Adjust `viewFor`'s signature if needed — Task 6 will simplify it once `uistate.Parse` is removed; for now match the existing two-arg shape: `handler.viewFor(r, name)`. The render closure runs without a request, so pass `nil`:

```go
handler.PushHub = NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
	view, ok := handler.viewFor(nil, name)
	if !ok {
		return nil, fmt.Errorf("no snapshot for %s", name)
	}
	return templates.DeviceCard(view), nil
})
```

`viewFor` calls `uistate.Parse(r)` which handles `r == nil` gracefully (returns zero state) — verify this in the existing implementation.

After the handler is built, compose the onPoll callback:

```go
onPoll := func(name string, snap Snapshot) {
	handler.SyncHomekit(name, snap)
	handler.PushHub.Notify(name, snap)
}
pollers, schedulers, pollersWg := startPollers(
	rootCtx, devices.Snapshot(), cfg.Daemon.PollInterval,
	stateDir, state, metrics, onPoll,
	handler.scheduleDial,
)
```

(Replace the `handler.SyncHomekit` argument in the existing `startPollers` call with the composed `onPoll`.)

- [ ] **Step 4: Make sure `uistate.Parse` tolerates `r == nil`**

Read `internal/uistate/state.go`. The first line of `Parse` is `c, err := r.Cookie(CookieName)` which would nil-deref. Patch it:

```go
func Parse(r *http.Request) State {
	if r == nil {
		return State{}
	}
	c, err := r.Cookie(CookieName)
	// ... rest unchanged
}
```

This is a one-line change to support the render closure being called from the poller (no request). Also: this entire package goes away in Task 6; the patch is temporary scaffolding that ages out.

- [ ] **Step 5: Implement `getUISSE` handler**

Create `cmd/breezyd/handlers_ui_sse.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
)

// keepaliveInterval is how often we emit a comment-line event so idle
// SSE connections aren't dropped by intermediaries (browsers, NATs,
// reverse proxies).
const keepaliveInterval = 30 * time.Second

// getUISSE serves the long-lived push channel. On connect, the handler:
//  1. Sends the current card for every configured device (initial state).
//  2. Subscribes to PushHub and forwards events until the client
//     disconnects.
//  3. Emits a comment-line keepalive every keepaliveInterval.
//
// Reconnects re-trigger the initial-state pass — the dashboard self-heals
// without Last-Event-ID resume.
func (h *Handler) getUISSE(w http.ResponseWriter, r *http.Request) {
	if h.PushHub == nil {
		http.Error(w, "push hub not configured", http.StatusInternalServerError)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Initial state: render each configured device's current card. The
	// view-builder gracefully handles devices without a Snapshot (they
	// render as `unreachable` placeholders), so users see "I see your
	// config, no data yet" instead of an empty grid.
	for _, view := range h.collectViews(r) {
		if err := h.PushHub.WriteCard(r.Context(), w, view.Name, templates.DeviceCard(view)); err != nil {
			slog.Debug("sse initial: write failed", "err", err)
			return
		}
	}
	flusher.Flush()

	sub := h.PushHub.Subscribe()
	defer h.PushHub.Unsubscribe(sub)

	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-sub.Events:
			if !ok {
				return // hub closed our channel; client should disconnect
			}
			if _, err := w.Write(ev); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
```

- [ ] **Step 6: Register the route in `server.go`**

In `cmd/breezyd/server.go::mux()`, after the existing `/ui/devices/{name}/card` line (around line 225):

```go
mux.HandleFunc("GET /ui/sse", h.getUISSE)
```

- [ ] **Step 7: Write handler tests**

Create `cmd/breezyd/handlers_ui_sse_test.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a-h/templ"
)

func TestGetUISSE_InitialStateAndPush(t *testing.T) {
	h := newTestHandlerWithFakedevice(t) // existing helper from handlers_ui_*_test.go
	h.PushHub = NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
		return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := w.Write([]byte("<div data-device=\"" + name + "\"></div>"))
			return err
		}), nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req := httptest.NewRequest("GET", "/ui/sse", nil).WithContext(ctx)
	rec := newFlushableRecorder()

	done := make(chan struct{})
	go func() {
		h.getUISSE(rec, req)
		close(done)
	}()

	// Wait briefly for the initial-state writes to land.
	time.Sleep(50 * time.Millisecond)
	body := rec.Body()
	if !strings.Contains(body, "event: datastar-merge-fragments") {
		t.Errorf("initial state: missing fragment event; body=%s", body)
	}
	if !strings.Contains(body, `data-device="bedroom"`) {
		t.Errorf("initial state: missing bedroom card; body=%s", body)
	}

	// Trigger a push.
	h.PushHub.Notify("bedroom", Snapshot{})
	time.Sleep(50 * time.Millisecond)

	body = rec.Body()
	count := strings.Count(body, `data-device="bedroom"`)
	if count < 2 {
		t.Errorf("expected ≥2 bedroom events (initial + push); got %d; body=%s", count, body)
	}

	// Cancel context; handler returns.
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Error("handler did not return after context cancel")
	}
}

func TestGetUISSE_HeartbeatOnIdleConnection(t *testing.T) {
	h := newTestHandlerWithFakedevice(t)
	h.PushHub = NewPushHub(func(name string, snap Snapshot) (templ.Component, error) {
		return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
			_, err := w.Write([]byte("<div data-device=\"" + name + "\"></div>"))
			return err
		}), nil
	})

	// Override keepalive to fire fast for the test.
	origInterval := keepaliveInterval
	t.Cleanup(func() {})
	// keepaliveInterval is a const; if we want test-friendly, refactor to a
	// package var. For now, accept the test runs ~30s. Skip in CI:
	if testing.Short() {
		t.Skip("keepalive test takes 30s; skipped in -short mode")
	}
	_ = origInterval

	ctx, cancel := context.WithTimeout(context.Background(), 31*time.Second)
	defer cancel()

	req := httptest.NewRequest("GET", "/ui/sse", nil).WithContext(ctx)
	rec := newFlushableRecorder()

	go h.getUISSE(rec, req)

	// Wait for one keepalive interval.
	time.Sleep(31 * time.Second)
	body := rec.Body()
	if !strings.Contains(body, ": keepalive") {
		t.Errorf("expected keepalive comment in body; got %s", body)
	}
}

// flushableRecorder is an httptest.ResponseRecorder variant that supports
// http.Flusher (recorder doesn't by default).
type flushableRecorder struct {
	mu     sync.Mutex
	body   bytes.Buffer
	header http.Header
	status int
}

func newFlushableRecorder() *flushableRecorder {
	return &flushableRecorder{header: http.Header{}}
}

func (r *flushableRecorder) Header() http.Header { return r.header }
func (r *flushableRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(p)
}
func (r *flushableRecorder) WriteHeader(code int) { r.status = code }
func (r *flushableRecorder) Flush()                {}
func (r *flushableRecorder) Body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}
```

If `newTestHandlerWithFakedevice` doesn't exist with that exact name, mirror the bootstrap pattern in `handlers_ui_write_test.go` — there's an existing helper for this kind of test (look for the function used by `TestPostUIPower_Success` and friends). Don't invent a new helper if an existing one fits.

The keepalive test refactors `keepaliveInterval` from a `const` to a package-level `var` so the test can shrink it. Adjust both files accordingly:

```go
// In handlers_ui_sse.go, change:
const keepaliveInterval = 30 * time.Second
// to:
var keepaliveInterval = 30 * time.Second
```

Then in the test:

```go
func TestGetUISSE_HeartbeatOnIdleConnection(t *testing.T) {
	orig := keepaliveInterval
	keepaliveInterval = 100 * time.Millisecond
	t.Cleanup(func() { keepaliveInterval = orig })

	// ...rest unchanged, with a 200ms wait instead of 31s.
}
```

- [ ] **Step 8: Run tests**

```bash
go test ./cmd/breezyd/ -run "TestPushHub|TestGetUISSE" -v
```

All pass.

- [ ] **Step 9: Run the full check**

```bash
just check
```

Clean. The dashboard still works as before (htmx polling drives it); the new `/ui/sse` endpoint is a parallel, unused channel.

- [ ] **Step 10: Commit**

```bash
git add cmd/breezyd/push_hub.go cmd/breezyd/handlers_ui_sse.go \
        cmd/breezyd/handlers_ui_sse_test.go cmd/breezyd/server.go \
        cmd/breezyd/main.go internal/uistate/state.go
git commit -m "ui: SSE push channel + onPoll fan-out (datastar prep)"
```

---

## Task 4: Cutover

**Goal:** Switch the dashboard from htmx to datastar in one cutover. Action handlers stop returning HTML; templates use `data-*` attrs; htmx is removed; `internal/uistate`, the four `DeviceView` UI fields, `computeDetailsOpen`, and the unused read endpoints (`GET /ui/devices`, `GET /ui/devices/{name}/card`) are deleted; ~210 lines of inline JS in `layout.templ` are replaced with a single datastar `<script>` and a body-level `data-on-load`. Render goldens are regenerated.

**Files:**
- Modify: `cmd/breezyd/handlers_ui_write.go` (every action handler)
- Modify: `cmd/breezyd/handlers_ui_read.go` (delete `getUIDeviceList`, `getUIDeviceCard`, `computeDetailsOpen`, `defaultOpen`; simplify `viewFor`)
- Modify: `cmd/breezyd/server.go` (drop two routes)
- Modify: `cmd/breezyd/ui/view.go` (drop four UI-state fields)
- Modify: `cmd/breezyd/ui/templates/layout.templ`
- Modify: `cmd/breezyd/ui/templates/device_card.templ`
- Modify: `cmd/breezyd/ui/templates/sensors_block.templ`
- Modify: `cmd/breezyd/ui/templates/energy_block.templ`
- Modify: `cmd/breezyd/ui/templates/schedule_block.templ`
- Modify: `cmd/breezyd/ui/templates/controls_block.templ`
- Modify: `cmd/breezyd/ui/style.css`
- Modify: `cmd/breezyd/ui_assets.go`
- Modify: `cmd/breezyd/ui/templates/render_test.go`
- Modify: `cmd/breezyd/ui/templates/testdata/golden_*.html` (regenerated)
- Modify: `cmd/breezyd/handlers_ui_read_test.go` (drop tests for deleted handlers and `computeDetailsOpen`)
- Modify: `cmd/breezyd/handlers_ui_write_test.go` (update for SSE-format error responses; drop HTML-body assertions on success)
- Delete: `internal/uistate/state.go`
- Delete: `internal/uistate/state_test.go`
- Delete: `cmd/breezyd/ui/vendor/htmx-2.0.4.min.js`
- Delete: `cmd/breezyd/ui/vendor/htmx-response-targets-2.0.4.min.js`

**Acceptance Criteria:**
- [ ] `just check` clean.
- [ ] No file references `internal/uistate`, `DetailsOpen`, `EditingPreset`, `Automode`, `MatchSpeeds`, `computeDetailsOpen`, `defaultOpen`, `getUIDeviceList`, `getUIDeviceCard`, `htmx-`, `hx-post`, `hx-trigger`, `hx-swap`, `hx-vals`, `hx-target`, `hx-disabled-elt`, `hx-include`, or `hx-confirm`.
- [ ] Dashboard renders and updates via SSE: open browser, change a fakedevice value, the card refreshes within one poll cycle without a page reload.
- [ ] Action endpoints return 200 OK + empty body on success, 422/401/502 + SSE error envelope on failure.
- [ ] All render goldens reflect the new attribute syntax.

**Verify:** `just check && just build && # smoke-test the binary with a real fakedevice` → dashboard works, polling-free.

This is the largest task. Execute it as multiple internal commits to keep diffs reviewable, but land the whole sequence before merging — intermediate states (htmx removed but datastar attrs not yet present) won't run the dashboard correctly.

**Steps:**

- [ ] **Step 1: Convert action handlers in `handlers_ui_write.go`**

For each `postUI*` handler (the existing surface: `postUIPower`, `postUIMode`, `postUISpeed`, `postUIPreset`, `postUIHeater`, `postUITimer`, `postUIResetFilter`, `postUIResetFaults`), apply this transformation:

Before:
```go
func (h *Handler) postUIPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok { http.NotFound(w, r); return }
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	onStr := r.FormValue("on")
	if onStr != "true" && onStr != "false" {
		h.uiValidationError(w, r, name, "missing or invalid 'on' field (true/false)")
		return
	}
	on := onStr == "true"
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil { h.uiWriteError(w, r, err); return }
	defer unlock(); defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second); defer cancel()
	if err := breezy.Power(ctx, rc, on); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}
```

After:
```go
func (h *Handler) postUIPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok { http.NotFound(w, r); return }
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	onStr := r.FormValue("on")
	if onStr != "true" && onStr != "false" {
		h.uiValidationError(w, r, name, "missing or invalid 'on' field (true/false)")
		return
	}
	on := onStr == "true"
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil { h.uiWriteError(w, r, err); return }
	defer unlock(); defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second); defer cancel()
	if err := breezy.Power(ctx, rc, on); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// notifyAfterWrite reads the post-write Snapshot from the cache and
// fans it out to SSE subscribers. Called from every successful action
// handler. The Snapshot was just refreshed by the breezy package's
// WriteThrough hook (set up in main.go via Client.SetWriteThrough), so
// it reflects the value we just sent to the device.
func (h *Handler) notifyAfterWrite(name string) {
	if h.PushHub == nil || h.State == nil {
		return
	}
	if snap, ok := h.State.Get(name); ok {
		h.PushHub.Notify(name, snap)
	}
}
```

Apply the same shape to `postUIMode`, `postUISpeed`, `postUIPreset`, `postUIHeater`, `postUITimer`. For `postUIResetFilter` and `postUIResetFaults` (no form body), the change is just `h.uiRenderCard(w, r, name)` → `h.notifyAfterWrite(name); w.WriteHeader(http.StatusOK)`.

Update `uiWriteError` and `uiValidationError` to emit SSE-format response bodies:

Before:
```go
func (h *Handler) uiWriteError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	switch {
	case errors.Is(err, breezy.ErrAuth):
		w.WriteHeader(http.StatusUnauthorized)
		_ = templates.ErrorBanner("device authentication failed").Render(r.Context(), w)
	default:
		w.WriteHeader(http.StatusBadGateway)
		_ = templates.ErrorBanner(err.Error()).Render(r.Context(), w)
	}
}

func (h *Handler) uiValidationError(w http.ResponseWriter, r *http.Request, name, msg string) {
	view, ok := h.viewFor(r, name)
	if !ok { http.NotFound(w, r); return }
	view.PostError = msg
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = templates.DeviceCard(view).Render(r.Context(), w)
}
```

After:
```go
func (h *Handler) uiWriteError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Content-Type", "text/event-stream")
	switch {
	case errors.Is(err, breezy.ErrAuth):
		w.WriteHeader(http.StatusUnauthorized)
		writeErrorBannerEvent(w, "device authentication failed")
	default:
		w.WriteHeader(http.StatusBadGateway)
		writeErrorBannerEvent(w, err.Error())
	}
}

func (h *Handler) uiValidationError(w http.ResponseWriter, r *http.Request, name, msg string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusUnprocessableEntity)
	writeErrorBannerEvent(w, msg)
}

// writeErrorBannerEvent emits a single datastar-merge-fragments event
// targeting #global-error-banner with the human-readable message.
func writeErrorBannerEvent(w io.Writer, msg string) {
	html := `<div class="err-banner" role="alert">` + templ.EscapeString(msg) + `</div>`
	_, _ = fmt.Fprint(w, "event: datastar-merge-fragments\n")
	_, _ = fmt.Fprint(w, "data: selector #global-error-banner\n")
	_, _ = fmt.Fprint(w, "data: mergeMode inner\n")
	_, _ = fmt.Fprintf(w, "data: fragments %s\n\n", html)
}
```

Add imports `"io"` and `"github.com/a-h/templ"` if not already present. (`templ.EscapeString` may need `templ.EscapedString`; verify against the templ package.)

Delete `uiRenderCard` — no longer used.

- [ ] **Step 2: Update action handler tests in `handlers_ui_write_test.go`**

For success-case tests: replace assertions like "body contains `data-device=\"bedroom\"`" with "PushHub.Notify was called" using a fake hub. Inject the fake hub into the test handler:

```go
type fakePushHub struct {
	calls []struct{ name string; snap Snapshot }
}

func (f *fakePushHub) Notify(name string, snap Snapshot) {
	f.calls = append(f.calls, struct{ name string; snap Snapshot }{name, snap})
}
```

Then in tests:
```go
fake := &fakePushHub{}
h.PushHub = fake // requires PushHub to be an interface or fake to embed *PushHub
```

`PushHub` is currently a struct; refactor to an interface for testability:

```go
// In push_hub.go:
type PushNotifier interface {
	Notify(name string, snap Snapshot)
}

// In server.go's Handler struct:
PushHub PushNotifier
```

Keep `*PushHub` as the production implementation. Update tests to use the fake. Or keep the struct and stub the inner render closure to a no-op for tests — choose whichever is simpler given the existing test bootstrap.

For 422/401/502 tests: assert the response body contains `event: datastar-merge-fragments` and the expected error message. Drop assertions about `<div class="card">`.

Run:
```bash
go test ./cmd/breezyd/ -run "TestPostUI" -v
```

All pass.

- [ ] **Step 3: Drop the unused read endpoints**

In `cmd/breezyd/handlers_ui_read.go`, delete:
- `getUIDeviceList` and its route registration
- `getUIDeviceCard` and its route registration
- `computeDetailsOpen`
- `defaultOpen`

Simplify `viewFor` and `collectViews` if they took an `r *http.Request` purely for cookie threading:

```go
// Before: func (h *Handler) viewFor(r *http.Request, name string) (ui.DeviceView, bool)
// After:  func (h *Handler) viewFor(name string) (ui.DeviceView, bool)
```

Drop the `state := uistate.Parse(r)` calls. `buildView` no longer takes a `uistate.State` argument:

```go
func (h *Handler) buildView(name string, snap Snapshot) ui.DeviceView {
	v := snapshotToView(name, snap)
	if h.Devices != nil {
		if cfg, ok := h.Devices.Get(name); ok {
			v.Serial = cfg.ID
		}
	}
	if h.Pollers != nil {
		if p, ok := h.Pollers[name]; ok && p != nil && p.Energy != nil {
			ev := p.Energy.Snapshot()
			v.Energy = energyViewFrom(ev)
		}
	}
	if h.Schedulers != nil {
		if sch, ok := h.Schedulers[name]; ok && sch != nil {
			v.Schedule = scheduleViewFrom(sch.Snapshot())
		}
	}
	return v
}
```

Update every caller of `viewFor` to pass `name` only. `grep -rn 'h\.viewFor' cmd/breezyd/` should turn up the call sites.

In `server.go::mux()`, delete:
```go
mux.HandleFunc("GET /ui/devices", h.getUIDeviceList)
mux.HandleFunc("GET /ui/devices/{name}/card", h.getUIDeviceCard)
```

In `cmd/breezyd/handlers_ui_read_test.go`, delete the corresponding tests (`TestBuildView_*` that exercise force-open precedence are no longer relevant; the rules are gone).

- [ ] **Step 4: Drop `internal/uistate` package and the four `DeviceView` fields**

```bash
rm -r internal/uistate
```

In `cmd/breezyd/ui/view.go`, delete the four fields:
- `DetailsOpen map[string]bool`
- `EditingPreset int`
- `Automode bool`
- `MatchSpeeds bool`

Run:
```bash
just generate
go build ./...
```

Compile errors will surface every reference. Fix each one:
- Templates that consult `v.DetailsOpen[...]` get rewritten in steps 6-9.
- Tests that set those fields get cleaned up.
- The render closure in `main.go` no longer cares.

- [ ] **Step 5: Update `layout.templ`**

Replace the entire body and inline `<script>` block. New `layout.templ`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package templates

type LayoutData struct {
	StyleHash       string
	DatastarVersion string
}

templ Layout(d LayoutData) {
	<!DOCTYPE html>
	<html lang="en">
		<head>
			<meta charset="utf-8"/>
			<meta name="viewport" content="width=device-width, initial-scale=1"/>
			<title>breezy</title>
			<script>
				var t = localStorage.getItem("theme");
				if (t === "light" || t === "dark") {
					document.documentElement.setAttribute("data-theme", t);
				}
			</script>
			<link rel="stylesheet" href={ "/ui/style-" + d.StyleHash + ".css" }/>
			<script type="module" src={ "/ui/vendor/datastar-" + d.DatastarVersion + ".min.js" }></script>
		</head>
		<body
			data-signals='{"editor": {"device": null, "preset": 0}}'
			data-on-load="@get('/ui/sse')"
		>
			@ThemePicker()
			<div id="global-error-banner" aria-live="polite"></div>
			<div id="device-list" class="grid"></div>
			<script>
(function() {
  var picker = document.querySelector('.theme-picker');
  if (!picker) return;
  picker.open = false;
  picker.addEventListener('click', function(ev) {
    var target = ev.target.closest('[data-theme-set]');
    if (!target) return;
    var theme = target.getAttribute('data-theme-set');
    if (theme === 'auto') {
      document.documentElement.removeAttribute('data-theme');
      localStorage.removeItem('theme');
    } else {
      document.documentElement.setAttribute('data-theme', theme);
      localStorage.setItem('theme', theme);
    }
    picker.open = false;
  });
  document.addEventListener('click', function(ev) {
    if (picker.open && !picker.contains(ev.target)) {
      picker.open = false;
    }
  });
})();
			</script>
		</body>
	</html>
}
```

The body's `data-on-load="@get('/ui/sse')"` opens the SSE stream on page load. The empty `#device-list` is filled by initial-state SSE events.

`LayoutData.HTMXVersion` becomes `DatastarVersion` — update every caller (likely just `cmd/breezyd/handlers_ui_read.go::getIndex`):

```go
// Before:
data := templates.LayoutData{StyleHash: ..., HTMXVersion: htmxVersion}
// After:
data := templates.LayoutData{StyleHash: ..., DatastarVersion: datastarVersion}
```

- [ ] **Step 6: Update `device_card.templ`**

Add `data-signals` and switch `<details>` to `data-bind:open`:

```go
templ DeviceCard(v ui.DeviceView) {
	if v.Unreachable {
		@unreachableCard(v)
	} else {
		<div class={ "card", templ.KV("stale", v.Stale) }
		     data-device={ v.Name }
		     data-signals={ initialCardSignals(v) }>
			if v.PostError != "" {
				<div class="card-error" role="alert">{ v.PostError }</div>
			}
			<details id={ "info-" + v.Name }
			         class={ "device-info", templ.KV("alert", v.NeedsAttention) }
			         data-bind:open={ "$detailsOpen.info" }>
				<summary>
					<h2>{ v.Name }</h2>
					<button
						type="button"
						class="toggle toggle-inline"
						data-on-click={ "@post('/ui/devices/" + v.Name + "/power', {on: " + boolJSON(!v.Power) + "})" }
						if v.Stale { disabled }
						aria-pressed={ boolAttr(v.Power) }
					>power</button>
				</summary>
				@kvRow("ip", v.IP)
				@kvRow("serial", v.Serial)
				@kvRow("firmware version", v.FirmwareVersion)
				@kvRow("firmware date", v.FirmwareDate)
				@kvRowWithAction("filter", filterStatusStr(v), "reset filter", "/ui/devices/"+v.Name+"/reset-filter")
				@kvRow("motor", v.MotorLifetime)
				@kvRow("RTC", v.RTCBattery)
				@kvRowWithAction("faults", v.FaultLevel, "reset faults", "/ui/devices/"+v.Name+"/reset-faults")
			</details>
			if v.Stale && v.LastPollAge != "" {
				<div class="row"><span class="ts red">{ v.LastPollAge } ago</span></div>
			} else if v.Stale {
				<div class="row"><span class="ts red">no poll</span></div>
			}
			@EnergyBlock(v.Name, v.Energy)
			@SensorsBlock(v.Name, v.Sensors)
			@ScheduleBlock(v.Name, v.Schedule, v.Stale)
			@controlsBlock(v)
		</div>
	}
}

// initialCardSignals returns the per-card datastar signals seed as JSON.
// Default-open rules: info=closed, sensors=open, energy=closed,
// schedule=closed. The `alert` CSS class on the <details> handles the
// "this needs attention" visual cue without forcing the panel open.
func initialCardSignals(v ui.DeviceView) string {
	s := map[string]any{
		"automode":    false,
		"matchSpeeds": true,
		"detailsOpen": map[string]bool{
			"info":     false,
			"sensors":  true,
			"energy":   false,
			"schedule": false,
		},
	}
	b, _ := json.Marshal(s)
	return string(b)
}

// kvRowWithAction is updated to use data-on-click instead of hx-post:
templ kvRowWithAction(k, v, label, postURL string) {
	<div class="row">
		<span class="k">{ k }</span>
		<span>{ v }</span>
		<button
			type="button"
			class="btn-inline"
			data-on-click={ "@post('" + postURL + "')" }
		>{ label }</button>
	</div>
}

// boolJSON returns "true" or "false" as a JSON literal — used inline in
// data-on-click expressions so they are valid JavaScript.
func boolJSON(b bool) string {
	if b { return "true" }
	return "false"
}
```

Add `"encoding/json"` to the imports.

- [ ] **Step 7: Update `sensors_block.templ` / `energy_block.templ` / `schedule_block.templ`**

Each block drops its `open bool` parameter — visibility is now driven by `data-bind:open` against the card-level signal.

`sensors_block.templ`:
```go
templ SensorsBlock(name string, s ui.SensorsView) {
	<details id={ "sensors-" + name }
	         class={ "block", "sensors", templ.KV("alert", s.AlertActive) }
	         data-bind:open={ "$detailsOpen.sensors" }>
		<summary><h3>Sensors</h3></summary>
		@sensorsGrid(name, s)
	</details>
}
```

`energy_block.templ`:
```go
templ EnergyBlock(name string, ev *ui.EnergyView) {
	if ev != nil {
		<details id={ "energy-" + name }
		         class="block energy"
		         data-bind:open={ "$detailsOpen.energy" }>
			<summary><h3>ENERGY</h3></summary>
			if ev.Error != "" {
				<div class="warn">{ ev.Error }</div>
			} else {
				@energyGrid(ev)
			}
		</details>
	}
}
```

`schedule_block.templ`:
```go
templ ScheduleBlock(name string, s ui.ScheduleView, stale bool) {
	if s.Present {
		<details id={ "schedule-" + name }
		         class={ "block", "schedule", templ.KV("alert", s.Alert) }
		         data-bind:open={ "$detailsOpen.schedule" }>
			<summary><h3>SCHEDULE</h3></summary>
			... (rest unchanged) ...
		</details>
	}
}
```

Update the call sites in `device_card.templ` (already done in step 6).

- [ ] **Step 8: Update `controls_block.templ`**

This is the most intricate template. Replace `hx-*` with `data-*`:

Preset chip:
```go
templ presetBtn(v ui.DeviceView, n int) {
	<button
		type="button"
		class="preset-chip"
		data-name={ v.Name }
		data-on-click={ presetClickExpr(v.Name, n) }
		if v.Stale { disabled }
		aria-pressed={ boolAttr(v.SpeedMode == fmt.Sprintf("preset%d", n)) }
	>{ presetLabel(v, n) }</button>
}

// presetClickExpr builds the inline datastar expression for a preset chip
// click: toggle the global $editor signal AND fire the activation POST.
func presetClickExpr(name string, n int) string {
	return fmt.Sprintf(
		"$editor = $editor.device === %q && $editor.preset === %d ? { device: null, preset: 0 } : { device: %q, preset: %d }; @post('/ui/devices/%s/speed', {preset: %d})",
		name, n, name, n, name, n,
	)
}
```

Manual chip:
```go
templ manualBtn(v ui.DeviceView) {
	<button
		type="button"
		data-name={ v.Name }
		data-on-click={ "$editor = { device: null, preset: 0 }; @post('/ui/devices/" + v.Name + "/speed', {manual: " + manualBtnPctStr(v) + "})" }
		if v.Stale { disabled }
		aria-pressed={ boolAttr(v.SpeedMode == "manual") }
	>manual</button>
}

func manualBtnPctStr(v ui.DeviceView) string { return fmt.Sprintf("%d", manualBtnPct(v)) }
```

Mode chip:
```go
templ modeBtn(v ui.DeviceView, label, value string) {
	<button
		type="button"
		data-name={ v.Name }
		data-on-click={ "$editor = { device: null, preset: 0 }; @post('/ui/devices/" + v.Name + "/mode', {mode: '" + value + "'})" }
		if v.Stale { disabled }
		aria-pressed={ boolAttr(v.AirflowMode == value) }
	>{ label }</button>
}
```

Manual slider:
```go
templ manualSliderRow(v ui.DeviceView) {
	<div class="slider-row fan-slider-row">
		<span class="fan-side"></span>
		<input
			type="range"
			min="10"
			max="100"
			step="1"
			value={ fmt.Sprintf("%d", v.ManualPct) }
			data-name={ v.Name }
			data-on-change={ "$editor = { device: null, preset: 0 }; @post('/ui/devices/" + v.Name + "/speed', {manual: $event.target.value})" }
		/>
		<span class="val">{ fmt.Sprintf("%d%%", v.ManualPct) }</span>
	</div>
}
```

Preset editor (the big one):
```go
templ presetEditor(v ui.DeviceView, n int, p ui.PresetView) {
	<div class="preset-editor"
	     data-show={ fmt.Sprintf("$editor.device === %q && $editor.preset === %d", v.Name, n) }>
		<label class="match-speeds">
			<input type="checkbox" data-bind="$automode"/>
			automode
		</label>
		<label class="match-speeds">
			<input type="checkbox" data-bind="$matchSpeeds"/>
			match speeds
		</label>
		<div class="slider-row">
			<span class="val-label">supply</span>
			<input
				type="range"
				min="0"
				max="100"
				step="1"
				value={ presetSliderValue(p.Supply) }
				data-on-change={ presetSliderExpr(v.Name, n, "supply") }
			/>
			<span class="val">{ fmt.Sprintf("%d%%", clampPresetDisplay(p.Supply)) }</span>
		</div>
		<div class="slider-row">
			<span class="val-label">exhaust</span>
			<input
				type="range"
				min="0"
				max="100"
				step="1"
				value={ presetSliderValue(p.Extract) }
				data-on-change={ presetSliderExpr(v.Name, n, "extract") }
			/>
			<span class="val">{ fmt.Sprintf("%d%%", clampPresetDisplay(p.Extract)) }</span>
		</div>
	</div>
}

// presetSliderExpr builds the data-on-change expression for a preset
// editor slider. The expression: snap 1..9 to 0; if matchSpeeds is on,
// mirror to the sibling slider; POST the new (supply, extract) pair when
// both are >= 10; fire an implied-mode POST per the spec table.
//
// `side` is "supply" or "extract" — identifies which input fired.
func presetSliderExpr(name string, n int, side string) string {
	other := "extract"
	if side == "extract" { other = "supply" }
	return fmt.Sprintf(`
		var v = parseInt($event.target.value, 10);
		if (v > 0 && v < 10) v = 0;
		$event.target.value = v;
		var sup = %s;
		var ext = %s;
		if ($matchSpeeds) {
			sup = v; ext = v;
			document.querySelector('[data-device=%q] [data-preset-editor="%d"] input[type=range][data-side=%q]').value = v;
		}
		if (sup >= 10 && ext >= 10) {
			@post('/ui/devices/%s/preset', {preset: %d, supply: sup, extract: ext});
		}
		var mode = null;
		if ($automode) {
			mode = 'ventilation';
		} else if (sup >= 10 && ext >= 10) {
			mode = 'regeneration';
		} else if (sup === 0 && ext >= 10) {
			mode = 'extract';
		} else if (sup >= 10 && ext === 0) {
			mode = 'supply';
		}
		if (mode && '$speedMode' === 'preset%d' && '$airflowMode' !== mode) {
			@post('/ui/devices/%s/mode', {mode: mode});
		}
	`,
		ifSide(side, "v", "parseInt(/* sibling */, 10)"),
		ifSide(other, "v", "parseInt(/* sibling */, 10)"),
		name, n, other,
		name, n,
		n,
		name,
	)
}
```

This expression is non-trivial. If it grows past ~20 lines or becomes too gnarly to read inline, factor the JS body out into a vendored `cmd/breezyd/ui/vendor/dashboard.js` (~80 lines), expose helpers like `window.dashboard.onPresetSlider($event, $signals, name, preset, side)`, and call them from the template:

```go
data-on-change={ fmt.Sprintf("dashboard.onPresetSlider($event, $signals, %q, %d, %q)", v.Name, n, side) }
```

Decision rule: keep inline if expression fits in 5 lines; promote to a helper otherwise. The preset-slider expression *will* exceed 5 lines, so the helper file is likely the right answer at this step. Create:

```go
// cmd/breezyd/ui/vendor/dashboard.js
window.dashboard = (function() {
	function snapZero(v) { return v > 0 && v < 10 ? 0 : v; }

	function onPresetSlider($event, $signals, name, preset, side) {
		var raw = parseInt($event.target.value, 10);
		var v = snapZero(raw);
		$event.target.value = v;

		var supplySel = '.card[data-device="' + name + '"] [data-preset-editor="' + preset + '"] input[data-side="supply"]';
		var extractSel = '.card[data-device="' + name + '"] [data-preset-editor="' + preset + '"] input[data-side="extract"]';
		var supplyEl = document.querySelector(supplySel);
		var extractEl = document.querySelector(extractSel);
		var sup = parseInt(supplyEl.value, 10);
		var ext = parseInt(extractEl.value, 10);
		if ($signals.matchSpeeds) {
			sup = v;
			ext = v;
			(side === 'supply' ? extractEl : supplyEl).value = v;
		}

		var actions = [];
		if (sup >= 10 && ext >= 10) {
			actions.push(['preset', { preset: preset, supply: sup, extract: ext }]);
		}

		var mode = null;
		if ($signals.automode) mode = 'ventilation';
		else if (sup >= 10 && ext >= 10) mode = 'regeneration';
		else if (sup === 0 && ext >= 10) mode = 'extract';
		else if (sup >= 10 && ext === 0) mode = 'supply';

		var card = document.querySelector('.card[data-device="' + name + '"]');
		if (mode && card.getAttribute('data-speed-mode') === 'preset' + preset
		         && card.getAttribute('data-airflow-mode') !== mode) {
			actions.push(['mode', { mode: mode }]);
		}

		return actions; // datastar expression iterates and POSTs each
	}

	return { snapZero: snapZero, onPresetSlider: onPresetSlider };
})();
```

Then the templ slider uses:
```go
data-on-change={ fmt.Sprintf("var acts = dashboard.onPresetSlider($event, $signals, %q, %d, %q); for (var i = 0; i < acts.length; i++) @post('/ui/devices/%s/' + acts[i][0], acts[i][1])", v.Name, n, side, v.Name) }
```

Note: datastar inline expressions can call regular JS functions on `window`. The `@post()` macro is datastar-specific and must appear textually in the expression — we can't move it into `dashboard.js`. So the helper returns the action list and the inline expression iterates.

The card root needs `data-speed-mode` and `data-airflow-mode` attributes for the helper to read — ADD these back to `device_card.templ` (they were dropped as part of the spec but the helper needs them):

```go
<div class="card"
     data-device={ v.Name }
     data-speed-mode={ v.SpeedMode }
     data-airflow-mode={ v.AirflowMode }
     data-signals={ initialCardSignals(v) }>
```

(Update the spec note to reflect the keep-decision once the implementation lands.)

- [ ] **Step 9: Update `style.css`**

Add the alert rule and remove the now-unused `.preset-editor[hidden]`:

```css
/* Alert: highlights a block heading when its underlying alert flag fires.
   Datastar's data-bind:open governs visibility; this is purely visual. */
.block.alert > summary > h3,
.device-info.alert > summary > h2 {
	color: var(--alert-fg, #c00);
}
.block.alert > summary::before,
.device-info.alert > summary::before {
	content: "⚠ ";
}
```

Find and delete the `.preset-editor[hidden]` rule (added in PR #57's Task 7 fix).

- [ ] **Step 10: Update `ui_assets.go`**

Drop the htmx vendor entries. Replace with datastar (already added in Task 1) plus the new `dashboard.js`:

```go
//go:embed vendor/dashboard.js
var dashboardJS []byte
```

Register the file-serving route. Drop the htmx-* embeds.

Delete the htmx vendor files:
```bash
rm cmd/breezyd/ui/vendor/htmx-2.0.4.min.js cmd/breezyd/ui/vendor/htmx-response-targets-2.0.4.min.js
```

Add `<script src="/ui/vendor/dashboard.js"></script>` to `layout.templ`'s `<head>` (after the datastar script).

- [ ] **Step 11: Regenerate render goldens**

```bash
just generate
go test ./cmd/breezyd/ui/templates/ -run TestRenderGoldens -update
```

If `-update` isn't supported, manually copy the actual rendered HTML into each `golden_*.html` file from the test failure output. Eight goldens to update.

The editor-open variant golden (`golden_editor_open_preset2.html`) is now redundant — the card HTML is the same regardless of editor state (`data-show` controls visibility client-side). Delete the fixture and remove its case from `render_test.go`.

- [ ] **Step 12: Verify**

```bash
just check
```

Clean. If any test fails on missing-cookie or missing-DetailsOpen, those tests need to be deleted (they were specific to the old architecture).

- [ ] **Step 13: Smoke-test the binary**

```bash
just build
./breezyd --config /path/to/test/config.toml &
# Open http://localhost:8080/ in a browser
# Open dev tools → Network → confirm an SSE connection to /ui/sse
# Click power, change speed, etc. — confirm cards update without reload
# Watch the `event: datastar-merge-fragments` events flowing on the SSE stream
kill %1
```

If anything's broken, fix before commit. The intermediate state of this task may have build errors during Steps 1–11; the final state must be clean.

- [ ] **Step 14: Commit**

```bash
git add -A
git commit -m "ui: cutover to datastar + SSE push (drop htmx + uistate)"
```

(The implementer is encouraged to split this into ~3 internal commits — handlers, templates, cleanup — for review-ability, but a single commit is acceptable.)

---

## Task 5: Convert remaining `/ui/*` fragment endpoints to SSE-format

**Goal:** The threshold inline editor and the schedule editor still use server-rendered fragments returned as HTML. Convert their GET/PUT endpoints to SSE-format responses (datastar `merge-fragments` events) so the dashboard's datastar `@get` / `@put` handlers consume them correctly. No UX changes — same endpoints, same fragments, just different wire format.

**Files:**
- Modify: `cmd/breezyd/handlers_ui_write.go` — the threshold and schedule fragment handlers (`getUIThresholdRead`, `getUIThresholdEdit`, `putUIThreshold`, `getUIScheduleRead`, `getUIScheduleEdit`, `getUIScheduleNewRow`, `putUISchedule`, `scheduleReadFrag`, `scheduleEditFrag`).
- Modify: corresponding tests in `cmd/breezyd/handlers_ui_write_test.go`.
- Modify: `cmd/breezyd/ui/templates/sensors_block.templ` and `schedule_block.templ` — `hx-get` / `hx-put` → `data-on-click="@get(...)"` / `data-on-submit="@put(...)"`.

**Acceptance Criteria:**
- [ ] Each `getUIThreshold*` and `getUISchedule*` handler returns `Content-Type: text/event-stream` with a `merge-fragments` event targeting the appropriate selector.
- [ ] `putUIThreshold` and `putUISchedule` similarly emit SSE on success and SSE error envelopes on failure.
- [ ] The threshold inline editor and schedule editor work end-to-end in the browser (manual smoke test).
- [ ] Existing handler tests pass after assertion updates.

**Verify:** `go test ./cmd/breezyd/ -run "TestUIThreshold|TestUISchedule" -v && just check` → all pass.

**Steps:**

- [ ] **Step 1: Build a small SSE-render helper in `handlers_ui_write.go`**

```go
// renderFragmentSSE writes a single datastar-merge-fragments event with
// the rendered component into w. selector and mergeMode are the wire
// fields; mergeMode is "outer" by default but threshold and schedule
// fragments use "outer" with a class-based selector pinned by the
// fragment's wrapping element id.
func renderFragmentSSE(ctx context.Context, w http.ResponseWriter, selector, mergeMode string, cmp templ.Component) {
	w.Header().Set("Content-Type", "text/event-stream")
	var html bytes.Buffer
	if err := cmp.Render(ctx, &html); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprint(w, "event: datastar-merge-fragments\n")
	fmt.Fprintf(w, "data: selector %s\n", selector)
	fmt.Fprintf(w, "data: mergeMode %s\n", mergeMode)
	for _, line := range bytes.Split(html.Bytes(), []byte("\n")) {
		fmt.Fprint(w, "data: fragments ")
		w.Write(line)
		w.Write([]byte("\n"))
	}
	w.Write([]byte("\n"))
}
```

(This duplicates `buildMergeFragmentEventFor` in `push_hub.go`; if duplication is awkward, lift the renderer into a shared helper file.)

- [ ] **Step 2: Update `getUIThresholdRead`**

```go
func (h *Handler) getUIThresholdRead(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	kind := r.PathValue("kind")
	if kind != "humidity" && kind != "co2" && kind != "voc" { http.NotFound(w, r); return }
	view, ok := h.viewFor(name)
	if !ok { http.NotFound(w, r); return }
	cmp := renderThresholdRead(view, kind)
	if cmp == nil { http.NotFound(w, r); return }
	selector := fmt.Sprintf(`.card[data-device=%q] [data-threshold-kind=%q]`, name, kind)
	renderFragmentSSE(r.Context(), w, selector, "outer", cmp)
}
```

The threshold-cell template already wraps each cell with a stable id; if not, add a `data-threshold-kind={kind}` attribute on the wrapping element so the selector matches.

Apply the same pattern to `getUIThresholdEdit`, `getUIScheduleRead`, `getUIScheduleEdit`, `getUIScheduleNewRow`, and the success path of `putUIThreshold` / `putUISchedule`.

- [ ] **Step 3: Update validation/error paths**

`scheduleEditFrag(name, errMsg)` currently returns 422 + HTML. Switch to:

```go
func (h *Handler) scheduleEditFrag(w http.ResponseWriter, r *http.Request, name, errMsg string) {
	view, ok := h.viewFor(name)
	if !ok { http.NotFound(w, r); return }
	if errMsg != "" { w.WriteHeader(http.StatusUnprocessableEntity) }
	selector := fmt.Sprintf(`.card[data-device=%q] .schedule`, name)
	renderFragmentSSE(r.Context(), w, selector, "outer", templates.ScheduleBlockEdit(name, view.Schedule, view.Stale, errMsg))
}
```

- [ ] **Step 4: Update templates that consume these endpoints**

In `sensors_block.templ` (threshold cells), find `hx-get="/ui/devices/{name}/threshold/{kind}/edit"` and replace with:

```go
data-on-click={ "@get('/ui/devices/" + name + "/threshold/" + kind + "/edit')" }
```

In `schedule_block.templ` (the "edit schedule" button, the "+" new-row button, the form's submit), apply the same transformation. The form's submission goes from:

```go
hx-put="/ui/devices/{name}/schedule" hx-target="closest .schedule"
```

to:

```go
data-on-submit={ "@put('/ui/devices/" + name + "/schedule')" }
```

Datastar's `@put` serializes the form's inputs automatically when used inside `data-on-submit`.

- [ ] **Step 5: Update tests**

Existing tests assert HTML response bodies. Switch assertions to:

```go
if !strings.Contains(string(rec.Body.Bytes()), "event: datastar-merge-fragments") {
	t.Errorf("response missing fragment event; body=%s", rec.Body.String())
}
if !strings.Contains(string(rec.Body.Bytes()), expectedSubstring) {
	t.Errorf("response missing expected content; body=%s", rec.Body.String())
}
```

Where `expectedSubstring` is something like `"value=\"45\""` for threshold edits.

- [ ] **Step 6: Verify**

```bash
just generate
go test ./cmd/breezyd/ -run "TestUIThreshold|TestUISchedule" -v
just check
```

All pass.

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "ui: convert threshold + schedule fragment endpoints to SSE"
```

---

## Task 6: Playwright suite update

**Goal:** Adapt `tests/ui/dashboard.spec.ts` to the SSE-driven dashboard. Drop cookie-based tests, replace 5s-poll waits with SSE-driven waits, add reconnect smoke + cross-tab synchronization tests. The screenshot script also needs an update for SSE-driven initial paint.

**Files:**
- Modify: `tests/ui/dashboard.spec.ts`
- Modify: `tests/ui/screenshot.ts`
- Modify: `tests/ui/global-setup.ts` (if it had htmx-specific bootstrap)

**Acceptance Criteria:**
- [ ] All cookie-based tests deleted.
- [ ] Tests that previously waited for `/ui/devices` GET (poll) now wait for SSE-driven updates: trigger a fakedevice change via the admin endpoint, assert the card updates.
- [ ] Cross-tab synchronization test: open two contexts, action in tab A, assert tab B updates.
- [ ] Reconnect smoke: kill the SSE handler from the test daemon, assert browser auto-reconnects and view recovers.
- [ ] `just test-ui` clean.

**Verify:** `just test-ui` → all pass.

**Steps:**

- [ ] **Step 1: Read the current spec**

```bash
grep -n 'hx-\|cookie\|breezy-ui\|waitForPoll' tests/ui/dashboard.spec.ts | head -50
```

Identify every test that depends on htmx polling, cookies, or `data-action="..."` selectors that may have changed shape.

- [ ] **Step 2: Drop cookie-based tests**

The tests added in PR #57 that exercise cookie behavior (e.g., `cookie: malformed value falls back to defaults without 5xx`, automode/match-speeds cookie persistence checks) are no longer applicable. Delete them.

- [ ] **Step 3: Replace polling waits with SSE-driven waits**

Tests that did:

```typescript
await reset(DEVICE);
await presets.asMode(DEVICE, "regeneration");
await waitForPoll();
const card = await loadCard(page);
await expect(card).toHaveAttribute("data-airflow-mode", "regeneration");
```

become:

```typescript
await reset(DEVICE);
await page.goto("/");
await page.locator(`.card[data-device="${DEVICE}"]`).waitFor();
await presets.asMode(DEVICE, "regeneration"); // mutates fakedevice via admin endpoint
// SSE push from poller refreshes the card; wait for the data attribute to change.
await expect(page.locator(`.card[data-device="${DEVICE}"]`)).toHaveAttribute("data-airflow-mode", "regeneration", { timeout: 6000 });
```

The 6 s timeout covers a 5 s poll cadence + buffer. Some assertions can use shorter timeouts where actions cause an immediate `Notify`.

- [ ] **Step 4: Add cross-tab synchronization test**

```typescript
test("cross-tab: action in tab A updates tab B via SSE", async ({ browser }) => {
	const ctxA = await browser.newContext();
	const ctxB = await browser.newContext();
	const pageA = await ctxA.newPage();
	const pageB = await ctxB.newPage();
	await pageA.goto("/");
	await pageB.goto("/");
	await pageA.locator(`.card[data-device="${DEVICE}"]`).waitFor();
	await pageB.locator(`.card[data-device="${DEVICE}"]`).waitFor();

	const cardA = pageA.locator(`.card[data-device="${DEVICE}"]`);
	const cardB = pageB.locator(`.card[data-device="${DEVICE}"]`);

	// Tab A toggles power off.
	await cardA.locator('button.toggle-inline[aria-pressed="true"]').click();
	await expect(cardA.locator('button.toggle-inline[aria-pressed="false"]')).toBeVisible({ timeout: 2000 });

	// Tab B should see the change via its own SSE stream.
	await expect(cardB.locator('button.toggle-inline[aria-pressed="false"]')).toBeVisible({ timeout: 6000 });

	await ctxA.close();
	await ctxB.close();
});
```

- [ ] **Step 5: Add reconnect smoke test**

```typescript
test("sse: browser reconnects after server-side close", async ({ page }) => {
	await page.goto("/");
	const card = page.locator(`.card[data-device="${DEVICE}"]`);
	await card.waitFor();

	// Force-close all open EventSources from the page side. EventSource
	// auto-reconnects after ~3s.
	await page.evaluate(() => {
		// Find datastar's internal EventSource and close it. Datastar exposes
		// an event hook for this purpose; if not, fall back to fetching /ui/sse
		// and aborting the response — but the simpler path is to just wait,
		// then trigger a server-side change and confirm the client receives it.
	});

	await page.waitForTimeout(4000); // allow auto-reconnect
	// Trigger a fakedevice change.
	await presets.asMode(DEVICE, "extract");
	await expect(card).toHaveAttribute("data-airflow-mode", "extract", { timeout: 6000 });
});
```

If datastar doesn't expose an EventSource handle to JS, do the simpler equivalent: after page load, kill the SSE handler from the daemon side via a fakedevice admin endpoint that disconnects all subscribers, then assert recovery. This requires adding a small admin endpoint to `cmd/fakedevice` — verify whether an existing one fits.

- [ ] **Step 6: Adapt `screenshot.ts`**

The 3-col screenshot currently waits for `/ui/devices` to populate. Switch to "wait for `.card[data-device=bedroom]` to be visible after SSE initial-state."

The "open preset 2 editor" step from PR #57 still applies — datastar's `data-show` toggles the editor based on `$editor.preset`, so clicking the chip is the same UX.

- [ ] **Step 7: Verify**

```bash
just generate
just build
just test-ui
just screenshot
```

All pass; the new screenshot reflects the datastar dashboard.

- [ ] **Step 8: Commit**

```bash
git add tests/ui/ tests/ui/screenshots/dashboard-3col.png
git commit -m "tests/ui: SSE-driven assertions + reconnect + cross-tab"
```

---

## Task 7: NixOS module nginx config

**Goal:** The auto-configured nginx location for the dashboard sets the headers SSE needs through a reverse proxy: `proxy_buffering off`, `proxy_cache off`, `proxy_http_version 1.1`, `proxy_set_header Connection ""`, `proxy_read_timeout 1d`. Without these, events buffer in nginx for several seconds before flushing, breaking the live-update feel.

**Files:**
- Modify: `nix/module.nix`

**Acceptance Criteria:**
- [ ] The location block emitted for the dashboard has all five settings.
- [ ] `nix-check` passes.
- [ ] (Manual) A NixOS deployment with the breezyd module + nginx integration enabled serves SSE updates without buffering.

**Verify:** `just nix-check && nix build` → both clean.

**Steps:**

- [ ] **Step 1: Read the current nginx config**

```bash
grep -n 'proxyPass\|locations' nix/module.nix
```

Find the `services.breezyd.nginx` block and the location it adds to the user's `services.nginx.virtualHosts.<vhost>.locations`.

- [ ] **Step 2: Add the SSE-required directives**

Modify the location's `extraConfig` (or equivalent) to include:

```nix
extraConfig = ''
	proxy_buffering off;
	proxy_cache off;
	proxy_http_version 1.1;
	proxy_set_header Connection "";
	proxy_read_timeout 1d;
	${cfg.nginx.extraConfig or ""}
'';
```

(Append any user-supplied `extraConfig` so they can still customize.)

- [ ] **Step 3: Verify**

```bash
just nix-check
nix build
```

Both clean.

- [ ] **Step 4: Commit**

```bash
git add nix/module.nix
git commit -m "nix: nginx SSE-friendly settings for dashboard proxy"
```

---

## Task 8: CHANGELOG + CLAUDE.md + README + screenshot

**Goal:** Bring docs in line with the new architecture. CHANGELOG describes the migration; CLAUDE.md updates the architecture summary and the test count; README's screenshot is regenerated from the datastar UI.

**Files:**
- Modify: `CHANGELOG.md`
- Modify: `CLAUDE.md`
- Modify: `README.md` (if test-count or architecture text references htmx specifically)
- Regenerate: `tests/ui/screenshots/dashboard-3col.png` (already done in Task 6 — verify it landed).

**Acceptance Criteria:**
- [ ] CHANGELOG `[Unreleased]` `### Added` describes the datastar substrate, SSE push channel, alert-as-CSS-class shift, and `.alert` on headings.
- [ ] CHANGELOG `### Changed` describes the dashboard moving from htmx to datastar + SSE, the read endpoints (`/ui/devices`, `/ui/devices/{name}/card`) being removed, and the action endpoints' response shape change.
- [ ] CHANGELOG `### Removed` lists `internal/uistate`, the cookie protocol, the `htmx-*` vendored files, and the four UI-state DeviceView fields.
- [ ] CLAUDE.md's architecture section reflects datastar, the push hub, and SSE.
- [ ] CLAUDE.md's test count is updated to whatever Playwright reports.
- [ ] No file in the repo references the dropped concepts (verified by grep).

**Verify:** `just check && grep -rn 'htmx-2\|breezy-ui\|computeDetailsOpen\|internal/uistate\|hx-post\|hx-trigger\|hx-vals' --include='*.go' --include='*.md' --include='*.templ' --include='*.css' --include='*.ts' --include='*.nix' .` → no hits.

**Steps:**

- [ ] **Step 1: Update `CHANGELOG.md`**

In `[Unreleased]`:

```markdown
### Added

- Datastar replaces htmx + the cookie-based UI-state machinery (#46/#49/#53 follow-up). Single library for client reactivity and server interaction; UI state declared inline via `data-signals` on each card; visibility via `data-bind:open` and `data-show`. ~600 LoC removed (the `internal/uistate` package, the `breezy-ui` cookie protocol, four `DeviceView` UI fields, `computeDetailsOpen`, and ~210 lines of inline JS).
- Server-Sent Events push channel: `GET /ui/sse` opens a long-lived stream per browser. The daemon's poller fans out updated cards to every subscribed connection on each successful UDP poll. Replaces the dashboard's 5-second client-side polling. Reconnects auto-rejoin via re-emitting current state.
- Alert-as-CSS: when a sensor or schedule alert fires, the relevant `<details>` block heading turns red with a `⚠` prefix. The user's open/closed choice is preserved across alert state changes.

### Changed

- Dashboard substrate is now datastar + SSE. The `/ui/*` endpoints under the dashboard's private namespace return SSE event streams (success: empty 200; failure: SSE-format error fragment). The `/v1/*` JSON API is unchanged.
- The `GET /ui/devices` and `GET /ui/devices/{name}/card` routes are removed — the SSE push channel obsoletes them.

### Removed

- `internal/uistate/` — entire package.
- `cmd/breezyd/ui/vendor/htmx-2.0.4.min.js` and `htmx-response-targets-2.0.4.min.js`.
- Four `DeviceView` fields (`DetailsOpen`, `EditingPreset`, `Automode`, `MatchSpeeds`) and the `computeDetailsOpen` / `defaultOpen` helpers.
- The `breezy-ui` cookie protocol.
```

- [ ] **Step 2: Update `CLAUDE.md`**

Find the architecture paragraph that describes htmx + the cookie. Replace with a datastar + SSE description. Update the test count to reflect the Task 6 outcome.

- [ ] **Step 3: Update `README.md`** (if applicable)

If README references htmx specifically, rewrite. Otherwise, just confirm the screenshot reference is still correct.

- [ ] **Step 4: Verify**

```bash
just check
grep -rn 'htmx-2\|breezy-ui\|computeDetailsOpen\|internal/uistate\|hx-post\|hx-trigger\|hx-vals' \
  --include='*.go' --include='*.md' --include='*.templ' --include='*.css' --include='*.ts' --include='*.nix' .
```

No hits.

- [ ] **Step 5: Commit**

```bash
git add CHANGELOG.md CLAUDE.md README.md
git commit -m "docs: datastar migration"
```

---

## Self-Review Notes

- **Spec coverage:** ✓ uistate deletion (Task 4), DeviceView fields (Task 4), computeDetailsOpen (Task 4), unused routes (Task 4), action-endpoint reshape (Task 4), SSE push channel (Task 3), client signals + alert class (Task 4), threshold/schedule fragment endpoints (Task 5), Playwright + screenshot (Task 6), nginx (Task 7), docs + CHANGELOG (Task 8).
- **Type/symbol consistency:** `PushHub`/`Subscriber` named consistently. `data-signals` / `data-bind:open` / `data-show` / `data-on-click` form a stable set. The `dashboard.js` helper is referenced from `controls_block.templ` and exposed via `window.dashboard.*`.
- **Risks called out in spec:** all addressed in the relevant task — datastar maturity (vendored version pin in Task 1), keepalive (Task 3), reconnect cost (Task 3 + Task 6 reconnect test), no Last-Event-ID resume (documented non-goal, no task), PushHub mutex (no task — flagged for future), datastar attribute escapes (Task 4 + render goldens), inline expression complexity (Task 4 — promoted to `dashboard.js` helper).
- **One known scope expansion:** the implementation found that the threshold/schedule fragment endpoints aren't compatible with datastar's `@get`/`@put` if they return raw HTML, so they need conversion to SSE-format too. Task 5 covers this — added during planning.
- **Smoke testing:** Task 4 includes a manual binary smoke-test step. Task 6's Playwright tests cover the automated equivalents. Task 7's nginx changes are deferred to manual verification on a real NixOS deployment, since the test harness doesn't run nginx.
