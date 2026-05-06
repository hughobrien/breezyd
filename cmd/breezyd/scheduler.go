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

// sortEntries sorts in-place by At ascending. Used after Load and Replace
// to keep the in-memory and on-disk state canonically ordered.
func sortEntries(entries []ScheduleEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].At < entries[j].At })
}
