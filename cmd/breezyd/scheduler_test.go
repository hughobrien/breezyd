// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// schedFakeClient implements breezy.DeviceClient for tests.
// mu protects writes so tests that poll writes from the main goroutine
// while a syncer/scheduler goroutine appends are race-free.
type schedFakeClient struct {
	mu     sync.Mutex
	writes [][]breezy.ParamWrite
	err    error
}

func (f *schedFakeClient) ReadParams(_ context.Context, _ []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
	return map[breezy.ParamID][]byte{}, nil
}
func (f *schedFakeClient) WriteParams(_ context.Context, ws []breezy.ParamWrite) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.writes = append(f.writes, append([]breezy.ParamWrite(nil), ws...))
	return nil
}
func (f *schedFakeClient) IsLocal() bool { return false }

// writeCount returns the number of WriteParams batches received so far.
func (f *schedFakeClient) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// flatWrites returns every ParamWrite in order across all WriteParams calls.
func (f *schedFakeClient) flatWrites() []breezy.ParamWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
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
func (schedFakeRaw) IsLocal() bool                                              { return false }
func (schedFakeRaw) Close() error                                               { return nil }

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

// helper: build a time at a given local HH:MM (date doesn't matter).
func atHM(h, m int) time.Time {
	return time.Date(2026, 5, 6, h, m, 0, 0, time.Local)
}

func TestScheduleTime_ParseAndString(t *testing.T) {
	is := is.New(t)
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
		is.NoErr(err)                // ParseScheduleTime accepts valid HH:MM
		is.Equal(got, c.want)        // parsed minutes match expected
		is.Equal(got.String(), c.in) // String() round-trips the input
	}
	bad := []string{"", "8:00", "08:0", "24:00", "08:60", "08-00", "abc", "08:00:00"}
	for _, in := range bad {
		_, err := ParseScheduleTime(in)
		is.True(err != nil) // malformed inputs must reject
	}
}

func TestScheduler_Validation(t *testing.T) {
	is := is.New(t)
	s := &Scheduler{}
	good := []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
		{At: 1320, Action: "off", Pct: 60},
	}
	is.NoErr(s.validate(good)) // good schedule accepted
	// validate accepts an empty entry slice — no rule depends on enabled.
	is.NoErr(s.validate(nil)) // empty entry slice accepted

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
		err := s.validate(b.entries)
		is.True(errors.Is(err, breezy.ErrInvalidArg)) // each bad case must surface ErrInvalidArg
	}

	// action=off allows any pct (including 0) since pct is ignored at the wire
	is.NoErr(s.validate([]ScheduleEntry{{At: 480, Action: "off", Pct: 0}})) // off action allows pct=0

	too := make([]ScheduleEntry, 25)
	for i := range too {
		too[i] = ScheduleEntry{At: ScheduleTime(i), Action: "regeneration", Pct: 60}
	}
	is.True(errors.Is(s.validate(too), breezy.ErrInvalidArg)) // > 24 entries rejected
}

func TestScheduleEntry_JSON(t *testing.T) {
	is := is.New(t)
	in := ScheduleEntry{At: 480, Action: "regeneration", Pct: 60}
	data, err := in.MarshalJSON()
	is.NoErr(err)
	want := `{"at":"08:00","action":"regeneration","pct":60}`
	is.Equal(string(data), want)
	var out ScheduleEntry
	is.NoErr(out.UnmarshalJSON([]byte(want)))
	is.Equal(out, in) // marshal/unmarshal round-trip
}

func TestScheduler_PersistRoundTrip(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	s := &Scheduler{Device: "playroom", StateDir: dir}

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
	is.NoErr(s.save())
	s.mu.Unlock()

	s2 := &Scheduler{Device: "playroom", StateDir: dir}
	s2.Load()
	snap := s2.Snapshot()
	is.True(snap.Enabled)          // enabled flag survives roundtrip
	is.Equal(len(snap.Entries), 2) // entries count survives roundtrip
	is.Equal(snap.Entries[0].At, ScheduleTime(480))
	is.Equal(snap.Entries[1].Action, "off")
	is.True(snap.LastApply != nil) // lastApply survives roundtrip
	is.Equal(snap.LastApply.Retries, 5)
	is.Equal(snap.LastApply.OK, false)
}

func TestScheduler_LoadMissingFileStartsEmpty(t *testing.T) {
	is := is.New(t)
	s := &Scheduler{Device: "x", StateDir: t.TempDir()}
	s.Load()
	snap := s.Snapshot()
	is.Equal(snap.Enabled, false)
	is.Equal(len(snap.Entries), 0)
	is.True(snap.LastApply == nil) // missing file means no lastApply
}

func TestScheduler_LoadMalformedFileStartsEmpty(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	s := &Scheduler{Device: "x", StateDir: dir}
	is.NoErr(os.WriteFile(s.statePath(), []byte("{not json"), 0o600))
	s.Load()
	snap := s.Snapshot()
	is.Equal(snap.Enabled, false)  // malformed file falls back to empty state
	is.Equal(len(snap.Entries), 0) // malformed file falls back to empty entries
}

func TestScheduler_SaveAtomic(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	s := &Scheduler{Device: "x", StateDir: dir}
	s.mu.Lock()
	s.enabled = false
	is.NoErr(s.save())
	s.mu.Unlock()

	entries, err := os.ReadDir(dir)
	is.NoErr(err)
	for _, e := range entries {
		is.True(filepath.Ext(e.Name()) != ".tmp") // temp files must not leak after save
	}
}

func TestScheduler_Tick_NoCatchupOnStartup(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(14, 0))
	is.Equal(len(fc.writes), 0) // first tick after startup must not catch up
	s.tick(context.Background(), atHM(14, 0))
	is.Equal(len(fc.writes), 0) // second tick at same minute must not fire
}

func TestScheduler_Tick_FiresOnAtTime_Regeneration(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	got := fc.flatWrites()
	is.True(len(got) >= 3) // expect at least Power, Mode, SpeedManual
	is.Equal(got[0].ID, breezy.ParamID(0x0001))
	is.Equal(got[0].Value[0], byte(1)) // first write is Power(true)
	is.Equal(got[1].ID, breezy.ParamID(0x00B7))
	is.Equal(got[1].Value[0], byte(1)) // second write is SetMode(regeneration=1)
	saw0x44 := false
	for _, w := range got[2:] {
		if w.ID == 0x0044 && w.Value[0] == 60 {
			saw0x44 = true
		}
	}
	is.True(saw0x44) // expected SpeedManual write of 60% via 0x44
	snap := s.Snapshot()
	is.True(snap.LastApply != nil) // lastApply must be recorded
	is.True(snap.LastApply.OK)
	is.Equal(snap.LastApply.At, ScheduleTime(480))
}

func TestScheduler_Tick_FiresOff(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 1320, Action: "off", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(21, 59))
	s.tick(context.Background(), atHM(22, 0))
	got := fc.flatWrites()
	is.Equal(len(got), 1) // off action issues exactly one write
	is.Equal(got[0].ID, breezy.ParamID(0x0001))
	is.Equal(got[0].Value[0], byte(0)) // off => Power(false)
}

func TestScheduler_Tick_DisabledIsInert(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = false
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	is.Equal(len(fc.writes), 0) // disabled scheduler must not fire
}

func TestScheduler_Tick_MultipleMatchFiresLatest(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
		{At: 540, Action: "ventilation", Pct: 70},
	}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(9, 1))
	got := fc.flatWrites()
	saw0x44_70 := false
	for _, w := range got {
		if w.ID == 0x0044 && w.Value[0] == 70 {
			saw0x44_70 = true
		}
	}
	is.True(saw0x44_70) // multi-match window must fire latest (09:00 → ventilation 70%)
	powerOnCount := 0
	for _, w := range got {
		if w.ID == 0x0001 && w.Value[0] == 1 {
			powerOnCount++
		}
	}
	is.Equal(powerOnCount, 1) // multi-match should fire one entry only
}

func TestScheduler_Tick_FiresAcrossMidnight(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 5, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(23, 59))
	is.Equal(len(fc.writes), 0) // priming tick must not fire
	s.tick(context.Background(), atHM(0, 6))
	got := fc.flatWrites()
	is.True(len(got) > 0) // 00:05 entry must fire across midnight
}

func TestScheduler_Fire_FailureRecordsLastApply(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	fc.err = errors.New("simulated UDP timeout")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	snap := s.Snapshot()
	is.True(snap.LastApply != nil)     // failure must record lastApply
	is.Equal(snap.LastApply.OK, false) // failed fire records ok=false
	is.True(snap.LastApply.Err != "")  // failed fire records an err message
}

func TestRetry_TimeoutInstallsRetry(t *testing.T) {
	is := is.New(t)
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
	is.True(r != nil)       // retry must be installed after transient failure
	is.Equal(r.attempts, 1) // attempts counter starts at 1
}

func TestRetry_AuthFailsNoRetry(t *testing.T) {
	is := is.New(t)
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
	is.True(r == nil)                                // auth failure must NOT install retry
	is.True(la != nil)                               // auth failure must still record lastApply
	is.Equal(la.OK, false)                           // auth failure records ok=false
	is.True(strings.Contains(la.Err, "auth_failed")) // err message includes auth_failed
}

func TestRetry_SucceedsClearsRetry(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	fc.err = nil
	// 8:01 is past the 30s nextAttempt (8:00:30); the retry fires and succeeds.
	s.tick(context.Background(), atHM(8, 1))
	s.mu.Lock()
	r := s.retry
	la := s.lastApply
	s.mu.Unlock()
	is.True(r == nil)  // retry must be cleared after success
	is.True(la != nil) // lastApply must exist after retry success
	is.True(la.OK)     // lastApply.ok must be true after retry success
}

func TestRetry_DeadlineAbandons(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	// March forward minute by minute; deadline is 8:10 (8:00 + 10m), so
	// the m=10 tick at 08:10:00 hits `now ≥ deadline` and abandons.
	for m := 1; m <= 11; m++ {
		s.tick(context.Background(), atHM(8, m))
	}
	s.mu.Lock()
	r := s.retry
	la := s.lastApply
	s.mu.Unlock()
	is.True(r == nil)      // retry must be abandoned past deadline
	is.True(la != nil)     // lastApply must still be present
	is.Equal(la.OK, false) // lastApply.ok stays false after deadline abandon
}

func TestRetry_SupersededByNextEntry(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
		{At: 540, Action: "ventilation", Pct: 70},
	}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	fc.err = nil
	s.tick(context.Background(), atHM(9, 0))
	s.mu.Lock()
	r := s.retry
	la := s.lastApply
	s.mu.Unlock()
	is.True(r == nil)                  // supersede clears retry
	is.True(la != nil)                 // lastApply present
	is.Equal(la.At, ScheduleTime(540)) // lastApply matches the superseding entry
	is.True(la.OK)                     // superseding entry succeeded
}

func TestRetry_DisableClearsRetry(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))
	s.mu.Lock()
	s.enabled = false
	s.mu.Unlock()
	s.tick(context.Background(), atHM(8, 1))
	s.mu.Lock()
	r := s.retry
	s.mu.Unlock()
	is.True(r == nil) // disable must clear in-flight retry
}

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

// TestRetry_RetriesCounterIncrements asserts that lastApply.Retries advances
// with each successive retry attempt: 0 after first failure, 1 after first
// retry, 2 after second retry, etc. The deadline-abandon test exercises the
// path but doesn't directly assert the counter.
func TestRetry_RetriesCounterIncrements(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0))    // attempt 1: first failure
	is.Equal(s.Snapshot().LastApply.Retries, 0) // Retries=0 after first failure
	s.tick(context.Background(), atHM(8, 1))    // attempt 2 (now ≥ nextAttempt=8:00:30)
	is.Equal(s.Snapshot().LastApply.Retries, 1) // Retries=1 after first retry
	s.tick(context.Background(), atHM(8, 2))    // attempt 3
	is.Equal(s.Snapshot().LastApply.Retries, 2) // Retries=2 after second retry
}

// TestScheduler_SetEnabled pins that SetEnabled flips only the enabled bit
// and persists without touching entries, firedAt, retry, or lastApply.
func TestScheduler_SetEnabled(t *testing.T) {
	is := is.New(t)
	s, _ := newSchedTest(t)

	// Seed some entries and state to verify they survive SetEnabled.
	entry := ScheduleEntry{At: 480, Action: "regeneration", Pct: 60}
	is.NoErr(s.Replace(true, []ScheduleEntry{entry}))

	// Manually set firedAt and lastApply to confirm they aren't touched.
	s.mu.Lock()
	s.firedAt = map[ScheduleTime]time.Time{480: atHM(8, 0)}
	s.lastApply = &LastApply{At: 480, Fired: atHM(8, 0), OK: true}
	s.mu.Unlock()

	// Disable.
	is.NoErr(s.SetEnabled(false))

	s.mu.Lock()
	isEnabled := s.enabled
	entriesLen := len(s.entries)
	firedAtLen := len(s.firedAt)
	lastApplyNil := s.lastApply == nil
	retryNil := s.retry == nil
	s.mu.Unlock()

	is.Equal(isEnabled, false)    // enabled must be false after SetEnabled(false)
	is.Equal(entriesLen, 1)       // entries must not change
	is.Equal(firedAtLen, 1)       // firedAt must not change
	is.Equal(lastApplyNil, false) // lastApply must not change
	is.Equal(retryNil, true)      // retry was nil and stays nil

	// Re-enable.
	is.NoErr(s.SetEnabled(true))

	s.mu.Lock()
	isEnabled = s.enabled
	s.mu.Unlock()

	is.Equal(isEnabled, true) // enabled must be true after SetEnabled(true)
}

// TestScheduler_ReplaceClearsInflightRetry asserts the spec contract that
// editing the schedule mid-retry drops the in-flight retry (and resets
// lastApply, since a fresh schedule starts fresh — no stale alert banner).
func TestScheduler_ReplaceClearsInflightRetry(t *testing.T) {
	is := is.New(t)
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0)) // installs retry
	s.mu.Lock()
	hadRetry := s.retry != nil
	s.mu.Unlock()
	is.True(hadRetry) // setup: retry should be installed before Replace

	is.NoErr(s.Replace(true, []ScheduleEntry{{At: 600, Action: "ventilation", Pct: 70}}))

	snap := s.Snapshot()
	s.mu.Lock()
	r := s.retry
	s.mu.Unlock()
	is.True(r == nil)              // Replace must clear in-flight retry
	is.True(snap.LastApply == nil) // Replace must clear lastApply (start fresh)
}

// atUTC builds a UTC moment for DST tests. Construct in UTC, then
// .In(ptLocation) at the call site — that gets unambiguous semantics
// for both the fall-back repeated hour and the spring-forward missing
// hour, where time.Date in a DST-affected location is ambiguous.
func atUTC(year, month, day, hour, minute int) time.Time {
	return time.Date(year, time.Month(month), day, hour, minute, 0, 0, time.UTC)
}

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
	firstCount := len(fc.flatWrites())
	is.True(firstCount >= 3) // Power(true) + SetMode + SetSpeedManual (2 writes)

	// Walk through 01:59 PDT.
	s.tick(context.Background(), atUTC(2026, 11, 1, 8, 59).In(pt))

	// Fall-back moment: 01:00 PST (UTC 09:00). No entry in window.
	s.tick(context.Background(), atUTC(2026, 11, 1, 9, 0).In(pt))

	// Second 01:30 (now PST). MUST NOT fire.
	s.tick(context.Background(), atUTC(2026, 11, 1, 9, 30).In(pt))
	is.Equal(len(fc.flatWrites()), firstCount) // count unchanged — no double-fire

	// And 02:00 PST for good measure.
	s.tick(context.Background(), atUTC(2026, 11, 1, 10, 0).In(pt))
	is.Equal(len(fc.flatWrites()), firstCount)
}

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
	firedCount := len(fc.flatWrites())
	is.True(firedCount >= 3) // Power(true) + SetMode + SetSpeedManual (2 writes)

	// 03:01 PDT: no fire.
	s.tick(context.Background(), atUTC(2026, 3, 8, 10, 1).In(pt))
	is.Equal(len(fc.flatWrites()), firedCount)
}

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
	is.True(len(fc.flatWrites()) >= 3)                             // Power(true) + SetMode + SetSpeedManual (2 writes)

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

// TestScheduler_IntegrationFiresWritesToFakedevice exercises the full
// Scheduler → recordingClient → ops → breezy.Client → fakedevice path.
// Unit tests cover the state machine with a fake DeviceClient; this test
// catches wiring regressions in dialRecording / LockUDP composition / the
// recordingClient callback that mocked tests can't see.
//
// Reads back the params after firing to confirm the device received the
// expected writes (Power, SetMode, SpeedManual flips speed_mode to 0xFF).
func TestScheduler_IntegrationFiresWritesToFakedevice(t *testing.T) {
	is := is.New(t)
	addr := newServerFakeDevice(t)

	var recorded [][]breezy.ParamWrite
	var recordedMu sync.Mutex

	s := &Scheduler{
		Device:   "playroom",
		StateDir: t.TempDir(),
		LockUDP:  func() func() { return func() {} },
		Dial: func(_ context.Context) (breezy.DeviceClient, HandlerClient, error) {
			raw, err := breezy.NewClient(addr, srvDeviceID, srvPassword)
			if err != nil {
				return nil, nil, err
			}
			rc := newRecordingClient(raw, func(ws []breezy.ParamWrite) {
				recordedMu.Lock()
				defer recordedMu.Unlock()
				recorded = append(recorded, append([]breezy.ParamWrite(nil), ws...))
			})
			return rc, raw, nil
		},
	}
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()

	s.tick(context.Background(), atHM(7, 59)) // prime
	s.tick(context.Background(), atHM(8, 0))  // fire

	// Read back the device state through the same library path.
	client, err := breezy.NewClient(addr, srvDeviceID, srvPassword)
	is.NoErr(err)
	defer client.Close() //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	vals, err := client.ReadParams(ctx, []breezy.ParamID{0x0001, 0x00B7, 0x0044, 0x0002})
	is.NoErr(err)
	is.Equal(len(vals[0x0001]), 1)
	is.Equal(vals[0x0001][0], byte(1)) // Power(true) landed
	is.Equal(len(vals[0x00B7]), 1)
	is.Equal(vals[0x00B7][0], byte(1)) // SetMode(regeneration=1) landed
	is.Equal(len(vals[0x0044]), 1)
	is.Equal(vals[0x0044][0], byte(60)) // SetSpeedManual(60) landed
	is.Equal(len(vals[0x0002]), 1)
	is.Equal(vals[0x0002][0], byte(0xFF)) // speed_mode flipped to 0xFF (manual)

	// Confirm the recordingClient's record callback fired for every write —
	// the production path uses this to drive cache writethrough + NoticeWrite.
	recordedMu.Lock()
	total := 0
	for _, batch := range recorded {
		total += len(batch)
	}
	recordedMu.Unlock()
	is.True(total >= 3) // recordingClient callback should fire ≥3 times (Power, SetMode, SpeedManual)

	// And the success-path lastApply.
	la := s.Snapshot().LastApply
	is.True(la != nil)                 // lastApply must be present
	is.True(la.OK)                     // lastApply must be OK
	is.Equal(la.At, ScheduleTime(480)) // lastApply matches fire time 08:00
}
