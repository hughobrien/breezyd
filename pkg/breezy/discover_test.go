package breezy_test

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/twinfresh/pkg/breezy"
	"github.com/hughobrien/twinfresh/pkg/breezy/fakedevice"
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
	const idA = "0025AAAAAAAAAAAA"
	const idB = "0025BBBBBBBBBBBB"

	a := newDiscoveryServer(t, idA, "1111")
	b := newDiscoveryServer(t, idB, "2222") // different password — should not matter

	// The shared snapshot has a fixed 0x007C; overwrite each fake's so it
	// echoes the ID it was configured with. This mirrors real hardware,
	// where 0x007C reports the device's actual ID.
	if err := writeServerParam(t, a, idA, "1111", 0x007C, []byte(idA)); err != nil {
		t.Fatalf("override 007C on A: %v", err)
	}
	if err := writeServerParam(t, b, idB, "2222", 0x007C, []byte(idB)); err != nil {
		t.Fatalf("override 007C on B: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	found, err := breezy.DiscoverAt(ctx, []string{a.Addr(), b.Addr()})
	if err != nil {
		t.Fatalf("DiscoverAt: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("expected 2 found, got %d: %+v", len(found), found)
	}

	gotIDs := make([]string, 0, 2)
	for _, f := range found {
		gotIDs = append(gotIDs, f.DeviceID)
		if f.IP != "127.0.0.1" {
			t.Errorf("expected IP 127.0.0.1, got %q for %+v", f.IP, f)
		}
		if f.UnitType != 17 { // snapshot value: 0x1100 LE = 17
			t.Errorf("expected UnitType 17, got %d for %+v", f.UnitType, f)
		}
	}
	sort.Strings(gotIDs)
	if gotIDs[0] != idA || gotIDs[1] != idB {
		t.Fatalf("wrong device IDs: got %v want [%q %q]", gotIDs, idA, idB)
	}
}

// TestDiscoverAt_AnyPasswordAccepted verifies the discovery wildcard is
// truly unauthenticated — the request's "1111" password should produce a
// response even when the server is configured with a different password.
// (This is also implicitly tested in TestDiscoverAt_TwoFakes via fake B,
// but a focused test makes the intent explicit.)
func TestDiscoverAt_AnyPasswordAccepted(t *testing.T) {
	const id = "0025DDDDDDDDDDDD"
	const pw = "secretpw"
	srv := newDiscoveryServer(t, id, pw)
	if err := writeServerParam(t, srv, id, pw, 0x007C, []byte(id)); err != nil {
		t.Fatalf("override 007C: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	found, err := breezy.DiscoverAt(ctx, []string{srv.Addr()})
	if err != nil {
		t.Fatalf("DiscoverAt: %v", err)
	}
	if len(found) != 1 || found[0].DeviceID != id {
		t.Fatalf("want one device with ID %q, got %+v", id, found)
	}
}

// TestDiscoverAt_NoResponders confirms that pointing the probe at a
// blackhole address (nothing listening) returns a clean (nil err, empty
// slice) result after the listen deadline elapses, not an error.
func TestDiscoverAt_NoResponders(t *testing.T) {
	// Use a context with a short deadline to keep the test fast — the
	// caller's deadline shortens the listen loop.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	found, err := breezy.DiscoverAt(ctx, []string{"127.0.0.1:1"})
	// 200ms ctx deadline means ctx.Err() is likely DeadlineExceeded by
	// the time we return; with no responders, len(out) == 0 so the
	// implementation surfaces ctx.Err() to the caller.
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected nil or DeadlineExceeded, got %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("expected empty slice, got %+v", found)
	}
}

// TestDiscoverAt_ContextCancel verifies that canceling ctx mid-listen
// unblocks the Read promptly instead of waiting out the 2s deadline.
func TestDiscoverAt_ContextCancel(t *testing.T) {
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

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if elapsed > 1*time.Second {
		t.Fatalf("cancel didn't abort listen promptly: elapsed %v", elapsed)
	}
}

// TestDiscoverAt_Concurrent calls DiscoverAt from multiple goroutines
// against the same fake; each call must allocate its own UDP socket and
// not interfere with the others. We check that every call gets exactly
// the one expected device back.
func TestDiscoverAt_Concurrent(t *testing.T) {
	const id = "0025CCCCCCCCCCCC"
	srv := newDiscoveryServer(t, id, "1111")
	if err := writeServerParam(t, srv, id, "1111", 0x007C, []byte(id)); err != nil {
		t.Fatalf("override 007C: %v", err)
	}

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
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	return c.WriteParamsUnsafe(ctx, []breezy.ParamWrite{{ID: id, Value: val}})
}
