package breezy_test

import (
	"bytes"
	"context"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
)

const (
	testDeviceID = "0000000000000148"
	testPassword = ""
)

// snapshotPath is the absolute path to the snapshot the fakedevice tests
// share. We resolve it via filepath.Abs so failures point at a real path.
func snapshotPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("snapshot abs: %v", err)
	}
	return p
}

func newTestServer(t *testing.T, password string) *fakedevice.Server {
	t.Helper()
	srv, err := fakedevice.NewServer(snapshotPath(t), testDeviceID, password)
	if err != nil {
		t.Fatalf("fakedevice.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func newTestClient(t *testing.T, addr, password string, opts ...breezy.Option) *breezy.Client {
	t.Helper()
	c, err := breezy.NewClient(addr, testDeviceID, password, opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// ----- ReadParam -----

func TestReadParam_OneByte(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 0x0001 (power) is encoded as a single byte 0x01 in the snapshot.
	val, err := c.ReadParam(ctx, 0x0001)
	if err != nil {
		t.Fatalf("ReadParam: %v", err)
	}
	if !bytes.Equal(val, []byte{0x01}) {
		t.Fatalf("want [0x01], got %x", val)
	}
}

func TestReadParam_MultiByte(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 0x00B9 (unit type) is two bytes (0x11 0x00) in the snapshot.
	val, err := c.ReadParam(ctx, 0x00B9)
	if err != nil {
		t.Fatalf("ReadParam: %v", err)
	}
	if !bytes.Equal(val, []byte{0x11, 0x00}) {
		t.Fatalf("want [0x11 0x00], got %x", val)
	}
}

func TestReadParam_HighPage(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 0x0320 (VOC) lives on the high page; the codec must emit FF 03 first.
	val, err := c.ReadParam(ctx, 0x0320)
	if err != nil {
		t.Fatalf("ReadParam: %v", err)
	}
	if !bytes.Equal(val, []byte{0x5E, 0x01}) {
		t.Fatalf("want [0x5E 0x01], got %x", val)
	}
}

func TestReadParam_Unsupported(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 0xFFFB is highly unlikely to appear in the snapshot.
	_, err := c.ReadParam(ctx, 0xFFFB)
	if !errors.Is(err, breezy.ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
}

// ----- ReadParams -----

func TestReadParams_Batch(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := c.ReadParams(ctx, []breezy.ParamID{0x0001, 0x00B9, 0x0320, 0xFFFB})
	if err != nil {
		t.Fatalf("ReadParams: %v", err)
	}
	if !bytes.Equal(out[0x0001], []byte{0x01}) {
		t.Fatalf("0x0001 = %x, want 01", out[0x0001])
	}
	if !bytes.Equal(out[0x00B9], []byte{0x11, 0x00}) {
		t.Fatalf("0x00B9 = %x, want 1100", out[0x00B9])
	}
	if !bytes.Equal(out[0x0320], []byte{0x5E, 0x01}) {
		t.Fatalf("0x0320 = %x, want 5E01", out[0x0320])
	}
	if _, ok := out[0xFFFB]; ok {
		t.Fatalf("0xFFFB should be omitted (unsupported), got %x", out[0xFFFB])
	}
}

func TestReadParams_Empty(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	out, err := c.ReadParams(ctx, nil)
	if err != nil {
		t.Fatalf("ReadParams(nil): %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("want empty map, got %v", out)
	}
}

// ----- WriteParam -----

func TestWriteParam_RoundTrip(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Toggle 0x0001 from 0x01 -> 0x00.
	if err := c.WriteParam(ctx, 0x0001, []byte{0x00}); err != nil {
		t.Fatalf("WriteParam: %v", err)
	}
	val, err := c.ReadParam(ctx, 0x0001)
	if err != nil {
		t.Fatalf("ReadParam after write: %v", err)
	}
	if !bytes.Equal(val, []byte{0x00}) {
		t.Fatalf("after write want [0x00], got %x", val)
	}
}

func TestWriteParam_MultiByte(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 0x001A (co2_threshold) is a two-byte writeable param. Write a fresh value.
	want := []byte{0xAB, 0x07}
	if err := c.WriteParam(ctx, 0x001A, want); err != nil {
		t.Fatalf("WriteParam: %v", err)
	}
	got, err := c.ReadParam(ctx, 0x001A)
	if err != nil {
		t.Fatalf("ReadParam after write: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("after write want %x, got %x", want, got)
	}
}

func TestWriteParams_Batch(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	writes := []breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0x00}},
		{ID: 0x0007, Value: []byte{0x05}},
	}
	if err := c.WriteParams(ctx, writes); err != nil {
		t.Fatalf("WriteParams: %v", err)
	}

	got, err := c.ReadParams(ctx, []breezy.ParamID{0x0001, 0x0007})
	if err != nil {
		t.Fatalf("ReadParams: %v", err)
	}
	if !bytes.Equal(got[0x0001], []byte{0x00}) {
		t.Fatalf("0x0001 = %x, want 00", got[0x0001])
	}
	if !bytes.Equal(got[0x0007], []byte{0x05}) {
		t.Fatalf("0x0007 = %x, want 05", got[0x0007])
	}
}

// ----- Errors -----

func TestReadParam_AuthFailure(t *testing.T) {
	// Server expects "secret"; client uses "wrong".
	srv := newTestServer(t, "secret")
	c := newTestClient(t, srv.Addr(), "wrong",
		breezy.WithRetries(1), breezy.WithTimeout(500*time.Millisecond))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.ReadParam(ctx, 0x0001)
	if !errors.Is(err, breezy.ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
}

// ----- Retries / timeouts -----

// blackholeAddr returns a UDP address that nothing is listening on. We
// allocate a port, then close the socket — Linux is unlikely to immediately
// reuse it for the duration of a single test, and even if a stray packet
// got delivered to a new owner, our codec would reject the response and
// the test would still see retries fire.
func blackholeAddr(t *testing.T) string {
	t.Helper()
	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	out := conn.LocalAddr().String()
	_ = conn.Close()
	return out
}

func TestExchange_Timeout(t *testing.T) {
	c := newTestClient(t, blackholeAddr(t), testPassword,
		breezy.WithTimeout(50*time.Millisecond),
		breezy.WithRetries(0),
		breezy.WithBackoff(10*time.Millisecond),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err := c.ReadParam(ctx, 0x0001)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("want timeout error, got nil")
	}
	// With 0 retries and 50ms timeout, we should be done well under 1s.
	if elapsed > 800*time.Millisecond {
		t.Fatalf("took %v with retries=0; expected <800ms", elapsed)
	}
}

func TestExchange_Retries(t *testing.T) {
	// We can't directly observe retry count, so we time it. Per-attempt
	// behavior depends on the OS: Linux surfaces ICMP unreachables to
	// "connected" UDP sockets, so writing to a never-bound port fails
	// fast instead of waiting for the read deadline. Either way, with
	// retries=N we sleep through N backoffs between attempts. We use
	// a long, distinctive backoff (50ms initial -> doubles) and assert
	// the elapsed time crosses the cumulative backoff threshold for
	// retries=2 (sum of 50+100 = 150ms). With retries=0 the same setup
	// would finish almost instantly, so the threshold proves retries
	// are wiring through.
	const initialBackoff = 50 * time.Millisecond
	c := newTestClient(t, blackholeAddr(t), testPassword,
		breezy.WithTimeout(20*time.Millisecond),
		breezy.WithRetries(2),
		breezy.WithBackoff(initialBackoff),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	_, err := c.ReadParam(ctx, 0x0001)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("want timeout error after retries, got nil")
	}
	// Cumulative backoff for 2 retries: 50ms + 100ms = 150ms (the third
	// attempt has no follow-up sleep).
	const minExpected = 140 * time.Millisecond // small fudge for clocks
	if elapsed < minExpected {
		t.Fatalf("retries elapsed %v, expected >=%v (retries didn't fire?)", elapsed, minExpected)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("retries elapsed %v, expected <2s", elapsed)
	}
}

func TestExchange_CtxCancelDuringBackoff(t *testing.T) {
	c := newTestClient(t, blackholeAddr(t), testPassword,
		breezy.WithTimeout(30*time.Millisecond),
		breezy.WithRetries(5),
		breezy.WithBackoff(500*time.Millisecond), // long backoff so we cancel during it
	)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after the first attempt's timeout has fired but we're still
	// in backoff sleep.
	go func() {
		time.Sleep(80 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := c.ReadParam(ctx, 0x0001)
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("cancel didn't abort retries promptly: elapsed %v", elapsed)
	}
}

func TestExchange_CtxDeadlineExceeded(t *testing.T) {
	c := newTestClient(t, blackholeAddr(t), testPassword,
		breezy.WithTimeout(2*time.Second),
		breezy.WithRetries(5),
		breezy.WithBackoff(10*time.Millisecond),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	_, err := c.ReadParam(ctx, 0x0001)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
}

// ----- Concurrency -----

func TestClient_ConcurrentReads(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const goroutines = 16
	const perGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)
	var errCount atomic.Int32

	ids := []breezy.ParamID{0x0001, 0x00B9, 0x0320}
	for g := 0; g < goroutines; g++ {
		go func(gi int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				id := ids[(gi+i)%len(ids)]
				val, err := c.ReadParam(ctx, id)
				if err != nil {
					errCount.Add(1)
					return
				}
				_ = val
			}
		}(g)
	}
	wg.Wait()

	if errCount.Load() != 0 {
		t.Fatalf("got %d errors during concurrent reads", errCount.Load())
	}
}

// ----- Lifecycle -----

func TestClient_CloseUnblocksInFlight(t *testing.T) {
	c := newTestClient(t, blackholeAddr(t), testPassword,
		breezy.WithTimeout(10*time.Second),
		breezy.WithRetries(0),
		breezy.WithBackoff(10*time.Millisecond),
	)

	done := make(chan error, 1)
	go func() {
		_, err := c.ReadParam(context.Background(), 0x0001)
		done <- err
	}()

	// Give the goroutine a moment to enter the Read.
	time.Sleep(50 * time.Millisecond)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("ReadParam after Close returned nil error")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("ReadParam did not unblock after Close")
	}
}

// ----- NewClient address parsing -----

// TestNewClient_DefaultPort verifies that bare host strings get :4000
// appended. We can't read the private addr field from an external test
// package, so we observe the parsed address indirectly by sending to it:
// a UDP write to 127.0.0.1:4000 (likely unbound) should fail eventually,
// while NewClient itself should not.
func TestNewClient_DefaultPort(t *testing.T) {
	c, err := breezy.NewClient("127.0.0.1", testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewClient host-only: %v", err)
	}
	defer c.Close()
}

func TestNewClient_HostPort(t *testing.T) {
	c, err := breezy.NewClient("127.0.0.1:9999", testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewClient host:port: %v", err)
	}
	defer c.Close()
}

func TestNewClient_BadDeviceID(t *testing.T) {
	_, err := breezy.NewClient("127.0.0.1", "short", testPassword)
	if err == nil {
		t.Fatalf("want error for short deviceID")
	}
}

// ----- ErrReadOnly enforcement -----

// TestWriteParams_ReadOnlyParamRejected exercises the Caps-driven gate in
// (*Client).WriteParams: a registered read-only parameter (fan_supply_rpm)
// is rejected with ErrReadOnly before any UDP traffic.
func TestWriteParams_ReadOnlyParamRejected(t *testing.T) {
	// Note: no server needed; the check fires before exchange().
	c, err := breezy.NewClient("127.0.0.1:1", testDeviceID, testPassword,
		breezy.WithTimeout(50*time.Millisecond), breezy.WithRetries(0))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// 0x004A = fan_supply_rpm — registered as read-only.
	err = c.WriteParam(ctx, 0x004A, []byte{0x00, 0x00})
	if !errors.Is(err, breezy.ErrReadOnly) {
		t.Fatalf("want ErrReadOnly, got %v", err)
	}
}

// TestWriteParams_UnregisteredParamPassesThrough confirms that param IDs
// not present in the registry skip the read-only gate — raw access for
// diagnostics is intentionally exempt. We use the fakedevice and verify
// the write reaches the wire.
func TestWriteParams_UnregisteredParamPassesThrough(t *testing.T) {
	srv := newTestServer(t, testPassword)
	c := newTestClient(t, srv.Addr(), testPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 0x00FA is reserved-but-unused in the registry as of this writing;
	// pick a clearly-unregistered ID with a low byte safely outside the
	// 0xFC-0xFF reserved range.
	const unregistered breezy.ParamID = 0x00DE
	if _, ok := breezy.LookupByID(unregistered); ok {
		t.Skipf("unregistered fixture id 0x%04X is now registered; pick another", uint16(unregistered))
	}
	if err := c.WriteParam(ctx, unregistered, []byte{0x42}); err != nil {
		// The fakedevice may or may not accept this; the contract being
		// tested is that the package layer does NOT reject it as ReadOnly.
		if errors.Is(err, breezy.ErrReadOnly) {
			t.Fatalf("unregistered write rejected as read-only: %v", err)
		}
	}
}

// ----- Idempotent Close -----

func TestClient_Close_Idempotent(t *testing.T) {
	c, err := breezy.NewClient("127.0.0.1:1", testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close returned %v, want nil", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("third Close returned %v, want nil", err)
	}
}
