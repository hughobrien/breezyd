// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// atLocal returns a fixed-date time at HH:MM in time.Local.
func atLocal(h, m int) time.Time {
	return time.Date(2026, 6, 1, h, m, 0, 0, time.Local)
}

// TestRTCSync_UntilNext pins the duration arithmetic for the daily
// scheduling loop.
func TestRTCSync_UntilNext(t *testing.T) {
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
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			is := is.New(t)
			got := untilNext(c.now, c.hour)
			is.Equal(got, c.want) // untilNext should produce the expected duration
		})
	}
}

// TestRTCSyncer_InitialSyncFires runs Run with a tiny initialDelay and
// confirms the first sync writes the RTC params (0x6F + 0x70) via the
// captured fake client.
func TestRTCSyncer_InitialSyncFires(t *testing.T) {
	is := is.New(t)

	orig := rtcInitialDelay
	rtcInitialDelay = 10 * time.Millisecond
	t.Cleanup(func() { rtcInitialDelay = orig })

	fc := &schedFakeClient{}
	syncer := &RTCSyncer{
		Device: "playroom",
		Dial: func(_ context.Context) (breezy.DeviceClient, HandlerClient, error) {
			return fc, schedFakeRaw{}, nil
		},
		LockUDP: func() func() { return func() {} },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { syncer.Run(ctx); close(done) }()

	// Wait until the fake client has captured at least one write batch.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fc.writeCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	is.True(fc.writeCount() >= 1) // initial sync should have written at least once

	// Confirm we wrote params 0x6F and 0x70.
	flat := fc.flatWrites()
	gotIDs := map[breezy.ParamID]bool{}
	for _, w := range flat {
		gotIDs[w.ID] = true
	}
	is.True(gotIDs[0x006F]) // wrote rtc_time
	is.True(gotIDs[0x0070]) // wrote rtc_calendar

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after ctx cancel")
	}
}

// TestRTCSyncer_FailureDoesNotStopGoroutine confirms that a dial error
// does not crash Run; the goroutine continues consuming ctx and exits
// cleanly on cancel.
func TestRTCSyncer_FailureDoesNotStopGoroutine(t *testing.T) {
	is := is.New(t)

	orig := rtcInitialDelay
	rtcInitialDelay = 10 * time.Millisecond
	t.Cleanup(func() { rtcInitialDelay = orig })

	failErr := errors.New("simulated dial failure")
	var dialCalls atomic.Int32
	syncer := &RTCSyncer{
		Device: "playroom",
		Dial: func(_ context.Context) (breezy.DeviceClient, HandlerClient, error) {
			dialCalls.Add(1)
			return nil, nil, failErr
		},
		LockUDP: func() func() { return func() {} },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { syncer.Run(ctx); close(done) }()

	// Wait until Dial has been called once (the initial sync).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && dialCalls.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	is.True(dialCalls.Load() >= 1) // initial sync attempt should have happened

	// Goroutine should still be alive — verified by clean cancel-exit.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after ctx cancel — goroutine likely deadlocked")
	}
}

// TestRTCSyncer_RespectsContextCancel verifies Run exits promptly when
// ctx is cancelled during the initial delay (i.e. before any sync has
// fired). Catches a regression where the select arms are mis-ordered.
func TestRTCSyncer_RespectsContextCancel(t *testing.T) {
	is := is.New(t)

	// Use the default 30s delay; we'll cancel quickly to verify the
	// select arm fires immediately.
	orig := rtcInitialDelay
	rtcInitialDelay = 30 * time.Second
	t.Cleanup(func() { rtcInitialDelay = orig })

	fc := &schedFakeClient{}
	syncer := &RTCSyncer{
		Device: "playroom",
		Dial: func(_ context.Context) (breezy.DeviceClient, HandlerClient, error) {
			return fc, schedFakeRaw{}, nil
		},
		LockUDP: func() func() { return func() {} },
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { syncer.Run(ctx); close(done) }()

	// Cancel immediately. Run must exit without waiting 30s and without
	// firing a sync.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit promptly on ctx cancel during initial delay")
	}
	is.Equal(fc.writeCount(), 0) // no sync should have fired
}
