# Scheduler DST handling — design

Date: 2026-05-10
Status: Designed.

## Goal

Replace the documented v1 limitation ("spring-forward skips an entry, fall-back fires twice") with deterministic, single-fire behaviour across both DST transitions. Minimum new concept; no relaxation of the existing **no-catch-up** rule.

## Why now

Vendor PDF + reverse-engineered param map (`docs/superpowers/specs/2026-05-03-param-map.md`) confirm the device has no DST awareness — RTC params `0x6F` (3-byte time) and `0x70` (4-byte calendar) are raw wall-clock with no UTC offset, no DST flag, no zone. The daemon owns scheduling; DST handling lives entirely in `cmd/breezyd/scheduler.go`.

## Background — current behaviour

`Scheduler.tick()` runs every minute. It computes `nowMinute = ScheduleTime(now.Hour()*60 + now.Minute())` from `time.Now()` in the daemon's local TZ and detects entries that crossed the half-open interval `(lastTick, nowMinute]`. The ticker (`time.NewTicker(1*time.Minute)`) is monotonic, so 60 monotonic seconds pass between ticks regardless of wall-clock jumps.

This produces two distinct DST artifacts:

- **Fall-back** (e.g. PT 2026-11-01: 02:00 PDT becomes 01:00 PST, repeating 01:00–01:59). At the first 01:30 PDT, the entry at minute-of-day 90 fires. The clock rolls back to 01:00 PST; window detection handles the wraparound and correctly skips entries until 01:30 PST, when `nowMinute = 90` and `lastTick = 89` again — entry at 90 fires a second time.
- **Spring-forward** (e.g. PT 2026-03-08: 02:00 PST becomes 03:00 PDT, skipping 02:00–02:59). For a daemon running through the transition, the 03:00 tick has `lastTick = 119` (01:59 PST) and `nowMinute = 180` (03:00 PDT) — entries in (119, 180] including any at 02:30 fire once at 03:00. This case is **already correct**. The only artifact is a daemon that *starts* during the missing hour: the first tick has `haveLastTick = false` and just records `lastTick`, silently losing any entries from that hour.

## Decisions

| Decision | Choice |
|---|---|
| Fall-back: which occurrence fires? | **First** (PDT in PT). Falls out of "did this entry already fire today" check. |
| Daemon-startup during missing hour | **Accept as residual edge case.** Matches the existing no-catch-up rule (spec §6); the running-daemon case already works. |
| State shape | New `firedAt map[ScheduleTime]time.Time` on `Scheduler`. |
| Persistence | Additive field on `persistedSchedule`; missing key in old files = empty map. No version bump. |

## Architecture

Add one field to `Scheduler` (in `cmd/breezyd/scheduler.go`):

```go
type Scheduler struct {
    // ... existing fields ...

    // firedAt records the timestamp of the most recent successful fire
    // for each entry's At-time. Used to suppress re-fires of the same
    // entry within the same local calendar day — the only case this
    // matters in practice is the fall-back DST transition, where an
    // entry's At-time appears twice on the wall clock.
    firedAt map[ScheduleTime]time.Time
}
```

## Tick-loop change

In the existing window-detection loop (lines ~387–397 of `scheduler.go`), gate entry consideration on the firedness check:

```go
for i, e := range entries {
    if !inWindow(e.At) {
        continue
    }
    if last, ok := s.firedAt[e.At]; ok && sameLocalDay(last, now) {
        continue
    }
    if latest == nil || dist(e.At) > dist(latest.At) {
        cp := e
        latest = &cp
        latestIdx = i
    }
}
```

Helper:

```go
// sameLocalDay reports whether a and b fall on the same local calendar
// day (Year + Month + Day in the daemon's local TZ). Used by the
// scheduler to suppress fall-back DST double-fires.
func sameLocalDay(a, b time.Time) bool {
    ya, ma, da := a.Date()
    yb, mb, db := b.Date()
    return ya == yb && ma == mb && da == db
}
```

`time.Time.Date()` returns the date in the time's location. Both `now` and `firedAt[e.At]` were observed via `s.now()` (which is `time.Now()` in production), so both are in local TZ — comparison is consistent.

The check is **only** needed in the tick's window-detection loop. The retry path doesn't need a parallel gate: `firedAt` is only set on success, and a retry can only exist when the most recent attempt failed (`s.retry` is cleared on success). So whenever `r != nil`, `firedAt[r.entry.At]` is either unset or stale from a prior day — the gate would never short-circuit. (Verified by tracing the four fall-back cases: success, fail-then-retry-success, fail-then-retry-abandon, and the spring-forward catches-up case. All correct without the extra gate.)

## Fire-post-success change

In `fire()` (lines ~438–445), inside the success branch, record the fire:

```go
if fireErr == nil {
    s.lastApply = &LastApply{At: e.At, Fired: now, OK: true}
    s.retry = nil
    if s.firedAt == nil {
        s.firedAt = make(map[ScheduleTime]time.Time)
    }
    s.firedAt[e.At] = now
    if err := s.save(); err != nil {
        slog.Warn("schedule: save after success failed", "device", s.Device, "err", err)
    }
    slog.Info("schedule: fired", ...)
    return
}
```

Only successful fires update `firedAt`. Auth failures, transient errors, and abandoned retries leave it untouched, so a retried entry that eventually succeeds is recorded once.

## Replace() clears firedAt

```go
func (s *Scheduler) Replace(enabled bool, entries []ScheduleEntry) error {
    // ... existing validate / sortEntries ...
    s.mu.Lock()
    defer s.mu.Unlock()
    s.enabled = enabled
    s.entries = cp
    s.lastApply = nil
    s.retry = nil
    s.firedAt = nil    // ← new
    if err := s.save(); err != nil {
        return fmt.Errorf("schedule: persist: %w", err)
    }
    return nil
}
```

Rationale: a fresh schedule should fire each entry on its first eligible tick. If the user removes and re-adds the same At-time, that's a deliberate user action; the new entry deserves a fresh start.

(Note: this means a user who toggles `enabled` off-and-on via Replace mid-day will re-fire today's already-fired entries. Acceptable given that the spec's no-catch-up rule already permits "deliberate-action" semantics on Replace.)

## Persistence

Extend `persistedSchedule`:

```go
type persistedSchedule struct {
    Version   int                          `json:"version"`
    Enabled   bool                         `json:"enabled"`
    Entries   []ScheduleEntry              `json:"entries"`
    LastApply *LastApply                   `json:"last_apply,omitempty"`
    FiredAt   map[ScheduleTime]time.Time   `json:"fired_at,omitempty"`
}
```

`encoding/json` serialises `map[ScheduleTime]time.Time` as a JSON object with stringified integer keys (e.g. `"fired_at": {"90": "2026-03-08T01:30:00-07:00"}`). Named integer types are supported as map keys since Go 1.7. `time.Time` marshals as RFC3339 with timezone offset, so the location survives the round-trip.

Existing `schedule_*.json` files lacking `fired_at` parse cleanly: Go's default unmarshal into a `nil` map is an empty map for read purposes. No version bump needed — additive field.

`Load()` reads `p.FiredAt` and assigns to `s.firedAt`:

```go
sortEntries(p.Entries)
s.enabled = p.Enabled
s.entries = p.Entries
s.lastApply = p.LastApply
s.firedAt = p.FiredAt    // ← new (nil if absent in old files; safe)
```

`save()` includes it:

```go
p := persistedSchedule{
    Version:   scheduleFileVersion,
    Enabled:   s.enabled,
    Entries:   s.entries,
    LastApply: s.lastApply,
    FiredAt:   s.firedAt,
}
```

The `omitempty` tag keeps the field absent in serialised JSON when `firedAt` is nil/empty, preserving the existing file shape for users who haven't yet had a fire.

## Map size

Bounded by `maxScheduleEntries = 24`. Old entries get overwritten when the same At-time fires again; never grows beyond the configured schedule's footprint. No explicit cleanup needed.

## Tests

Three new tests in `cmd/breezyd/scheduler_test.go`, all driving DST traversal via `s.Now` injection with `America/Los_Angeles` (the canonical real-world DST zone in Go's tzdata).

### TestScheduler_FallBackDeDup

Walk a fake clock through 2026-11-01 (DST end) in PT:
- Set schedule: enabled, entry `{At: 01:30, Action: "regeneration", Pct: 50}`.
- Tick at 2026-11-01 01:00 PDT → no fire.
- Tick at 2026-11-01 01:30 PDT → fires once. Assert `lastApply.At == 90`, `firedAt[90] == 01:30 PDT`.
- Tick at 2026-11-01 01:59 PDT → no fire.
- Tick at 2026-11-01 01:00 PST (the second 01:00) → no fire.
- Tick at 2026-11-01 01:30 PST → **must not fire**. Assert `lastApply` unchanged.
- Tick at 2026-11-01 02:00 PST → no fire.

### TestScheduler_SpringForwardRunningDaemon

Walk a fake clock through 2026-03-08 (DST start) in PT:
- Set schedule: enabled, entry `{At: 02:30, Action: "regeneration", Pct: 60}`.
- Tick at 2026-03-08 01:59 PST → no fire. `lastTick = 119`.
- Tick at 2026-03-08 03:00 PDT → fires once. Assert `lastApply.At == 150`, `firedAt[150] == 03:00 PDT`.
- Tick at 2026-03-08 03:01 PDT → no fire.

### TestScheduler_ReplaceClearsFiredAt

- Set schedule with entry at 08:00.
- Drive a tick at 08:00 — fires; `firedAt[480] = today 08:00`.
- Call `Replace(enabled=true, entries=[{At: 08:00, ...}])` (same entry, fresh state) at 08:30 same day.
- Assert `s.firedAt == nil`.
- Drive a tick at 08:30 same day — `inWindow` doesn't include 08:00 (it's behind the new `lastTick`), so no fire. (Confirms Replace's interaction with the existing window/lastTick rules.)

### Persistence regression: TestScheduler_Load_FiredAtRoundTrip

- Create a `persistedSchedule` with a populated `FiredAt` and a separate one without.
- Write each to a temp file.
- `Load()` each; assert the in-memory `firedAt` matches the original (or is nil for the without case).
- This guards against future serialiser drift.

## Doc updates

Replace the DST paragraph in both files:

**CLAUDE.md** (in the "Schedule system" section):
```
**DST handling.** Times are local wall-clock. The daemon honours basic
DST transitions:

- Spring-forward: an entry whose At-time falls in the missing hour
  fires once at the first tick after the skipped hour. Handled by the
  existing window-detection logic for a running daemon.
- Fall-back: an entry whose At-time falls in the repeated hour fires
  exactly once, at the first occurrence. Per-entry firedness tracking
  (the `firedAt` map on `Scheduler`) suppresses the second appearance.
- Residual edge case: if the daemon starts during the missing hour
  (spring-forward), entries inside that hour are silently skipped.
  Matches the no-catch-up rule.
- Non-1h-DST zones (e.g. Lord Howe's 30-min): the firedness check
  uses calendar-day comparison, so any DST offset de-duplicates
  correctly.
```

**`docs/superpowers/specs/2026-05-06-schedule-system-design.md`**: replace the existing "No DST adjustment" paragraph (it currently appears in the "Out of scope" section near the top) with the same text, moved to the appropriate place in the document.

## Verification

- `just test ./cmd/breezyd -run TestScheduler` — all green including the 3 new tests + 1 persistence test.
- `just ci` — full CI matrix green before push.
- Manual: render a Playwright dashboard against a fakedevice schedule with a fall-back-day entry; verify the "last apply" cell shows a single timestamp, not two.

## Risk

The change touches `tick()`, `fire()`, `Replace()`, `Load()`, `save()`, and `persistedSchedule`. Each touch is small; tests pin every invariant. The persistence is additive (no migration; old files parse cleanly).

Single residual edge case (daemon-start-during-missing-hour) is documented and matches existing spec language.

## Out of scope

- **Device-side RTC sync across DST.** The device's panel display drifts by one hour after each transition unless the user runs `breezy <name> rtc set <now>` manually. Separate concern; could be addressed by adding a daemon-side periodic RTC sync. Not in this PR.
- **Day-of-week scheduling.** Current schedule is daily-only. The firedness map's calendar-day comparison generalises trivially to "same week + same day-of-week" if day-of-week ever lands.
- **TZ change handling.** If the operator changes the daemon's TZ mid-day, `firedAt` entries from the old TZ may suppress fires in the new TZ that conceptually shouldn't be suppressed. Acceptable: TZ changes mid-day are unusual and the user can `Replace()` the schedule to reset.
