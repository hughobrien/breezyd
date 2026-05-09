// SPDX-License-Identifier: GPL-3.0-or-later

package fakedevice

import (
	"bytes"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
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
	t.Cleanup(func() { _ = conn.Close() })
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
	is := is.New(t)
	_, err := NewServer("/nonexistent/path/snapshot.json", testDeviceID, testPassword)
	is.True(err != nil) // expected error for missing snapshot
}

func TestServer_AddrAndClose(t *testing.T) {
	is := is.New(t)
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	is.NoErr(err)
	addr := srv.Addr()
	is.True(addr != "") // Addr returned non-empty
	host, port, err := net.SplitHostPort(addr)
	is.NoErr(err)
	is.Equal(host, "127.0.0.1")
	is.True(port != "0") // expected concrete port
	is.NoErr(srv.Close())
	// Multiple closes are safe.
	is.NoErr(srv.Close())
}

func TestRoundTrip_ReadUnitType(t *testing.T) {
	is := is.New(t)
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	is.NoErr(err)
	t.Cleanup(func() { _ = srv.Close() })

	conn := newClient(t, srv)
	// Read 0x00B9: snapshot value is "1100" (LE) = 17.
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x00B9}))
	is.NoErr(err)
	is.Equal(fn, breezy.FuncResponse)
	pvs, err := breezy.ParseDataBlock(data)
	is.NoErr(err)
	is.Equal(len(pvs), 1)
	is.Equal(pvs[0].ID, breezy.ParamID(0x00B9))
	is.True(bytes.Equal(pvs[0].Value, []byte{0x11, 0x00}))
}

func TestRoundTrip_ReadHighPageVOC(t *testing.T) {
	is := is.New(t)
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	is.NoErr(err)
	t.Cleanup(func() { _ = srv.Close() })

	conn := newClient(t, srv)
	// Read 0x0320: VOC index = 350 LE = 5E 01.
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x0320}))
	is.NoErr(err)
	is.Equal(fn, breezy.FuncResponse)
	pvs, err := breezy.ParseDataBlock(data)
	is.NoErr(err)
	is.Equal(len(pvs), 1)
	is.Equal(pvs[0].ID, breezy.ParamID(0x0320))
	got := uint16(pvs[0].Value[0]) | uint16(pvs[0].Value[1])<<8
	is.Equal(got, uint16(350))
}

func TestRoundTrip_WriteWithReply(t *testing.T) {
	is := is.New(t)
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	is.NoErr(err)
	t.Cleanup(func() { _ = srv.Close() })

	conn := newClient(t, srv)
	// Write 0x0001 = 0x00 (turn off).
	wbuf := breezy.BuildWriteDataBlock([]breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0x00}},
	})
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword, breezy.FuncWriteWithReply, wbuf)
	is.NoErr(err)
	is.Equal(fn, breezy.FuncResponse)
	pvs, err := breezy.ParseDataBlock(data)
	is.NoErr(err)
	is.Equal(len(pvs), 1)
	is.Equal(pvs[0].ID, breezy.ParamID(0x0001))
	is.True(bytes.Equal(pvs[0].Value, []byte{0x00}))

	// Now read back 0x0001; should be 0.
	fn, data, err = roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x0001}))
	is.NoErr(err)
	is.Equal(fn, breezy.FuncResponse)
	pvs, err = breezy.ParseDataBlock(data)
	is.NoErr(err)
	is.Equal(len(pvs), 1)
	is.Equal(pvs[0].ID, breezy.ParamID(0x0001))
	is.True(bytes.Equal(pvs[0].Value, []byte{0x00}))
}

func TestRoundTrip_WriteNoResponse(t *testing.T) {
	is := is.New(t)
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	is.NoErr(err)
	t.Cleanup(func() { _ = srv.Close() })

	conn := newClient(t, srv)

	// Write-no-response: server should apply but not reply.
	wbuf := breezy.BuildWriteDataBlock([]breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0x00}},
	})
	pkt := breezy.EncodeRequest(testDeviceID, testPassword, breezy.FuncWriteNoResponse, wbuf)
	_, err = conn.Write(pkt)
	is.NoErr(err)
	// Read with short timeout — should time out (no response).
	is.NoErr(conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)))
	buf := make([]byte, 2048)
	_, err = conn.Read(buf)
	is.True(err != nil) // expected timeout; got response
	nerr, ok := err.(net.Error)
	is.True(ok)             // expected net.Error
	is.True(nerr.Timeout()) // expected timeout

	// Verify the write was applied by reading back.
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x0001}))
	is.NoErr(err)
	is.Equal(fn, breezy.FuncResponse)
	pvs, err := breezy.ParseDataBlock(data)
	is.NoErr(err)
	is.Equal(len(pvs), 1)
	is.Equal(pvs[0].ID, breezy.ParamID(0x0001))
	is.True(bytes.Equal(pvs[0].Value, []byte{0x00}))
}

func TestRoundTrip_UnsupportedParam(t *testing.T) {
	is := is.New(t)
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	is.NoErr(err)
	t.Cleanup(func() { _ = srv.Close() })

	conn := newClient(t, srv)
	// 0x0003 was FD in the sweep — should be unsupported.
	fn, data, err := roundTrip(t, conn, testDeviceID, testPassword,
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x0003}))
	is.NoErr(err)
	is.Equal(fn, breezy.FuncResponse)
	pvs, err := breezy.ParseDataBlock(data)
	is.NoErr(err)
	is.Equal(len(pvs), 1)
	is.Equal(pvs[0].ID, breezy.ParamID(0x0003))
	is.True(pvs[0].Unsupported) // expected Unsupported=true for missing param
}

func TestRoundTrip_WrongPassword(t *testing.T) {
	is := is.New(t)
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	is.NoErr(err)
	t.Cleanup(func() { _ = srv.Close() })

	conn := newClient(t, srv)
	_, _, err = roundTrip(t, conn, testDeviceID, "wrongpw",
		breezy.FuncRead, breezy.BuildReadDataBlock([]breezy.ParamID{0x00B9}))
	is.True(errors.Is(err, breezy.ErrAuth)) // expected ErrAuth
}

func TestRoundTrip_WrongDeviceIDDropped(t *testing.T) {
	is := is.New(t)
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	is.NoErr(err)
	t.Cleanup(func() { _ = srv.Close() })

	conn := newClient(t, srv)
	pkt := breezy.EncodeRequest("BREEZYNOTTHISONE", testPassword,
		breezy.FuncRead, []byte{0xB9})
	_, err = conn.Write(pkt)
	is.NoErr(err)
	is.NoErr(conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)))
	buf := make([]byte, 2048)
	_, err = conn.Read(buf)
	is.True(err != nil) // expected timeout (silent drop); got response
	nerr, ok := err.(net.Error)
	is.True(ok)             // expected net.Error
	is.True(nerr.Timeout()) // expected timeout
}

func TestConcurrentReads(t *testing.T) {
	srv, err := NewServer(snapshotPath(t), testDeviceID, testPassword)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

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
			defer func() { _ = conn.Close() }()

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
