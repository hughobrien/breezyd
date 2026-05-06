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
)

// schedFakeClient implements breezy.DeviceClient for tests.
type schedFakeClient struct {
	writes [][]breezy.ParamWrite
	err    error
}

func (f *schedFakeClient) ReadParams(_ context.Context, _ []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
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
	if err := s.validate(good); err != nil {
		t.Errorf("good schedule rejected: %v", err)
	}
	// validate accepts an empty entry slice — no rule depends on enabled.
	if err := s.validate(nil); err != nil {
		t.Errorf("empty schedule rejected: %v", err)
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
		err := s.validate(b.entries)
		if err == nil {
			t.Errorf("%s: want error, got nil", b.name)
			continue
		}
		if !errors.Is(err, breezy.ErrInvalidArg) {
			t.Errorf("%s: want ErrInvalidArg, got %v", b.name, err)
		}
	}

	// action=off allows any pct (including 0) since pct is ignored at the wire
	if err := s.validate([]ScheduleEntry{{At: 480, Action: "off", Pct: 0}}); err != nil {
		t.Errorf("off action with pct=0 rejected: %v", err)
	}

	too := make([]ScheduleEntry, 25)
	for i := range too {
		too[i] = ScheduleEntry{At: ScheduleTime(i), Action: "regeneration", Pct: 60}
	}
	if err := s.validate(too); !errors.Is(err, breezy.ErrInvalidArg) {
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

func TestScheduler_PersistRoundTrip(t *testing.T) {
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
	if err := s.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	s.mu.Unlock()

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

func TestScheduler_Tick_NoCatchupOnStartup(t *testing.T) {
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(14, 0))
	if len(fc.writes) != 0 {
		t.Errorf("first tick fired unexpectedly: %+v", fc.writes)
	}
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
	s.tick(context.Background(), atHM(7, 59))
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
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(9, 1))
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
	s.entries = []ScheduleEntry{{At: 5, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
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
	s.tick(context.Background(), atHM(8, 0))
	fc.err = nil
	// 8:01 is past the 30s nextAttempt (8:00:30); the retry fires and succeeds.
	s.tick(context.Background(), atHM(8, 1))
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
	s.tick(context.Background(), atHM(8, 0))
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
	s, fc := newSchedTest(t)
	fc.err = errors.New("transient")
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 60}}
	s.mu.Unlock()
	s.tick(context.Background(), atHM(7, 59))
	s.tick(context.Background(), atHM(8, 0)) // attempt 1: first failure
	if r := s.Snapshot().LastApply.Retries; r != 0 {
		t.Errorf("after first failure: Retries=%d, want 0", r)
	}
	s.tick(context.Background(), atHM(8, 1)) // attempt 2 (now ≥ nextAttempt=8:00:30)
	if r := s.Snapshot().LastApply.Retries; r != 1 {
		t.Errorf("after first retry: Retries=%d, want 1", r)
	}
	s.tick(context.Background(), atHM(8, 2)) // attempt 3
	if r := s.Snapshot().LastApply.Retries; r != 2 {
		t.Errorf("after second retry: Retries=%d, want 2", r)
	}
}

// TestScheduler_ReplaceClearsInflightRetry asserts the spec contract that
// editing the schedule mid-retry drops the in-flight retry (and resets
// lastApply, since a fresh schedule starts fresh — no stale alert banner).
func TestScheduler_ReplaceClearsInflightRetry(t *testing.T) {
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
	if !hadRetry {
		t.Fatal("setup: retry should be installed before Replace")
	}

	if err := s.Replace(true, []ScheduleEntry{{At: 600, Action: "ventilation", Pct: 70}}); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	snap := s.Snapshot()
	s.mu.Lock()
	r := s.retry
	s.mu.Unlock()
	if r != nil {
		t.Errorf("Replace should clear in-flight retry: %+v", r)
	}
	if snap.LastApply != nil {
		t.Errorf("Replace should clear lastApply (start fresh): %+v", snap.LastApply)
	}
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
	if err != nil {
		t.Fatalf("readback dial: %v", err)
	}
	defer client.Close() //nolint:errcheck
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	vals, err := client.ReadParams(ctx, []breezy.ParamID{0x0001, 0x00B7, 0x0044, 0x0002})
	if err != nil {
		t.Fatalf("readback ReadParams: %v", err)
	}
	if v := vals[0x0001]; len(v) != 1 || v[0] != 1 {
		t.Errorf("Power(true) didn't land: 0x0001 = %v", v)
	}
	if v := vals[0x00B7]; len(v) != 1 || v[0] != 1 { // 1 = regeneration
		t.Errorf("SetMode(regeneration) didn't land: 0x00B7 = %v", v)
	}
	if v := vals[0x0044]; len(v) != 1 || v[0] != 60 {
		t.Errorf("SetSpeedManual(60) didn't land: 0x0044 = %v", v)
	}
	if v := vals[0x0002]; len(v) != 1 || v[0] != 0xFF {
		t.Errorf("speed_mode didn't flip to 0xFF (manual): 0x0002 = %v", v)
	}

	// Confirm the recordingClient's record callback fired for every write —
	// the production path uses this to drive cache writethrough + NoticeWrite.
	recordedMu.Lock()
	total := 0
	for _, batch := range recorded {
		total += len(batch)
	}
	recordedMu.Unlock()
	if total < 3 {
		t.Errorf("recordingClient callback fired %d times; want ≥3 (Power, SetMode, SpeedManual)", total)
	}

	// And the success-path lastApply.
	if la := s.Snapshot().LastApply; la == nil || !la.OK || la.At != 480 {
		t.Errorf("lastApply not recorded as OK at 08:00: %+v", la)
	}
}
