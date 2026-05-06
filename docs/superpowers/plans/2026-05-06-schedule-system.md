# Schedule System — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a per-device, daemon-driven 24-hour cyclic schedule (`At | Action | Pct`) configurable from the dashboard's new SCHEDULE block. The daemon fires writes at each At-time, retries transient failures with bounded backoff, and surfaces alerts when fires fail.

**Architecture:** A new per-device `Scheduler` goroutine, sibling to `EnergyTracker` and `Poller`, started by `startPollers`. State is persisted to `<state_dir>/schedule_<device>.json`. The scheduler's minute-aligned ticker drives `tick(now)`, which detects At-time crossings and dispatches `fire(entry)` writes through the same per-device `udpMu` mutex and `dialRecording` path used by the HTTP handlers. New `GET`/`PUT /v1/devices/{name}/schedule` endpoints read and replace the schedule wholesale; the in-memory schedule is swapped under the scheduler's mutex on PUT. The dashboard adds a collapsible SCHEDULE `<details>` block above Controls; the panel auto-expands when `service.schedule.alert === true`, mirroring the Sensors block's alert behaviour.

**Tech Stack:** Go 1.22+, `net/http` enhanced patterns, embedded HTML/JS, `@playwright/test` (pnpm), `just` recipes.

**Spec:** `docs/superpowers/specs/2026-05-06-schedule-system-design.md`

---

## File Structure

| File | Action | Purpose |
|------|--------|---------|
| `cmd/breezyd/scheduler.go` | Create | Per-device `Scheduler` — types, validation, persistence, tick/fire, retry state machine, `Run` loop |
| `cmd/breezyd/scheduler_test.go` | Create | Unit tests for the Scheduler subsystem |
| `cmd/breezyd/handlers_schedule.go` | Create | `GET`/`PUT /v1/devices/{name}/schedule` |
| `cmd/breezyd/server.go` | Modify | Register the two new routes; add `Schedulers map[string]*Scheduler` to `Handler` |
| `cmd/breezyd/server_test.go` | Modify | Add `TestHandler_GetSchedule_*`, `TestHandler_PutSchedule_*` |
| `cmd/breezyd/main.go` | Modify | `startPollers` constructs a `Scheduler` per device alongside the `EnergyTracker`; returns the schedulers map; `run()` wires it into the `Handler` |
| `cmd/breezyd/handlers_device.go` | Modify | `getDevice` glues `service.schedule` onto the response (mirrors how `service.energy` is glued today) |
| `cmd/breezyd/ui/index.html` | Modify | New SCHEDULE `<details>` block above Controls; renderer, click delegation, save handler, alert force-expand |
| `tests/ui/dashboard.spec.ts` | Modify | New Playwright cases (empty, populated, off-greys-pct, validation, alert force-expand, save round-trip) |
| `tests/ui/screenshot.ts` | Modify | Set one device with a populated schedule so the regenerated PNG shows the SCHEDULE block expanded |
| `tests/ui/screenshots/dashboard-3col.png` | Regenerate | `just screenshot` |
| `tests/ui/screenshots/dashboard-1col.png` | Regenerate | `just screenshot` |
| `README.md` | Modify | Mention SCHEDULE in the dashboard feature list |
| `CHANGELOG.md` | Modify | Add entry |
| `CLAUDE.md` | Modify | New "Schedule system" subsection under Architecture |

---

## Task 1: Scheduler types, parsing, validation

**Goal:** Lay down the data types (`Scheduler`, `ScheduleEntry`, `ScheduleTime`, `LastApply`, `retryState`), the HH:MM↔minute-of-day parser, and the `validate()` function. No persistence, no goroutine, no fire logic yet — those land in later tasks.

**Files:**
- Create: `cmd/breezyd/scheduler.go`
- Create: `cmd/breezyd/scheduler_test.go`

**Acceptance Criteria:**
- [ ] `ScheduleTime` is a typed int (0..1439); `String()` returns `"HH:MM"`; `ParseScheduleTime("HH:MM")` returns `(ScheduleTime, error)`.
- [ ] `ScheduleEntry` has `At ScheduleTime`, `Action string`, `Pct int` and `MarshalJSON`/`UnmarshalJSON` so on-disk and over-wire JSON uses `"at": "HH:MM"`.
- [ ] `Scheduler` struct exists with the fields required by later tasks (no methods yet besides validation/parsing).
- [ ] `(s *Scheduler) validate(enabled bool, entries []ScheduleEntry) error` enforces every rule from the spec: action ∈ five values, `pct` ∈ [10,100], no duplicate `At`, `len(entries)` ≤ 24. Errors wrap `breezy.ErrInvalidArg`.
- [ ] All validation rules covered by unit tests.

**Verify:**

```sh
go test ./cmd/breezyd/ -run TestScheduler_Validation -v
go test ./cmd/breezyd/ -run TestScheduleTime -v
```

Expected: PASS.

**Steps:**

- [ ] **Step 1: Write the failing tests in `cmd/breezyd/scheduler_test.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

func TestScheduleTime_ParseAndString(t *testing.T) {
	cases := []struct {
		in   string
		want ScheduleTime
	}{
		{"00:00", 0},
		{"08:00", 480},
		{"22:30", 22*60 + 30},
		{"23:59", 1439},
	}
	for _, c := range cases {
		got, err := ParseScheduleTime(c.in)
		if err != nil {
			t.Errorf("ParseScheduleTime(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseScheduleTime(%q) = %d, want %d", c.in, got, c.want)
		}
		if back := got.String(); back != c.in {
			t.Errorf("%d.String() = %q, want %q", got, back, c.in)
		}
	}
	bad := []string{"", "8:00", "08:0", "24:00", "08:60", "08-00", "abc", "08:00:00"}
	for _, in := range bad {
		if _, err := ParseScheduleTime(in); err == nil {
			t.Errorf("ParseScheduleTime(%q): want error, got nil", in)
		}
	}
}

func TestScheduler_Validation(t *testing.T) {
	s := &Scheduler{}
	good := []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
		{At: 1320, Action: "off", Pct: 60},
	}
	if err := s.validate(true, good); err != nil {
		t.Errorf("good schedule rejected: %v", err)
	}
	if err := s.validate(false, nil); err != nil {
		t.Errorf("empty disabled schedule rejected: %v", err)
	}
	if err := s.validate(true, nil); err != nil {
		t.Errorf("empty enabled schedule rejected: %v", err) // empty + enabled is a no-op, not an error
	}

	type badCase struct {
		name    string
		entries []ScheduleEntry
	}
	bads := []badCase{
		{"unknown action", []ScheduleEntry{{At: 480, Action: "boost", Pct: 60}}},
		{"pct too low", []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 5}}},
		{"pct too high", []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 101}}},
		{"pct zero", []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 0}}},
		{"duplicate at", []ScheduleEntry{
			{At: 600, Action: "regeneration", Pct: 60},
			{At: 600, Action: "off", Pct: 60},
		}},
	}
	for _, b := range bads {
		err := s.validate(true, b.entries)
		if err == nil {
			t.Errorf("%s: want error, got nil", b.name)
			continue
		}
		if !errors.Is(err, breezy.ErrInvalidArg) {
			t.Errorf("%s: want ErrInvalidArg, got %v", b.name, err)
		}
	}

	// >24 entries
	too := make([]ScheduleEntry, 25)
	for i := range too {
		too[i] = ScheduleEntry{At: ScheduleTime(i), Action: "regeneration", Pct: 60}
	}
	if err := s.validate(true, too); !errors.Is(err, breezy.ErrInvalidArg) {
		t.Errorf("25 entries: want ErrInvalidArg, got %v", err)
	}
}

func TestScheduleEntry_JSON(t *testing.T) {
	in := ScheduleEntry{At: 480, Action: "regeneration", Pct: 60}
	data, err := in.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"at":"08:00","action":"regeneration","pct":60}`
	if string(data) != want {
		t.Errorf("marshal: got %s, want %s", data, want)
	}
	var out ScheduleEntry
	if err := out.UnmarshalJSON([]byte(want)); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("roundtrip: %+v != %+v", out, in)
	}
}
```

- [ ] **Step 2: Run tests, verify they fail**

```sh
go test ./cmd/breezyd/ -run TestSchedule -v
```

Expected: FAIL — types and functions are undefined.

- [ ] **Step 3: Implement the types and helpers in `cmd/breezyd/scheduler.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// Per-device Scheduler that fires Power/Mode/Speed writes at user-configured
// At-times each day. Lives next to EnergyTracker as a sibling per-device
// subsystem; one goroutine per device, started by startPollers.
//
// See docs/superpowers/specs/2026-05-06-schedule-system-design.md for the
// behavioural spec (no-catch-up, retry policy, alert force-expand, etc.).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// ScheduleTime is a minute-of-day in 24-hour local time, range [0, 1440).
type ScheduleTime int

// String renders as "HH:MM".
func (s ScheduleTime) String() string {
	return fmt.Sprintf("%02d:%02d", int(s)/60, int(s)%60)
}

// ParseScheduleTime parses an "HH:MM" string. Hours 0–23, minutes 0–59,
// always exactly two digits each. Returns ErrInvalidArg on any deviation.
func ParseScheduleTime(s string) (ScheduleTime, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, fmt.Errorf("%w: expected HH:MM, got %q", breezy.ErrInvalidArg, s)
	}
	var h, m int
	if _, err := fmt.Sscanf(s, "%2d:%2d", &h, &m); err != nil {
		return 0, fmt.Errorf("%w: parse HH:MM %q: %v", breezy.ErrInvalidArg, s, err)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%w: hours 0-23 / minutes 0-59, got %q", breezy.ErrInvalidArg, s)
	}
	return ScheduleTime(h*60 + m), nil
}

// ScheduleEntry is one "at this time, set this state" row.
type ScheduleEntry struct {
	At     ScheduleTime
	Action string // off | regeneration | ventilation | supply | extract
	Pct    int    // 10..100; ignored when Action == "off"
}

type scheduleEntryWire struct {
	At     string `json:"at"`
	Action string `json:"action"`
	Pct    int    `json:"pct"`
}

// MarshalJSON renders At as "HH:MM" so the on-disk and over-wire shape
// match the human-friendly format the dashboard speaks.
func (e ScheduleEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal(scheduleEntryWire{
		At:     e.At.String(),
		Action: e.Action,
		Pct:    e.Pct,
	})
}

// UnmarshalJSON parses the "HH:MM" form back into ScheduleTime.
func (e *ScheduleEntry) UnmarshalJSON(data []byte) error {
	var w scheduleEntryWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	at, err := ParseScheduleTime(w.At)
	if err != nil {
		return err
	}
	e.At = at
	e.Action = w.Action
	e.Pct = w.Pct
	return nil
}

// LastApply records the outcome of the most recent fire attempt for the
// JSON status block and the UI alert force-expand.
type LastApply struct {
	At      ScheduleTime `json:"at"`
	Fired   time.Time    `json:"fired"`
	OK      bool         `json:"ok"`
	Err     string       `json:"err,omitempty"`
	Retries int          `json:"retries"`
}

// retryState tracks an in-flight retry. Cleared on success, on deadline,
// when superseded by a newer entry's At-time, on user edit, or on disable.
type retryState struct {
	entry       ScheduleEntry
	entryIndex  int
	attempts    int
	nextAttempt time.Time
	deadline    time.Time
}

// Scheduler is the per-device subsystem that fires Power/Mode/Speed writes
// at each At-time. Concurrency: all mutable state is guarded by mu.
type Scheduler struct {
	Device   string
	StateDir string

	// LockUDP serialises with the poller and HTTP handlers via the same
	// per-device mutex. Returns the unlock func. May be nil in tests.
	LockUDP func() func()

	// Dial returns a recordingClient (so cache write-through and
	// NoticeWrite happen automatically) plus the underlying HandlerClient
	// for Close(), and an unlock that should NOT be combined with LockUDP
	// (the caller picks one path; production wires Dial to h.dialRecording
	// which already locks). Tests inject a fake.
	Dial func(ctx context.Context) (rc breezy.DeviceClient, raw HandlerClient, err error)

	// Now is a test seam; defaults to time.Now.
	Now func() time.Time

	mu           sync.Mutex
	enabled      bool
	entries      []ScheduleEntry
	lastApply    *LastApply
	retry        *retryState
	lastTick     ScheduleTime
	haveLastTick bool
}

// validAction is the set of action strings the Scheduler accepts.
var validAction = map[string]bool{
	"off":          true,
	"regeneration": true,
	"ventilation":  true,
	"supply":       true,
	"extract":      true,
}

const maxScheduleEntries = 24

// validate checks the rules from the spec (action set, pct range, no
// duplicate At, ≤ maxScheduleEntries). Returns errors wrapping
// breezy.ErrInvalidArg so the HTTP handler can map cleanly to 400.
func (s *Scheduler) validate(_ bool, entries []ScheduleEntry) error {
	if len(entries) > maxScheduleEntries {
		return fmt.Errorf("%w: at most %d entries (got %d)", breezy.ErrInvalidArg, maxScheduleEntries, len(entries))
	}
	seen := make(map[ScheduleTime]bool, len(entries))
	for i, e := range entries {
		if !validAction[e.Action] {
			return fmt.Errorf("%w: entries[%d].action %q not one of off/regeneration/ventilation/supply/extract", breezy.ErrInvalidArg, i, e.Action)
		}
		if e.Pct < 10 || e.Pct > 100 {
			return fmt.Errorf("%w: entries[%d].pct must be 10-100 (got %d)", breezy.ErrInvalidArg, i, e.Pct)
		}
		if seen[e.At] {
			return fmt.Errorf("%w: duplicate entry at %s", breezy.ErrInvalidArg, e.At)
		}
		seen[e.At] = true
	}
	return nil
}

// now returns the current local wall-clock, honouring s.Now for tests.
func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}
```

- [ ] **Step 4: Re-run tests, verify they pass**

```sh
go test ./cmd/breezyd/ -run TestSchedule -v
```

Expected: PASS.

- [ ] **Step 5: Confirm no regressions**

```sh
just check
```

Expected: green.

- [ ] **Step 6: Commit**

```sh
git add cmd/breezyd/scheduler.go cmd/breezyd/scheduler_test.go
git commit -m "breezyd: scheduler types, HH:MM parsing, and validation"
```

---

## Task 2: Scheduler persistence (Load/save)

**Goal:** Add disk persistence for the schedule state. Atomic temp+rename writes, malformed-file recovery (start empty + slog warn), location at `<state_dir>/schedule_<device>.json`.

**Files:**
- Modify: `cmd/breezyd/scheduler.go` (add `statePath`, `Load`, `save`, `Snapshot`)
- Modify: `cmd/breezyd/scheduler_test.go` (add persistence tests)

**Acceptance Criteria:**
- [ ] `Load()` reads `<state_dir>/schedule_<device>.json` and populates `enabled`, `entries`, `lastApply`. Always returns nil; missing file → empty disabled state; malformed file → empty disabled + `slog.Warn` (matches `EnergyTracker.Load` semantics).
- [ ] `save()` atomically writes the current state via temp+rename, mode `0600`. Caller must hold `s.mu`.
- [ ] `Snapshot()` returns a value-copy of the public state (`enabled`, `entries`, `lastApply`) under the lock — used by HTTP handlers and the status JSON glue.
- [ ] Persisted file omits `last_apply` when `lastApply == nil`.

**Verify:**

```sh
go test ./cmd/breezyd/ -run TestScheduler_Persist -v
go test ./cmd/breezyd/ -run TestScheduler_Load -v
```

Expected: PASS.

**Steps:**

- [ ] **Step 1: Write failing tests in `cmd/breezyd/scheduler_test.go`**

```go
func TestScheduler_PersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &Scheduler{Device: "playroom", StateDir: dir}

	// Mutate under mu directly (mirrors what later production paths do).
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
		{At: 1320, Action: "off", Pct: 60},
	}
	s.lastApply = &LastApply{
		At:      1320,
		Fired:   time.Date(2026, 5, 6, 22, 0, 14, 0, time.UTC),
		OK:      false,
		Err:     "device_unreachable: i/o timeout",
		Retries: 5,
	}
	if err := s.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	s.mu.Unlock()

	// New scheduler, same dir, Load should restore everything.
	s2 := &Scheduler{Device: "playroom", StateDir: dir}
	if err := s2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	snap := s2.Snapshot()
	if !snap.Enabled || len(snap.Entries) != 2 || snap.Entries[0].At != 480 || snap.Entries[1].Action != "off" {
		t.Errorf("entries did not survive roundtrip: %+v", snap)
	}
	if snap.LastApply == nil || snap.LastApply.Retries != 5 || snap.LastApply.OK {
		t.Errorf("lastApply did not survive roundtrip: %+v", snap.LastApply)
	}
}

func TestScheduler_LoadMissingFileStartsEmpty(t *testing.T) {
	s := &Scheduler{Device: "x", StateDir: t.TempDir()}
	if err := s.Load(); err != nil {
		t.Fatalf("load missing: %v", err)
	}
	snap := s.Snapshot()
	if snap.Enabled || len(snap.Entries) != 0 || snap.LastApply != nil {
		t.Errorf("expected empty state, got %+v", snap)
	}
}

func TestScheduler_LoadMalformedFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := &Scheduler{Device: "x", StateDir: dir}
	if err := os.WriteFile(s.statePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed bad file: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("load malformed: %v", err)
	}
	snap := s.Snapshot()
	if snap.Enabled || len(snap.Entries) != 0 {
		t.Errorf("malformed file should start empty, got %+v", snap)
	}
}

func TestScheduler_SaveAtomic(t *testing.T) {
	dir := t.TempDir()
	s := &Scheduler{Device: "x", StateDir: dir}
	s.mu.Lock()
	s.enabled = false
	if err := s.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	s.mu.Unlock()

	// No leftover .tmp sibling.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}
```

Add the missing imports at the top: `os`, `path/filepath`, `time`.

- [ ] **Step 2: Run tests, verify they fail**

```sh
go test ./cmd/breezyd/ -run TestScheduler_Persist -v
go test ./cmd/breezyd/ -run TestScheduler_Load -v
go test ./cmd/breezyd/ -run TestScheduler_Save -v
```

Expected: FAIL — methods undefined.

- [ ] **Step 3: Add `Snapshot`, `Load`, `save`, `statePath` to `cmd/breezyd/scheduler.go`**

```go
// ScheduleSnapshot is the public, value-copy view of the schedule used by
// HTTP handlers and the status JSON glue.
type ScheduleSnapshot struct {
	Enabled   bool
	Entries   []ScheduleEntry
	LastApply *LastApply
}

// persistedSchedule is the on-disk JSON shape.
type persistedSchedule struct {
	Version   int             `json:"version"`
	Enabled   bool            `json:"enabled"`
	Entries   []ScheduleEntry `json:"entries"`
	LastApply *LastApply      `json:"last_apply,omitempty"`
}

const scheduleFileVersion = 1

// statePath is the JSON file path used for persistence.
func (s *Scheduler) statePath() string {
	return filepath.Join(s.StateDir, fmt.Sprintf("schedule_%s.json", s.Device))
}

// Snapshot returns a value copy of the scheduler's public state under the
// lock. Safe to call from any goroutine.
func (s *Scheduler) Snapshot() ScheduleSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ScheduleSnapshot{
		Enabled: s.enabled,
		Entries: append([]ScheduleEntry(nil), s.entries...),
	}
	if s.lastApply != nil {
		la := *s.lastApply
		out.LastApply = &la
	}
	return out
}

// Load reads the persisted state file and populates the scheduler. Always
// returns nil: a missing or malformed file starts empty (with a slog.Warn
// in the malformed case), matching EnergyTracker.Load's semantics.
func (s *Scheduler) Load() error {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("schedule: failed to read state file; starting empty",
				"device", s.Device, "err", err)
		}
		return nil
	}
	var p persistedSchedule
	if err := json.Unmarshal(data, &p); err != nil {
		slog.Warn("schedule: failed to unmarshal state file; starting empty",
			"device", s.Device, "err", err)
		return nil
	}
	if err := s.validate(p.Enabled, p.Entries); err != nil {
		slog.Warn("schedule: persisted file failed validation; starting empty",
			"device", s.Device, "err", err)
		return nil
	}
	sortEntries(p.Entries)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = p.Enabled
	s.entries = p.Entries
	s.lastApply = p.LastApply
	return nil
}

// save writes the current state atomically via temp+rename. Caller MUST
// hold s.mu.
func (s *Scheduler) save() error {
	p := persistedSchedule{
		Version:   scheduleFileVersion,
		Enabled:   s.enabled,
		Entries:   s.entries,
		LastApply: s.lastApply,
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("schedule: marshal: %w", err)
	}
	tmp := s.statePath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("schedule: write temp: %w", err)
	}
	if err := os.Rename(tmp, s.statePath()); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("schedule: rename temp: %w", err)
	}
	return nil
}

// sortEntries sorts in-place by At ascending. Used after Load and after
// PUT to keep the in-memory and on-disk state canonically ordered.
func sortEntries(entries []ScheduleEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].At < entries[j].At })
}
```

Add imports `errors`, `log/slog`, `os`, `path/filepath`, `sort` to the top of `scheduler.go`.

- [ ] **Step 4: Re-run tests, verify they pass**

```sh
go test ./cmd/breezyd/ -run TestScheduler_ -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add cmd/breezyd/scheduler.go cmd/breezyd/scheduler_test.go
git commit -m "breezyd: scheduler persistence (atomic save, malformed-recovery load)"
```

---

## Task 3: Scheduler tick + fire (no retry yet)

**Goal:** Implement `tick(ctx, now)` and `fire(ctx, entry)` covering the "happy path" + structural edge cases — no retry state machine yet (Task 4). Includes: no-catch-up first tick, multiple-match fires latest only, midnight wraparound, disabled is inert, action→writes mapping (Power, Mode, SpeedManual), `lastApply` populated on success and on failure, persistence on every state change.

**Files:**
- Modify: `cmd/breezyd/scheduler.go` (add `tick`, `fire`, action→writes helper)
- Modify: `cmd/breezyd/scheduler_test.go` (add tick/fire tests with a fake client and fake clock)

**Acceptance Criteria:**
- [ ] First tick after construction (`!haveLastTick`) records `lastTick = nowMinute`, sets `haveLastTick = true`, fires nothing — even if entries' At-times are before `now`.
- [ ] Subsequent tick where any entry's At lies in `(lastTick, nowMinute]` calls `fire` for the **latest** matching entry only.
- [ ] Midnight wraparound: when `nowMinute < lastTick`, the window is `(lastTick, 1440) ∪ [0, nowMinute]`.
- [ ] When `enabled == false`, `tick` is a no-op except clearing any in-flight retry state (the latter not yet exercised — pending Task 4).
- [ ] `fire(off)` issues `Power(false)` only.
- [ ] `fire(regeneration|ventilation|supply|extract)` issues `Power(true)` → `SetMode(action)` → `SetSpeedManual(pct)` in that order through one client connection.
- [ ] On success, `lastApply{ok=true, retries=0, ...}` is set and persisted.
- [ ] On failure (any error), `lastApply{ok=false, err: ..., retries=1}` is set and persisted — but no retry yet (Task 4 wires it in).

**Verify:**

```sh
go test ./cmd/breezyd/ -run TestScheduler_Tick -v
go test ./cmd/breezyd/ -run TestScheduler_Fire -v
```

Expected: PASS.

**Steps:**

- [ ] **Step 1: Add a tiny test fake at the top of `scheduler_test.go`** (after imports)

```go
// schedFakeClient implements breezy.DeviceClient for tests.
type schedFakeClient struct {
	writes [][]breezy.ParamWrite
	err    error // error returned from WriteParams; reset between calls
}

func (f *schedFakeClient) ReadParams(_ context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
	return map[breezy.ParamID][]byte{}, nil
}
func (f *schedFakeClient) WriteParams(_ context.Context, ws []breezy.ParamWrite) error {
	if f.err != nil {
		return f.err
	}
	f.writes = append(f.writes, append([]breezy.ParamWrite(nil), ws...))
	return nil
}

// flatWrites returns every ParamWrite in order across all WriteParams calls.
func (f *schedFakeClient) flatWrites() []breezy.ParamWrite {
	out := []breezy.ParamWrite{}
	for _, batch := range f.writes {
		out = append(out, batch...)
	}
	return out
}

// schedFakeRaw implements HandlerClient (so Scheduler.Dial can return one).
type schedFakeRaw struct{}

func (schedFakeRaw) ReadParams(_ context.Context, _ []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
	return nil, nil
}
func (schedFakeRaw) WriteParams(_ context.Context, _ []breezy.ParamWrite) error { return nil }
func (schedFakeRaw) Close() error                                                { return nil }

// newSchedTest builds a Scheduler wired to a fake client whose writes
// the test can inspect afterwards.
func newSchedTest(t *testing.T) (*Scheduler, *schedFakeClient) {
	t.Helper()
	fc := &schedFakeClient{}
	s := &Scheduler{
		Device:   "playroom",
		StateDir: t.TempDir(),
		LockUDP:  func() func() { return func() {} },
		Dial: func(_ context.Context) (breezy.DeviceClient, HandlerClient, error) {
			return fc, schedFakeRaw{}, nil
		},
	}
	return s, fc
}
```

- [ ] **Step 2: Add tick/fire tests**

```go
// helper: build a time at a given local HH:MM (date doesn't matter).
func atHM(h, m int) time.Time {
	return time.Date(2026, 5, 6, h, m, 0, 0, time.Local)
}

func TestScheduler_Tick_NoCatchupOnStartup(t *testing.T) {
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}} // 08:00
	s.mu.Unlock()
	s.tick(context.Background(), atHM(14, 0))
	if len(fc.writes) != 0 {
		t.Errorf("first tick fired unexpectedly: %+v", fc.writes)
	}
	// Second tick at the same minute: still nothing, because we've
	// past-recorded lastTick.
	s.tick(context.Background(), atHM(14, 0))
	if len(fc.writes) != 0 {
		t.Errorf("second tick at same minute should not fire: %+v", fc.writes)
	}
}

func TestScheduler_Tick_FiresOnAtTime_Regeneration(t *testing.T) {
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	// Prime lastTick so the next tick is "subsequent".
	s.tick(context.Background(), atHM(7, 59))
	// Cross 08:00.
	s.tick(context.Background(), atHM(8, 0))
	got := fc.flatWrites()
	if len(got) < 3 {
		t.Fatalf("want >=3 writes (Power, Mode, SpeedManual), got %d: %+v", len(got), got)
	}
	if got[0].ID != 0x0001 || got[0].Value[0] != 1 {
		t.Errorf("first write should be Power(true); got id=0x%04X val=%v", uint16(got[0].ID), got[0].Value)
	}
	if got[1].ID != 0x00B7 || got[1].Value[0] != 1 { // 1 = regeneration
		t.Errorf("second write should be SetMode(regeneration); got id=0x%04X val=%v", uint16(got[1].ID), got[1].Value)
	}
	// SetSpeedManual writes both 0x44 and flips 0x02; assert 0x44 made it.
	saw0x44 := false
	for _, w := range got[2:] {
		if w.ID == 0x0044 && w.Value[0] == 60 {
			saw0x44 = true
		}
	}
	if !saw0x44 {
		t.Errorf("expected SpeedManual write of 60%% via 0x44; writes=%+v", got)
	}
	snap := s.Snapshot()
	if snap.LastApply == nil || !snap.LastApply.OK || snap.LastApply.At != 480 {
		t.Errorf("lastApply not recorded as OK at 08:00: %+v", snap.LastApply)
	}
}

func TestScheduler_Tick_FiresOff(t *testing.T) {
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 1320, Action: "off", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(21, 59))
	s.tick(context.Background(), atHM(22, 0))
	got := fc.flatWrites()
	if len(got) != 1 {
		t.Fatalf("want exactly one Power(false), got %d writes: %+v", len(got), got)
	}
	if got[0].ID != 0x0001 || got[0].Value[0] != 0 {
		t.Errorf("off should be Power(false); got id=0x%04X val=%v", uint16(got[0].ID), got[0].Value)
	}
}

func TestScheduler_Tick_DisabledIsInert(t *testing.T) {
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = false
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	if len(fc.writes) != 0 {
		t.Errorf("disabled scheduler should not fire: %+v", fc.writes)
	}
}

func TestScheduler_Tick_MultipleMatchFiresLatest(t *testing.T) {
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
		{At: 540, Action: "ventilation", Pct: 70},
	}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59)) // prime
	s.tick(context.Background(), atHM(9, 1))  // jumped past both entries
	got := fc.flatWrites()
	saw0x44_70 := false
	for _, w := range got {
		if w.ID == 0x0044 && w.Value[0] == 70 {
			saw0x44_70 = true
		}
	}
	if !saw0x44_70 {
		t.Errorf("multi-match window should fire latest (09:00 → ventilation 70%%); writes=%+v", got)
	}
	// Only one fire's worth of writes — Power(true) issued once, not twice.
	powerOnCount := 0
	for _, w := range got {
		if w.ID == 0x0001 && w.Value[0] == 1 {
			powerOnCount++
		}
	}
	if powerOnCount != 1 {
		t.Errorf("multi-match should fire one entry only; got %d Power(true) writes", powerOnCount)
	}
}

func TestScheduler_Tick_FiresAcrossMidnight(t *testing.T) {
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 5, Action: "regeneration", Pct: 60}} // 00:05
	s.mu.Unlock()
	// Prime at 23:59 so the next tick wraps midnight.
	s.tick(context.Background(), atHM(23, 59))
	if len(fc.writes) != 0 {
		t.Fatalf("priming tick should not fire: %+v", fc.writes)
	}
	s.tick(context.Background(), atHM(0, 6))
	got := fc.flatWrites()
	if len(got) == 0 {
		t.Errorf("00:05 entry should fire across midnight: writes=%+v", got)
	}
}

func TestScheduler_Fire_FailureRecordsLastApply(t *testing.T) {
	s, fc := newSchedTest(t)
	fc.err = errors.New("simulated UDP timeout")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	snap := s.Snapshot()
	if snap.LastApply == nil || snap.LastApply.OK {
		t.Errorf("failed fire should record lastApply.ok=false: %+v", snap.LastApply)
	}
	if snap.LastApply.Err == "" {
		t.Errorf("failed fire should record an err message: %+v", snap.LastApply)
	}
}
```

- [ ] **Step 3: Run tests, verify they fail**

```sh
go test ./cmd/breezyd/ -run TestScheduler_Tick -v
go test ./cmd/breezyd/ -run TestScheduler_Fire -v
```

Expected: FAIL.

- [ ] **Step 4: Implement `tick` and `fire` in `cmd/breezyd/scheduler.go`**

```go
// fireTimeout bounds a single fire attempt's UDP round-trip. Matches the
// 5s used by the HTTP handlers.
const fireTimeout = 5 * time.Second

// tick processes one minute boundary. now is the wall-clock; tests pass
// a synthetic value via s.Now.
func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	enabled := s.enabled
	entries := append([]ScheduleEntry(nil), s.entries...)
	haveLastTick := s.haveLastTick
	lastTick := s.lastTick
	s.mu.Unlock()

	nowMinute := ScheduleTime(now.Hour()*60 + now.Minute())

	if !enabled {
		// Disabled: still advance lastTick so re-enabling later doesn't
		// accidentally fire a flood of just-crossed entries.
		s.mu.Lock()
		s.lastTick = nowMinute
		s.haveLastTick = true
		// Task 4 will also clear any in-flight retry here.
		s.mu.Unlock()
		return
	}

	if !haveLastTick {
		s.mu.Lock()
		s.lastTick = nowMinute
		s.haveLastTick = true
		s.mu.Unlock()
		return
	}

	if nowMinute == lastTick {
		return
	}

	// Identify entries whose At lies in the half-open window
	// (lastTick, nowMinute]. With wraparound across midnight, the window
	// becomes (lastTick, 1440) ∪ [0, nowMinute].
	inWindow := func(at ScheduleTime) bool {
		if nowMinute > lastTick {
			return at > lastTick && at <= nowMinute
		}
		return at > lastTick || at <= nowMinute
	}
	var latest *ScheduleEntry
	var latestIdx int = -1
	for i := range entries {
		e := entries[i]
		if !inWindow(e.At) {
			continue
		}
		// Pick the latest in temporal order through the window. With a
		// wrapped window we must rotate so "later in the window" reads
		// linearly: anchor at lastTick.
		if latest == nil {
			cp := e
			latest = &cp
			latestIdx = i
			continue
		}
		// Compare distance from lastTick (mod 1440); larger distance =
		// later in the window.
		dist := func(t ScheduleTime) int {
			d := int(t) - int(lastTick)
			if d <= 0 {
				d += 1440
			}
			return d
		}
		if dist(e.At) > dist(latest.At) {
			cp := e
			latest = &cp
			latestIdx = i
		}
	}

	s.mu.Lock()
	s.lastTick = nowMinute
	s.haveLastTick = true
	s.mu.Unlock()

	if latest != nil {
		s.fire(ctx, *latest, latestIdx, now)
	}
}

// fire dispatches one entry's writes through Dial. Records lastApply on
// completion (success or failure) and persists. Retry installation lands
// in Task 4.
func (s *Scheduler) fire(ctx context.Context, e ScheduleEntry, _ int, now time.Time) {
	if s.LockUDP != nil {
		unlock := s.LockUDP()
		defer unlock()
	}
	cctx, cancel := context.WithTimeout(ctx, fireTimeout)
	defer cancel()

	var fireErr error
	if s.Dial == nil {
		fireErr = errors.New("scheduler: Dial not configured")
	} else {
		client, raw, err := s.Dial(cctx)
		if err != nil {
			fireErr = err
		} else {
			defer func() { _ = raw.Close() }()
			fireErr = applyAction(cctx, client, e)
		}
	}

	la := &LastApply{
		At:    e.At,
		Fired: now,
		OK:    fireErr == nil,
	}
	if fireErr != nil {
		la.Err = fireErr.Error()
		la.Retries = 1 // Task 4 will refine this with the retry counter
		slog.Warn("schedule: fire failed", "device", s.Device, "at", e.At.String(), "err", fireErr)
	} else {
		slog.Info("schedule: fired", "device", s.Device, "at", e.At.String(), "action", e.Action, "pct", e.Pct)
	}

	s.mu.Lock()
	s.lastApply = la
	if err := s.save(); err != nil {
		slog.Warn("schedule: save after fire failed", "device", s.Device, "err", err)
	}
	s.mu.Unlock()
}

// applyAction issues the device-side writes corresponding to one entry's
// Action. Order: Power → Mode → SpeedManual. "off" issues Power(false) only.
func applyAction(ctx context.Context, c breezy.DeviceClient, e ScheduleEntry) error {
	if e.Action == "off" {
		return breezy.Power(ctx, c, false)
	}
	if err := breezy.Power(ctx, c, true); err != nil {
		return err
	}
	if err := breezy.SetMode(ctx, c, e.Action); err != nil {
		return err
	}
	return breezy.SetSpeedManual(ctx, c, e.Pct)
}
```

- [ ] **Step 5: Re-run tests, verify they pass**

```sh
go test ./cmd/breezyd/ -run TestScheduler_ -v
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add cmd/breezyd/scheduler.go cmd/breezyd/scheduler_test.go
git commit -m "breezyd: scheduler tick + fire (no-catchup, midnight wrap, latest-of-many)"
```

---

## Task 4: Retry state machine

**Goal:** Wire bounded retries into `tick`/`fire`. Rule D from the spec: 30s cadence, 10-minute deadline, abandoned early when superseded by a newer entry's At-time. ErrAuth is a config error: log once, no retry.

**Files:**
- Modify: `cmd/breezyd/scheduler.go`
- Modify: `cmd/breezyd/scheduler_test.go`

**Acceptance Criteria:**
- [ ] On a non-auth fire error, `s.retry` is installed: `attempts=1`, `nextAttempt=now+30s`, `deadline=now+10m`. `lastApply.retries` reflects `attempts`.
- [ ] Subsequent ticks where `now >= retry.nextAttempt && now < retry.deadline` re-call `fire`. On success, `retry` is cleared and `lastApply.ok=true`.
- [ ] On `now >= retry.deadline` without success, `retry` is cleared. `lastApply.ok` stays false (the UI keeps the alert).
- [ ] When a newer entry's At-time arrives in the same tick (window covers another entry past the in-flight retry's entry), the retry is dropped and the newer entry fires.
- [ ] `breezy.ErrAuth` does NOT install a retry. `lastApply.err` includes "auth_failed".
- [ ] Disabling the schedule (`enabled=false` on next tick) clears any in-flight retry.

**Verify:**

```sh
go test ./cmd/breezyd/ -run TestRetry -v
```

Expected: PASS.

**Steps:**

- [ ] **Step 1: Write failing tests in `scheduler_test.go`**

```go
func TestRetry_TimeoutInstallsRetry(t *testing.T) {
	s, fc := newSchedTest(t)
	fc.err = errors.New("i/o timeout")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	s.mu.Lock()
	r := s.retry
	s.mu.Unlock()
	if r == nil {
		t.Fatalf("retry not installed after failure")
	}
	if r.attempts != 1 {
		t.Errorf("attempts=%d, want 1", r.attempts)
	}
}

func TestRetry_AuthFailsNoRetry(t *testing.T) {
	s, fc := newSchedTest(t)
	fc.err = breezy.ErrAuth
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	s.mu.Lock()
	r := s.retry
	la := s.lastApply
	s.mu.Unlock()
	if r != nil {
		t.Errorf("auth failure should not install retry: %+v", r)
	}
	if la == nil || la.OK {
		t.Errorf("expected lastApply.ok=false, got %+v", la)
	}
	if !strings.Contains(la.Err, "auth_failed") {
		t.Errorf("expected auth_failed in err, got %q", la.Err)
	}
}

func TestRetry_SucceedsClearsRetry(t *testing.T) {
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))   // installs retry
	fc.err = nil                                 // clear the simulated failure
	s.tick(context.Background(), atHM(8, 1))    // 30s+ later → retries
	s.mu.Lock()
	r := s.retry
	la := s.lastApply
	s.mu.Unlock()
	if r != nil {
		t.Errorf("retry should be cleared after success: %+v", r)
	}
	if la == nil || !la.OK {
		t.Errorf("expected lastApply.ok=true after retry success: %+v", la)
	}
}

func TestRetry_DeadlineAbandons(t *testing.T) {
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))   // installs retry, deadline 08:10
	// March forward minute by minute; every retry attempt fails.
	for m := 1; m <= 11; m++ {
		s.tick(context.Background(), atHM(8, m))
	}
	s.mu.Lock()
	r := s.retry
	la := s.lastApply
	s.mu.Unlock()
	if r != nil {
		t.Errorf("retry should be abandoned past deadline: %+v", r)
	}
	if la == nil || la.OK {
		t.Errorf("lastApply.ok should remain false after deadline: %+v", la)
	}
}

func TestRetry_SupersededByNextEntry(t *testing.T) {
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60}, // 08:00
		{At: 540, Action: "ventilation", Pct: 70},  // 09:00
	}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))   // installs retry for 08:00
	fc.err = nil                                 // clear so 09:00 fire succeeds
	s.tick(context.Background(), atHM(9, 0))   // crosses 09:00 — should fire it
	s.mu.Lock()
	r := s.retry
	la := s.lastApply
	s.mu.Unlock()
	if r != nil {
		t.Errorf("supersede should clear retry: %+v", r)
	}
	if la == nil || la.At != 540 || !la.OK {
		t.Errorf("expected lastApply for 09:00 ok: %+v", la)
	}
}

func TestRetry_DisableClearsRetry(t *testing.T) {
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0)) // installs retry
	s.mu.Lock()
	s.enabled = false
	s.mu.Unlock()
	s.tick(context.Background(), atHM(8, 1))
	s.mu.Lock()
	r := s.retry
	s.mu.Unlock()
	if r != nil {
		t.Errorf("disable should clear retry: %+v", r)
	}
}
```

Add `strings` to the imports.

- [ ] **Step 2: Run tests, verify they fail**

```sh
go test ./cmd/breezyd/ -run TestRetry -v
```

Expected: FAIL.

- [ ] **Step 3: Implement the retry state machine**

Update the disabled branch of `tick`:

```go
	if !enabled {
		s.mu.Lock()
		s.lastTick = nowMinute
		s.haveLastTick = true
		s.retry = nil // disable clears any in-flight retry
		s.mu.Unlock()
		return
	}
```

Insert the retry-handling block in `tick` immediately after the `nowMinute == lastTick` short-circuit and before transition detection:

```go
	// Retry handling. If a retry is in flight, decide whether to:
	//  (a) supersede it because a newer entry crosses now in this tick,
	//  (b) attempt the retry because nextAttempt has arrived,
	//  (c) abandon it because we've passed the deadline.
	s.mu.Lock()
	r := s.retry
	s.mu.Unlock()
	if r != nil {
		// (a) supersede: any entry whose At is in the window AND is later
		// than r.entry.At wins. Detection happens below in the latest-of-
		// many path; here we just check if any such entry exists. If yes,
		// drop the retry; the latest-of-many block will fire it.
		hasNewer := false
		for _, e := range entries {
			if !inWindow(e.At) {
				continue
			}
			if e.At != r.entry.At {
				hasNewer = true
				break
			}
		}
		if hasNewer {
			s.mu.Lock()
			s.retry = nil
			s.mu.Unlock()
		} else if !now.Before(r.deadline) {
			// (c) abandon
			s.mu.Lock()
			s.retry = nil
			if err := s.save(); err != nil {
				slog.Warn("schedule: save after deadline-abandon failed", "device", s.Device, "err", err)
			}
			s.mu.Unlock()
		} else if !now.Before(r.nextAttempt) {
			// (b) attempt retry. fire() updates lastApply and re-installs
			// retry on failure with attempts incremented.
			s.fire(ctx, r.entry, r.entryIndex, now)
			// fire's success/failure also persists; loop on to the
			// transition detection in case a newer entry also fires.
		}
	}
```

(`inWindow` is defined below the snapshot block in `tick`; move it up so the retry path can use it. Re-shape `tick` so `inWindow` is defined before both paths use it.)

Update `fire` to install/update retry state appropriately:

```go
func (s *Scheduler) fire(ctx context.Context, e ScheduleEntry, idx int, now time.Time) {
	if s.LockUDP != nil {
		unlock := s.LockUDP()
		defer unlock()
	}
	cctx, cancel := context.WithTimeout(ctx, fireTimeout)
	defer cancel()

	var fireErr error
	if s.Dial == nil {
		fireErr = errors.New("scheduler: Dial not configured")
	} else {
		client, raw, err := s.Dial(cctx)
		if err != nil {
			fireErr = err
		} else {
			defer func() { _ = raw.Close() }()
			fireErr = applyAction(cctx, client, e)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if fireErr == nil {
		s.lastApply = &LastApply{At: e.At, Fired: now, OK: true}
		s.retry = nil
		if err := s.save(); err != nil {
			slog.Warn("schedule: save after success failed", "device", s.Device, "err", err)
		}
		slog.Info("schedule: fired", "device", s.Device, "at", e.At.String(), "action", e.Action, "pct", e.Pct)
		return
	}

	// Failure path.
	if errors.Is(fireErr, breezy.ErrAuth) {
		s.lastApply = &LastApply{
			At: e.At, Fired: now, OK: false,
			Err:     "auth_failed: " + fireErr.Error(),
			Retries: 0,
		}
		s.retry = nil // do not retry auth failures
		if err := s.save(); err != nil {
			slog.Warn("schedule: save after auth-fail failed", "device", s.Device, "err", err)
		}
		slog.Warn("schedule: auth failure, not retrying", "device", s.Device, "at", e.At.String())
		return
	}

	attempts := 1
	deadline := now.Add(retryDeadline)
	if s.retry != nil && s.retry.entry.At == e.At {
		attempts = s.retry.attempts + 1
		deadline = s.retry.deadline // keep the original 10-min cap from the first failure
	}
	s.retry = &retryState{
		entry:       e,
		entryIndex:  idx,
		attempts:    attempts,
		nextAttempt: now.Add(retryCadence),
		deadline:    deadline,
	}
	s.lastApply = &LastApply{
		At: e.At, Fired: now, OK: false,
		Err:     fireErr.Error(),
		Retries: attempts,
	}
	if err := s.save(); err != nil {
		slog.Warn("schedule: save after fail failed", "device", s.Device, "err", err)
	}
	slog.Warn("schedule: fire failed; retry installed",
		"device", s.Device, "at", e.At.String(), "attempts", attempts, "err", fireErr)
}
```

Add the constants near `fireTimeout`:

```go
const (
	retryCadence  = 30 * time.Second
	retryDeadline = 10 * time.Minute
)
```

Refactor `tick` so `inWindow` and the entry list are computed before retry handling:

```go
func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	enabled := s.enabled
	entries := append([]ScheduleEntry(nil), s.entries...)
	haveLastTick := s.haveLastTick
	lastTick := s.lastTick
	s.mu.Unlock()

	nowMinute := ScheduleTime(now.Hour()*60 + now.Minute())

	if !enabled {
		s.mu.Lock()
		s.lastTick = nowMinute
		s.haveLastTick = true
		s.retry = nil
		s.mu.Unlock()
		return
	}
	if !haveLastTick {
		s.mu.Lock()
		s.lastTick = nowMinute
		s.haveLastTick = true
		s.mu.Unlock()
		return
	}
	if nowMinute == lastTick {
		return
	}

	inWindow := func(at ScheduleTime) bool {
		if nowMinute > lastTick {
			return at > lastTick && at <= nowMinute
		}
		return at > lastTick || at <= nowMinute
	}

	// Retry handling first (Task 4).
	s.mu.Lock()
	r := s.retry
	s.mu.Unlock()
	if r != nil {
		hasNewer := false
		for _, e := range entries {
			if inWindow(e.At) && e.At != r.entry.At {
				hasNewer = true
				break
			}
		}
		switch {
		case hasNewer:
			s.mu.Lock()
			s.retry = nil
			s.mu.Unlock()
		case !now.Before(r.deadline):
			s.mu.Lock()
			s.retry = nil
			_ = s.save()
			s.mu.Unlock()
		case !now.Before(r.nextAttempt):
			s.fire(ctx, r.entry, r.entryIndex, now)
		}
	}

	// Transition detection (Task 3).
	var latest *ScheduleEntry
	var latestIdx = -1
	dist := func(t ScheduleTime) int {
		d := int(t) - int(lastTick)
		if d <= 0 {
			d += 1440
		}
		return d
	}
	for i, e := range entries {
		if !inWindow(e.At) {
			continue
		}
		if latest == nil || dist(e.At) > dist(latest.At) {
			cp := e
			latest = &cp
			latestIdx = i
		}
	}

	s.mu.Lock()
	s.lastTick = nowMinute
	s.haveLastTick = true
	s.mu.Unlock()

	if latest != nil {
		s.fire(ctx, *latest, latestIdx, now)
	}
}
```

- [ ] **Step 4: Re-run tests**

```sh
go test ./cmd/breezyd/ -run TestRetry -v
go test ./cmd/breezyd/ -run TestScheduler_ -v
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```sh
git add cmd/breezyd/scheduler.go cmd/breezyd/scheduler_test.go
git commit -m "breezyd: scheduler retry state machine (D rule: 30s, 10m, supersede)"
```

---

## Task 5: Run loop + wire into startPollers

**Goal:** Add `Scheduler.Run(ctx)` (minute-aligned ticker), and have `startPollers` build a per-device Scheduler beside the EnergyTracker. Expose `Schedulers map[string]*Scheduler` on `Handler`. Production wiring: each Scheduler gets `LockUDP = poller.LockUDP` and `Dial = h.dialRecording(name)`.

**Files:**
- Modify: `cmd/breezyd/scheduler.go` (add `Run`, `alignToNextMinute`)
- Modify: `cmd/breezyd/scheduler_test.go` (smoke test for `Run` exits on ctx cancel)
- Modify: `cmd/breezyd/main.go` (extend `startPollers`)
- Modify: `cmd/breezyd/server.go` (add `Schedulers` field on `Handler`; small helper to build a `Dial` closure compatible with `Scheduler.Dial`)
- Modify: `cmd/breezyd/main_test.go` if needed (build smoke)

**Acceptance Criteria:**
- [ ] `Scheduler.Run(ctx)` blocks until `ctx.Done`, ticks once a minute aligned to wall-clock, and calls `tick` on each tick.
- [ ] `startPollers` returns `(map[string]*Poller, map[string]*Scheduler, *sync.WaitGroup)`. Both pollers and schedulers are added to the same WaitGroup so shutdown drains both.
- [ ] Each Scheduler is constructed with `Device`, `StateDir`, `LockUDP = poller.LockUDP`, `Dial = h.scheduleDial(name)` (a new helper that wraps `dialRecording` to match the `Scheduler.Dial` signature). `Load()` is called before `Run`.
- [ ] `Handler.Schedulers` is populated and the existing routes still work (smoke via existing handler tests).

**Verify:**

```sh
go build ./...
just check
```

Expected: build green, all tests pass.

**Steps:**

- [ ] **Step 1: Add `Run` and `alignToNextMinute` to `scheduler.go`**

```go
// Run blocks until ctx is done, ticking once a minute aligned to the
// wall-clock minute boundary. The first tick fires immediately after
// alignment to give callers a deterministic startup point.
func (s *Scheduler) Run(ctx context.Context) {
	s.alignToNextMinute(ctx)
	if ctx.Err() != nil {
		return
	}
	t := time.NewTicker(1 * time.Minute)
	defer t.Stop()
	s.tick(ctx, s.now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx, s.now())
		}
	}
}

// alignToNextMinute sleeps until the next :00 second boundary so subsequent
// ticks land within a few hundred ms of HH:MM:00. Cancellable via ctx.
func (s *Scheduler) alignToNextMinute(ctx context.Context) {
	now := s.now()
	wait := time.Duration(60-now.Second())*time.Second - time.Duration(now.Nanosecond())
	if wait <= 0 || wait > time.Minute {
		wait = time.Minute - time.Duration(now.Second())*time.Second
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
```

- [ ] **Step 2: Add a smoke test for `Run` in `scheduler_test.go`**

```go
func TestScheduler_Run_ExitsOnContextCancel(t *testing.T) {
	s, _ := newSchedTest(t)
	s.Now = func() time.Time { return atHM(8, 0) } // pinned so alignment ~60s
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit on context cancel")
	}
}
```

- [ ] **Step 3: Add `Handler.scheduleDial` helper in `cmd/breezyd/server.go`**

After `dialRecording` (around line 344):

```go
// scheduleDial returns a Dial closure compatible with Scheduler.Dial.
// It mirrors dialRecording but does NOT acquire the per-device UDP mutex
// (the Scheduler holds it via Scheduler.LockUDP set to poller.LockUDP, so
// taking it again here would deadlock). Cache write-through and
// NoticeWrite happen via the recordingClient as usual.
func (h *Handler) scheduleDial(name string) func(ctx context.Context) (breezy.DeviceClient, HandlerClient, error) {
	return func(ctx context.Context) (breezy.DeviceClient, HandlerClient, error) {
		if h.ClientFactory == nil {
			return nil, nil, errors.New("server: ClientFactory not configured")
		}
		raw, err := h.ClientFactory(name)
		if err != nil {
			return nil, nil, err
		}
		rc := newRecordingClient(raw, func(ws []breezy.ParamWrite) {
			h.recordWrite(name, ws)
		})
		return rc, raw, nil
	}
}
```

Add `Schedulers` to `Handler`:

```go
	Pollers map[string]*Poller
	// Schedulers are per-device subsystems that fire scheduled
	// Power/Mode/Speed writes. Populated by startPollers.
	Schedulers map[string]*Scheduler
```

- [ ] **Step 4: Extend `startPollers` in `cmd/breezyd/main.go`**

Change the signature and body:

```go
func startPollers(
	parent context.Context,
	devices map[string]DeviceConfig,
	interval time.Duration,
	stateDir string,
	state *State,
	metrics *Metrics,
	onPoll func(name string, snap Snapshot),
	scheduleDialFor func(name string) func(ctx context.Context) (breezy.DeviceClient, HandlerClient, error),
) (map[string]*Poller, map[string]*Scheduler, *sync.WaitGroup) {
	pollers := map[string]*Poller{}
	schedulers := map[string]*Scheduler{}
	wg := &sync.WaitGroup{}

	for name, d := range devices {
		if d.IP == "" {
			slog.Warn("no IP for device; skipping until discovery succeeds", "name", name)
			continue
		}
		devName := name
		devID := d.ID

		tr := &EnergyTracker{Device: devName, StateDir: stateDir}
		tr.Load()

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
			Energy: tr,
		}
		pollers[devName] = p

		sch := &Scheduler{
			Device:   devName,
			StateDir: stateDir,
			LockUDP:  p.LockUDP,
			Dial:     scheduleDialFor(devName),
		}
		_ = sch.Load()
		schedulers[devName] = sch

		wg.Add(2)
		go func() { defer wg.Done(); p.Run(parent) }()
		go func() { defer wg.Done(); sch.Run(parent) }()
	}

	return pollers, schedulers, wg
}
```

Update the caller in `run()`:

```go
	pollers, schedulers, pollersWg := startPollers(
		rootCtx, devices.Snapshot(), cfg.Daemon.PollInterval,
		stateDir, state, metrics, handler.SyncHomekit,
		handler.scheduleDial,
	)
	handler.Pollers = pollers
	handler.Schedulers = schedulers
```

- [ ] **Step 5: Build and run the full test suite**

```sh
go build ./...
just check
```

Expected: green.

- [ ] **Step 6: Commit**

```sh
git add cmd/breezyd/scheduler.go cmd/breezyd/scheduler_test.go cmd/breezyd/server.go cmd/breezyd/main.go
git commit -m "breezyd: wire Scheduler into startPollers; minute-aligned Run loop"
```

---

## Task 6: HTTP `GET`/`PUT /v1/devices/{name}/schedule`

**Goal:** Expose the schedule for read and replace via JSON. Validation echoes the rules from `Scheduler.validate`. On a valid PUT, the in-memory schedule is swapped (under `Scheduler.mu`), `lastApply` is cleared, and the file is atomically rewritten.

**Files:**
- Create: `cmd/breezyd/handlers_schedule.go`
- Modify: `cmd/breezyd/server.go` (route registration)
- Modify: `cmd/breezyd/server_test.go` (handler tests)
- Modify: `cmd/breezyd/scheduler.go` (add `Replace(enabled, entries)` so the handler doesn't reach into `Scheduler.mu` directly)

**Acceptance Criteria:**
- [ ] `GET /v1/devices/playroom/schedule` returns the in-memory state as JSON. 200 always (no UDP). 404 if device unknown.
- [ ] `PUT /v1/devices/playroom/schedule` with a valid body returns 200 and the saved schedule (echo). Invalid body returns 400 with `code:"bad_request"`.
- [ ] `Replace` swaps the in-memory state, sorts entries by At, clears `lastApply` and `retry`, and persists to disk atomically. Subsequent `Snapshot` reflects the new state.
- [ ] An empty schedule with `enabled=true` is allowed (no-op).

**Verify:**

```sh
go test ./cmd/breezyd/ -run TestHandler_GetSchedule -v
go test ./cmd/breezyd/ -run TestHandler_PutSchedule -v
```

Expected: PASS.

**Steps:**

- [ ] **Step 1: Add `Replace` to `scheduler.go`**

```go
// Replace swaps the schedule wholesale. Validates, sorts entries, clears
// retry and lastApply (a fresh schedule starts fresh — no stale alert
// banner), and persists. Returns errors wrapping ErrInvalidArg on bad
// input.
func (s *Scheduler) Replace(enabled bool, entries []ScheduleEntry) error {
	if err := s.validate(enabled, entries); err != nil {
		return err
	}
	cp := append([]ScheduleEntry(nil), entries...)
	sortEntries(cp)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = enabled
	s.entries = cp
	s.lastApply = nil
	s.retry = nil
	if err := s.save(); err != nil {
		return fmt.Errorf("schedule: persist: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Write failing handler tests in `server_test.go`**

Add near the other PUT/POST handler tests:

```go
func TestHandler_GetSchedule_Empty(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/schedule", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["enabled"] != false {
		t.Errorf("enabled=%v, want false", body["enabled"])
	}
	if entries, _ := body["entries"].([]any); len(entries) != 0 {
		t.Errorf("entries=%v, want empty", entries)
	}
}

func TestHandler_PutSchedule_Roundtrip(t *testing.T) {
	h, _, _ := newServerHandler(t)
	put := map[string]any{
		"enabled": true,
		"entries": []map[string]any{
			{"at": "08:00", "action": "regeneration", "pct": 60},
			{"at": "22:00", "action": "off", "pct": 60},
		},
	}
	rec := doRequest(t, h, http.MethodPut, "/v1/devices/playroom/schedule", put)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/schedule", nil)
	var got map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &got)
	if got["enabled"] != true {
		t.Errorf("enabled=%v, want true", got["enabled"])
	}
	entries, _ := got["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("entries=%v, want 2", entries)
	}
}

func TestHandler_PutSchedule_Validation(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
	}{
		{"bad action", map[string]any{"enabled": true, "entries": []map[string]any{{"at": "08:00", "action": "boost", "pct": 60}}}},
		{"low pct", map[string]any{"enabled": true, "entries": []map[string]any{{"at": "08:00", "action": "regeneration", "pct": 5}}}},
		{"high pct", map[string]any{"enabled": true, "entries": []map[string]any{{"at": "08:00", "action": "regeneration", "pct": 101}}}},
		{"bad at", map[string]any{"enabled": true, "entries": []map[string]any{{"at": "08:60", "action": "regeneration", "pct": 60}}}},
		{"duplicate at", map[string]any{"enabled": true, "entries": []map[string]any{
			{"at": "10:00", "action": "regeneration", "pct": 60},
			{"at": "10:00", "action": "off", "pct": 60},
		}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPut, "/v1/devices/playroom/schedule", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_PutSchedule_Persists(t *testing.T) {
	h, stateDir, _ := newServerHandler(t)
	put := map[string]any{
		"enabled": true,
		"entries": []map[string]any{{"at": "08:00", "action": "regeneration", "pct": 60}},
	}
	rec := doRequest(t, h, http.MethodPut, "/v1/devices/playroom/schedule", put)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(stateDir, "schedule_playroom.json"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(data), `"action":"regeneration"`) {
		t.Errorf("file missing entry: %s", data)
	}
}
```

If `newServerHandler` doesn't already return `stateDir`, extend it to do so — most likely `newServerHandler` constructs an in-memory `Handler` and you'll need to also wire a per-test Scheduler (one per device). The simplest retrofit:

In `server_test.go`'s helper that builds the Handler, also build a `*Scheduler` for each test device with `StateDir: t.TempDir()` and assign `h.Schedulers = map[string]*Scheduler{...}`. Adjust the helper return to include the `stateDir` path so the persistence test can read the file.

If your existing `newServerHandler` is structured differently, follow whatever pattern the energy tracker tests use to plumb a per-device `EnergyTracker`. The principle: tests get a real `Scheduler`; the new HTTP handlers go through `Handler.Schedulers[name]`.

- [ ] **Step 3: Run, verify failures**

```sh
go test ./cmd/breezyd/ -run TestHandler_GetSchedule -v
go test ./cmd/breezyd/ -run TestHandler_PutSchedule -v
```

Expected: FAIL (route unregistered, helpers not in place).

- [ ] **Step 4: Implement the handlers in `cmd/breezyd/handlers_schedule.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// Schedule HTTP endpoints.
package main

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// scheduleResponse is the over-wire JSON shape for GET and PUT.
type scheduleResponse struct {
	Enabled   bool            `json:"enabled"`
	Entries   []ScheduleEntry `json:"entries"`
	LastApply *LastApply      `json:"last_apply,omitempty"`
}

// getSchedule renders the in-memory schedule.
func (h *Handler) getSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	sch, ok := h.Schedulers[name]
	if !ok || sch == nil {
		// Shouldn't happen in production (every device has a Scheduler),
		// but tests may construct a Handler without one.
		writeJSON(w, http.StatusOK, scheduleResponse{Enabled: false, Entries: []ScheduleEntry{}})
		return
	}
	snap := sch.Snapshot()
	writeJSON(w, http.StatusOK, scheduleResponse{
		Enabled:   snap.Enabled,
		Entries:   nilToEmpty(snap.Entries),
		LastApply: snap.LastApply,
	})
}

// putSchedule replaces the schedule wholesale. Validation lives in
// Scheduler.Replace; ErrInvalidArg → 400 bad_request.
func (h *Handler) putSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		Enabled bool            `json:"enabled"`
		Entries []ScheduleEntry `json:"entries"`
	}
	if !readBody(w, r, &body) {
		return
	}
	sch, ok := h.Schedulers[name]
	if !ok || sch == nil {
		writeErr(w, "internal", fmt.Sprintf("device %q has no scheduler wired", name))
		return
	}
	if err := sch.Replace(body.Enabled, body.Entries); err != nil {
		if errors.Is(err, breezy.ErrInvalidArg) {
			writeErr(w, "bad_request", err.Error())
			return
		}
		writeErr(w, "internal", err.Error())
		return
	}
	snap := sch.Snapshot()
	writeJSON(w, http.StatusOK, scheduleResponse{
		Enabled:   snap.Enabled,
		Entries:   nilToEmpty(snap.Entries),
		LastApply: snap.LastApply,
	})
}

// nilToEmpty makes JSON render `[]` instead of `null` when the schedule
// has no entries.
func nilToEmpty(e []ScheduleEntry) []ScheduleEntry {
	if e == nil {
		return []ScheduleEntry{}
	}
	return e
}
```

- [ ] **Step 5: Register routes in `cmd/breezyd/server.go`**

Inside `routes()`, near the other GET/POST registrations:

```go
	mux.HandleFunc("GET /v1/devices/{name}/schedule", h.getSchedule)
	mux.HandleFunc("PUT /v1/devices/{name}/schedule", h.putSchedule)
```

- [ ] **Step 6: Run handler tests**

```sh
go test ./cmd/breezyd/ -run TestHandler_GetSchedule -v
go test ./cmd/breezyd/ -run TestHandler_PutSchedule -v
just check
```

Expected: PASS.

- [ ] **Step 7: Commit**

```sh
git add cmd/breezyd/handlers_schedule.go cmd/breezyd/server.go cmd/breezyd/server_test.go cmd/breezyd/scheduler.go
git commit -m "breezyd: GET/PUT /v1/devices/{name}/schedule handlers"
```

---

## Task 7: Status JSON `service.schedule` glue

**Goal:** Have `getDevice` (`GET /v1/devices/{name}`) include `service.schedule` in the response so the dashboard's renderer can read it without calling `/schedule` separately. Mirrors how `service.energy` is glued today.

**Files:**
- Modify: `cmd/breezyd/handlers_device.go` (extend `getDevice`)
- Modify: `cmd/breezyd/server_test.go` (add a short assertion that `service.schedule` is present)

**Acceptance Criteria:**
- [ ] When the Handler has a Scheduler for the device, `GET /v1/devices/{name}` JSON has `service.schedule = {enabled, entries, alert, last_apply?}`.
- [ ] `alert` is `last_apply != nil && !last_apply.ok` (boolean), separate from the nested `last_apply` object so the UI can branch on it without descending.
- [ ] When the device has no Scheduler (test cases that don't wire one), the field is omitted entirely.

**Verify:**

```sh
go test ./cmd/breezyd/ -run TestHandler_GetDevice_IncludesSchedule -v
```

Expected: PASS.

**Steps:**

- [ ] **Step 1: Write the failing assertion**

Add to `server_test.go`:

```go
func TestHandler_GetDevice_IncludesSchedule(t *testing.T) {
	h, _, _ := newServerHandler(t)
	// Seed a schedule so service.schedule isn't trivially empty.
	put := map[string]any{
		"enabled": true,
		"entries": []map[string]any{{"at": "08:00", "action": "regeneration", "pct": 60}},
	}
	if rec := doRequest(t, h, http.MethodPut, "/v1/devices/playroom/schedule", put); rec.Code != http.StatusOK {
		t.Fatalf("seed PUT failed: %s", rec.Body.String())
	}
	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	svc, _ := body["service"].(map[string]any)
	sched, _ := svc["schedule"].(map[string]any)
	if sched == nil {
		t.Fatalf("service.schedule missing from response: %s", rec.Body.String())
	}
	if sched["enabled"] != true {
		t.Errorf("service.schedule.enabled=%v, want true", sched["enabled"])
	}
	if entries, _ := sched["entries"].([]any); len(entries) != 1 {
		t.Errorf("service.schedule.entries=%v, want 1", entries)
	}
	if _, ok := sched["alert"]; !ok {
		t.Errorf("service.schedule.alert missing")
	}
}
```

- [ ] **Step 2: Run, verify failure**

```sh
go test ./cmd/breezyd/ -run TestHandler_GetDevice_IncludesSchedule -v
```

Expected: FAIL — service.schedule missing.

- [ ] **Step 3: Extend `getDevice` in `cmd/breezyd/handlers_device.go`**

Replace the existing tail of `getDevice` (after `BuildStatusWithEnergy`):

```go
	resp := breezy.BuildStatusWithEnergy(snap.Values, name, cfg.ID, ip, lastPoll, ev)
	if sch, ok := h.Schedulers[name]; ok && sch != nil {
		s := sch.Snapshot()
		alert := s.LastApply != nil && !s.LastApply.OK
		resp.Service["schedule"] = map[string]any{
			"enabled":    s.Enabled,
			"entries":    nilToEmpty(s.Entries),
			"alert":      alert,
			"last_apply": s.LastApply, // omitted by JSON encoder when nil
		}
	}
	writeJSON(w, http.StatusOK, resp)
```

The `last_apply` field will JSON-encode as `null` when nil; if you want it omitted entirely, build the map conditionally:

```go
		entry := map[string]any{
			"enabled": s.Enabled,
			"entries": nilToEmpty(s.Entries),
			"alert":   alert,
		}
		if s.LastApply != nil {
			entry["last_apply"] = s.LastApply
		}
		resp.Service["schedule"] = entry
```

Use the second form.

- [ ] **Step 4: Re-run tests**

```sh
go test ./cmd/breezyd/ -run TestHandler_GetDevice -v
just check
```

Expected: PASS.

- [ ] **Step 5: Commit**

```sh
git add cmd/breezyd/handlers_device.go cmd/breezyd/server_test.go
git commit -m "breezyd: include service.schedule on GET /v1/devices/{name}"
```

---

## Task 8: SCHEDULE UI block

**Goal:** Build the collapsible SCHEDULE block with table, inline editing, validation feedback, save handler, and alert force-expand. Place it immediately above the existing Controls block.

**Files:**
- Modify: `cmd/breezyd/ui/index.html`

**Acceptance Criteria:**
- [ ] A new `<details class="block schedule">` appears in each card immediately above the Controls block. Collapsed by default.
- [ ] `service.schedule.alert === true` forces it open (`<details open>`).
- [ ] Renders a table of rows: At (`<input type="time">`), Action (`<select>` with the five options), Pct (`<input type="number" min="10" max="100">`), and a `×` remove button.
- [ ] Pct input is `readonly` and visually greyed when Action=`off`.
- [ ] `[+ add entry]` appends a row with defaults `08:00 / regeneration / 60`.
- [ ] An "Enabled" checkbox sits in the panel header / first row alongside `[save]`.
- [ ] `[save]` is disabled when validation fails (duplicate At-times, blank At, pct out of range). Error rows have a `.row-error` class.
- [ ] Clicking `[save]` PUTs `{enabled, entries[]}` to `/v1/devices/{name}/schedule` via `postWrite` (or a new `putWrite` mirroring it). Pct values <10 are clamped to 10 client-side before sending. On error, a toast is shown via the existing toast machinery.
- [ ] When `service.schedule.alert === true`, a `<div class="warn">` line below the table reads `⚠ last apply HH:MM failed: <err>`, with a second line `retried N times` when `retries > 0`.
- [ ] On `.stale` cards, the `[save]` button is disabled (consistent with the rest of the controls).

**Verify:** Manual smoke — start the daemon, open the dashboard, expand a SCHEDULE block, add a row, save, refresh, confirm round-trip. Playwright (Task 9) locks the behaviour.

**Steps:**

- [ ] **Step 1: Read the existing `index.html` rendering pattern**

Specifically look at:
- The Sensors block (`<details class="block sensors">`) for the alert force-expand pattern (around line 501).
- `renderControls` for the click-delegation conventions.
- `postWrite` for the request/error/toast machinery (around line 1294).

You'll mirror Sensors for the open-state computation and the renderControls click pattern for the save button.

- [ ] **Step 2: Add CSS for the schedule block**

Inside the `<style>` element, after the existing `.block.sensors` rules:

```css
  /* SCHEDULE block layout. Mirrors .block.sensors for the open chevron
     and force-expand handling; adds a small table grid for the entries. */
  details.block.schedule > summary {
    display: flex; align-items: baseline; gap: 1ch; cursor: pointer;
    list-style: none;
  }
  details.block.schedule > summary::-webkit-details-marker { display: none; }
  details.block.schedule > summary::before {
    content: "▶"; font-size: 0.65em; color: #888; align-self: center;
  }
  details.block.schedule[open] > summary::before { content: "▼"; }
  details.block.schedule > summary > h3 { margin: 0; }
  .schedule-toolbar {
    display: flex; gap: 0.5rem; align-items: center;
    margin: 0.4rem 0; font-size: 0.8rem;
  }
  .schedule-toolbar .grow { flex: 1 1 auto; }
  .schedule-table { width: 100%; border-collapse: collapse; font-size: 0.8rem; }
  .schedule-table th, .schedule-table td {
    padding: 0.2rem 0.3rem; text-align: left; border-bottom: 1px solid #eee;
  }
  .schedule-table th { color: #666; font-weight: 600; font-size: 0.7rem; text-transform: uppercase; }
  .schedule-table input[type="time"],
  .schedule-table select,
  .schedule-table input[type="number"] {
    font-family: inherit; font-size: 0.8rem; padding: 0.1rem 0.2rem;
  }
  .schedule-table input.pct-disabled { background: #eee; color: #888; }
  .schedule-table tr.row-error td { background: #fee; }
  .schedule-table button.del { font-size: 0.85rem; padding: 0 0.4rem; }
```

- [ ] **Step 3: Add a renderer function `renderSchedule(name, snap, stale)` near `renderEnergy`**

```js
// scheduleEdits[name] holds the in-flight, user-edited rows. Cleared on
// save success and on a successful refresh that observes the new state.
const scheduleEdits = {};

function renderSchedule(name, snap, stale) {
  const sch = snap.service?.schedule;
  if (!sch) return ""; // no scheduler wired (test handler / pre-feature daemon)

  const alert = sch.alert === true;
  const open = alert || (scheduleOpen[name] === true);

  // Source of truth: an in-flight edit buffer if the user is editing,
  // otherwise the server's snapshot.
  const edit = scheduleEdits[name];
  const enabled = edit ? edit.enabled : (sch.enabled === true);
  const entries = edit ? edit.entries : (sch.entries || []);

  const rows = entries.map((e, i) => renderScheduleRow(name, e, i, stale)).join("");
  const validation = validateSchedule(entries);

  return `<details class="block schedule"${open ? " open" : ""}>
    <summary><h3>SCHEDULE</h3></summary>
    <div class="schedule-toolbar">
      <label>
        <input type="checkbox" data-action="schedule-enabled" data-name="${esc(name)}"
               ${enabled ? "checked" : ""} ${stale ? "disabled" : ""}>
        enabled
      </label>
      <span class="grow"></span>
      <button data-action="schedule-add" data-name="${esc(name)}" ${stale ? "disabled" : ""}>+ add entry</button>
      <button data-action="schedule-save" data-name="${esc(name)}"
              ${stale || !validation.ok ? "disabled" : ""}
              title="${esc(validation.tooltip)}">save</button>
    </div>
    ${entries.length === 0 ? `<div class="ctrl-label">no entries</div>` : `
      <table class="schedule-table">
        <thead><tr><th>at</th><th>action</th><th>pct</th><th></th></tr></thead>
        <tbody>${rows}</tbody>
      </table>`}
    ${alert && sch.last_apply
      ? `<div class="warn">⚠ last apply ${esc(formatScheduleAt(sch.last_apply.at))} failed: ${esc(sch.last_apply.err || "")}
         ${sch.last_apply.retries > 0 ? `<br>retried ${sch.last_apply.retries} times` : ""}</div>`
      : ""}
    ${(toasts[name] || {}).schedule ? `<div class="toast" role="alert">${esc(toasts[name].schedule)}</div>` : ""}
  </details>`;
}

function renderScheduleRow(name, e, i, stale) {
  const dis = stale ? "disabled" : "";
  const isOff = e.action === "off";
  const opts = ["off", "regeneration", "ventilation", "supply", "extract"]
    .map(v => `<option value="${v}"${e.action === v ? " selected" : ""}>${v}</option>`).join("");
  const pctClass = isOff ? "pct-disabled" : "";
  const pctReadonly = isOff ? "readonly" : "";
  return `<tr data-row="${i}" data-name="${esc(name)}">
    <td><input type="time" data-action="schedule-at" data-row="${i}" data-name="${esc(name)}"
               value="${esc(e.at || "")}" ${dis}></td>
    <td><select data-action="schedule-action" data-row="${i}" data-name="${esc(name)}" ${dis}>${opts}</select></td>
    <td><input type="number" min="10" max="100" data-action="schedule-pct" data-row="${i}" data-name="${esc(name)}"
               class="${pctClass}" value="${e.pct ?? 60}" ${pctReadonly} ${dis}></td>
    <td><button class="del" data-action="schedule-del" data-row="${i}" data-name="${esc(name)}" ${dis}>×</button></td>
  </tr>`;
}

function validateSchedule(entries) {
  if (entries.length > 24) return { ok: false, tooltip: "at most 24 entries" };
  const seen = new Set();
  for (const e of entries) {
    if (!/^\d{2}:\d{2}$/.test(e.at || "")) return { ok: false, tooltip: "missing or invalid time" };
    const [h, m] = e.at.split(":").map(Number);
    if (h > 23 || m > 59) return { ok: false, tooltip: `invalid time ${e.at}` };
    if (seen.has(e.at)) return { ok: false, tooltip: `two entries at ${e.at}` };
    seen.add(e.at);
    const pct = Number(e.pct);
    if (!Number.isFinite(pct) || pct < 10 || pct > 100) return { ok: false, tooltip: "pct must be 10-100" };
  }
  return { ok: true, tooltip: "" };
}

function formatScheduleAt(at) {
  // last_apply.at is a ScheduleTime int over JSON; render as HH:MM.
  if (typeof at === "number") {
    const h = Math.floor(at / 60);
    const m = at % 60;
    return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}`;
  }
  return String(at ?? "");
}
```

Note: the `last_apply.at` field is serialised by Go as the integer underlying type of `ScheduleTime`. If you'd rather have it serialised as `"HH:MM"`, change `LastApply.At` to also use a JSON-aware wrapper in `scheduler.go` — but the renderer above handles both cases.

- [ ] **Step 4: Insert `renderSchedule` into `renderCard`** — locate the line that calls `renderControls` (around line 511) and insert immediately above it:

```js
    ${renderSchedule(name, snap, stale)}

    ${renderControls(name, snap, stale)}
```

Also add the `scheduleOpen` state object next to the existing `energyOpen` and `sensorsCollapsed` objects:

```js
const scheduleOpen = {};
```

And add the toggle listener at the bottom of the file (where `energyOpen` is tracked, around line 1330):

```js
document.addEventListener("toggle", (e) => {
  const det = e.target;
  if (!(det instanceof HTMLDetailsElement)) return;
  // existing energy/sensors handling above this line
  if (det.classList.contains("schedule")) {
    const card = det.closest(".card");
    const name = card?.querySelector("h2")?.textContent?.trim();
    if (name) scheduleOpen[name] = det.open;
  }
}, true);
```

- [ ] **Step 5: Wire click delegation** — locate the existing `case "heater":` switch and add cases for the schedule actions:

```js
    case "schedule-enabled": {
      const e = el; // the checkbox
      ensureScheduleEdit(name);
      scheduleEdits[name].enabled = e.checked;
      reflow();
      break;
    }
    case "schedule-add":
      ensureScheduleEdit(name);
      scheduleEdits[name].entries.push({ at: "08:00", action: "regeneration", pct: 60 });
      reflow();
      break;
    case "schedule-del": {
      const idx = Number(el.dataset.row);
      ensureScheduleEdit(name);
      scheduleEdits[name].entries.splice(idx, 1);
      reflow();
      break;
    }
    case "schedule-save": {
      const buf = scheduleEdits[name];
      if (!buf) break; // nothing to save
      // Clamp pct to >= 10 client-side (matches firmware floor + spec).
      const cleaned = buf.entries.map(e => ({
        at: e.at,
        action: e.action,
        pct: Math.max(10, Math.min(100, Number(e.pct) || 10)),
      }));
      const ok = await postWrite(name, "schedule",
        "/v1/devices/" + encodeURIComponent(name) + "/schedule",
        { enabled: buf.enabled, entries: cleaned },
        "PUT");
      if (ok) delete scheduleEdits[name];
      break;
    }
```

For input change events (At and Pct cells), add an `input` listener — these don't go through the `click` switch:

```js
document.addEventListener("input", (e) => {
  const el = e.target;
  if (!(el instanceof HTMLInputElement || el instanceof HTMLSelectElement)) return;
  const action = el.dataset.action;
  const name = el.dataset.name;
  const row = Number(el.dataset.row);
  if (!name || isNaN(row)) return;
  if (action === "schedule-at" || action === "schedule-action" || action === "schedule-pct") {
    ensureScheduleEdit(name);
    const entry = scheduleEdits[name].entries[row];
    if (!entry) return;
    if (action === "schedule-at") entry.at = el.value;
    else if (action === "schedule-action") entry.action = el.value;
    else entry.pct = Number(el.value);
    reflow();
  }
});

function ensureScheduleEdit(name) {
  if (scheduleEdits[name]) return;
  // Seed the edit buffer from the latest server snapshot.
  const sch = lastSnap[name]?.service?.schedule || { enabled: false, entries: [] };
  scheduleEdits[name] = {
    enabled: sch.enabled === true,
    entries: (sch.entries || []).map(e => ({ at: e.at, action: e.action, pct: e.pct })),
  };
}
```

`lastSnap` is whatever variable currently holds the most recent rendered snapshots. If your code uses a different name (e.g. `data` inside `render`), use that — search for where `renderCard(name, snap, stale)` is called from and find the parent map.

`reflow` is whatever function currently triggers a re-render of one card or the whole grid. Match the existing pattern (often just a wrapper around `render(currentData)`).

- [ ] **Step 6: Extend `postWrite` to support PUT** — search for `postWrite` (around line 1294) and add an optional method parameter:

```js
async function postWrite(name, controlKey, url, body, method = "POST") {
  // ... existing in-flight logic
  const resp = await fetch(url, {
    method,
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  });
  // ... existing response handling
}
```

If your existing implementation hard-codes "POST", change it to use `method` as above — backward-compatible because the default stays "POST".

- [ ] **Step 7: Build the daemon and smoke-test in a browser**

```sh
just build
./breezyd -config ~/.config/breezy/config.toml &
sleep 1
xdg-open http://localhost:9876/   # or open the URL manually
```

Confirm visually:
1. Each card has a `▶ SCHEDULE` summary.
2. Expanding it shows an empty table and the "+ add entry" / "save" buttons.
3. Adding a row populates At/Action/Pct fields with the defaults.
4. Setting Action to "off" greys the Pct field and makes it read-only.
5. Setting two rows to the same At-time disables the save button.
6. A successful save round-trips: refresh the page; the new entries appear from the server.

```sh
kill %1 2>/dev/null || true
```

- [ ] **Step 8: Commit**

```sh
git add cmd/breezyd/ui/index.html
git commit -m "ui: add SCHEDULE block with inline editor, validation, alert force-expand"
```

---

## Task 9: Playwright tests

**Goal:** Lock the SCHEDULE UI contract: rendering, validation feedback, alert force-expand, save round-trip.

**Files:**
- Modify: `tests/ui/dashboard.spec.ts`

**Acceptance Criteria:**
- [ ] `just test-ui` passes.
- [ ] Six new test cases cover empty, populated, off-greys-pct, validation, alert force-expand, save round-trip.

**Verify:**

```sh
just test-ui
```

Expected: PASS.

**Steps:**

- [ ] **Step 1: Read the existing harness**

Open `tests/ui/dashboard.spec.ts`. Identify the names of: the snapshot builder, the route helper, and any post-capture helper. Use them as-is — do not introduce new helpers.

- [ ] **Step 2: Add test cases at the end of the file**

```ts
test("schedule empty: collapsed block with no entries", async ({ page }) => {
  await serve(page, [
    baseSnapshot("playroom", { service: { schedule: { enabled: false, entries: [], alert: false } } }),
  ]);
  const card = page.locator(".card", { has: page.locator("h2", { hasText: "playroom" }) });
  await expect(card.locator("details.schedule")).toBeVisible();
  await expect(card.locator("details.schedule")).not.toHaveAttribute("open", "");
  await expect(card).toContainText("no entries");
});

test("schedule populated: rows render with At, Action, Pct", async ({ page }) => {
  await serve(page, [
    baseSnapshot("playroom", { service: { schedule: {
      enabled: true,
      entries: [
        { at: "08:00", action: "regeneration", pct: 60 },
        { at: "22:00", action: "off", pct: 60 },
      ],
      alert: false,
    } } }),
  ]);
  const card = page.locator(".card", { has: page.locator("h2", { hasText: "playroom" }) });
  await card.locator("details.schedule > summary").click();
  await expect(card.locator(".schedule-table tbody tr")).toHaveCount(2);
  await expect(card.locator(".schedule-table tbody tr").first()).toContainText("regeneration");
});

test("schedule action=off greys the pct input", async ({ page }) => {
  await serve(page, [
    baseSnapshot("playroom", { service: { schedule: {
      enabled: true,
      entries: [{ at: "22:00", action: "off", pct: 60 }],
      alert: false,
    } } }),
  ]);
  const card = page.locator(".card", { has: page.locator("h2", { hasText: "playroom" }) });
  await card.locator("details.schedule > summary").click();
  const pct = card.locator('input[data-action="schedule-pct"]');
  await expect(pct).toHaveAttribute("readonly", "");
  await expect(pct).toHaveClass(/pct-disabled/);
});

test("schedule duplicate-at disables save", async ({ page }) => {
  await serve(page, [
    baseSnapshot("playroom", { service: { schedule: {
      enabled: true,
      entries: [
        { at: "10:00", action: "regeneration", pct: 60 },
        { at: "10:00", action: "off", pct: 60 },
      ],
      alert: false,
    } } }),
  ]);
  const card = page.locator(".card", { has: page.locator("h2", { hasText: "playroom" }) });
  await card.locator("details.schedule > summary").click();
  const save = card.locator('button[data-action="schedule-save"]');
  await expect(save).toBeDisabled();
});

test("schedule alert force-expand: panel open with warn line", async ({ page }) => {
  await serve(page, [
    baseSnapshot("playroom", { service: { schedule: {
      enabled: true,
      entries: [{ at: "22:00", action: "off", pct: 60 }],
      alert: true,
      last_apply: { at: 1320, fired: "2026-05-06T22:00:14+01:00", ok: false,
                    err: "device_unreachable: i/o timeout", retries: 5 },
    } } }),
  ]);
  const card = page.locator(".card", { has: page.locator("h2", { hasText: "playroom" }) });
  await expect(card.locator("details.schedule")).toHaveAttribute("open", "");
  await expect(card.locator("details.schedule .warn")).toContainText("22:00");
  await expect(card.locator("details.schedule .warn")).toContainText("device_unreachable");
  await expect(card.locator("details.schedule .warn")).toContainText("retried 5 times");
});

test("schedule save: PUTs the edited table", async ({ page }) => {
  await serve(page, [
    baseSnapshot("playroom", { service: { schedule: { enabled: false, entries: [], alert: false } } }),
  ]);
  const card = page.locator(".card", { has: page.locator("h2", { hasText: "playroom" }) });
  await card.locator("details.schedule > summary").click();

  const post = await captureNextPost(page, "/v1/devices/playroom/schedule"); // PUT also matches
  await card.locator('button[data-action="schedule-add"]').click();
  await card.locator('button[data-action="schedule-save"]').click();
  const captured = await post;
  expect(captured).toBeTruthy();
  expect(captured!.method).toBe("PUT");
  expect(captured!.body.entries.length).toBe(1);
  expect(captured!.body.entries[0]).toMatchObject({ at: "08:00", action: "regeneration", pct: 60 });
});
```

If `captureNextPost` doesn't capture PUTs as well as POSTs, extend it minimally to forward the method to the captured object. The change should be ≤5 lines.

- [ ] **Step 3: Run**

```sh
just test-ui
```

Expected: PASS.

- [ ] **Step 4: Commit**

```sh
git add tests/ui/dashboard.spec.ts
git commit -m "tests/ui: cover SCHEDULE block render, validation, alert, save round-trip"
```

---

## Task 10: Docs + screenshots

**Goal:** Update README, CHANGELOG, and CLAUDE.md to reflect the new feature; regenerate screenshots so the README's 3-col image shows the SCHEDULE block.

**Files:**
- Modify: `tests/ui/screenshot.ts` (set one device to a populated schedule and force the panel open)
- Regenerate: `tests/ui/screenshots/dashboard-3col.png`, `tests/ui/screenshots/dashboard-1col.png`
- Modify: `README.md`
- Modify: `CHANGELOG.md`
- Modify: `CLAUDE.md`

**Acceptance Criteria:**
- [ ] `just screenshot` runs cleanly; both PNGs change.
- [ ] The regenerated 3-col PNG shows at least one card with the SCHEDULE block expanded and at least one row visible.
- [ ] README's dashboard feature paragraph mentions SCHEDULE.
- [ ] CHANGELOG has a new entry summarising the feature.
- [ ] CLAUDE.md has a "Schedule system" subsection under Architecture, describing the per-device Scheduler, state file, no-catch-up rule, and retry policy.

**Verify:**

```sh
just screenshot
git diff --stat tests/ui/screenshots/ README.md CHANGELOG.md CLAUDE.md
```

Expected: all four touched.

**Steps:**

- [ ] **Step 1: Update `tests/ui/screenshot.ts`**

Find where the snapshots are built. For `bedroom` (or another suitable device), inject a populated schedule via the `service.schedule` field in the snapshot:

```ts
service: {
  ...existingService,
  schedule: {
    enabled: true,
    entries: [
      { at: "08:00", action: "regeneration", pct: 60 },
      { at: "22:00", action: "off", pct: 60 },
    ],
    alert: false,
  },
},
```

Then in the screenshot script, after navigation, click the SCHEDULE summary on that card so the table is visible in the screenshot. Use the same selector pattern as existing per-card setup actions.

- [ ] **Step 2: Run the screenshot recipe**

```sh
just screenshot
```

Expected: completes cleanly, both PNGs updated.

- [ ] **Step 3: Visually confirm**

Open `tests/ui/screenshots/dashboard-3col.png`. The bedroom card should show:
- A `▼ SCHEDULE` heading (open).
- An "enabled" checkbox checked.
- Two rows in the table: 08:00/regeneration/60 and 22:00/off/—.

If layout looks wrong, fix CSS in `cmd/breezyd/ui/index.html` and re-run.

- [ ] **Step 4: Update README.md**

Find the dashboard feature paragraph (around line 30 — "The bundled web UI is one HTML file …"). Add `, schedule` to the list of controlled features:

```
The bundled web UI is one HTML file served from the daemon at `GET /`; auto-refreshes every 5 s, controls power/mode/speed/heater/timer and edits per-device schedules.
```

Add a short bullet to the feature list (around line 59-ish — the same area you'd update for any new feature):

```
- Daemon-driven per-device schedules (24hr cyclic): edit a small `At | Action | Pct` table from the dashboard; the daemon fires writes on schedule with retry-on-failure and an alert banner when a fire fails.
```

- [ ] **Step 5: Update CHANGELOG.md**

Add a new entry at the top, following the format of the most recent entry. Mention:
- New per-device `Scheduler` subsystem; daemon-driven 24hr cyclic schedule.
- New `GET`/`PUT /v1/devices/{name}/schedule` endpoints.
- New SCHEDULE block in the dashboard UI with collapsible panel, inline editing, and alert force-expand.
- Retry policy: 30s cadence, 10m deadline, superseded by next entry.
- State persisted to `<state_dir>/schedule_<device>.json`.

- [ ] **Step 6: Update CLAUDE.md**

Find the Architecture section's "Energy tracking (always on)" subsection. Add a parallel "Schedule system" subsection beneath it:

```markdown
### Schedule system (per device, opt-in via UI)

Each configured device gets a `Scheduler` goroutine (in `cmd/breezyd/scheduler.go`) that fires Power/Mode/SpeedManual writes at user-configured At-times each day. State is persisted at `<state_dir>/schedule_<device>.json` and survives restart. The schedule is event-driven, NOT state-driven: on daemon startup or schedule re-enable, the daemon does NOT immediately apply the entry-in-effect — only future transitions fire. (See `docs/superpowers/specs/2026-05-06-schedule-system-design.md` for why.)

Entries have `At | Action | Pct`. Action ∈ `{off, regeneration, ventilation, supply, extract}`; off powers the unit off, the others power-on + set the airflow mode + set speed=manual at the given Pct. Times are in the daemon host's local timezone.

Failed fires retry every 30 s for up to 10 min, abandoning early when the next entry's At-time arrives. `breezy.ErrAuth` is treated as a config error and does not retry. Failures surface as a force-expanded alert on the SCHEDULE block in the dashboard.

Editing happens exclusively from the web UI via `GET`/`PUT /v1/devices/{name}/schedule`. There are no CLI verbs and no HomeKit exposure for the schedule itself.
```

- [ ] **Step 7: Final check**

```sh
just check-all
```

Expected: lint + tests + race + Playwright all green.

- [ ] **Step 8: Commit**

```sh
git add tests/ui/screenshot.ts tests/ui/screenshots/dashboard-3col.png tests/ui/screenshots/dashboard-1col.png \
        README.md CHANGELOG.md CLAUDE.md
git commit -m "docs: document daemon-driven schedule system across README/CHANGELOG/CLAUDE"
```

---

## Final verification

After all tasks land:

```sh
just check-all
```

Expected: lint + fast tests + race tests + Playwright all green. The README's 3-col screenshot shows the SCHEDULE block with two entries on at least one card.

---

## Self-Review

**Spec coverage:**
- Per-device, persistent state at `<state_dir>/schedule_<device>.json` → Tasks 1, 2, 5 ✓
- HH:MM time picker, 5-action dropdown, 10–100 pct → Tasks 1, 8 ✓
- Local-time semantics → Task 3 (uses `now.Hour()`/`now.Minute()` against the local-zone time the daemon receives) ✓
- No-catch-up startup → Task 3 ✓
- Manual override permitted between transitions → Task 3 (no re-assertion, schedule fires at minute boundaries only) ✓
- Disable = no auto commands → Task 3 ✓
- Retry policy (30 s, 10 min, supersede, no-retry-on-auth) → Task 4 ✓
- Alert force-expand → Tasks 7 (status JSON) + 8 (UI) ✓
- HTTP `GET`/`PUT /schedule` with PUT-not-PATCH wholesale replace → Task 6 ✓
- Service JSON glue → Task 7 ✓
- UI block above Controls, collapsible, validation feedback, off-greys-pct, save handler → Task 8 ✓
- Tests (Go unit, HTTP handler, Playwright) → Tasks 1–9 ✓
- Screenshot regeneration → Task 10 ✓
- Documentation (README, CHANGELOG, CLAUDE.md) → Task 10 ✓
- Out-of-scope items (CLI verbs, HomeKit, day-of-week, baseline mode) — explicitly absent from all tasks ✓

**Placeholder scan:** none — every step has the actual code or command.

**Type consistency:**
- `ScheduleTime` is used as a typed int across all tasks; `String()` and `ParseScheduleTime` form the canonical HH:MM↔int pair.
- `ScheduleEntry.Action` is `string`, not an enum, with `validAction` as the source-of-truth set in `scheduler.go`. The handler, the UI, and `applyAction` all use the same five-string set.
- `Scheduler.Replace`, `Scheduler.Snapshot`, and the HTTP handler all agree on `(enabled bool, entries []ScheduleEntry)` as the public surface.
- `Scheduler.Dial` returns `(breezy.DeviceClient, HandlerClient, error)` — matched by `Handler.scheduleDial` and the test fake.
- The action→writes map (Power/SetMode/SetSpeedManual) is centralised in `applyAction`; both production fire and tests rely on the same path.
