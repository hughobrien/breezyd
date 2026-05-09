// SPDX-License-Identifier: GPL-3.0-or-later

package breezy_test

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
	"github.com/matryer/is"
)

// discoverySnapshotPath returns the absolute path to the shared snapshot
// fixture used by discovery tests. The snapshot has 0x007C (device ID
// echo) and 0x00B9 (unit type) populated, which is exactly what
// discovery probes for.
func discoverySnapshotPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("snapshot abs: %v", err)
	}
	return p
}

func newDiscoveryServer(t *testing.T, deviceID, password string) *fakedevice.Server {
	t.Helper()
	srv, err := fakedevice.NewServer(discoverySnapshotPath(t), deviceID, password)
	if err != nil {
		t.Fatalf("fakedevice.NewServer(%q): %v", deviceID, err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// TestDiscoverAt_TwoFakes is the core acceptance test: two fakes with
// different real device IDs both respond to a wildcard probe, and
// DiscoverAt returns one Found per device with the *real* ID surfaced.
// Importantly, the snapshot's 0x007C field for both fakes is the same
// canned value from the captured snapshot — so to exercise the "extract
// real ID from response" path we override 0x007C on each server with the
// ID we configured it under. (Real hardware always reports its own ID at
// 0x007C; the snapshot just happens to have a hard-coded one from the
// device the capture was taken from.)
func TestDiscoverAt_TwoFakes(t *testing.T) {
	is := is.New(t)
	const idA = "0025AAAAAAAAAAAA"
	const idB = "0025BBBBBBBBBBBB"

	a := newDiscoveryServer(t, idA, "1111")
	b := newDiscoveryServer(t, idB, "2222") // different password — should not matter

	// The shared snapshot has a fixed 0x007C; overwrite each fake's so it
	// echoes the ID it was configured with. This mirrors real hardware,
	// where 0x007C reports the device's actual ID.
	is.NoErr(writeServerParam(t, a, idA, "1111", 0x007C, []byte(idA))) // override 007C on A
	is.NoErr(writeServerParam(t, b, idB, "2222", 0x007C, []byte(idB))) // override 007C on B

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	found, err := breezy.DiscoverAt(ctx, []string{a.Addr(), b.Addr()})
	is.NoErr(err)
	is.Equal(len(found), 2) // expected 2 found

	gotIDs := make([]string, 0, 2)
	for _, f := range found {
		gotIDs = append(gotIDs, f.DeviceID)
		is.Equal(f.IP, "127.0.0.1")      // expected IP 127.0.0.1
		is.Equal(f.UnitType, uint16(17)) // snapshot value: 0x1100 LE = 17
	}
	sort.Strings(gotIDs)
	is.Equal(gotIDs[0], idA)
	is.Equal(gotIDs[1], idB)
}

// TestDiscoverAt_AnyPasswordAccepted verifies the discovery wildcard is
// truly unauthenticated — the request's "1111" password should produce a
// response even when the server is configured with a different password.
// (This is also implicitly tested in TestDiscoverAt_TwoFakes via fake B,
// but a focused test makes the intent explicit.)
func TestDiscoverAt_AnyPasswordAccepted(t *testing.T) {
	is := is.New(t)
	const id = "0025DDDDDDDDDDDD"
	const pw = "secretpw"
	srv := newDiscoveryServer(t, id, pw)
	is.NoErr(writeServerParam(t, srv, id, pw, 0x007C, []byte(id)))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	found, err := breezy.DiscoverAt(ctx, []string{srv.Addr()})
	is.NoErr(err)
	is.Equal(len(found), 1)
	is.Equal(found[0].DeviceID, id)
}

// TestDiscoverAt_NoResponders confirms that pointing the probe at a
// blackhole address (nothing listening) returns a clean (nil err, empty
// slice) result after the listen deadline elapses, not an error.
func TestDiscoverAt_NoResponders(t *testing.T) {
	is := is.New(t)
	// Use a context with a short deadline to keep the test fast — the
	// caller's deadline shortens the listen loop.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	found, err := breezy.DiscoverAt(ctx, []string{"127.0.0.1:1"})
	// 200ms ctx deadline means ctx.Err() is likely DeadlineExceeded by
	// the time we return; with no responders, len(out) == 0 so the
	// implementation surfaces ctx.Err() to the caller.
	is.True(err == nil || errors.Is(err, context.DeadlineExceeded)) // expected nil or DeadlineExceeded
	is.Equal(len(found), 0)                                         // expected empty slice
}

// TestDiscoverAt_ContextCancel verifies that canceling ctx mid-listen
// unblocks the Read promptly instead of waiting out the 2s deadline.
func TestDiscoverAt_ContextCancel(t *testing.T) {
	is := is.New(t)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	// "127.0.0.1:1" is a blackhole — nothing replies, so the listen
	// loop sits in ReadFrom until either the deadline (2s) or ctx
	// cancel kicks in. We assert it's the cancel.
	_, err := breezy.DiscoverAt(ctx, []string{"127.0.0.1:1"})
	elapsed := time.Since(start)

	is.True(errors.Is(err, context.Canceled)) // expected context.Canceled
	is.True(elapsed <= 1*time.Second)         // cancel aborted listen promptly
}

// TestDiscoverAt_Concurrent calls DiscoverAt from multiple goroutines
// against the same fake; each call must allocate its own UDP socket and
// not interfere with the others. We check that every call gets exactly
// the one expected device back.
func TestDiscoverAt_Concurrent(t *testing.T) {
	is := is.New(t)
	const id = "0025CCCCCCCCCCCC"
	srv := newDiscoveryServer(t, id, "1111")
	is.NoErr(writeServerParam(t, srv, id, "1111", 0x007C, []byte(id)))

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)

	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			found, err := breezy.DiscoverAt(ctx, []string{srv.Addr()})
			if err != nil {
				errCh <- err
				return
			}
			if len(found) != 1 || found[0].DeviceID != id {
				errCh <- errors.New("unexpected discovery result")
				return
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Errorf("goroutine: %v", e)
	}
}

// writeServerParam updates a single param value on a running fake by
// issuing a write-with-reply request through a transient client. We do
// this rather than reach into the fake's internal map because the map is
// private to the fakedevice package; a protocol-level write is the
// supported surface for mutating fake state from external tests. The
// caller supplies the server's deviceID and password so we can address
// the fake correctly (NewClient pins both at construction time).
//
// Uses WriteParamsUnsafe so test fixtures can stuff arbitrary values
// (including for params the registry marks read-only on real hardware,
// e.g. 0x007C device_id_search) without tripping the safety gate.
func writeServerParam(t *testing.T, srv *fakedevice.Server, deviceID, password string, id breezy.ParamID, val []byte) error {
	t.Helper()
	c, err := breezy.NewClient(srv.Addr(), deviceID, password,
		breezy.WithTimeout(500*time.Millisecond), breezy.WithRetries(1))
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	return c.WriteParamsUnsafe(ctx, []breezy.ParamWrite{{ID: id, Value: val}})
}

func TestUnitTypeName(t *testing.T) {
	is := is.New(t)
	cases := map[uint16]string{
		17: "Breezy 160",
		20: "Breezy Eco 160",
		22: "Breezy 200",
		24: "Breezy Eco 200",
		0:  "unknown(0)",
		99: "unknown(99)",
	}
	for code, want := range cases {
		is.Equal(breezy.UnitTypeName(code), want)
	}
}
