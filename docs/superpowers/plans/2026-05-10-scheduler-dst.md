# Scheduler DST handling — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land one commit on `fix/200-scheduler-dst` that adds per-entry firedness tracking to the scheduler, plus tests for both DST transitions, plus doc updates that replace the existing v1-limitation paragraph.

**Architecture:** A new `firedAt map[ScheduleTime]time.Time` field on `Scheduler` records the most recent successful fire per entry-At. The tick's window-detection loop skips entries whose `firedAt[e.At]` falls on the same local calendar day as `now`. The field persists alongside the rest of the schedule state (additive JSON field, no migration, no version bump) and clears on `Replace()` so a fresh schedule starts fresh.

**Tech Stack:** Go 1.26, `time` package (Go-native DST handling via tzdata), `matryer/is` for tests.

---

## Task 0: Branch off main (already done)

Branch `fix/200-scheduler-dst` already exists with the spec commit. Nothing to do — proceed to Task 1.

---

## Task 1: Implement DST de-dup with tests and doc updates

**Goal:** Single commit containing the helper, field, persistence wiring, tick gate, fire-success update, `Replace()` clear, four new tests, and prose updates in CLAUDE.md + the schedule-system design spec.

**Files:**
- Modify: `cmd/breezyd/scheduler.go`
- Modify: `cmd/breezyd/scheduler_test.go`
- Modify: `CLAUDE.md`
- Modify: `docs/superpowers/specs/2026-05-06-schedule-system-design.md`

**Acceptance Criteria:**
- [ ] `sameLocalDay(a, b time.Time) bool` exists in scheduler.go.
- [ ] `Scheduler` has a `firedAt map[ScheduleTime]time.Time` field, persisted in `persistedSchedule.FiredAt` with `omitempty`.
- [ ] `Load()` reads `p.FiredAt` into `s.firedAt`. `save()` writes it.
- [ ] `tick()`'s window-detection skips entries where `firedAt[e.At]` is the same local day as `now`.
- [ ] `fire()` on success sets `firedAt[e.At] = now`.
- [ ] `Replace()` sets `s.firedAt = nil`.
- [ ] `TestScheduler_FallBackDeDup` passes.
- [ ] `TestScheduler_SpringForwardRunningDaemon` passes.
- [ ] `TestScheduler_ReplaceClearsFiredAt` passes.
- [ ] `TestScheduler_FiredAt_PersistsAcrossLoad` passes.
- [ ] CLAUDE.md schedule DST paragraph replaced with the new behaviour.
- [ ] `docs/superpowers/specs/2026-05-06-schedule-system-design.md` "No DST adjustment" entry replaced.
- [ ] `just ci` green.

**Verify:** `just ci` → all green (vet + go test + race + asan + msan + Playwright + golangci-lint + templ-drift + admin-tag).

**Steps:**

- [ ] **Step 1: Add the `sameLocalDay` helper to `cmd/breezyd/scheduler.go`.**

Place it just below `sortEntries` (around line 280):

```go
// sameLocalDay reports whether a and b fall on the same local calendar
// day (Year + Month + Day in the daemon's local TZ). Used by the
// scheduler's firedAt check to suppress fall-back DST double-fires.
func sameLocalDay(a, b time.Time) bool {
	ya, ma, da := a.Date()
	yb, mb, db := b.Date()
	return ya == yb && ma == mb && da == db
}
```

- [ ] **Step 2: Add the `firedAt` field to `Scheduler`.**

Modify the struct around line 106. Place the new field immediately after `retry`:

```go
type Scheduler struct {
	Device   string
	StateDir string

	LockUDP func() func()

	Dial func(ctx context.Context) (rc breezy.DeviceClient, raw HandlerClient, err error)

	Now func() time.Time

	mu           sync.Mutex
	enabled      bool
	entries      []ScheduleEntry
	lastApply    *LastApply
	retry        *retryState
	// firedAt records the timestamp of the most recent successful fire
	// for each entry's At-time. Used to suppress re-fires of the same
	// entry within the same local calendar day — the case that matters
	// is the fall-back DST transition, where an entry's At-time appears
	// twice on the wall clock.
	firedAt      map[ScheduleTime]time.Time
	lastTick     ScheduleTime
	haveLastTick bool
}
```

- [ ] **Step 3: Extend `persistedSchedule` to include `FiredAt`.**

Modify the struct around line 175:

```go
// persistedSchedule is the on-disk JSON shape.
type persistedSchedule struct {
	Version   int                          `json:"version"`
	Enabled   bool                         `json:"enabled"`
	Entries   []ScheduleEntry              `json:"entries"`
	LastApply *LastApply                   `json:"last_apply,omitempty"`
	FiredAt   map[ScheduleTime]time.Time   `json:"fired_at,omitempty"`
}
```

`omitempty` keeps old/empty files unchanged in shape.

- [ ] **Step 4: Wire `FiredAt` through `Load()` and `save()`.**

In `Load()` (around line 235), after `s.lastApply = p.LastApply`:

```go
sortEntries(p.Entries)
s.enabled = p.Enabled
s.entries = p.Entries
s.lastApply = p.LastApply
s.firedAt = p.FiredAt
```

In `save()` (around line 240), include the field:

```go
p := persistedSchedule{
	Version:   scheduleFileVersion,
	Enabled:   s.enabled,
	Entries:   s.entries,
	LastApply: s.lastApply,
	FiredAt:   s.firedAt,
}
```

- [ ] **Step 5: Clear `firedAt` in `Replace()`.**

In `Replace()` (around line 267):

```go
s.enabled = enabled
s.entries = cp
s.lastApply = nil
s.retry = nil
s.firedAt = nil
if err := s.save(); err != nil {
	return fmt.Errorf("schedule: persist: %w", err)
}
return nil
```

- [ ] **Step 6: Gate the tick's window-detection on `firedAt`.**

In `tick()`, two edits — the snapshot block at the top and the window-detection loop. See the "Concurrency" note below for full code.

Snapshot block (around line 301) — extend to copy `firedAt`:

```go
s.mu.Lock()
enabled := s.enabled
entries := append([]ScheduleEntry(nil), s.entries...)
haveLastTick := s.haveLastTick
lastTick := s.lastTick
firedAt := make(map[ScheduleTime]time.Time, len(s.firedAt))
for k, v := range s.firedAt {
	firedAt[k] = v
}
s.mu.Unlock()
```

Window-detection loop (around lines 388–397) — gate on local `firedAt`:

```go
if !retryFired {
	for i, e := range entries {
		if !inWindow(e.At) {
			continue
		}
		if last, ok := firedAt[e.At]; ok && sameLocalDay(last, now) {
			continue
		}
		if latest == nil || dist(e.At) > dist(latest.At) {
			cp := e
			latest = &cp
			latestIdx = i
		}
	}
}
```

**Concurrency**: `s.firedAt` is shared state. `tick()` runs single-goroutine via `Run()`, and other writers (`fire()`, `Replace()`, `Load()`) all hold `s.mu`. To match the pattern already used for `s.entries` (snapshotted under mu at the top of `tick()`), snapshot `firedAt` the same way.

Modify the initial mutex-protected snapshot block at the top of `tick()` (around line 301) to also copy `firedAt`:

```go
s.mu.Lock()
enabled := s.enabled
entries := append([]ScheduleEntry(nil), s.entries...)
haveLastTick := s.haveLastTick
lastTick := s.lastTick
firedAt := make(map[ScheduleTime]time.Time, len(s.firedAt))
for k, v := range s.firedAt {
	firedAt[k] = v
}
s.mu.Unlock()
```

Then the window-detection loop reads the local `firedAt` (not `s.firedAt`):

```go
if !retryFired {
	for i, e := range entries {
		if !inWindow(e.At) {
			continue
		}
		if last, ok := firedAt[e.At]; ok && sameLocalDay(last, now) {
			continue
		}
		if latest == nil || dist(e.At) > dist(latest.At) {
			cp := e
			latest = &cp
			latestIdx = i
		}
	}
}
```

- [ ] **Step 7: Record the fire in `firedAt` on success.**

In `fire()` (around line 438), in the success branch:

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
	slog.Info("schedule: fired", "device", s.Device, "at", e.At.String(), "action", e.Action, "pct", e.Pct)
	return
}
```

`fire()` already holds `s.mu` for the entire success/error branch (acquired around line 435) — so this write is safe.

- [ ] **Step 8: Run the existing scheduler tests to confirm no regression.**

Run:
```bash
go test ./cmd/breezyd -run TestSchedul -v
```

Expected: all existing tests pass. The change should be transparent to entries that fire only once per day in static TZ tests.

If any existing test fails, diagnose before proceeding. Likely culprit: a test that fires the same At-time twice in one simulated day, which previously worked and now gets de-duped. If found, inspect — if the test was exercising real "fire twice for a reason" behaviour, the fix is wrong; if it was incidentally firing twice, update the test.

- [ ] **Step 9: Add `TestScheduler_FallBackDeDup`.**

Append to `cmd/breezyd/scheduler_test.go`. Helper first — only one needed; tests call `atUTC(...).In(pt)` to produce an unambiguous wall-clock moment in PT (this avoids `time.Date(... ptLocation)`'s default-to-later behaviour for ambiguous times and gracefully handles non-existent times):

```go
// atUTC builds a UTC moment for DST tests. Construct in UTC, then
// .In(ptLocation) at the call site — that gets unambiguous semantics
// for both the fall-back repeated hour and the spring-forward missing
// hour, where time.Date in a DST-affected location is ambiguous.
func atUTC(year, month, day, hour, minute int) time.Time {
	return time.Date(year, time.Month(month), day, hour, minute, 0, 0, time.UTC)
}
```

Then the test. Fall-back day in PT 2026 is **2026-11-01** (first Sunday of November); 02:00 PDT becomes 01:00 PST. PDT is UTC-7; PST is UTC-8. So:
- 01:30 PDT = 08:30 UTC = the FIRST occurrence
- 01:00 PST = 09:00 UTC = the moment of fall-back
- 01:30 PST = 09:30 UTC = the SECOND occurrence

```go
// TestScheduler_FallBackDeDup verifies an entry whose At-time falls in
// the repeated hour fires exactly once (the first occurrence). Closes
// the documented v1 limitation.
func TestScheduler_FallBackDeDup(t *testing.T) {
	is := is.New(t)
	pt, err := time.LoadLocation("America/Los_Angeles")
	is.NoErr(err)

	s, fc := newSchedTest(t)
	err = s.Replace(true, []ScheduleEntry{
		{At: 90, Action: "regeneration", Pct: 50}, // 01:30
	})
	is.NoErr(err)

	// Pre-fall-back tick: prime lastTick at 00:59 PDT.
	s.tick(context.Background(), atUTC(2026, 11, 1, 7, 59).In(pt))

	// First 01:30 PDT: should fire.
	s.tick(context.Background(), atUTC(2026, 11, 1, 8, 30).In(pt))
	is.Equal(len(fc.flatWrites()), 3) // Power(true) + SetMode + SetSpeedManual

	// Walk through 01:59 PDT.
	s.tick(context.Background(), atUTC(2026, 11, 1, 8, 59).In(pt))

	// Fall-back moment: 01:00 PST (UTC 09:00). No entry in window.
	s.tick(context.Background(), atUTC(2026, 11, 1, 9, 0).In(pt))

	// Second 01:30 (now PST). MUST NOT fire.
	s.tick(context.Background(), atUTC(2026, 11, 1, 9, 30).In(pt))
	is.Equal(len(fc.flatWrites()), 3) // still 3 — no double-fire

	// And 02:00 PST for good measure.
	s.tick(context.Background(), atUTC(2026, 11, 1, 10, 0).In(pt))
	is.Equal(len(fc.flatWrites()), 3)
}
```

- [ ] **Step 10: Run TestScheduler_FallBackDeDup.**

Run:
```bash
go test ./cmd/breezyd -run TestScheduler_FallBackDeDup -v
```

Expected: PASS. If FAIL, diagnose:
- If `len(flatWrites)` is 0 after the first 01:30 tick: the fire path didn't run; check `tick()`'s window logic against the lastTick / nowMinute values at that point.
- If `len(flatWrites)` is 6 at the second 01:30: the firedAt gate isn't firing; check the snapshot in step 6 and the per-iteration check.

- [ ] **Step 11: Add `TestScheduler_SpringForwardRunningDaemon`.**

Spring-forward day in PT 2026 is **2026-03-08** (second Sunday of March); 02:00 PST becomes 03:00 PDT. PST is UTC-8; PDT is UTC-7. So:
- 01:59 PST = 09:59 UTC
- 03:00 PDT = 10:00 UTC (one minute of monotonic time later)

```go
// TestScheduler_SpringForwardRunningDaemon verifies the running-daemon
// case for spring-forward: an entry in the missing hour fires once at
// the first tick after the skipped hour. The existing window-detection
// already handles this, but the test pins it explicitly.
func TestScheduler_SpringForwardRunningDaemon(t *testing.T) {
	is := is.New(t)
	pt, err := time.LoadLocation("America/Los_Angeles")
	is.NoErr(err)

	s, fc := newSchedTest(t)
	err = s.Replace(true, []ScheduleEntry{
		{At: 150, Action: "regeneration", Pct: 60}, // 02:30 (in the missing hour)
	})
	is.NoErr(err)

	// Pre-jump tick at 01:59 PST. lastTick becomes 119.
	s.tick(context.Background(), atUTC(2026, 3, 8, 9, 59).In(pt))

	// Next tick: 03:00 PDT (one wall-clock minute later by the user,
	// but 01:01 elapsed UTC). Window = (119, 180] includes 150.
	s.tick(context.Background(), atUTC(2026, 3, 8, 10, 0).In(pt))
	is.Equal(len(fc.flatWrites()), 3)

	// 03:01 PDT: no fire.
	s.tick(context.Background(), atUTC(2026, 3, 8, 10, 1).In(pt))
	is.Equal(len(fc.flatWrites()), 3)
}
```

- [ ] **Step 12: Add `TestScheduler_ReplaceClearsFiredAt`.**

```go
// TestScheduler_ReplaceClearsFiredAt verifies that Replace() resets the
// firedAt map so a re-added entry can fire again the same day.
func TestScheduler_ReplaceClearsFiredAt(t *testing.T) {
	is := is.New(t)
	pt, err := time.LoadLocation("America/Los_Angeles")
	is.NoErr(err)

	s, fc := newSchedTest(t)
	err = s.Replace(true, []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60}, // 08:00
	})
	is.NoErr(err)

	// Fire the 08:00 entry.
	s.tick(context.Background(), atUTC(2026, 5, 6, 14, 59).In(pt)) // 07:59 PDT
	s.tick(context.Background(), atUTC(2026, 5, 6, 15, 0).In(pt))  // 08:00 PDT
	is.Equal(len(fc.flatWrites()), 3)

	// firedAt now has 480 → 08:00.
	s.mu.Lock()
	is.True(s.firedAt != nil)
	is.True(!s.firedAt[480].IsZero())
	s.mu.Unlock()

	// Replace with the same schedule. firedAt clears.
	err = s.Replace(true, []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
	})
	is.NoErr(err)
	s.mu.Lock()
	is.Equal(s.firedAt, map[ScheduleTime]time.Time(nil))
	s.mu.Unlock()
}
```

- [ ] **Step 13: Add `TestScheduler_FiredAt_PersistsAcrossLoad`.**

```go
// TestScheduler_FiredAt_PersistsAcrossLoad verifies the firedAt map
// round-trips through the JSON state file. Without persistence, a
// daemon restart after a same-day fire would re-fire the entry on the
// fall-back occurrence — defeating the de-dup.
func TestScheduler_FiredAt_PersistsAcrossLoad(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()

	// Build a Scheduler, populate firedAt, save.
	src := &Scheduler{Device: "playroom", StateDir: dir}
	src.enabled = true
	src.entries = []ScheduleEntry{
		{At: 90, Action: "regeneration", Pct: 50},
	}
	fired := time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC) // 01:30 PDT
	src.firedAt = map[ScheduleTime]time.Time{90: fired}
	is.NoErr(src.save())

	// Build a fresh Scheduler and Load.
	dst := &Scheduler{Device: "playroom", StateDir: dir}
	dst.Load()

	is.True(dst.firedAt != nil)
	is.True(dst.firedAt[90].Equal(fired)) // round-trips the exact instant
}
```

- [ ] **Step 14: Run all four new tests.**

```bash
go test ./cmd/breezyd -run 'TestScheduler_FallBackDeDup|TestScheduler_SpringForwardRunningDaemon|TestScheduler_ReplaceClearsFiredAt|TestScheduler_FiredAt_PersistsAcrossLoad' -v
```

Expected: 4/4 PASS.

- [ ] **Step 15: Update CLAUDE.md DST paragraph.**

In `CLAUDE.md`, find the existing paragraph (in the "Schedule system (per device, opt-in via UI)" section):

```
**DST behaviour (known v1 limitation):** times are local wall-clock, so spring-forward skips an entry that lands in the missing hour and fall-back fires an entry in the repeated hour twice. Acceptable for residential ERV control; revisit if scheduling grows day-of-week support.
```

Replace with:

```
**DST handling.** Times are local wall-clock. The daemon honours basic DST transitions:

- Spring-forward: an entry whose At-time falls in the missing hour fires once at the first tick after the skipped hour. Handled by the existing window-detection for a running daemon.
- Fall-back: an entry whose At-time falls in the repeated hour fires exactly once, at the first occurrence. The per-entry `firedAt` map on `Scheduler` suppresses the second appearance.
- Residual edge case: if the daemon starts during the missing hour (spring-forward), entries in that hour are silently skipped. Matches the no-catch-up rule.
- Non-1h-DST zones (e.g. Lord Howe's 30-min): the firedness check uses calendar-day comparison, so any DST offset de-duplicates correctly.
```

- [ ] **Step 16: Update the schedule-system design spec.**

In `docs/superpowers/specs/2026-05-06-schedule-system-design.md`, find the "Out of scope" item:

```
- No DST adjustment. Times are wall-clock local: a spring-forward skip means an entry whose At-time lies in the missing hour does not fire that day; a fall-back means an entry in the repeated hour fires twice. Acceptable for residential ERV control given the surrounding ±1-minute tick precision; revisit if scheduling grows day-of-week support.
```

Replace with (move out of "Out of scope" into a new "DST handling" subsection adjacent to the "No catch-up" item):

```
- DST handling. Wall-clock entries honour both DST transitions for a running daemon: spring-forward catches up via window-detection, fall-back is de-duplicated via per-entry firedness tracking (`Scheduler.firedAt`). Spec'd in `docs/superpowers/specs/2026-05-10-scheduler-dst-design.md`. Residual edge case (daemon-start during the missing hour) matches the no-catch-up rule above.
```

Read the file first to find the exact insertion point — the "Out of scope" section format may have shifted; preserve the existing list ordering and prose voice.

- [ ] **Step 17: Final pre-push gate.**

```bash
just ci
```

Expected: all green (vet + tests + race + asan + msan + Playwright 19/19 + golangci-lint + templ-drift + admin-tag).

If golangci-lint flags the new helper or field as unused, something's wrong — verify the tick gate and fire-success update are actually reaching the new field.

- [ ] **Step 18: Commit.**

```bash
git add cmd/breezyd/scheduler.go cmd/breezyd/scheduler_test.go CLAUDE.md docs/superpowers/specs/2026-05-06-schedule-system-design.md
git commit -m "$(cat <<'EOF'
fix(scheduler): honour basic DST transitions (closes #200)

Per-entry firedness tracking suppresses fall-back double-fires; the
running daemon already handles spring-forward via existing
window-detection. Daemon-start-during-missing-hour stays a documented
edge case per the no-catch-up rule.

New state:
- Scheduler.firedAt map[ScheduleTime]time.Time
- Persisted as persistedSchedule.FiredAt (omitempty; old files parse
  cleanly into a nil map; no version bump)

Test coverage (TestScheduler_FallBackDeDup,
TestScheduler_SpringForwardRunningDaemon,
TestScheduler_ReplaceClearsFiredAt,
TestScheduler_FiredAt_PersistsAcrossLoad) walks fake clocks through
2026-11-01 and 2026-03-08 in America/Los_Angeles via UTC construction
to avoid time.Date ambiguity on the transitions themselves.

CLAUDE.md and the schedule-system design spec updated to replace the
old "v1 limitation" paragraph with the new behaviour.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: PR with auto-merge

**Goal:** Push the branch, open a PR closing #200, enable squash auto-merge, watch through merge.

**Files:** none (CI/PR operations).

**Acceptance Criteria:**
- [ ] Branch pushed to origin.
- [ ] PR opened against `main` referencing #200.
- [ ] Auto-merge enabled (squash).
- [ ] PR merges green (all CI checks pass).

**Verify:** `gh pr view <n> --json state` returns `MERGED`.

**Steps:**

- [ ] **Step 1: Push the branch.**

```bash
git push -u origin fix/200-scheduler-dst
```

- [ ] **Step 2: Open the PR.**

```bash
gh pr create --base main --title "fix(scheduler): honour basic DST transitions (closes #200)" --body "$(cat <<'EOF'
## Summary

Per-entry firedness tracking (\`Scheduler.firedAt\`) suppresses fall-back DST double-fires. Spring-forward already worked correctly for a running daemon via the existing window-detection — that case is now pinned by an explicit test. Daemon-start-during-missing-hour remains a documented edge case per the no-catch-up rule.

Design: \`docs/superpowers/specs/2026-05-10-scheduler-dst-design.md\`.

## Changes
- \`cmd/breezyd/scheduler.go\`: new \`firedAt\` field + \`sameLocalDay\` helper + tick-loop gate + fire-success update + \`Replace()\` clear + persistence wiring (\`persistedSchedule.FiredAt\` with \`omitempty\`).
- \`cmd/breezyd/scheduler_test.go\`: 4 new tests covering both transitions (PT 2026-11-01 fall-back, PT 2026-03-08 spring-forward), \`Replace()\` semantics, and JSON round-trip.
- \`CLAUDE.md\` + \`docs/superpowers/specs/2026-05-06-schedule-system-design.md\`: DST paragraph rewritten.

## Test plan
- [x] \`just ci\` green (vet + tests + race + asan + msan + Playwright + golangci-lint + templ-drift)
- [x] All four new tests pass
- [x] Existing scheduler tests unaffected

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: Enable auto-merge.**

```bash
gh pr merge $(gh pr view --json number -q .number) --squash --auto
```

- [ ] **Step 4: Watch for merge.**

Set a Monitor that polls every 60s for state=MERGED or any check failure. If a check fails, investigate the failure and fix it on the branch — don't force-merge.
