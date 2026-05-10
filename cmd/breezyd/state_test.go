// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

func TestState_RoundTrip(t *testing.T) {
	is := is.New(t)
	s := NewState()
	now := time.Now().UTC().Truncate(time.Second)
	in := Snapshot{
		IP: "192.168.1.148",
		Values: map[breezy.ParamID][]byte{
			0x01: {0x01},
			0x86: {0x06, 0x10, 0x01, 0x06},
		},
		LastPoll: now,
		LastErr:  nil,
	}
	s.Set("playroom", in)

	got, ok := s.Get("playroom")
	is.True(ok) // Get must return ok=true after Set
	is.Equal(got.IP, in.IP)
	is.True(got.LastPoll.Equal(in.LastPoll)) // LastPoll must round-trip
	is.Equal(got.LastErr, nil)
	is.Equal(got.Values, in.Values)
}

func TestState_RoundTrip_WithError(t *testing.T) {
	is := is.New(t)
	s := NewState()
	wantErr := errors.New("timeout")
	s.Set("a", Snapshot{IP: "10.0.0.1", LastErr: wantErr})

	got, ok := s.Get("a")
	is.True(ok)                 // Get must return ok=true
	is.True(got.LastErr != nil) // LastErr must round-trip
	is.Equal(got.LastErr.Error(), "timeout")
}

func TestState_Get_Missing(t *testing.T) {
	is := is.New(t)
	s := NewState()
	got, ok := s.Get("nonexistent")
	is.True(!ok) // Get must return ok=false for missing key
	is.Equal(got, Snapshot{})
}

func TestState_DeepCopy_OnGet(t *testing.T) {
	is := is.New(t)
	s := NewState()
	s.Set("a", Snapshot{Values: map[breezy.ParamID][]byte{0x01: {1, 2, 3}}})

	got, _ := s.Get("a")
	got.Values[0x01][0] = 99
	got.Values[0x02] = []byte{42}

	again, _ := s.Get("a")
	is.True(again.Values[0x01][0] != 99) // mutation of returned slice must not leak into storage
	_, ok := again.Values[0x02]
	is.True(!ok) // addition of new key must not leak into storage
}

func TestState_DeepCopy_OnSet(t *testing.T) {
	is := is.New(t)
	s := NewState()
	src := map[breezy.ParamID][]byte{0x01: {1, 2, 3}}
	s.Set("a", Snapshot{Values: src})

	// Mutate the original map after Set; must not affect storage.
	src[0x01][0] = 99
	src[0x02] = []byte{42}

	got, _ := s.Get("a")
	is.True(got.Values[0x01][0] != 99) // mutation of caller's slice must not leak into storage
	_, ok := got.Values[0x02]
	is.True(!ok) // addition to caller's map must not leak into storage
}

func TestState_UpdateIP(t *testing.T) {
	is := is.New(t)
	s := NewState()
	now := time.Now().UTC()
	s.Set("a", Snapshot{
		IP:       "1.1.1.1",
		Values:   map[breezy.ParamID][]byte{0x01: {1}},
		LastPoll: now,
	})

	s.UpdateIP("a", "2.2.2.2")

	got, ok := s.Get("a")
	is.True(ok) // Get must return ok=true
	is.Equal(got.IP, "2.2.2.2")
	is.True(got.LastPoll.Equal(now))                           // LastPoll must not be mutated by UpdateIP
	is.Equal(got.Values, map[breezy.ParamID][]byte{0x01: {1}}) // Values must not be disturbed by UpdateIP
}

func TestState_UpdateIP_NoExistingSnapshot(t *testing.T) {
	is := is.New(t)
	s := NewState()
	s.UpdateIP("a", "10.0.0.5")

	got, ok := s.Get("a")
	is.True(ok) // UpdateIP must create snapshot for missing key
	is.Equal(got.IP, "10.0.0.5")
	is.Equal(got.Values, map[breezy.ParamID][]byte(nil))
	is.True(got.LastPoll.IsZero()) // LastPoll must be zero on a fresh snapshot
	is.Equal(got.LastErr, nil)
}

func TestState_Devices_Sorted(t *testing.T) {
	is := is.New(t)
	s := NewState()
	s.Set("zulu", Snapshot{})
	s.Set("alpha", Snapshot{})
	s.Set("mike", Snapshot{})

	got := s.Devices()
	want := []string{"alpha", "mike", "zulu"}
	is.Equal(got, want)
}

func TestState_Devices_Empty(t *testing.T) {
	is := is.New(t)
	s := NewState()
	got := s.Devices()
	is.Equal(len(got), 0)
}

func TestState_Delete(t *testing.T) {
	is := is.New(t)
	s := NewState()
	s.Set("a", Snapshot{IP: "1.1.1.1"})
	s.Set("b", Snapshot{IP: "2.2.2.2"})

	s.Delete("a")

	_, ok := s.Get("a")
	is.True(!ok) // a must be gone after Delete
	_, ok = s.Get("b")
	is.True(ok) // b must still be present

	// Deleting a missing key must not panic.
	s.Delete("nonexistent")
}

func TestState_WriteThrough_FreshSnapshot(t *testing.T) {
	is := is.New(t)
	s := NewState()
	s.WriteThrough("a", []breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0x01}},
		{ID: 0x0044, Value: []byte{0x32}},
	})
	got, ok := s.Get("a")
	is.True(ok) // WriteThrough must create snapshot for missing key
	is.Equal(got.Values[0x0001], []byte{0x01})
	is.Equal(got.Values[0x0044], []byte{0x32})
}

func TestState_WriteThrough_PreservesPollMetadata(t *testing.T) {
	is := is.New(t)
	s := NewState()
	now := time.Now().UTC().Truncate(time.Second)
	wantErr := errors.New("transport")
	s.Set("a", Snapshot{
		IP: "1.1.1.1",
		Values: map[breezy.ParamID][]byte{
			0x0001: {0x00},       // power off
			0x004A: {0x10, 0x27}, // fan_supply_rpm 10000
		},
		LastPoll: now,
		LastErr:  wantErr,
	})
	s.WriteThrough("a", []breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0x01}}, // user turns power on
	})
	got, ok := s.Get("a")
	is.True(ok) // Get must return ok=true
	is.Equal(got.IP, "1.1.1.1")
	is.True(got.LastPoll.Equal(now)) // LastPoll must be preserved
	is.True(got.LastErr != nil)      // LastErr must be preserved
	is.Equal(got.LastErr.Error(), "transport")
	is.Equal(got.Values[0x0001], []byte{0x01})       // 0x0001 must be updated
	is.Equal(got.Values[0x004A], []byte{0x10, 0x27}) // unwritten params must be preserved
}

func TestState_WriteThrough_DeepCopiesValues(t *testing.T) {
	is := is.New(t)
	s := NewState()
	val := []byte{0x42}
	s.WriteThrough("a", []breezy.ParamWrite{{ID: 0x0001, Value: val}})
	val[0] = 0x99
	got, _ := s.Get("a")
	is.Equal(got.Values[0x0001][0], byte(0x42)) // WriteThrough must deep-copy
}

func TestState_WriteThrough_Empty(t *testing.T) {
	is := is.New(t)
	s := NewState()
	s.WriteThrough("a", nil)
	_, ok := s.Get("a")
	is.True(!ok) // empty WriteThrough must not create a snapshot for missing device
	s.Set("b", Snapshot{IP: "1.1.1.1"})
	s.WriteThrough("b", []breezy.ParamWrite{})
	got, _ := s.Get("b")
	is.Equal(got.IP, "1.1.1.1") // empty WriteThrough must not disturb existing snapshot
}

func TestParsePeriodicDiscovery(t *testing.T) {
	is := is.New(t)
	cases := []struct {
		in     string
		want   time.Duration
		wantOk bool
	}{
		{"periodic:5m", 5 * time.Minute, true},
		{"periodic:30s", 30 * time.Second, true},
		{"periodic:bogus", 0, false},
		{"on-start", 0, false},
		{"off", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := parsePeriodicDiscovery(tc.in)
		is.Equal(ok, tc.wantOk) // ok must match expected for input tc.in
		if ok {
			is.Equal(got, tc.want) // duration must match expected for input tc.in
		}
	}
}

// TestRunDiscoveryWith_UnknownIDLogged pins the spec'd INFO log when
// discovery surfaces a device whose ID is not in the registry. Without
// this signal, an operator who plugs in a new unit has no breadcrumb
// telling them to add a [devices.NAME] block — discovery just silently
// ignores them. Captures slog output via a custom handler.
func TestRunDiscoveryWith_UnknownIDLogged(t *testing.T) {
	is := is.New(t)

	// Swap the global slog default for a capturing handler scoped to this test.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	devices := NewDeviceRegistry(map[string]DeviceConfig{
		"playroom": {ID: "TESTID0000000001", Password: "1111", IP: "1.1.1.1:4000"},
	})
	// Mix one known and one unknown ID so we can assert the unknown branch
	// fires but the known branch still updates IP.
	stub := func(ctx context.Context) ([]breezy.Found, error) {
		return []breezy.Found{
			{DeviceID: "TESTID0000000001", IP: "10.0.0.5", UnitType: 17}, // known
			{DeviceID: "BREEZY9999000000", IP: "10.0.0.6", UnitType: 17}, // unknown
		}, nil
	}
	is.NoErr(runDiscoveryWith(context.Background(), devices, stub))

	logged := buf.String()
	is.True(strings.Contains(logged, "unconfigured device")) // INFO log substring
	is.True(strings.Contains(logged, "[devices.NAME]"))      // hint mentions config block
	is.True(strings.Contains(logged, "BREEZY9999000000"))    // ID of the unknown device
	is.True(strings.Contains(logged, "10.0.0.6"))            // IP of the unknown device
}

func TestRunDiscoveryWith_UpdatesIPViaRegistry(t *testing.T) {
	is := is.New(t)
	devices := NewDeviceRegistry(map[string]DeviceConfig{
		"playroom": {ID: "TESTID0000000001", Password: "1111", IP: "1.1.1.1:4000"},
	})
	stub := func(ctx context.Context) ([]breezy.Found, error) {
		return []breezy.Found{
			{DeviceID: "TESTID0000000001", IP: "10.0.0.5", UnitType: 17},
		}, nil
	}
	is.NoErr(runDiscoveryWith(context.Background(), devices, stub))
	d, _ := devices.Get("playroom")
	is.Equal(d.IP, "10.0.0.5:4000") // registry IP must be updated by discovery
}

// G-daemon-4: errors from the injected discover stub propagate out of
// runDiscoveryWith. Pinning the function-boundary contract — main.go::run
// is responsible for swallowing the error to keep the daemon ready, but
// the seam itself must surface it so callers can choose the policy.
// Catches a regression where someone "helpfully" logs-and-swallows
// inside runDiscoveryWith, breaking runPeriodicDiscovery's WARN-on-tick
// log line and any future caller that wants to fail-fast.
func TestRunDiscoveryWith_PropagatesDiscoverError(t *testing.T) {
	is := is.New(t)
	devices := NewDeviceRegistry(map[string]DeviceConfig{
		"playroom": {ID: "TESTID0000000001", Password: "1111", IP: "1.1.1.1:4000"},
	})
	wantErr := errors.New("listen failed")
	stub := func(ctx context.Context) ([]breezy.Found, error) {
		return nil, wantErr
	}
	err := runDiscoveryWith(context.Background(), devices, stub)
	is.True(err != nil)              // discover error must propagate
	is.True(errors.Is(err, wantErr)) // and must be the same error (not wrapped to opacity)
	d, _ := devices.Get("playroom")
	is.Equal(d.IP, "1.1.1.1:4000") // registry IP must be unchanged on discover error
}

// G-daemon-6: runDiscovery picks the password-bearing discover only when
// [daemon].password is non-empty AND non-default. Stubs both package-level
// closures and observes which fires for each input. Catches a regression
// where the gate flips (e.g. drops the empty check, drops the
// DefaultDiscoveryPassword check, or inverts the condition), causing
// either spurious DiscoverWithPassword calls with "1111" or — worse — a
// silent fallback to bare Discover when the operator has set a real
// password and the firmware variant requires it.
func TestRunDiscovery_SelectsClosureByPassword(t *testing.T) {
	cases := []struct {
		name          string
		password      string
		wantPlainCall bool
		wantPwdCall   bool
		wantPwdArg    string
	}{
		{"empty password uses Discover", "", true, false, ""},
		{"factory default uses Discover", breezy.DefaultDiscoveryPassword, true, false, ""},
		{"custom password uses DiscoverWithPassword", "s3cret", false, true, "s3cret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			devices := NewDeviceRegistry(map[string]DeviceConfig{
				"playroom": {ID: "TESTID0000000001", Password: "1111", IP: "1.1.1.1:4000"},
			})

			var plainCalls, pwdCalls int
			var gotPwdArg string
			origPlain := defaultDiscover
			origPwd := defaultDiscoverWithPassword
			t.Cleanup(func() {
				defaultDiscover = origPlain
				defaultDiscoverWithPassword = origPwd
			})
			defaultDiscover = func(ctx context.Context) ([]breezy.Found, error) {
				plainCalls++
				return nil, nil
			}
			defaultDiscoverWithPassword = func(ctx context.Context, pwd string) ([]breezy.Found, error) {
				pwdCalls++
				gotPwdArg = pwd
				return nil, nil
			}

			is.NoErr(runDiscovery(context.Background(), devices, tc.password))

			if tc.wantPlainCall {
				is.Equal(plainCalls, 1) // plain Discover must fire exactly once
				is.Equal(pwdCalls, 0)   // DiscoverWithPassword must not fire
			}
			if tc.wantPwdCall {
				is.Equal(plainCalls, 0)            // plain Discover must not fire
				is.Equal(pwdCalls, 1)              // DiscoverWithPassword must fire exactly once
				is.Equal(gotPwdArg, tc.wantPwdArg) // DiscoverWithPassword must receive the configured password
			}
		})
	}
}

// TestRunPeriodicDiscovery_TicksAndExits pins the cadence + cancellation
// contract of the periodic-discovery goroutine. Spec: tick at the
// configured cadence (calling defaultDiscover each tick), exit promptly
// on context cancellation. Without coverage, a regression that turns the
// goroutine into a single-shot or leaks past ctx.Done() would only
// surface in production.
//
// Uses interval=10ms + a ~50ms cancel deadline so the test stays fast
// (<200ms total). Asserts ≥3 calls (not exactly N) to absorb scheduler
// jitter on loaded CI.
func TestRunPeriodicDiscovery_TicksAndExits(t *testing.T) {
	is := is.New(t)

	// Swap defaultDiscover for a counting stub. runPeriodicDiscovery calls
	// runDiscovery -> runDiscoveryWith, which falls through to defaultDiscover
	// when the password is empty/default.
	var calls int64
	prev := defaultDiscover
	defaultDiscover = func(ctx context.Context) ([]breezy.Found, error) {
		atomic.AddInt64(&calls, 1)
		return nil, nil
	}
	t.Cleanup(func() { defaultDiscover = prev })

	devices := NewDeviceRegistry(map[string]DeviceConfig{
		"playroom": {ID: "TESTID0000000001", Password: "1111", IP: "1.1.1.1:4000"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPeriodicDiscovery(ctx, devices, 10*time.Millisecond, "")
	}()

	// Let the ticker fire several times.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// Assert the goroutine returned promptly after cancel.
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runPeriodicDiscovery did not return within 2s of ctx cancel")
	}

	got := atomic.LoadInt64(&calls)
	is.True(got >= 3) // expected ≥3 ticks across ~50ms at 10ms cadence; got fewer (timing-jitter floor)
}

// TestRunPeriodicDiscovery_ContinuesThroughErrors pins the second half
// of the contract: a single failing tick must not bail the loop. A
// transient discovery error (network blip, IPv4 broadcast filter) would
// otherwise permanently disable IP refresh until the daemon restarts.
//
// Stub returns an error every other call; we still expect the call count
// to grow past the first error.
func TestRunPeriodicDiscovery_ContinuesThroughErrors(t *testing.T) {
	is := is.New(t)

	var calls int64
	prev := defaultDiscover
	defaultDiscover = func(ctx context.Context) ([]breezy.Found, error) {
		n := atomic.AddInt64(&calls, 1)
		if n%2 == 0 {
			return nil, errors.New("simulated discovery failure")
		}
		return nil, nil
	}
	t.Cleanup(func() { defaultDiscover = prev })

	devices := NewDeviceRegistry(map[string]DeviceConfig{
		"playroom": {ID: "TESTID0000000001", Password: "1111", IP: "1.1.1.1:4000"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runPeriodicDiscovery(ctx, devices, 10*time.Millisecond, "")
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runPeriodicDiscovery did not return within 2s of ctx cancel")
	}

	got := atomic.LoadInt64(&calls)
	is.True(got >= 3) // loop must continue past the first errored tick (got fewer)
}

func TestDeviceRegistry_ConcurrentReadAndUpdate(t *testing.T) {
	r := NewDeviceRegistry(map[string]DeviceConfig{
		"a": {ID: "TESTID0000000001", Password: "1111", IP: "1.1.1.1:4000"},
	})
	var wg sync.WaitGroup
	const N = 200
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			r.UpdateIP("a", "2.2.2.2:4000")
			r.UpdateIP("a", "3.3.3.3:4000")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_, _ = r.Get("a")
			_ = r.Snapshot()
			_ = r.Names()
		}
	}()
	wg.Wait()
}

func TestState_Concurrent(t *testing.T) {
	s := NewState()
	var wg sync.WaitGroup
	const goroutines = 10
	const ops = 1000

	for i := 0; i < goroutines; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				s.Set("a", Snapshot{
					IP:     "1.1.1.1",
					Values: map[breezy.ParamID][]byte{0x01: {1, 2, 3}},
				})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				snap, ok := s.Get("a")
				if ok && snap.Values != nil {
					// touch the bytes — must not race with writers
					for _, v := range snap.Values {
						if len(v) > 0 {
							_ = v[0]
						}
					}
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				s.UpdateIP("a", "2.2.2.2")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				_ = s.Devices()
			}
		}()
	}
	wg.Wait()
}
