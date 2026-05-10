# Poller: preserve `Snapshot.LastPoll` across failed ticks — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the daemon dashboard's 3×poll-interval stale gate and the `breezyd_last_poll_timestamp` Prometheus alert fire as documented, by redefining `Snapshot.LastPoll` as "last successful poll" and preserving it (and `Values`) across failed ticks.

**Architecture:** Two-line behavioral change in `cmd/breezyd/poller.go::tick` — both failure paths read `prev := p.State.Get(p.Name)` once and carry `prev.LastPoll` and `prev.Values` forward. Doc comments in `state.go` and `SPECIFICATION-daemon.md` updated to match new semantics. Existing Go test that pinned the *old* behavior is updated; two new Go tests cover the dial-failure path and success-path resumption. Playwright stale-class test is un-skipped.

**Tech Stack:** Go (`cmd/breezyd`), `matryer/is` test framework, Playwright (`tests/ui/`), `templ` (no template changes here).

**Spec:** `docs/superpowers/specs/2026-05-09-poller-lastpoll-failed-ticks-design.md`
**Issue:** [#178](https://github.com/hughobrien/breezyd/issues/178)

---

## File map

- **Modify:** `cmd/breezyd/poller.go` (tick function — both failure paths)
- **Modify:** `cmd/breezyd/state.go` (Snapshot.LastPoll doc comment, line 22)
- **Modify:** `cmd/breezyd/poller_test.go` (update one existing test, add two new ones)
- **Modify:** `SPECIFICATION-daemon.md` (failed-poll cache semantics paragraph)
- **Modify:** `tests/ui/dashboard.spec.ts` (un-skip stale-class test, line 556)

No new files.

---

### Task 1: Update poller failure paths to preserve `LastPoll` and `Values`

**Goal:** `cmd/breezyd/poller.go::tick` carries the prior snapshot's `LastPoll` and `Values` forward on both dial-failure and read-failure branches; existing tests updated and new tests added to pin the new behavior.

**Files:**
- Modify: `cmd/breezyd/poller_test.go` (update `TestPoller_FailedPollPreservesPriorValues` line 657; add two new tests)
- Modify: `cmd/breezyd/poller.go:165-233` (the `tick` function)

**Acceptance Criteria:**
- [ ] `tick` reads the prior snapshot once at the top of the function via `p.State.Get(p.Name)`.
- [ ] Dial-failure path emits a `Snapshot` carrying `prev.Values` and `prev.LastPoll` (only `LastErr` is updated).
- [ ] Read-failure path uses `prev.LastPoll` instead of `p.now()` when `lastErr != nil`. The success path still uses `p.now()`.
- [ ] Existing test `TestPoller_FailedPollPreservesPriorValues` asserts `second.LastPoll.Equal(first.LastPoll)` (was: `second.LastPoll.After(first.LastPoll)`).
- [ ] New test `TestPoller_FailedDial_PreservesPriorSnapshot` covers the dial-failure carry-forward.
- [ ] New test `TestPoller_LastPollResumesAfterFailureClears` pins that `LastPoll` advances again once the failure clears.
- [ ] Empty-state edge case: if no prior snapshot exists, `prev.LastPoll` is the zero `time.Time`, which `snapshotToView` and `metrics.go` already handle correctly.

**Verify:** `go test ./cmd/breezyd -run "TestPoller_FailedPollPreservesPriorValues|TestPoller_FailedDial_PreservesPriorSnapshot|TestPoller_LastPollResumesAfterFailureClears" -v` → all PASS.

**Steps:**

- [ ] **Step 1: Update existing failing-behavior test in `cmd/breezyd/poller_test.go`**

The current test at line 623 (`TestPoller_FailedPollPreservesPriorValues`) asserts `second.LastPoll.After(first.LastPoll)` — that pins the *old* behavior we're changing. Replace lines 654-664 (the section after the second `tick` call) with the new assertion. The full test body becomes:

```go
func TestPoller_FailedPollPreservesPriorValues(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{0x0001: {1}}}

	// Inject a clock so we can prove LastPoll DOES NOT advance on failed ticks.
	clock := time.Unix(1_700_000_000, 0)
	advance := func(d time.Duration) { clock = clock.Add(d) }

	p := &Poller{
		Name:     "dev",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		Now: func() time.Time { return clock },
	}

	p.tick(context.Background())
	first, ok := state.Get("dev")
	is.True(ok)             // first tick records a snapshot
	is.NoErr(first.LastErr) // first tick is the success that primes Values
	is.Equal(first.Values[0x0001], []byte{1})

	// Flip to failure and tick again — Values AND LastPoll must persist.
	fc.mu.Lock()
	fc.err = errors.New("read failed")
	fc.mu.Unlock()
	advance(5 * time.Minute) // wall clock advances, but LastPoll must not

	p.tick(context.Background())
	second, ok := state.Get("dev")
	is.True(ok)
	is.True(second.LastErr != nil)                  // failed tick marks LastErr
	is.Equal(second.Values[0x0001], []byte{1})      // prior success value preserved
	is.True(second.LastPoll.Equal(first.LastPoll))  // LastPoll preserved across failure

	// Third still-failing tick must still preserve Values and LastPoll.
	advance(5 * time.Minute)
	p.tick(context.Background())
	third, ok := state.Get("dev")
	is.True(ok)
	is.True(third.LastErr != nil)
	is.Equal(third.Values[0x0001], []byte{1})       // continued preservation
	is.True(third.LastPoll.Equal(first.LastPoll))   // continued preservation
}
```

- [ ] **Step 2: Add new test `TestPoller_FailedDial_PreservesPriorSnapshot`**

Insert immediately after `TestPoller_FailedPollPreservesPriorValues`. This test exercises the *dial-failure* branch (which today drops both `Values` and `LastPoll`); it complements the read-failure coverage above.

```go
// TestPoller_FailedDial_PreservesPriorSnapshot pins the dial-failure
// branch of tick(): once a successful poll has primed Values+LastPoll,
// a subsequent failure to construct the client must NOT overwrite them
// with empty/now. Without this, the dashboard would briefly drop to
// "unreachable" on a transient dial error.
func TestPoller_FailedDial_PreservesPriorSnapshot(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{0x0001: {1}}}

	clock := time.Unix(1_700_000_000, 0)
	advance := func(d time.Duration) { clock = clock.Add(d) }

	dialErr := errors.New("dial refused")
	dialFails := false

	p := &Poller{
		Name:     "dev",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		NewClient: func() (PollerClient, error) {
			if dialFails {
				return nil, dialErr
			}
			return fc, nil
		},
		Now: func() time.Time { return clock },
	}

	// Successful tick primes Values+LastPoll.
	p.tick(context.Background())
	first, ok := state.Get("dev")
	is.True(ok)
	is.NoErr(first.LastErr)
	is.Equal(first.Values[0x0001], []byte{1})

	// Force dial failures and tick again.
	dialFails = true
	advance(5 * time.Minute)
	p.tick(context.Background())

	second, ok := state.Get("dev")
	is.True(ok)
	is.True(errors.Is(second.LastErr, dialErr))    // dial error recorded
	is.Equal(second.Values[0x0001], []byte{1})     // prior values preserved
	is.True(second.LastPoll.Equal(first.LastPoll)) // LastPoll preserved
	is.Equal(second.IP, first.IP)                  // IP preserved
}
```

- [ ] **Step 3: Add new test `TestPoller_LastPollResumesAfterFailureClears`**

Insert immediately after the dial-failure test.

```go
// TestPoller_LastPollResumesAfterFailureClears pins that once a transient
// failure clears, the success path resumes advancing LastPoll. This
// guards against an over-correction that would freeze LastPoll forever.
func TestPoller_LastPollResumesAfterFailureClears(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{0x0001: {1}}}

	clock := time.Unix(1_700_000_000, 0)
	advance := func(d time.Duration) { clock = clock.Add(d) }

	p := &Poller{
		Name:     "dev",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		Now: func() time.Time { return clock },
	}

	// Tick 1: success.
	p.tick(context.Background())
	first, _ := state.Get("dev")
	is.NoErr(first.LastErr)

	// Tick 2: failure — LastPoll must hold.
	fc.mu.Lock()
	fc.err = errors.New("transient")
	fc.mu.Unlock()
	advance(time.Minute)
	p.tick(context.Background())
	failed, _ := state.Get("dev")
	is.True(failed.LastErr != nil)
	is.True(failed.LastPoll.Equal(first.LastPoll))

	// Tick 3: failure clears, LastPoll must advance.
	fc.mu.Lock()
	fc.err = nil
	fc.mu.Unlock()
	advance(time.Minute)
	p.tick(context.Background())
	resumed, _ := state.Get("dev")
	is.NoErr(resumed.LastErr)
	is.True(resumed.LastPoll.After(first.LastPoll)) // success advances clock
}
```

- [ ] **Step 4: Run the three tests; confirm they FAIL against unchanged poller**

Run: `go test ./cmd/breezyd -run "TestPoller_FailedPollPreservesPriorValues|TestPoller_FailedDial_PreservesPriorSnapshot|TestPoller_LastPollResumesAfterFailureClears" -v`

Expected: `TestPoller_FailedPollPreservesPriorValues` and the two new tests FAIL because the current `tick` writes `LastPoll: p.now()` on both failure paths (and the dial-failure path also drops `Values`).

- [ ] **Step 5: Update `cmd/breezyd/poller.go::tick` to preserve prior `LastPoll` and `Values` on failure**

Replace the body of `tick` (lines 165-233) with the version below. Three substantive changes are marked in comments:

```go
// tick performs one full poll cycle: build the per-tick ID list (settle
// filter applied), open a client, read each batch, record one Snapshot.
//
// Failed-tick semantics: the prior Snapshot's LastPoll and Values are
// carried forward across both the dial-failure and read-failure branches.
// LastPoll therefore reflects the most recent SUCCESSFUL poll — see
// SPECIFICATION-daemon.md "failed-poll cache semantics" and the
// SPECIFICATION-web.md "Card states" stale-window definition.
func (p *Poller) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	ids := p.idsForThisTick()

	// Hold udpMu for the entire tick — dial, read batches, close — so
	// concurrent HTTP handler writes can't interleave at the UDP layer.
	unlock := p.LockUDP()
	defer unlock()

	// (1) Read the prior snapshot once at the top of the tick. Get returns
	// a deep copy on hit and a zero Snapshot on miss; either is a safe
	// carry-forward source for failed-tick branches below.
	prev, _ := p.State.Get(p.Name)

	client, err := p.dial()
	if err != nil {
		p.recordErr(err)
		// (2) Dial-failure branch: carry prev.Values and prev.LastPoll
		// forward (was: empty Values + p.now()).
		p.State.RecordPoll(p.Name, Snapshot{
			IP:       p.IP,
			Values:   prev.Values,
			LastPoll: prev.LastPoll,
			LastErr:  err,
		})
		return
	}
	defer func() { _ = client.Close() }()

	values := make(map[breezy.ParamID][]byte, len(ids))
	var lastErr error
	for start := 0; start < len(ids); start += pollBatchSize {
		end := start + pollBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		got, err := client.ReadParams(ctx, batch)
		if err != nil {
			lastErr = err
			p.recordErr(err)
			continue
		}
		for k, v := range got {
			values[k] = v
		}
	}

	// If every read in this tick failed, preserve the last successful
	// Snapshot's Values so the dashboard keeps showing last-known-good
	// (marked stale by LastErr) rather than dropping to unreachable.
	// This matters most for the in-process MemClient backend, where a
	// forced ErrTimeout returns instantly and would otherwise overwrite
	// good state on the very first failed tick.
	if lastErr != nil && len(values) == 0 && len(prev.Values) > 0 {
		values = prev.Values
	}

	// (3) Read-failure branch: LastPoll holds at prev.LastPoll on failure;
	// success advances it to p.now().
	lastPoll := p.now()
	if lastErr != nil {
		lastPoll = prev.LastPoll
	}
	snap := Snapshot{
		IP:       p.IP,
		Values:   values,
		LastPoll: lastPoll,
		LastErr:  lastErr,
	}
	p.State.RecordPoll(p.Name, snap)
	if lastErr == nil {
		if p.Energy != nil {
			p.Energy.Tick(values, time.Now())
		}
		if p.OnPoll != nil {
			p.OnPoll(p.Name, snap)
		}
	}
}
```

Note that the existing read-failure cache-preservation block was inlined into the same expression that recomputes `prev` (we already have `prev` from step 1; no second `p.State.Get` call).

- [ ] **Step 6: Re-run the targeted tests; confirm they PASS**

Run: `go test ./cmd/breezyd -run "TestPoller_FailedPollPreservesPriorValues|TestPoller_FailedDial_PreservesPriorSnapshot|TestPoller_LastPollResumesAfterFailureClears" -v`

Expected: all three PASS.

- [ ] **Step 7: Run the full poller test suite to catch regressions**

Run: `go test ./cmd/breezyd -run TestPoller -v`

Expected: every `TestPoller_*` test passes. Pay particular attention to `TestPoller_ReadError_RecordedInSnapshot` (line 587) — it doesn't assert `LastPoll`, so it should still pass.

- [ ] **Step 8: Run `just check` (lint + fast tests + templ-drift)**

Run: `just check`

Expected: all green.

- [ ] **Step 9: Commit**

```bash
git add cmd/breezyd/poller.go cmd/breezyd/poller_test.go
git commit -m "$(cat <<'EOF'
fix(daemon): preserve Snapshot.LastPoll and Values across failed ticks

Both the dial-failure and read-failure branches in cmd/breezyd/poller.go
now carry the prior snapshot's LastPoll and Values forward instead of
overwriting with p.now() and empty. LastPoll is therefore the time of
the most recent SUCCESSFUL poll, which is what SPECIFICATION-web.md
"Card states" requires for the 3×poll-interval stale gate to fire and
what the breezyd_last_poll_timestamp Prometheus alert pattern needs.

Updates the existing TestPoller_FailedPollPreservesPriorValues to assert
the new semantics; adds TestPoller_FailedDial_PreservesPriorSnapshot
and TestPoller_LastPollResumesAfterFailureClears.

Refs: #178

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Update doc comment and spec to match new semantics

**Goal:** `Snapshot.LastPoll` doc comment in `state.go` and the failed-poll cache paragraph in `SPECIFICATION-daemon.md` accurately describe the new "last successful poll" semantics.

**Files:**
- Modify: `cmd/breezyd/state.go:22-23`
- Modify: `SPECIFICATION-daemon.md` (failed-poll cache semantics paragraph, around line 195)

**Acceptance Criteria:**
- [ ] `Snapshot.LastPoll`'s doc comment says "most recent successful poll" and notes preservation across failed ticks.
- [ ] `SPECIFICATION-daemon.md` failed-poll-cache paragraph mentions `LastPoll` preservation alongside the existing `Values` preservation.

**Verify:** `just check` → all green; `grep -n "LastPoll" cmd/breezyd/state.go` shows updated comment.

**Steps:**

- [ ] **Step 1: Update doc comment in `cmd/breezyd/state.go`**

Replace lines 22-23:

```go
	// LastPoll is the wall-clock time of the most recent poll attempt.
	LastPoll time.Time
```

with:

```go
	// LastPoll is the wall-clock time of the most recent SUCCESSFUL poll.
	// Failed ticks (dial errors, all-read failures) preserve the prior
	// LastPoll rather than overwriting it; this is what the dashboard's
	// 3×poll-interval stale gate (SPECIFICATION-web.md "Card states")
	// and the breezyd_last_poll_timestamp Prometheus alert require.
	// Zero until the first successful poll has produced a snapshot.
	LastPoll time.Time
```

- [ ] **Step 2: Extend `SPECIFICATION-daemon.md` failed-poll cache paragraph**

The paragraph currently reads (around line 193-197):

```
(5) **failed-poll cache semantics** —
if every batch failed AND the previous tick's `Snapshot` had non-empty
`Values`, reuse those `Values` so the dashboard renders "stale" with
last-known data instead of dropping to "unreachable" (matters most for
in-process backends where forced timeouts return instantly; real-UDP
timeouts are slow enough that the branch rarely fires in production);
```

Replace with:

```
(5) **failed-poll cache semantics** —
if a batch fails or the dial fails, reuse the prior `Snapshot`'s
`Values` AND `LastPoll` so the dashboard renders "stale" with
last-known data instead of dropping to "unreachable", and so
`LastPoll` reflects the most recent *successful* poll (which is what
the 3×poll-interval stale gate and the `breezyd_last_poll_timestamp`
Prometheus alert pattern require). Matters most for in-process
backends where forced timeouts return instantly; real-UDP timeouts
are slow enough that this branch rarely fires in production;
```

- [ ] **Step 3: Run `just check`**

Run: `just check`

Expected: all green (this is a doc-only commit, but lint runs anyway).

- [ ] **Step 4: Commit**

```bash
git add cmd/breezyd/state.go SPECIFICATION-daemon.md
git commit -m "$(cat <<'EOF'
docs: Snapshot.LastPoll is the last SUCCESSFUL poll

Updates the Snapshot.LastPoll doc comment and the daemon spec's
failed-poll cache paragraph to match the new semantics introduced in
the previous commit. Both now state that LastPoll AND Values are
preserved across failed ticks, and that LastPoll reflects the most
recent successful poll.

Refs: #178

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.5 (added mid-execution): Split `Poller.OnPoll` into success-only vs. every-tick fan-out

**Goal:** the dashboard's SSE push fires on every tick (success and failure) so `$lastPollAge` and `$stale` signals advance under sustained UDP timeouts; HomeKit + Energy continue to fire only on successes.

**Why this task exists:** discovered during Task 3 — Task 1's cache-side fix was correct but the poller's `OnPoll` callback (which dispatches to `PushHub.Notify`) is gated on `lastErr == nil`, so failed ticks never produce an SSE patch and the browser never learns. See spec addendum.

**Files:**
- Modify: `cmd/breezyd/poller.go` — add `OnTick func(name string, snap Snapshot)` field; call it after `RecordPoll` regardless of `lastErr`. Update doc on the existing `OnPoll` field.
- Modify: `cmd/breezyd/main.go` — split the poll fan-out into two closures (`onPoll` for HomeKit, `onTick` for `PushHub.Notify`); add `onTick` parameter to `startPollers`; wire it on the `Poller` literal.
- Modify: `cmd/breezyd/poller_test.go` — update `TestPoller_PostTickHooksGatedOnSuccess` (line 888) to reflect the new contract; add a new `TestPoller_OnTickFiresOnEveryTick`.
- Modify: `SPECIFICATION-daemon.md` "Push hub (poll → SSE fan-out)" section — document every-tick semantics.

**Acceptance criteria:**
- [ ] `Poller.OnTick` fires on success (asserted by new test).
- [ ] `Poller.OnTick` fires on failure (asserted by new test and updated `TestPoller_PostTickHooksGatedOnSuccess`).
- [ ] `Poller.OnPoll` still success-only (unchanged in spirit; `TestPoller_PostTickHooksGatedOnSuccess` still asserts this for `OnPoll` and `Energy.Tick`).
- [ ] `cmd/breezyd/main.go` wires `onTick: handler.PushHub.Notify` and `onPoll: handler.SyncHomekit`. (`testOnPollHook` keeps firing through `onPoll`.)
- [ ] `just check` green.

**Verify:** `go test ./cmd/breezyd -run TestPoller -v` and `just check`.

**Steps:**

- [ ] **Step 1: Update `cmd/breezyd/poller.go` — add `OnTick` field**

In the `Poller` struct (around line 89-94), keep the existing `OnPoll` doc and field unchanged. Immediately after the `OnPoll` field, add:

```go
	// OnTick is called after every tick (success OR failure) with the
	// device Name and the Snapshot that was just written to State. Use
	// this for consumers that need to learn about failed ticks too —
	// e.g. PushHub.Notify so the dashboard's $lastPollAge / $stale
	// signals advance under sustained UDP timeouts. Optional; a nil
	// OnTick is a no-op. OnPoll fires only on success and is the right
	// hook for HomeKit characteristic sync and Energy accumulation.
	OnTick func(name string, snap Snapshot)
```

In `tick`, after `p.State.RecordPoll(p.Name, snap)` (the line currently at ~243), add the `OnTick` call BEFORE the existing `if lastErr == nil` block:

```go
	p.State.RecordPoll(p.Name, snap)
	if p.OnTick != nil {
		p.OnTick(p.Name, snap)
	}
	if lastErr == nil {
		if p.Energy != nil {
			p.Energy.Tick(values, time.Now())
		}
		if p.OnPoll != nil {
			p.OnPoll(p.Name, snap)
		}
	}
```

- [ ] **Step 2: Update `cmd/breezyd/main.go` — split the fan-out**

Around line 193, the current `onPoll` closure dispatches to both `handler.SyncHomekit` and `handler.PushHub.Notify`. Split it. Replace lines 193-203 with:

```go
	onPoll := func(name string, snap Snapshot) {
		// testOnPollHook is the run-level test seam: when a test sets it,
		// it fires synchronously on every successful poll tick (with handler
		// attached so wiring-order assertions can read handler.Pollers).
		// Production leaves it nil and incurs zero cost.
		if hook := testOnPollHook; hook != nil {
			hook(handler, name, snap)
		}
		handler.SyncHomekit(name, snap)
	}
	onTick := func(name string, snap Snapshot) {
		// PushHub.Notify must fire on EVERY tick (success or failure) so
		// the dashboard's $lastPollAge / $stale signals advance under
		// sustained UDP timeouts (SPECIFICATION-web.md "Card states").
		handler.PushHub.Notify(name, snap)
	}
```

Then update the `startPollers` call (around line 204) to pass `onTick`:

```go
	pollers, schedulers, pollersWg, startPollerGoroutines := startPollers(
		rootCtx, devices.Snapshot(), cfg.Daemon.PollInterval,
		stateDir, state, metrics, onPoll, onTick,
		handler.scheduleDial, memClients,
	)
```

Update the `startPollers` function signature (line 375) to accept `onTick`:

```go
func startPollers(
	parent context.Context,
	devices map[string]DeviceConfig,
	interval time.Duration,
	stateDir string,
	state *State,
	metrics *Metrics,
	onPoll func(name string, snap Snapshot),
	onTick func(name string, snap Snapshot),
	scheduleDialFor func(name string) func(ctx context.Context) (breezy.DeviceClient, HandlerClient, error),
	memClients map[string]*breezy.MemClient,
) (map[string]*Poller, map[string]*Scheduler, *sync.WaitGroup, func()) {
```

And inside the loop where the `Poller` is constructed (around line 405-419), set `OnTick`:

```go
		p := &Poller{
			Name:     devName,
			IP:       d.IP,
			DeviceID: d.ID,
			Password: d.Password,
			Interval: interval,
			State:    state,
			ReadIDs:  defaultReadIDs(),
			OnError: func(n, kind string) {
				metrics.RecordPollError(n, devID, kind)
				slog.Debug("poll error", "device", n, "kind", kind)
			},
			OnPoll: onPoll,
			OnTick: onTick,
			Energy: tr,
		}
```

- [ ] **Step 3: Update `TestPoller_PostTickHooksGatedOnSuccess`**

The existing test at `cmd/breezyd/poller_test.go:888` pins that `OnPoll` and `Energy.Tick` don't fire on failure. The test's docstring claims this protects PushHub from "stale card" patches — that intent is now obsolete (we explicitly want PushHub to receive failed ticks). Update the docstring AND add an `OnTick` assertion:

Replace the function body (and its docstring) with:

```go
// TestPoller_PostTickHooksGatedOnSuccess pins the contract for the
// poller's per-tick hooks. After the SSE-on-failure addendum:
//   - OnPoll  : success-only (HomeKit characteristic sync; stale data
//               there would mark a healthy device wrong).
//   - Energy  : success-only (would corrupt daily/lifetime kWh
//               accumulators with stale Values).
//   - OnTick  : every tick — the dashboard's $lastPollAge / $stale
//               signals must advance even when polls are timing out.
func TestPoller_PostTickHooksGatedOnSuccess(t *testing.T) {
	is := is.New(t)
	state := NewState()
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()

	fc := &fakeClient{err: errors.New("read failed")}

	var mu sync.Mutex
	var onPollCalls, onTickCalls int

	p := &Poller{
		Name:     "p",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001, 0x00B9},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		Energy: tr,
		OnPoll: func(name string, snap Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			onPollCalls++
		},
		OnTick: func(name string, snap Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			onTickCalls++
		},
	}

	p.tick(context.Background())

	// Snapshot must record the failure.
	snap, ok := state.Get("p")
	is.True(ok)
	is.True(snap.LastErr != nil)

	// Energy.Tick must not have been called (LastTick still zero).
	tr.mu.Lock()
	is.True(tr.LastTick.IsZero()) // Energy accumulation must not run on a failed tick
	tr.mu.Unlock()

	mu.Lock()
	defer mu.Unlock()
	is.Equal(onPollCalls, 0) // OnPoll must not run on a failed tick (HomeKit sync)
	is.Equal(onTickCalls, 1) // OnTick MUST run on every tick (dashboard SSE)
}
```

- [ ] **Step 4: Add `TestPoller_OnTickFiresOnEveryTick`**

Insert immediately after `TestPoller_PostTickHooksGatedOnSuccess`:

```go
// TestPoller_OnTickFiresOnEveryTick pins that OnTick is called for both
// success and failure ticks, in order, with the recorded Snapshot. This
// is what powers the dashboard's stale-class fan-out under sustained
// UDP timeouts (SPECIFICATION-web.md "Card states").
func TestPoller_OnTickFiresOnEveryTick(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{0x0001: {1}}}

	var mu sync.Mutex
	var snaps []Snapshot

	p := &Poller{
		Name:     "p",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		OnTick: func(name string, snap Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			snaps = append(snaps, snap)
		},
	}

	// Tick 1: success.
	p.tick(context.Background())

	// Tick 2: failure.
	fc.mu.Lock()
	fc.err = errors.New("transient")
	fc.mu.Unlock()
	p.tick(context.Background())

	// Tick 3: success again.
	fc.mu.Lock()
	fc.err = nil
	fc.mu.Unlock()
	p.tick(context.Background())

	mu.Lock()
	defer mu.Unlock()
	is.Equal(len(snaps), 3)        // every tick fires OnTick
	is.NoErr(snaps[0].LastErr)     // first tick was a success
	is.True(snaps[1].LastErr != nil) // second tick was a failure
	is.NoErr(snaps[2].LastErr)     // third tick was a success
}
```

- [ ] **Step 5: Update `SPECIFICATION-daemon.md` "Push hub (poll → SSE fan-out)" section**

The current text says producers include "the poller's `OnPoll` hook." Update to reflect that the hub is wired to `OnTick`, not `OnPoll`. Find the line near line 535:

```
that backs the dashboard's SSE stream. Producers (the poller's `OnPoll`
hook, action handlers' post-write paths)
```

Replace with:

```
that backs the dashboard's SSE stream. Producers (the poller's `OnTick`
hook — every tick, success or failure — and action handlers' post-write paths)
```

- [ ] **Step 6: Run targeted tests**

Run: `go test ./cmd/breezyd -run "TestPoller_PostTickHooksGatedOnSuccess|TestPoller_OnTickFiresOnEveryTick" -v`

Expected: both PASS.

- [ ] **Step 7: Run full poller suite + `just check`**

Run: `go test ./cmd/breezyd -run TestPoller -v && just check`

Expected: green. Watch for any test that builds a Poller without setting `OnTick` and depends on it being unused — there shouldn't be any (the field is optional with a nil-check), but confirm.

- [ ] **Step 8: Commit**

```bash
git add cmd/breezyd/poller.go cmd/breezyd/main.go cmd/breezyd/poller_test.go SPECIFICATION-daemon.md
git commit -m "$(cat <<'EOF'
fix(daemon): push to dashboard on every tick, not just successes (refs #178)

Splits Poller.OnPoll into two hooks:
- OnPoll (success-only): SyncHomekit, Energy.Tick. Avoids HomeKit
  churn on transient failures and corrupted energy counters.
- OnTick (every tick): PushHub.Notify. Dashboard's $lastPollAge and
  $stale signals advance under sustained UDP timeouts so the
  data-class:stale="$stale" toggle fires per SPECIFICATION-web.md
  "Card states".

This is the second half of the #178 fix. Task 1 corrected the
cache-side LastPoll semantics; without this commit the metric-side
behavior was right but the user-visible dashboard never learned.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Un-skip Playwright stale-class test

**Goal:** `tests/ui/dashboard.spec.ts:556` runs (no longer `test.skip`) and passes against the fixed daemon.

**Files:**
- Modify: `tests/ui/dashboard.spec.ts:544-569`

**Acceptance Criteria:**
- [ ] The stale-class test is `test(...)`, not `test.skip(...)`.
- [ ] The multiline justification comment that pointed at issue #178 is replaced with a one-line spec reference.
- [ ] `just test-ui` passes including this test.

**Verify:** `just test-ui` (or `pnpm --dir tests/ui test --grep "stale class"`) → PASS.

**Steps:**

- [ ] **Step 1: Replace the skipped block in `tests/ui/dashboard.spec.ts`**

The current lines 544-569 (the comment block + `test.skip`):

```ts
  // Skipped: surfaced a deeper bug that is out of scope for #135.
  // The daemon's poller updates LastPoll on EVERY tick, including
  // failed ones (cmd/breezyd/poller.go:181 and :221), so under
  // simulateUDPTimeout the age never grows past one poll interval and
  // the card never crosses the 3×interval stale threshold. Spec
  // SPECIFICATION-web.md "Card states" describes stale as "no
  // successful poll" — the fix is to preserve LastPoll on failed
  // ticks (or split into LastSuccessfulPoll) so age reflects time
  // since last *successful* poll. Filed as a follow-up; once landed,
  // un-skip and adjust the timeout to ~3×poll_interval + slack.
  // The 3×poll-interval derivation itself is verified by the Go test
  // TestBuildView_StaleWindow_DerivesFromPollInterval.
  test.skip("stale class applied via signal patch preserves card identity", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    await card.evaluate((el) => { (el as HTMLElement).dataset.testTag = "marker-1"; });
    await simulateUDPTimeout(DEVICE, true);
    try {
      await expect(card).toHaveClass(/stale/, { timeout: 8_000 });
      const stillTagged = await card.evaluate((el) => (el as HTMLElement).dataset.testTag);
      expect(stillTagged).toBe("marker-1");
    } finally {
      await simulateUDPTimeout(DEVICE, false);
    }
  });
```

becomes:

```ts
  // Verifies SPECIFICATION-web.md "Card states": after the stale window
  // (3×poll_interval = 3s with the test daemon's poll_interval=1s) of
  // failed polls, the card gets the stale class via signal patch
  // without DOM replacement (data-testTag survives).
  test("stale class applied via signal patch preserves card identity", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    await card.evaluate((el) => { (el as HTMLElement).dataset.testTag = "marker-1"; });
    await simulateUDPTimeout(DEVICE, true);
    try {
      await expect(card).toHaveClass(/stale/, { timeout: 8_000 });
      const stillTagged = await card.evaluate((el) => (el as HTMLElement).dataset.testTag);
      expect(stillTagged).toBe("marker-1");
    } finally {
      await simulateUDPTimeout(DEVICE, false);
    }
  });
```

The `{ timeout: 8_000 }` is unchanged — already exceeds 3×1s + slack. The test body is otherwise unchanged.

- [ ] **Step 2: Run `just test-ui`**

Run: `just test-ui`

Expected: all Playwright specs pass, including the previously-skipped `stale class applied via signal patch preserves card identity`.

If it fails, the most likely cause is timing: the dashboard polls `/ui/devices/.../sse` and the stale class is patched via SSE. Verify the daemon is actually polling (check stdout) and that `simulateUDPTimeout(DEVICE, true)` injected the timeout. The `8_000` ms timeout should be ample.

- [ ] **Step 3: Commit**

```bash
git add tests/ui/dashboard.spec.ts
git commit -m "$(cat <<'EOF'
test(ui): un-skip stale-class card-identity test (closes #178)

The poller fix landed in the prior commits — Snapshot.LastPoll now
reflects the most recent successful poll, so the dashboard's
3×poll-interval stale gate fires under sustained simulateUDPTimeout
as SPECIFICATION-web.md "Card states" describes.

Strips the multiline justification comment that pointed at #178 and
replaces it with a one-line spec reference.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Push, open PR, enable auto-merge

**Goal:** PR open against `main`, full pre-push gate green, auto-merge enabled (per the user's standing preference).

**Files:** none — this is git/gh plumbing.

**Acceptance Criteria:**
- [ ] `just check-all` passes locally before push.
- [ ] PR is open with a body that summarises the change and references issue #178.
- [ ] Auto-merge (squash) is enabled.

**Verify:** `gh pr view --json url,autoMergeRequest` shows the PR URL and `autoMergeRequest != null`.

**Steps:**

- [ ] **Step 1: Run the full pre-push gate**

Run: `just check-all`

Expected: green. This gate runs lint + fast tests + race + Playwright + templ-drift.

If anything fails, fix it before pushing. Do NOT skip hooks.

- [ ] **Step 2: Push the branch**

If still on `main` locally, create a feature branch first:

```bash
git checkout -b fix/178-poller-laststpoll-failed-ticks
git push -u origin fix/178-poller-laststpoll-failed-ticks
```

If already on a feature branch (e.g. brainstorming created a worktree), just push.

- [ ] **Step 3: Open the PR**

```bash
gh pr create --title "fix(daemon): preserve Snapshot.LastPoll across failed ticks (closes #178)" --body "$(cat <<'EOF'
## Summary

- Both failure branches in `cmd/breezyd/poller.go::tick` now preserve the prior snapshot's `LastPoll` and `Values`. `LastPoll` reflects the most recent **successful** poll, which is what the dashboard's 3×poll-interval stale gate (per `SPECIFICATION-web.md` "Card states") and the `breezyd_last_poll_timestamp` Prometheus alert pattern (per `SPECIFICATION-daemon.md`) require.
- Updates `Snapshot.LastPoll`'s doc comment and the daemon spec's failed-poll-cache paragraph to match.
- Un-skips the Playwright `stale class applied via signal patch preserves card identity` test, which was waiting on this fix (originally skipped in PR #177).

Approach A from issue #178 — semantic shift on `LastPoll`, no schema change. JSON field name (`last_poll`) and metric name (`breezyd_last_poll_timestamp`) unchanged. Spec at `docs/superpowers/specs/2026-05-09-poller-lastpoll-failed-ticks-design.md`.

## Test plan

- [x] `go test ./cmd/breezyd -run TestPoller -v` (covers updated + new tests)
- [x] `just check`
- [x] `just check-all` (race + Playwright)
- [x] `just test-ui` (un-skipped stale-class test passes)

Closes #178.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Enable auto-merge (squash)**

```bash
PR_NUM=$(gh pr view --json number -q .number)
gh pr merge "$PR_NUM" --squash --auto
```

- [ ] **Step 5: Report URL to user**

Run: `gh pr view --json url -q .url`

Expected output: a github.com PR URL — share it as the final message.

---

## Self-review

**Spec coverage:**
- ✅ Approach A (preserve `LastPoll`) — Task 1 step 5.
- ✅ `Values` carry-forward on dial-failure — Task 1 step 5.
- ✅ Doc comment update — Task 2 step 1.
- ✅ `SPECIFICATION-daemon.md` paragraph extension — Task 2 step 2.
- ✅ Go test for read-failure path — Task 1 step 1 (existing test updated).
- ✅ Go test for dial-failure path — Task 1 step 2 (new test).
- ✅ Go test for success-path resumption — Task 1 step 3 (new test).
- ✅ Playwright un-skip — Task 3.
- ✅ Edge case "first tick fails" — covered by `prev.LastPoll` zero default; called out in spec.

**Placeholder scan:** no TBD/TODO/etc. All test code and replacement code is fully written.

**Type consistency:** `Poller.Now` (line 105 of poller.go) is `func() time.Time` — matches every test's `Now: func() time.Time { return clock }`. `prev.Values` is `map[breezy.ParamID][]byte` — matches the existing literal at line 188. `prev.LastPoll` is `time.Time` — matches the existing literal. No drift.
