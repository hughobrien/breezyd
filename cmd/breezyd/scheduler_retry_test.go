// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// driveClock mimics Scheduler.loop against an injected clock: it jumps from
// from to end, at each step to the sooner of the next minute boundary and
// the pending retry's wake time, running tick on boundaries and retryTick on
// wakes. Every fire attempt's timestamp is returned (observed through
// lastApply.Fired, which each attempt rewrites).
func driveClock(s *Scheduler, from, end time.Time) []time.Time {
	var attempts []time.Time
	var last time.Time
	s.mu.Lock()
	if s.lastApply != nil {
		last = s.lastApply.Fired
	}
	s.mu.Unlock()
	record := func() {
		s.mu.Lock()
		la := s.lastApply
		s.mu.Unlock()
		if la != nil && !la.Fired.Equal(last) {
			last = la.Fired
			attempts = append(attempts, la.Fired)
		}
	}
	now := from
	for {
		next := now.Truncate(time.Minute).Add(time.Minute)
		minute := true
		if d, ok := s.retryWake(now); ok && now.Add(d).Before(next) {
			next = now.Add(d)
			minute = false
		}
		if next.After(end) {
			return attempts
		}
		now = next
		if minute {
			s.tick(context.Background(), now)
		} else {
			s.retryTick(context.Background(), now)
		}
		record()
	}
}

func hms(h, m, sec int) time.Time {
	return time.Date(2026, 5, 6, h, m, sec, 0, time.Local)
}

func newRetrySched(t *testing.T, entries ...ScheduleEntry) (*Scheduler, *schedFakeClient) {
	t.Helper()
	s, fc := newSchedTest(t)
	s.mu.Lock()
	s.enabled = true
	s.entries = entries
	s.mu.Unlock()
	s.tick(context.Background(), hms(7, 59, 0)) // prime lastTick
	return s, fc
}

// A transient failure is retried every 30 s (not once a minute) and
// abandoned at the 10-minute deadline: 20 attempts, 08:00:00 .. 08:09:30.
func TestRetryCadence_Every30sUntilDeadline(t *testing.T) {
	is := is.New(t)
	s, fc := newRetrySched(t, ScheduleEntry{At: 480, Action: "regeneration", Pct: 60})
	fc.err = errors.New("transient")
	got := driveClock(s, hms(7, 59, 0), hms(8, 15, 0))
	var want []time.Time
	for i := 0; i < 20; i++ {
		want = append(want, hms(8, 0, 0).Add(time.Duration(i)*30*time.Second))
	}
	is.Equal(got, want) // attempt times
	s.mu.Lock()
	is.True(s.retry == nil)  // abandoned after the deadline
	is.True(!s.lastApply.OK) // alert stays up
	is.Equal(s.lastApply.Retries, 19)
	s.mu.Unlock()
}

// A retry that succeeds mid-minute clears the retry and stops further attempts.
func TestRetryCadence_SucceedsAtHalfMinute(t *testing.T) {
	is := is.New(t)
	s, fc := newRetrySched(t, ScheduleEntry{At: 480, Action: "regeneration", Pct: 60})
	fc.err = errors.New("transient")
	driveClock(s, hms(7, 59, 0), hms(8, 0, 10))
	fc.err = nil
	got := driveClock(s, hms(8, 0, 10), hms(8, 5, 0))
	is.Equal(got, []time.Time{hms(8, 0, 30)}) // one successful retry, then quiet
	s.mu.Lock()
	is.True(s.retry == nil)
	is.True(s.lastApply.OK)
	s.mu.Unlock()
}

// A newer entry still supersedes a pending retry, and the old entry is not
// retried after that.
func TestRetryCadence_NewerEntrySupersedes(t *testing.T) {
	is := is.New(t)
	s, fc := newRetrySched(t,
		ScheduleEntry{At: 480, Action: "regeneration", Pct: 60},
		ScheduleEntry{At: 482, Action: "ventilation", Pct: 70},
	)
	fc.err = errors.New("transient")
	got := driveClock(s, hms(7, 59, 0), hms(8, 1, 59))
	is.Equal(got, []time.Time{hms(8, 0, 0), hms(8, 0, 30), hms(8, 1, 0), hms(8, 1, 30)})
	fc.err = nil
	got = driveClock(s, hms(8, 1, 59), hms(8, 5, 0))
	is.Equal(got, []time.Time{hms(8, 2, 0)}) // newer entry fires once; old retry gone
	s.mu.Lock()
	is.True(s.retry == nil)
	is.Equal(s.lastApply.At, ScheduleTime(482))
	is.True(s.lastApply.OK)
	s.mu.Unlock()
}

// ErrAuth is a config error: no retry, so nothing is scheduled to wake.
func TestRetryCadence_AuthNeverRetries(t *testing.T) {
	is := is.New(t)
	s, fc := newRetrySched(t, ScheduleEntry{At: 480, Action: "regeneration", Pct: 60})
	fc.err = breezy.ErrAuth
	got := driveClock(s, hms(7, 59, 0), hms(8, 15, 0))
	is.Equal(got, []time.Time{hms(8, 0, 0)})
	_, pending := s.retryWake(hms(8, 0, 0))
	is.True(!pending)
}

// A mid-minute retry wake must not disturb window detection: lastTick and
// lastTickTime stay put, and the next minute tick fires an entry that
// becomes due exactly once.
func TestRetryTick_LeavesWindowStateAlone(t *testing.T) {
	is := is.New(t)
	s, fc := newRetrySched(t,
		ScheduleEntry{At: 480, Action: "regeneration", Pct: 60},
		ScheduleEntry{At: 481, Action: "ventilation", Pct: 70},
	)
	fc.err = errors.New("transient")
	s.tick(context.Background(), hms(8, 0, 0))
	s.mu.Lock()
	lt, ltt := s.lastTick, s.lastTickTime
	s.mu.Unlock()
	s.retryTick(context.Background(), hms(8, 0, 30)) // fails again
	s.mu.Lock()
	is.Equal(s.lastTick, lt)
	is.True(s.lastTickTime.Equal(ltt))
	is.Equal(s.retry.attempts, 2)
	s.mu.Unlock()
	fc.err = nil
	s.tick(context.Background(), hms(8, 1, 0))
	s.mu.Lock()
	is.Equal(s.lastApply.At, ScheduleTime(481)) // newer entry fired once at its minute
	is.True(s.lastApply.OK)
	is.True(s.retry == nil)
	s.mu.Unlock()
	n := fc.writeCount()
	s.tick(context.Background(), hms(8, 2, 0))
	is.Equal(fc.writeCount(), n) // no double fire
}

// Run's loop itself wakes for retries between minute ticks. The minute
// channel never fires here, so every attempt after the first is a
// retry-timer wake.
func TestSchedulerLoop_WakesForRetries(t *testing.T) {
	old := retryCadence
	retryCadence = 20 * time.Millisecond
	t.Cleanup(func() { retryCadence = old })

	s, _ := newSchedTest(t)
	var mu sync.Mutex
	dials := 0
	s.Dial = func(context.Context) (breezy.DeviceClient, HandlerClient, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		return nil, nil, errors.New("transient")
	}
	entry := ScheduleEntry{At: 480, Action: "regeneration", Pct: 60}
	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{entry}
	s.mu.Unlock()
	s.fire(context.Background(), entry, 0, time.Now()) // first failed attempt installs retry

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.loop(ctx, make(chan time.Time)); close(done) }()
	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := dials
		mu.Unlock()
		if n >= 4 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("only %d attempts; loop is not waking for retries", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}
