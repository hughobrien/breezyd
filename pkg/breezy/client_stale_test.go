// SPDX-License-Identifier: GPL-3.0-or-later

package breezy_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// lateReplyServer answers only the first datagram it receives, after delay,
// with a read response for 0x25 (value 0x07). Every later request is
// dropped, modelling a device that is slow once and then silent.
func lateReplyServer(t *testing.T, delay time.Duration) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		_, peer, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		time.Sleep(delay)
		resp := breezy.EncodeRequest(testDeviceID, testPassword, breezy.FuncResponse, []byte{0x25, 0x07})
		_, _ = pc.WriteTo(resp, peer)
		for {
			if _, _, err := pc.ReadFrom(buf); err != nil {
				return
			}
		}
	}()
	return pc.LocalAddr().String()
}

// A reply that lands after its request timed out and BEFORE the next
// request is sent must not be taken as the answer to that next request.
func TestExchange_StaleReplyBeforeNextSend_Discarded(t *testing.T) {
	is := is.New(t)
	addr := lateReplyServer(t, 150*time.Millisecond)
	c := newTestClient(t, addr, testPassword, breezy.WithTimeout(100*time.Millisecond), breezy.WithRetries(0))

	_, err := c.ReadParam(context.Background(), 0x25)
	is.True(err != nil) // timed out

	time.Sleep(150 * time.Millisecond) // late reply now sits in the socket buffer

	err = c.WriteParam(context.Background(), 0x01, []byte{0x00})
	is.True(err != nil) // the device never acknowledged this write
}

// A reply that lands after its request timed out and WHILE the next request
// is waiting must be recognised as not matching it.
func TestExchange_StaleReplyDuringNextWait_Discarded(t *testing.T) {
	is := is.New(t)
	addr := lateReplyServer(t, 150*time.Millisecond)
	c := newTestClient(t, addr, testPassword, breezy.WithTimeout(100*time.Millisecond), breezy.WithRetries(0))

	_, err := c.ReadParam(context.Background(), 0x25)
	is.True(err != nil) // timed out

	err = c.WriteParam(context.Background(), 0x01, []byte{0x00})
	is.True(err != nil) // stale 0x25 reply must not ack a write of 0x01

	_, err = c.ReadParam(context.Background(), 0x44)
	is.True(err != nil) // nor answer a read of 0x44
}
