// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
	"github.com/matryer/is"
)

const (
	pollerTestDeviceID = "TESTID0000000001"
	pollerTestPassword = "1111"
)

func pollerSnapshotPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../pkg/breezy/fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

// newFakeServer brings up a fakedevice and ensures it gets closed by t.Cleanup.
func newFakeServer(t *testing.T) *fakedevice.Server {
	t.Helper()
	srv, err := fakedevice.NewServer(pollerSnapshotPath(t), pollerTestDeviceID, pollerTestPassword)
	if err != nil {
		t.Fatalf("fakedevice.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// waitForSnapshot polls state.Get until the named device has a non-zero
// LastPoll, returning the snapshot. Fails the test if the deadline elapses
// without a recorded poll.
func waitForSnapshot(t *testing.T, state *State, name string, deadline time.Duration) Snapshot {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	end := time.Now().Add(deadline)
	for {
		snap, ok := state.Get(name)
		if ok && !snap.LastPoll.IsZero() {
			return snap
		}
		if time.Now().After(end) {
			t.Fatalf("no snapshot recorded for %q within %v", name, deadline)
		}
		<-tick.C
	}
}

func TestPoller_HappyPath_SingleTick(t *testing.T) {
	is := is.New(t)
	srv := newFakeServer(t)
	state := NewState()

	p := &Poller{
		Name:     "test",
		IP:       srv.Addr(),
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 50 * time.Millisecond,
		State:    state,
		ReadIDs: []breezy.ParamID{
			0x0001, 0x00B9, 0x0044, 0x004A, 0x004B,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	snap := waitForSnapshot(t, state, "test", 1*time.Second)
	cancel()
	<-done

	is.NoErr(snap.LastErr)        // happy-path tick must record no error
	is.Equal(snap.IP, srv.Addr()) // snapshot records the dialled IP
	for _, id := range []breezy.ParamID{0x0001, 0x00B9, 0x0044} {
		_, ok := snap.Values[id]
		is.True(ok) // each requested non-fan id must land in snapshot
	}
}

func TestPoller_RecordsLatestSnapshot_MultipleTicks(t *testing.T) {
	is := is.New(t)
	srv := newFakeServer(t)
	state := NewState()

	p := &Poller{
		Name:     "dev",
		IP:       srv.Addr(),
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 20 * time.Millisecond,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	first := waitForSnapshot(t, state, "dev", 1*time.Second)

	// Wait for at least one further tick to fire.
	deadline := time.Now().Add(1 * time.Second)
	var latest Snapshot
	for time.Now().Before(deadline) {
		latest, _ = state.Get("dev")
		if latest.LastPoll.After(first.LastPoll) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	cancel()
	<-done

	is.True(latest.LastPoll.After(first.LastPoll)) // a later tick must have fired
	is.NoErr(latest.LastErr)                       // multi-tick run must stay error-free
	_, ok := latest.Values[0x0001]
	is.True(ok) // latest snapshot must contain 0x0001
}

func TestPoller_AuthError_ClassifiedCorrectly(t *testing.T) {
	is := is.New(t)
	srv := newFakeServer(t)
	state := NewState()

	var (
		mu    sync.Mutex
		kinds []string
	)
	p := &Poller{
		Name:     "bad",
		IP:       srv.Addr(),
		DeviceID: pollerTestDeviceID,
		Password: "9999", // wrong
		Interval: 50 * time.Millisecond,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		OnError: func(name, kind string) {
			mu.Lock()
			defer mu.Unlock()
			if name == "bad" {
				kinds = append(kinds, kind)
			}
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	// Wait for at least one auth error to be recorded.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(kinds)
		mu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	is.True(len(kinds) > 0)    // OnError must fire at least once for auth failure
	is.Equal(kinds[0], "auth") // first error must classify as "auth"

	// The cache should still record the failed poll, with LastErr set.
	snap, ok := state.Get("bad")
	is.True(ok)                                      // failed device still has a snapshot
	is.True(snap.LastErr != nil)                     // LastErr recorded for failed poll
	is.True(errors.Is(snap.LastErr, breezy.ErrAuth)) // LastErr unwraps to ErrAuth
}

func TestPoller_ContextCancellation_Stops(t *testing.T) {
	srv := newFakeServer(t)
	state := NewState()

	p := &Poller{
		Name:     "x",
		IP:       srv.Addr(),
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour, // we only want the initial tick to fire.
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	waitForSnapshot(t, state, "x", 1*time.Second)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancellation")
	}
}

// fakeClient is an in-process PollerClient that records the IDs it was asked
// to read on each batch. Used to verify NoticeWrite filtering without timing.
type fakeClient struct {
	mu      sync.Mutex
	batches [][]breezy.ParamID
	values  map[breezy.ParamID][]byte
	err     error
}

func (f *fakeClient) ReadParams(ctx context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]breezy.ParamID, len(ids))
	copy(cp, ids)
	f.batches = append(f.batches, cp)
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[breezy.ParamID][]byte, len(ids))
	for _, id := range ids {
		if v, ok := f.values[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func (f *fakeClient) Close() error  { return nil }
func (f *fakeClient) IsLocal() bool { return false }

func (f *fakeClient) seenIDs() []breezy.ParamID {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []breezy.ParamID
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

func TestPoller_NoticeWrite_SkipsFanReadsDuringSettleWindow(t *testing.T) {
	is := is.New(t)
	state := NewState()

	fc := &fakeClient{
		values: map[breezy.ParamID][]byte{
			0x0001: {1},
			0x0044: {50},
			0x004A: {0, 0},
			0x004B: {0, 0},
		},
	}

	// Controllable clock.
	var nowAtomic atomic.Int64
	nowAtomic.Store(time.Unix(1_700_000_000, 0).UnixNano())
	now := func() time.Time { return time.Unix(0, nowAtomic.Load()) }

	p := &Poller{
		Name:     "settle",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001, 0x0044, 0x004A, 0x004B},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		Now: now,
	}

	ctx := context.Background()

	// Tick before any write — should read everything.
	p.tick(ctx)
	got := fc.seenIDs()
	want := map[breezy.ParamID]bool{0x0001: true, 0x0044: true, 0x004A: true, 0x004B: true}
	for id := range want {
		found := false
		for _, g := range got {
			if g == id {
				found = true
				break
			}
		}
		is.True(found) // pre-write tick must include every id (no settle suppression)
	}

	// Reset batches and notice a fan-affecting write.
	fc.mu.Lock()
	fc.batches = nil
	fc.mu.Unlock()
	p.NoticeWrite(0x0044) // manual fan %

	// Tick during settle window: 0x004A and 0x004B must be skipped.
	p.tick(ctx)
	got = fc.seenIDs()
	for _, id := range got {
		is.True(id != 0x004A && id != 0x004B) // fan RPM ids must be skipped during settle window
	}
	// And 0x0001 / 0x0044 should still be polled.
	hasPower, hasManual := false, false
	for _, id := range got {
		if id == 0x0001 {
			hasPower = true
		}
		if id == 0x0044 {
			hasManual = true
		}
	}
	is.True(hasPower)  // settle-tick still reads 0x0001
	is.True(hasManual) // settle-tick still reads 0x0044

	// Advance the clock past the settle window.
	nowAtomic.Add(int64(fanSettleDuration + time.Second))

	fc.mu.Lock()
	fc.batches = nil
	fc.mu.Unlock()

	p.tick(ctx)
	got = fc.seenIDs()
	hasA, hasB := false, false
	for _, id := range got {
		if id == 0x004A {
			hasA = true
		}
		if id == 0x004B {
			hasB = true
		}
	}
	is.True(hasA) // post-settle tick reads 0x004A again
	is.True(hasB) // post-settle tick reads 0x004B again
}

func TestPoller_NoticeWrite_NonFanWriteDoesNotSetSettle(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{0x004A: {0, 0}}}

	p := &Poller{
		Name:     "x",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x004A},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
	}

	// 0x0068 (heater) is not a fan-speed write.
	p.NoticeWrite(0x0068)
	p.tick(context.Background())
	got := fc.seenIDs()
	found := false
	for _, id := range got {
		if id == 0x004A {
			found = true
		}
	}
	is.True(found) // non-fan write must not set settle deadline (0x004A still polled)
}

func TestPoller_NoticeWrite_TimerWriteSetsSettle(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{0x004A: {0, 0}, 0x0001: {1}}}

	p := &Poller{
		Name:     "x",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001, 0x004A},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
	}

	// 0x0007 (timer) DOES change fan speed — entering turbo ramps up,
	// entering night ramps down. Suppress fan-sensitive reads for the
	// settle window after such a write.
	p.NoticeWrite(0x0007)
	p.tick(context.Background())
	got := fc.seenIDs()
	for _, id := range got {
		is.True(id != 0x004A) // 0x004A must be skipped during settle window after timer write
	}
	hasPower := false
	for _, id := range got {
		if id == 0x0001 {
			hasPower = true
		}
	}
	is.True(hasPower) // settle-tick must still read non-fan params
}

func TestPoller_FanSettle_SkippedForLocalClient(t *testing.T) {
	is := is.New(t)
	// A MemClient is in-process; writes land instantly, so the firmware settle
	// delay does not apply. NoticeWrite should not set a settle deadline when
	// the last dialed client reports IsLocal() == true.
	state := NewState()

	mc, err := breezy.NewMemClientFromFile(pollerSnapshotPath(t))
	is.NoErr(err)

	p := &Poller{
		Name:     "local",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001, 0x0044, 0x004A, 0x004B},
		NewClient: func() (PollerClient, error) {
			return mc, nil
		},
	}

	ctx := context.Background()

	// Run one tick so dial() records the MemClient in p.lastClient.
	p.tick(ctx)

	// Now fire a fan-affecting write (0x0002 = speed_mode).
	p.NoticeWrite(0x0002)

	// idsForThisTick must include fan-sensitive reads — no suppression.
	ids := p.idsForThisTick()
	idSet := make(map[breezy.ParamID]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	is.True(idSet[0x004A]) // local-client writes must not suppress 0x004A
	is.True(idSet[0x004B]) // local-client writes must not suppress 0x004B
}

func TestPoller_BatchesLargeReadList(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{}}

	// 75 distinct IDs => with batch size 30 we expect 3 batches (30, 30, 15).
	ids := make([]breezy.ParamID, 75)
	for i := range ids {
		ids[i] = breezy.ParamID(0x1000 + i)
	}

	p := &Poller{
		Name:     "big",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  ids,
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
	}
	p.tick(context.Background())

	fc.mu.Lock()
	defer fc.mu.Unlock()
	is.Equal(len(fc.batches), 3) // 75 ids / batch size 30 => 3 batches
	for i, b := range fc.batches {
		if i < 2 {
			is.Equal(len(b), pollBatchSize) // first two batches saturate to pollBatchSize
		}
	}
	is.Equal(len(fc.batches[2]), 15) // last batch holds the remainder
}

func TestPoller_ErrorClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"checksum", breezy.ErrChecksum, "checksum"},
		{"auth", breezy.ErrAuth, "auth"},
		{"deadline", context.DeadlineExceeded, "timeout"},
		{"net-timeout", &fakeNetErr{timeout: true}, "timeout"},
		{"other", errors.New("boom"), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			var got string
			p := &Poller{
				Name: "n",
				OnError: func(_, kind string) {
					got = kind
				},
			}
			p.recordErr(tc.err)
			is.Equal(got, tc.want) // error kind classification
		})
	}
}

// fakeNetErr satisfies net.Error for the timeout-classification test.
type fakeNetErr struct{ timeout bool }

func (e *fakeNetErr) Error() string   { return "fake net error" }
func (e *fakeNetErr) Timeout() bool   { return e.timeout }
func (e *fakeNetErr) Temporary() bool { return false }

func TestPoller_OnPollFiresOnSuccess(t *testing.T) {
	is := is.New(t)
	srv := newFakeServer(t)
	state := NewState()

	var mu sync.Mutex
	var calls int

	p := &Poller{
		Name:     "onpoll",
		IP:       srv.Addr(),
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 50 * time.Millisecond,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		OnPoll: func(name string, snap Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			calls++
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()
	<-done

	mu.Lock()
	got := calls
	mu.Unlock()
	is.True(got >= 2) // OnPoll should fire at least twice across 200ms × 50ms interval
}

func TestPoller_ReadError_RecordedInSnapshot(t *testing.T) {
	is := is.New(t)
	state := NewState()
	wantErr := errors.New("read failed")
	fc := &fakeClient{err: wantErr}

	var kinds []string
	var mu sync.Mutex
	p := &Poller{
		Name:     "err",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		OnError: func(_, kind string) {
			mu.Lock()
			kinds = append(kinds, kind)
			mu.Unlock()
		},
	}
	p.tick(context.Background())

	snap, ok := state.Get("err")
	is.True(ok)                  // snapshot recorded even on error
	is.True(snap.LastErr != nil) // LastErr is set on failed tick
	mu.Lock()
	defer mu.Unlock()
	is.Equal(len(kinds), 1)     // exactly one OnError fire
	is.Equal(kinds[0], "other") // generic error classifies as "other"
}

func TestPoller_FailedPollPreservesPriorValues(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{0x0001: {1}}}

	// Inject a clock so we can prove LastPoll DOES NOT advance on failed ticks.
	clock := time.Unix(1_700_000_000, 0)
	advance := func(d time.Duration) { clock = clock.Add(d) }

	p := &Poller{
		Name:     "dev",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		Now: func() time.Time { return clock },
	}

	p.tick(context.Background())
	first, ok := state.Get("dev")
	is.True(ok)             // first tick records a snapshot
	is.NoErr(first.LastErr) // first tick is the success that primes Values
	is.Equal(first.Values[0x0001], []byte{1})

	// Flip to failure and tick again — Values AND LastPoll must persist.
	fc.mu.Lock()
	fc.err = errors.New("read failed")
	fc.mu.Unlock()
	advance(5 * time.Minute) // wall clock advances, but LastPoll must not

	p.tick(context.Background())
	second, ok := state.Get("dev")
	is.True(ok)
	is.True(second.LastErr != nil)                 // failed tick marks LastErr
	is.Equal(second.Values[0x0001], []byte{1})     // prior success value preserved
	is.True(second.LastPoll.Equal(first.LastPoll)) // LastPoll preserved across failure

	// Third still-failing tick must still preserve Values and LastPoll.
	advance(5 * time.Minute)
	p.tick(context.Background())
	third, ok := state.Get("dev")
	is.True(ok)
	is.True(third.LastErr != nil)
	is.Equal(third.Values[0x0001], []byte{1})     // continued preservation
	is.True(third.LastPoll.Equal(first.LastPoll)) // continued preservation
}

// TestPoller_FailedDial_PreservesPriorSnapshot pins the dial-failure
// branch of tick(): once a successful poll has primed Values+LastPoll,
// a subsequent failure to construct the client must NOT overwrite them
// with empty/now. Without this, the dashboard would briefly drop to
// "unreachable" on a transient dial error.
func TestPoller_FailedDial_PreservesPriorSnapshot(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{0x0001: {1}}}

	clock := time.Unix(1_700_000_000, 0)
	advance := func(d time.Duration) { clock = clock.Add(d) }

	dialErr := errors.New("dial refused")
	dialFails := false

	p := &Poller{
		Name:     "dev",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		NewClient: func() (PollerClient, error) {
			if dialFails {
				return nil, dialErr
			}
			return fc, nil
		},
		Now: func() time.Time { return clock },
	}

	// Successful tick primes Values+LastPoll.
	p.tick(context.Background())
	first, ok := state.Get("dev")
	is.True(ok)
	is.NoErr(first.LastErr)
	is.Equal(first.Values[0x0001], []byte{1})

	// Force dial failures and tick again.
	dialFails = true
	advance(5 * time.Minute)
	p.tick(context.Background())

	second, ok := state.Get("dev")
	is.True(ok)
	is.True(errors.Is(second.LastErr, dialErr))    // dial error recorded
	is.Equal(second.Values[0x0001], []byte{1})     // prior values preserved
	is.True(second.LastPoll.Equal(first.LastPoll)) // LastPoll preserved
	is.Equal(second.IP, first.IP)                  // IP preserved
}

// TestPoller_LastPollResumesAfterFailureClears pins that once a transient
// failure clears, the success path resumes advancing LastPoll. This
// guards against an over-correction that would freeze LastPoll forever.
func TestPoller_LastPollResumesAfterFailureClears(t *testing.T) {
	is := is.New(t)
	state := NewState()
	fc := &fakeClient{values: map[breezy.ParamID][]byte{0x0001: {1}}}

	clock := time.Unix(1_700_000_000, 0)
	advance := func(d time.Duration) { clock = clock.Add(d) }

	p := &Poller{
		Name:     "dev",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		Now: func() time.Time { return clock },
	}

	// Tick 1: success.
	p.tick(context.Background())
	first, _ := state.Get("dev")
	is.NoErr(first.LastErr)

	// Tick 2: failure — LastPoll must hold.
	fc.mu.Lock()
	fc.err = errors.New("transient")
	fc.mu.Unlock()
	advance(time.Minute)
	p.tick(context.Background())
	failed, _ := state.Get("dev")
	is.True(failed.LastErr != nil)
	is.True(failed.LastPoll.Equal(first.LastPoll))

	// Tick 3: failure clears, LastPoll must advance.
	fc.mu.Lock()
	fc.err = nil
	fc.mu.Unlock()
	advance(time.Minute)
	p.tick(context.Background())
	resumed, _ := state.Get("dev")
	is.NoErr(resumed.LastErr)
	is.True(resumed.LastPoll.After(first.LastPoll)) // success advances clock
}

func TestPoller_LockUDP_SerialisesWithConcurrentCallers(t *testing.T) {
	p := &Poller{}
	first := p.LockUDP()

	// A second LockUDP call from a different goroutine must block until the
	// first releases. We assert by scheduling the second acquire and checking
	// it doesn't complete while the first is still holding.
	gotSecond := make(chan struct{})
	go func() {
		unlock := p.LockUDP()
		close(gotSecond)
		unlock()
	}()

	select {
	case <-gotSecond:
		t.Fatal("second LockUDP returned before first unlocked")
	case <-time.After(50 * time.Millisecond):
		// expected: blocked
	}

	first()

	select {
	case <-gotSecond:
		// expected: unblocked once first released
	case <-time.After(time.Second):
		t.Fatal("second LockUDP never returned after first unlocked")
	}
}

// TestPoller_FanSettle_DropsSensitiveReads_OverUDP exercises the settle window
// end-to-end through real fakedevice UDP. A *breezy.Client dials the server,
// we issue a fan-affecting NoticeWrite, and we verify that the next tick drops
// 0x4A/0x4B from its read-IDs — then re-admits them once virtual time passes
// the window. No actual sleeping: we inject Now to control the clock.
func TestPoller_FanSettle_DropsSensitiveReads_OverUDP(t *testing.T) {
	is := is.New(t)
	srv := newFakeServer(t)
	state := NewState()

	// Controllable clock: start well away from zero so deadline comparisons
	// against time.Time{} (IsZero) behave correctly.
	base := time.Unix(1_700_000_000, 0)
	var offset atomic.Int64 // nanoseconds added to base
	virtualNow := func() time.Time { return base.Add(time.Duration(offset.Load())) }

	// NewClient injects a real *breezy.Client over UDP — this is what makes the
	// test exercise the production path rather than an in-process stub.
	p := &Poller{
		Name:     "udp-settle",
		IP:       srv.Addr(),
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour, // manual ticks only
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001, 0x0044, 0x004A, 0x004B},
		NewClient: func() (PollerClient, error) {
			return breezy.NewClient(srv.Addr(), pollerTestDeviceID, pollerTestPassword)
		},
		Now: virtualNow,
	}

	ctx := context.Background()

	// 1. Tick before any write: all IDs should be read (and p.lastClient gets set).
	p.tick(ctx)
	snap, ok := state.Get("udp-settle")
	is.True(ok)            // pre-write tick must record a snapshot
	is.NoErr(snap.LastErr) // pre-write tick must succeed

	// 2. Note a fan-affecting write (0x0002 = speed_mode).
	p.NoticeWrite(0x0002)

	// 3. Confirm settle window is active: idsForThisTick must exclude fan RPMs.
	ids := p.idsForThisTick()
	idSet := make(map[breezy.ParamID]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	is.True(!idSet[0x004A]) // 0x004A dropped during settle window
	is.True(!idSet[0x004B]) // 0x004B dropped during settle window
	// Non-sensitive params must still be present.
	is.True(idSet[0x0001]) // 0x0001 still present during settle window
	is.True(idSet[0x0044]) // 0x0044 still present during settle window

	// 4. Advance virtual time past the window; fan RPMs must reappear.
	offset.Store(int64(fanSettleDuration + time.Second))
	ids = p.idsForThisTick()
	idSet = make(map[breezy.ParamID]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	is.True(idSet[0x004A]) // 0x004A re-admitted after settle expires
	is.True(idSet[0x004B]) // 0x004B re-admitted after settle expires

	// 5. Do a real tick over UDP post-settle to confirm the server responds.
	p.tick(ctx)
	snap, ok = state.Get("udp-settle")
	is.True(ok)            // post-settle tick records snapshot
	is.NoErr(snap.LastErr) // post-settle tick succeeds
}

// TestPoller_PostTickHooksGatedOnSuccess pins that both Energy.Tick AND
// OnPoll fire only when the tick recorded no error. A failed tick
// (every read returned err) must not trigger Energy accumulation
// (would corrupt the daily/lifetime counters with stale Values) and
// must not push a half-empty snapshot through OnPoll (PushHub would
// patch dashboards with a stale card).
func TestPoller_PostTickHooksGatedOnSuccess(t *testing.T) {
	is := is.New(t)
	state := NewState()
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()

	fc := &fakeClient{err: errors.New("read failed")}

	var mu sync.Mutex
	var onPollCalls int

	p := &Poller{
		Name:     "p",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001, 0x00B9},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		Energy: tr,
		OnPoll: func(name string, snap Snapshot) {
			mu.Lock()
			defer mu.Unlock()
			onPollCalls++
		},
	}

	p.tick(context.Background())

	// Snapshot must record the failure.
	snap, ok := state.Get("p")
	is.True(ok)
	is.True(snap.LastErr != nil)

	// Energy.Tick must not have been called (LastTick still zero).
	tr.mu.Lock()
	is.True(tr.LastTick.IsZero()) // Energy accumulation must not run on a failed tick
	tr.mu.Unlock()

	// OnPoll must not have fired.
	mu.Lock()
	defer mu.Unlock()
	is.Equal(onPollCalls, 0) // OnPoll must not run on a failed tick
}

func TestPoller_EnergyTickCalled(t *testing.T) {
	is := is.New(t)
	// Wire a real EnergyTracker (with t.TempDir for state) onto a poller
	// running against a fakeClient; assert Tick advances LastTick (proxy
	// for "Tick was actually called after a successful poll").
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "p", StateDir: dir}
	tr.Load()

	state := NewState()
	fc := &fakeClient{
		values: map[breezy.ParamID][]byte{
			0x0001: {1},    // power_state (on)
			0x00B9: {0, 0}, // unit_type
		},
	}

	p := &Poller{
		Name:     "p",
		IP:       "127.0.0.1:0",
		DeviceID: pollerTestDeviceID,
		Password: pollerTestPassword,
		Interval: 1 * time.Hour,
		State:    state,
		ReadIDs:  []breezy.ParamID{0x0001, 0x00B9},
		NewClient: func() (PollerClient, error) {
			return fc, nil
		},
		Energy: tr,
	}

	p.tick(context.Background())

	snap := tr.Snapshot()
	_ = snap // not asserting energy math here — just that Tick was called

	tr.mu.Lock() // reading internal state for white-box test; mu held
	lastTick := tr.LastTick
	tr.mu.Unlock()

	is.True(!lastTick.IsZero()) // EnergyTracker.Tick must have run after a successful poll
}
