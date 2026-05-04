package fakedevice

import (
	"bytes"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/twinfresh/pkg/breezy"
)

const (
	testDeviceID = "BREEZY00000000A0"
	testPassword = "1111"
)

// snapshotPath returns the absolute path to the committed snapshot fixture.
func snapshotPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("snapshot_148.json")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

// newClient dials a UDP socket connected to the server's address. The
// returned conn has a 1-second read deadline so a misbehaving server can't
// hang the test.
func newClient(t *testing.T, srv *Server) *net.UDPConn {
	t.Helper()
	raddr, err := net.ResolveUDPAddr("udp", srv.Addr())
	if err != nil {
		t.Fatalf("ResolveUDPAddr: %v", err)
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		t.Fatalf("DialUDP: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	return conn
}

// roundTrip sends one request and reads one response. Returns the decoded
// (function, dataBlock).
func roundTrip(t *testing.T, conn *net.UDPConn, deviceID, password string, fn byte, data []byte) (byte, []byte, error) {
	t.Helper()
	pkt := breezy.EncodeRequest(deviceID, password, fn, data)
	if _, err := conn.Write(pkt); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, nil, err
	}
	return breezy.DecodeResponse(buf[:n], deviceID, password)
}

func TestNewServer_BadSnapshotPath(t *testing.T) {
	_, err := NewServer("/nonexistent/path/snapshot.json", testDeviceID, testPassword)
	if err == nil {
		t.Fatal("expected error for missing snapshot, got nil")
	}
}

func TestServer_AddrAndClose(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	addr := srv.Addr()
	if addr == "" {
		t.Fatal("Addr returned empty string")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("expected 127.0.0.1, got %s", host)
	}
	if port == "0" {
		t.Errorf("expected concrete port, got 0")
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Multiple closes are safe.
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestRoundTrip_ReadUnitType(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	conn := newClient(t, srv)
	// Read 0x00B9: snapshot value is "1100" (LE) = 17.
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x00B9}))
	if err != nil {
		t.Fatalf("roundTrip: %v", err)
	}
	if fn != breezy.FuncResponse {
		t.Fatalf("function: got 0x%02x want 0x06", fn)
	}
	pvs, err := breezy.ParseDataBlock(data)
	if err != nil {
		t.Fatalf("ParseDataBlock: %v", err)
	}
	if len(pvs) != 1 {
		t.Fatalf("expected 1 entry, got %d (%+v)", len(pvs), pvs)
	}
	if pvs[0].ID != 0x00B9 {
		t.Errorf("ID: got 0x%04x want 0x00B9", pvs[0].ID)
	}
	if !bytes.Equal(pvs[0].Value, []byte{0x11, 0x00}) {
		t.Errorf("Value: got %x want 1100", pvs[0].Value)
	}
}

func TestRoundTrip_ReadHighPageVOC(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	conn := newClient(t, srv)
	// Read 0x0320: VOC index = 350 LE = 5E 01.
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x0320}))
	if err != nil {
		t.Fatalf("roundTrip: %v", err)
	}
	if fn != breezy.FuncResponse {
		t.Fatalf("function: got 0x%02x want 0x06", fn)
	}
	pvs, err := breezy.ParseDataBlock(data)
	if err != nil {
		t.Fatalf("ParseDataBlock: %v", err)
	}
	if len(pvs) != 1 {
		t.Fatalf("expected 1 entry, got %d (%+v)", len(pvs), pvs)
	}
	if pvs[0].ID != 0x0320 {
		t.Errorf("ID: got 0x%04x want 0x0320", pvs[0].ID)
	}
	got := uint16(pvs[0].Value[0]) | uint16(pvs[0].Value[1])<<8
	if got != 350 {
		t.Errorf("VOC: got %d want 350", got)
	}
}

func TestRoundTrip_WriteWithReply(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	conn := newClient(t, srv)
	// Write 0x0001 = 0x00 (turn off).
	wbuf := breezy.BuildWriteDataBlock([]breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0x00}},
	})
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword, breezy.FuncWriteWithReply, wbuf)
	if err != nil {
		t.Fatalf("write roundTrip: %v", err)
	}
	if fn != breezy.FuncResponse {
		t.Fatalf("function: got 0x%02x want 0x06", fn)
	}
	pvs, err := breezy.ParseDataBlock(data)
	if err != nil {
		t.Fatalf("ParseDataBlock: %v", err)
	}
	if len(pvs) != 1 || pvs[0].ID != 0x0001 || !bytes.Equal(pvs[0].Value, []byte{0x00}) {
		t.Fatalf("write reply mismatch: %+v", pvs)
	}

	// Now read back 0x0001; should be 0.
	fn, data, err = roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x0001}))
	if err != nil {
		t.Fatalf("read roundTrip: %v", err)
	}
	if fn != breezy.FuncResponse {
		t.Fatalf("function: got 0x%02x want 0x06", fn)
	}
	pvs, err = breezy.ParseDataBlock(data)
	if err != nil {
		t.Fatalf("ParseDataBlock: %v", err)
	}
	if len(pvs) != 1 || pvs[0].ID != 0x0001 || !bytes.Equal(pvs[0].Value, []byte{0x00}) {
		t.Fatalf("read mismatch after write: %+v", pvs)
	}
}

func TestRoundTrip_WriteNoResponse(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	conn := newClient(t, srv)

	// Write-no-response: server should apply but not reply.
	wbuf := breezy.BuildWriteDataBlock([]breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0x00}},
	})
	pkt := breezy.EncodeRequest(testDeviceID, testPassword, breezy.FuncWriteNoResponse, wbuf)
	if _, err := conn.Write(pkt); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// Read with short timeout — should time out (no response).
	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 2048)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected timeout; got response")
	} else if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}

	// Verify the write was applied by reading back.
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x0001}))
	if err != nil {
		t.Fatalf("read roundTrip: %v", err)
	}
	if fn != breezy.FuncResponse {
		t.Fatalf("function: got 0x%02x want 0x06", fn)
	}
	pvs, err := breezy.ParseDataBlock(data)
	if err != nil {
		t.Fatalf("ParseDataBlock: %v", err)
	}
	if len(pvs) != 1 || pvs[0].ID != 0x0001 || !bytes.Equal(pvs[0].Value, []byte{0x00}) {
		t.Fatalf("write-no-response not applied: %+v", pvs)
	}
}

func TestRoundTrip_UnsupportedParam(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	conn := newClient(t, srv)
	// 0x0003 was FD in the sweep — should be unsupported.
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x0003}))
	if err != nil {
		t.Fatalf("roundTrip: %v", err)
	}
	if fn != breezy.FuncResponse {
		t.Fatalf("function: got 0x%02x want 0x06", fn)
	}
	pvs, err := breezy.ParseDataBlock(data)
	if err != nil {
		t.Fatalf("ParseDataBlock: %v", err)
	}
	if len(pvs) != 1 {
		t.Fatalf("expected 1 entry, got %d (%+v)", len(pvs), pvs)
	}
	if pvs[0].ID != 0x0003 {
		t.Errorf("ID: got 0x%04x want 0x0003", pvs[0].ID)
	}
	if !pvs[0].Unsupported {
		t.Errorf("expected Unsupported=true for missing param")
	}
}

func TestRoundTrip_WrongPassword(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	conn := newClient(t, srv)
	_, _, err = roundTrip(t, conn, testDeviceID, "wrongpw",
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x00B9}))
	if !errors.Is(err, breezy.ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}

func TestRoundTrip_WrongDeviceIDDropped(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	conn := newClient(t, srv)
	pkt := breezy.EncodeRequest("BREEZYNOTTHISONE", testPassword,
		breezy.FuncRead, []byte{0xB9})
	if _, err := conn.Write(pkt); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	buf := make([]byte, 2048)
	if _, err := conn.Read(buf); err == nil {
		t.Fatal("expected timeout (silent drop); got response")
	} else if nerr, ok := err.(net.Error); !ok || !nerr.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func TestConcurrentReads(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	const goroutines = 10
	const reqsPerG = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines*reqsPerG)

	for g := 0; g < goroutines; g++ {
		go func(gid int) {
			defer wg.Done()
			raddr, err := net.ResolveUDPAddr("udp", srv.Addr())
			if err != nil {
				errCh <- err
				return
			}
			conn, err := net.DialUDP("udp", nil, raddr)
			if err != nil {
				errCh <- err
				return
			}
			defer conn.Close()

			for i := 0; i < reqsPerG; i++ {
				pkt := breezy.EncodeRequest(testDeviceID, testPassword,
					breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x00B9}))
				if _, err := conn.Write(pkt); err != nil {
					errCh <- err
					return
				}
				if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
					errCh <- err
					return
				}
				buf := make([]byte, 2048)
				n, err := conn.Read(buf)
				if err != nil {
					errCh <- err
					return
				}
				fn, data, err := breezy.DecodeResponse(buf[:n], testDeviceID, testPassword)
				if err != nil {
					errCh <- err
					return
				}
				if fn != breezy.FuncResponse {
					errCh <- errors.New("unexpected function in response")
					return
				}
				pvs, err := breezy.ParseDataBlock(data)
				if err != nil {
					errCh <- err
					return
				}
				if len(pvs) != 1 || pvs[0].ID != 0x00B9 {
					errCh <- errors.New("unexpected payload")
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Errorf("goroutine error: %v", e)
	}
}
