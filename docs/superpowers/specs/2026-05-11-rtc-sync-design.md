# Daily RTC sync — design

Date: 2026-05-11
Status: Designed.

## Goal

The device's panel-display clock stays correct across DST transitions, manual battery swaps, RTC drift, and any other clock skew. The daemon writes the device's RTC once a day at a fixed local time, plus once shortly after daemon startup.

## Why now

The DST fix (#200) made the daemon's scheduler DST-aware, but the device itself has no DST awareness — its RTC is raw wall-clock. After each spring-forward / fall-back the device's panel display drifts by 1 hour. A user reading "01:30" off the panel during DST week sees the wrong time even though the daemon's writes still fire at the right moments. A daily sync closes that loop.

Also addresses long-term drift (RTC oscillator inaccuracy: typically ±20 ppm = ~10 min/year), battery-replacement reset (CR2032 cell at param `0x24`), and any one-off corruption from power-cycle.

## Decisions

| Decision | Choice |
|---|---|
| Cadence | **Daily at 04:00 local time.** Past both DST transitions (02:00); past midnight rollover; outside typical user hours. |
| Initial sync | **~30s after the per-device goroutine starts.** Gives the poller time for its initial reads before the sync write competes for the UDP lock. |
| TZ semantics | **Local wall-clock** (whatever `time.Now()` returns with `time.Local`). Same convention the scheduler uses for At-times. |
| Persistence | **None.** State is fully derivable from `time.Now()`; daemon restart re-establishes the cycle with the initial-sync covering the gap. |
| Failure handling | Log + continue. No retry within the day; next 04:00 retries naturally. No UI surface. |
| Opt-in / configurability | **Always-on, no config knob.** YAGNI; the cost of a wrong panel clock is small, the cost of running a no-op write daily is negligible. |
| Read-and-diff | **No.** Blind write is cheaper than poll-then-diff (2 fewer UDP reads per cycle) and equally correct since the device just stores what we send. |

## Architecture

One new file: `cmd/breezyd/rtc_sync.go`. One struct + one goroutine per configured device.

```go
// RTCSyncer keeps the device's hardware clock aligned with the daemon's
// local time. Fires once shortly after startup, then daily at 04:00.
// The device has no DST or timezone awareness — its RTC is raw wall-
// clock — so without this, the panel display drifts after each DST
// transition, battery replacement, or long-term oscillator drift.
type RTCSyncer struct {
    Device  string
    Dial    func(ctx context.Context) (rc breezy.DeviceClient, raw HandlerClient, err error)
    LockUDP func() func()
    Now     func() time.Time // test seam; nil → time.Now
}

func (r *RTCSyncer) Run(ctx context.Context) { /* sleep-then-sync loop */ }

func (r *RTCSyncer) now() time.Time {
    if r.Now != nil { return r.Now() }
    return time.Now()
}
```

The loop:

```go
// Initial sync after a short delay.
select {
case <-ctx.Done():
    return
case <-time.After(initialDelay):  // 30 * time.Second
}
r.syncOnce(ctx)

// Daily at 04:00 thereafter.
for {
    wait := untilNext(r.now(), syncHour)  // syncHour = 4
    select {
    case <-ctx.Done():
        return
    case <-time.After(wait):
    }
    r.syncOnce(ctx)
}
```

`untilNext(now time.Time, hour int) time.Duration` returns the duration until the next occurrence of HH:00:00 local. If `now.Hour() < hour`, that's today at HH:00; otherwise tomorrow at HH:00. Computed via `time.Date(year, month, day, hour, 0, 0, 0, time.Local).Sub(now)`, adding 24h if non-positive.

`syncOnce(ctx)`:
1. Acquire UDP lock via `r.LockUDP()` and `defer unlock()`. (Same per-device serialisation the scheduler uses.)
2. Bound the operation: `cctx, cancel := context.WithTimeout(ctx, syncTimeout)` where `syncTimeout = 5 * time.Second` (matches scheduler's `fireTimeout`).
3. `client, raw, err := r.Dial(cctx)` — same Dial path as scheduler.
4. If dial fails: `slog.Warn("rtc sync: dial failed", ...)`, return.
5. `defer raw.Close()`.
6. `err = breezy.SetRTC(cctx, client, r.now())`.
7. If `err != nil`: `slog.Warn("rtc sync: write failed", ...)`. Don't distinguish ErrAuth — the warning is enough; spec doesn't surface RTC failures to the user.
8. Else: `slog.Info("rtc sync: wrote", "device", r.Device, "at", r.now().Format(time.RFC3339))`.

## Wiring

In `cmd/breezyd/main.go::run()` — specifically the per-device wiring loop around lines 432-446 — extend the existing pattern. After the `Scheduler` is constructed and added to `startFns`:

```go
syncer := &RTCSyncer{
    Device:  devName,
    LockUDP: p.LockUDP,
    Dial:    scheduleDialFor(devName),
}

startFns = append(startFns, func() {
    wg.Add(3)  // was 2 — now Poller + Scheduler + RTCSyncer
    go func() { defer wg.Done(); p.Run(parent) }()
    go func() { defer wg.Done(); sch.Run(parent) }()
    go func() { defer wg.Done(); syncer.Run(parent) }()
})
```

(The existing `startFns = append(startFns, func() { wg.Add(2); ... })` block becomes the above; the previous two-goroutine launch is replaced with the three-goroutine version.) Reuses `p.LockUDP` (the per-device UDP lock from `Poller`) and `scheduleDialFor(devName)` (the per-device dial closure factory that `Scheduler` also uses).

No new return value from the wiring function; the `RTCSyncer` lives only inside the closure.

## State

**None persisted.** Daemon restart re-establishes the cycle:

- Initial sync fires ~30s after restart, regardless of clock time.
- Next daily sync computed from the *current* local time at startup.

Worst case after restart: if the daemon restarts at 04:01 (one minute after the daily sync would have fired), the next sync is at 04:00 tomorrow — but the initial sync 30s after startup already covered the day's drift. No correctness gap.

## Tests

New file `cmd/breezyd/rtc_sync_test.go`.

### Test 1: `TestRTCSync_UntilNext`

Table-driven, tests `untilNext`:

```go
cases := []struct {
    name string
    now  time.Time
    hour int
    want time.Duration
}{
    {"midnight, target 04:00", atLocal(0, 0), 4, 4 * time.Hour},
    {"03:59, target 04:00", atLocal(3, 59), 4, time.Minute},
    {"04:00 exactly, target 04:00", atLocal(4, 0), 4, 24 * time.Hour},
    {"04:01, target 04:00", atLocal(4, 1), 4, 24*time.Hour - time.Minute},
    {"23:59, target 04:00", atLocal(23, 59), 4, 4*time.Hour + time.Minute},
}
```

`atLocal(h, m int) time.Time` returns a fixed-date time at HH:MM in `time.Local`.

### Test 2: `TestRTCSyncer_InitialSyncFires`

Uses a fake `Dial` that returns a capturing `schedFakeClient` (reuse from `scheduler_test.go`).

```go
fc := &schedFakeClient{}
syncer := &RTCSyncer{
    Device:  "playroom",
    Dial:    func(_ context.Context) (breezy.DeviceClient, HandlerClient, error) { return fc, schedFakeRaw{}, nil },
    LockUDP: func() func() { return func() {} },
    Now:     func() time.Time { return /* fixed time */ },
}

ctx, cancel := context.WithCancel(context.Background())
done := make(chan struct{})
go func() { syncer.Run(ctx); close(done) }()

// Wait for initial sync (uses a shorter initialDelay in the test).
// Pattern: override package var `rtcInitialDelay` from default 30s
// to e.g. 10ms in this test, restore via t.Cleanup.

// Assert SetRTC writes appear in fc.flatWrites().
```

Pin: initial sync writes the expected RTC values (params `0x006F` time + `0x0070` calendar).

### Test 3: `TestRTCSyncer_FailureDoesNotStopGoroutine`

Fake Dial returns an error. Assert:
- Initial sync logged a warning (not a crash).
- Goroutine is still alive (still consuming ctx.Done).
- A second mock-clock advance to 04:00 triggers another sync attempt.

Mocking time-after-time-after-time is the awkward part. Practical approach: also override `rtcInitialDelay` and have `untilNext` use the test's `Now` function (pass `now()` from the syncer everywhere instead of `time.Now()`).

To keep tests simple, structure `Run` so the sleep duration computation is testable in isolation (Test 1) and `syncOnce` is called the right number of times by the test, e.g. by exposing a hook:

```go
var rtcInitialDelay = 30 * time.Second  // package var, tests can shrink
```

Plus: `Run` calls `syncOnce` via a function-typed field on the receiver, defaulting to `r.syncOnce` — letting Test 3 inject a counting stub that asserts call sequencing.

Actual mechanism left to the implementer — the design constraint is "tests must verify both initial-sync and daily-sync behaviour without sleeping for 24h". A package var for the delays is the lightest-weight approach.

### Out-of-scope test cases

- Real DST transition. The DST math lives in `time.Date(...).Sub(now)`; the stdlib is correct. The test for the daemon's DST handling (#200) already covers the scheduler-side; the syncer's `untilNext` is a thin wrapper over `time.Date`.
- TZ change mid-day. Behaves correctly — `untilNext` recomputes on every loop iteration from `r.now()` which respects whatever `time.Local` is at that moment.
- 30-min DST zones (Lord Howe). Same — `time.Date` handles it.

## Doc updates

In `CLAUDE.md`, add a short subsection alongside the existing "Schedule system" and "Energy tracking" descriptions:

```
### Daily RTC sync (always on)

Each configured device runs an `RTCSyncer` goroutine that writes the
device's RTC (params 0x6F + 0x70) once shortly after daemon startup
and then daily at 04:00 local time. Closes the panel-display drift
introduced by DST transitions, battery replacement (CR2032 at 0x24),
and long-term oscillator drift. Per-device, no persisted state, no
configuration knob — the cycle is fully derived from time.Now() and
restarts cleanly on daemon restart. Failures (UDP timeout, auth) log
a warning and continue; next 04:00 retries naturally. See
`cmd/breezyd/rtc_sync.go`.
```

## Verification

- `just ci` green: vet + tests + race + asan + msan + Playwright + golangci-lint + templ-drift + admin-tag.
- Manual: deploy to a real device; verify the panel display matches wall clock within seconds of daemon startup; verify it stays correct after a `date -s` simulating DST transition.

## Risk

- **Daemon writes during DST transition window.** Mitigated by 04:00 sync time (2 hours after the 02:00 transition).
- **UDP lock contention with poller.** Mitigated by 30s initial delay (poller's typical poll interval is 30s; initial poll lands first).
- **Auth failure spam in logs.** A device with the wrong password gets one warn-log per day from RTC sync, plus the scheduler's own per-tick log if a schedule entry fires. Acceptable.

## Out of scope

- Configurable sync time, opt-in flag, interval-based mode. YAGNI; if any user needs them, the constant + the always-on wiring are both one-line changes.
- Surfacing last-sync time in dashboard or `/v1` status. RTC is invisible-when-it-works infrastructure.
- Read-then-diff before write. Costs 2 UDP reads per cycle for zero correctness gain.
- Detecting "device was offline at scheduled sync time" and triggering a sync on next reachability. The daily cadence plus the initial-sync-on-startup covers practical cases; a long offline gap (>24h) gets re-synced by the next 04:00 tick or by the next daemon-restart-initial-sync.
- Logging the *diff* between previous and new RTC values. Would require pre-reading the device clock (see above) — not worth the cost.
