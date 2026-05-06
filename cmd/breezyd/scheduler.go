// SPDX-License-Identifier: GPL-3.0-or-later

// Per-device Scheduler that fires Power/Mode/Speed writes at user-configured
// At-times each day. Lives next to EnergyTracker as a sibling per-device
// subsystem; one goroutine per device, started by startPollers (wiring lands in Task 5).
//
// See docs/superpowers/specs/2026-05-06-schedule-system-design.md for the
// behavioural spec (no-catch-up, retry policy, alert force-expand, etc.).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	Action string
	Pct    int
}

type scheduleEntryWire struct {
	At     string `json:"at"`
	Action string `json:"action"`
	Pct    int    `json:"pct"`
}

func (e ScheduleEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal(scheduleEntryWire{
		At:     e.At.String(),
		Action: e.Action,
		Pct:    e.Pct,
	})
}

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

// LastApply records the outcome of the most recent fire attempt.
type LastApply struct {
	At      ScheduleTime `json:"at"`
	Fired   time.Time    `json:"fired"`
	OK      bool         `json:"ok"`
	Err     string       `json:"err,omitempty"`
	Retries int          `json:"retries"`
}

// retryState tracks an in-flight retry; populated and consumed in Task 4.
type retryState struct {
	entry       ScheduleEntry
	entryIndex  int
	attempts    int
	nextAttempt time.Time
	deadline    time.Time
}

// Scheduler is the per-device subsystem that fires writes at At-times.
// All mutable state guarded by mu.
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
	lastTick     ScheduleTime
	haveLastTick bool
}

var validAction = map[string]bool{
	"off":          true,
	"regeneration": true,
	"ventilation":  true,
	"supply":       true,
	"extract":      true,
}

const maxScheduleEntries = 24

// validate enforces every rule from the spec; errors wrap breezy.ErrInvalidArg.
func (s *Scheduler) validate(entries []ScheduleEntry) error {
	if len(entries) > maxScheduleEntries {
		return fmt.Errorf("%w: at most %d entries (got %d)", breezy.ErrInvalidArg, maxScheduleEntries, len(entries))
	}
	seen := make(map[ScheduleTime]bool, len(entries))
	for i, e := range entries {
		if !validAction[e.Action] {
			return fmt.Errorf("%w: entries[%d].action %q not one of off/regeneration/ventilation/supply/extract", breezy.ErrInvalidArg, i, e.Action)
		}
		if e.Pct > 100 {
			return fmt.Errorf("%w: entries[%d].pct must be ≤ 100 (got %d)", breezy.ErrInvalidArg, i, e.Pct)
		}
		if e.Action != "off" && e.Pct < 10 {
			return fmt.Errorf("%w: entries[%d].pct must be 10-100 for action %q (got %d)", breezy.ErrInvalidArg, i, e.Action, e.Pct)
		}
		if seen[e.At] {
			return fmt.Errorf("%w: duplicate entry at %s", breezy.ErrInvalidArg, e.At)
		}
		seen[e.At] = true
	}
	return nil
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

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

// Snapshot returns a value copy of the scheduler's public state.
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

// Load reads the persisted state file. Always returns nil: missing file
// → empty state; malformed or invalid file → empty state + slog.Warn.
// Mirrors EnergyTracker.Load semantics. Caller must guarantee no
// concurrent access — Load is called before the scheduler's Run goroutine
// starts, so no mutex is needed (and acquiring it here would imply a
// false guarantee about safety against concurrent Loads).
func (s *Scheduler) Load() error {
	data, err := os.ReadFile(s.statePath())
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("schedule: failed to read state file; starting empty",
				"device", s.Device, "err", err)
		}
		return nil
	}
	// version is read but not yet checked; extend here if the schema changes.
	var p persistedSchedule
	if err := json.Unmarshal(data, &p); err != nil {
		slog.Warn("schedule: failed to unmarshal state file; starting empty",
			"device", s.Device, "err", err)
		return nil
	}
	if err := s.validate(p.Entries); err != nil {
		slog.Warn("schedule: persisted file failed validation; starting empty",
			"device", s.Device, "err", err)
		return nil
	}
	sortEntries(p.Entries)
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

// Replace swaps the schedule wholesale. Validates, sorts entries, clears
// retry and lastApply (a fresh schedule starts fresh — no stale alert
// banner), and persists. Returns errors wrapping ErrInvalidArg on bad
// input.
func (s *Scheduler) Replace(enabled bool, entries []ScheduleEntry) error {
	if err := s.validate(entries); err != nil {
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

// sortEntries sorts in-place by At ascending. Used after Load and Replace
// to keep the in-memory and on-disk state canonically ordered.
func sortEntries(entries []ScheduleEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].At < entries[j].At })
}

// fireTimeout bounds a single fire attempt's UDP round-trip.
const fireTimeout = 5 * time.Second

const (
	retryCadence  = 30 * time.Second
	retryDeadline = 10 * time.Minute
)

// tick processes one minute boundary. now is the wall-clock; tests pass
// a synthetic value via s.Now.
//
// Window detection uses the half-open interval (lastTick, nowMinute];
// midnight wraparound (nowMinute < lastTick) becomes the union
// (lastTick, 1440) ∪ [0, nowMinute]. Multiple matches fire the latest
// one only — earlier matches are stale.
//
// Retry state-machine: after a transient failure, fire retries every 30s
// for up to 10 minutes. A newer entry arriving in the same tick window
// supersedes the in-flight retry. Disable always clears it.
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
		// fire a backlog of just-crossed entries. Also clear any
		// in-flight retry.
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

	// Retry path. If a retry is in flight, decide whether to:
	//   (a) supersede it because a newer entry crosses now this tick,
	//   (b) attempt the retry because nextAttempt has arrived,
	//   (c) abandon it because we've passed the deadline.
	s.mu.Lock()
	r := s.retry
	s.mu.Unlock()
	retryFired := false
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
			// Drop the retry; transition detection below will fire the newer entry.
			s.mu.Lock()
			s.retry = nil
			s.mu.Unlock()
		case !now.Before(r.deadline):
			// Abandon. lastApply.ok stays false so the UI keeps the alert.
			s.mu.Lock()
			s.retry = nil
			if err := s.save(); err != nil {
				slog.Warn("schedule: save after deadline-abandon failed", "device", s.Device, "err", err)
			}
			s.mu.Unlock()
		case !now.Before(r.nextAttempt):
			s.fire(ctx, r.entry, r.entryIndex, now)
			retryFired = true
		}
	}

	// Transition detection. Skip when a retry attempt fired this tick —
	// in practice the window will have moved past r.entry.At by then,
	// but the explicit gate avoids any risk of double-fire.
	dist := func(t ScheduleTime) int {
		d := int(t) - int(lastTick)
		if d <= 0 {
			d += 1440
		}
		return d
	}
	var latest *ScheduleEntry
	latestIdx := -1
	if !retryFired {
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
// completion and persists. On transient failure, installs or extends the
// retry state machine (30s cadence, 10-minute deadline). ErrAuth fails
// fast with no retry.
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

	if errors.Is(fireErr, breezy.ErrAuth) {
		s.lastApply = &LastApply{
			At: e.At, Fired: now, OK: false,
			Err:     "auth_failed: " + fireErr.Error(),
			Retries: 0,
		}
		s.retry = nil
		if err := s.save(); err != nil {
			slog.Warn("schedule: save after auth-fail failed", "device", s.Device, "err", err)
		}
		slog.Warn("schedule: auth failure, not retrying", "device", s.Device, "at", e.At.String())
		return
	}

	// Transient failure: install or extend retry.
	attempts := 1
	deadline := now.Add(retryDeadline)
	if s.retry != nil && s.retry.entry.At == e.At {
		attempts = s.retry.attempts + 1
		deadline = s.retry.deadline // keep the original 10-min cap
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
		Err: fireErr.Error(),
		// attempts counts fire calls for this entry (1 = first attempt,
		// 2 = first retry, ...). Retries counts retries beyond the first
		// attempt, so it's attempts - 1.
		Retries: attempts - 1,
	}
	if err := s.save(); err != nil {
		slog.Warn("schedule: save after fail failed", "device", s.Device, "err", err)
	}
	slog.Warn("schedule: fire failed; retry installed",
		"device", s.Device, "at", e.At.String(), "attempts", attempts, "err", fireErr)
}

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
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
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
