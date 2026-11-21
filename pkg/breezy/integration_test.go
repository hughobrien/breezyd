// SPDX-License-Identifier: GPL-3.0-or-later

//go:build integration

// Live integration tests against a real Breezy device. These are gated by
// both the `integration` build tag (so the default `go test ./...` won't
// even compile them) and the BREEZY_INTEGRATION=1 env var (so accidentally
// invoking with the tag still produces no-op skips). Device address, ID,
// and password come from BREEZY_TEST_DEVICE_{IP,ID,PASSWORD}.
//
// Any test that writes MUST register a t.Cleanup to restore the original
// value, so re-running the suite leaves the unit in its prior state.
package breezy_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// envOrSkip returns the value of env var k, or t.Skip's with a clear
// message when it's missing/empty.
func envOrSkip(t *testing.T, k string) string {
	t.Helper()
	v := os.Getenv(k)
	if v == "" {
		t.Skipf("set %s to run integration tests", k)
	}
	return v
}

// requireIntegration enforces the runtime gate. Even with the build tag,
// tests should be a no-op unless BREEZY_INTEGRATION=1.
func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("BREEZY_INTEGRATION") != "1" {
		t.Skip("set BREEZY_INTEGRATION=1 and BREEZY_TEST_DEVICE_* envs to run integration tests")
	}
}

// newClient builds a Client from the test env vars, registering Close on
// cleanup. Skips (not fails) if any required env var is missing.
func newClient(t *testing.T) *breezy.Client {
	t.Helper()
	requireIntegration(t)
	addr := envOrSkip(t, "BREEZY_TEST_DEVICE_IP")
	id := envOrSkip(t, "BREEZY_TEST_DEVICE_ID")
	pw := envOrSkip(t, "BREEZY_TEST_DEVICE_PASSWORD")

	c, err := breezy.NewClient(addr, id, pw, breezy.WithTimeout(2*time.Second))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestIntegration_ReadUnitType(t *testing.T) {
	is := is.New(t)
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.ReadParam(ctx, 0x00B9)
	is.NoErr(err)
	is.Equal(len(raw), 2) // 2-byte device type

	// Decode via the registry too, to exercise the full path.
	p, ok := breezy.LookupByID(0x00B9)
	is.True(ok) // 0x00B9 in registry
	v, err := p.Decode(raw)
	is.NoErr(err)

	typ := binary.LittleEndian.Uint16(raw)
	is.True(typ != 0) // device type non-zero
	t.Logf("device type: %d (%s)", typ, v)
}

func TestIntegration_ReadFirmware(t *testing.T) {
	is := is.New(t)
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.ReadParam(ctx, 0x0086)
	is.NoErr(err)

	p, ok := breezy.LookupByID(0x0086)
	is.True(ok) // 0x0086 in registry
	v, err := p.Decode(raw)
	is.NoErr(err)

	fw, ok := v.(breezy.FirmwareMetaValue)
	is.True(ok)                             // value is FirmwareMetaValue
	is.True(fw.Major != 0 || fw.Minor != 0) // firmware version not 0.0
	t.Logf("firmware: %s", fw)
}

func TestIntegration_ReadRTCBattery(t *testing.T) {
	is := is.New(t)
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.ReadParam(ctx, 0x0024)
	is.NoErr(err)
	is.Equal(len(raw), 2) // 2-byte rtc battery value
	mv := binary.LittleEndian.Uint16(raw)
	is.True(mv >= 1000 && mv <= 5000) // rtc battery in sane range
	t.Logf("rtc battery: %d mV", mv)
}

func TestIntegration_HighPageRead(t *testing.T) {
	is := is.New(t)
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 0x0320 lives on page 3; reading it end-to-end validates that the
	// codec emits the FF page-switch marker and the device honors it.
	raw, err := c.ReadParam(ctx, 0x0320)
	is.NoErr(err)
	is.Equal(len(raw), 2) // 2-byte voc index
	voc := binary.LittleEndian.Uint16(raw)
	is.True(voc <= 500) // voc index in expected range
	t.Logf("voc index: %d", voc)
}

func TestIntegration_MultiByteWriteRoundtrip(t *testing.T) {
	is := is.New(t)
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Save the current night-timer duration so we can restore it.
	orig, err := c.ReadParam(ctx, 0x0302)
	is.NoErr(err)
	is.Equal(len(orig), 2) // 2-byte duration
	t.Logf("original night_duration: %02d:%02d (raw %x)", orig[1], orig[0], orig)

	// Restore on cleanup using a fresh context — the test's ctx may have
	// fired by the time cleanup runs.
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// Take a defensive copy so any later mutation of orig can't poison
		// the cleanup write.
		restore := make([]byte, len(orig))
		copy(restore, orig)
		if err := c.WriteParam(cleanCtx, 0x0302, restore); err != nil {
			t.Errorf("cleanup WriteParam(0x0302, %x): %v", restore, err)
		}
	})

	// Write 7h 45m. Wire encoding is [minute, hour] LE.
	target := []byte{0x2D, 0x07}
	is.NoErr(c.WriteParam(ctx, 0x0302, target))

	got, err := c.ReadParam(ctx, 0x0302)
	is.NoErr(err)
	is.True(len(got) == 2 && got[0] == target[0] && got[1] == target[1]) // readback matches
	t.Logf("multi-byte write+readback ok: %x", got)
}

func TestIntegration_SpeedRoundtrip(t *testing.T) {
	is := is.New(t)
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	orig, err := c.ReadParam(ctx, 0x0044)
	is.NoErr(err)
	is.Equal(len(orig), 1) // 1-byte manual %
	t.Logf("original speed_manual_pct: %d", orig[0])

	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		restore := []byte{orig[0]}
		if err := c.WriteParam(cleanCtx, 0x0044, restore); err != nil {
			t.Errorf("cleanup WriteParam(0x0044, %x): %v", restore, err)
		}
	})

	const target byte = 20
	is.NoErr(c.WriteParam(ctx, 0x0044, []byte{target}))

	got, err := c.ReadParam(ctx, 0x0044)
	is.NoErr(err)
	is.True(len(got) == 1 && got[0] == target) // readback matches
	t.Logf("single-byte write+readback ok: %d", got[0])
}

func TestIntegration_DiscoverFindsDevice(t *testing.T) {
	is := is.New(t)
	requireIntegration(t)
	expectedID := envOrSkip(t, "BREEZY_TEST_DEVICE_ID")

	// Discovery probes broadcast and waits up to 2s by default; allow a
	// generous outer ceiling here for slow networks.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	found, err := breezy.Discover(ctx)
	is.NoErr(err)
	if len(found) == 0 {
		t.Logf("no devices responded to broadcast — network may filter; skipping")
		t.Skip("no broadcast replies")
	}

	for _, f := range found {
		if f.DeviceID == expectedID {
			t.Logf("found expected device %s at %s (unit type %d)", f.DeviceID, f.IP, f.UnitType)
			return
		}
	}
	t.Errorf("expected device %s not in discovery results: %+v", expectedID, found)
}

// ---- Reply-ID subset tests (issue #295) ------------------------------------
//
// Client.replyMatches (client.go) accepts a reply only if every param ID in
// it was one the request asked for or wrote. These tests confirm that
// assumption against real firmware by speaking the wire protocol on a private
// UDP socket (EncodeRequest / DecodeResponse / ParseDataBlock are exported),
// which lets us see every raw reply datagram, including extras the Client
// would silently discard.

// settleWait is the firmware's fan-settle window (see CLAUDE.md). RPM and
// air-quality params are not trustworthy until it has elapsed after a
// speed/mode write. Do not shorten.
const settleWait = 12 * time.Second

// rawReply is one decoded reply datagram.
type rawReply struct {
	fn     byte
	params []breezy.ParamValue
}

func (r rawReply) ids() []breezy.ParamID {
	out := make([]breezy.ParamID, len(r.params))
	for i, p := range r.params {
		out[i] = p.ID
	}
	return out
}

type rawDev struct {
	t        *testing.T
	conn     net.Conn
	id, pass string
}

func newRawDev(t *testing.T) *rawDev {
	t.Helper()
	requireIntegration(t)
	addr := envOrSkip(t, "BREEZY_TEST_DEVICE_IP")
	id := envOrSkip(t, "BREEZY_TEST_DEVICE_ID")
	pw := envOrSkip(t, "BREEZY_TEST_DEVICE_PASSWORD")
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &rawDev{t: t, conn: conn, id: id, pass: pw}
}

// exchange sends one request and returns every reply datagram that arrives
// within a short collection window after the first (so a second, "extra"
// reply would also be seen).
func (d *rawDev) exchange(fn byte, block []byte) []rawReply {
	d.t.Helper()
	buf := make([]byte, 2048)
	// Drain stale datagrams.
	for {
		_ = d.conn.SetReadDeadline(time.Now().Add(time.Millisecond))
		if _, err := d.conn.Read(buf); err != nil {
			break
		}
	}
	if _, err := d.conn.Write(breezy.EncodeRequest(d.id, d.pass, fn, block)); err != nil {
		d.t.Fatalf("write: %v", err)
	}
	var out []rawReply
	wait := 2 * time.Second
	for {
		_ = d.conn.SetReadDeadline(time.Now().Add(wait))
		n, err := d.conn.Read(buf)
		if err != nil {
			break
		}
		wait = 700 * time.Millisecond
		rfn, body, derr := breezy.DecodeResponse(buf[:n], d.id, d.pass)
		if derr != nil {
			d.t.Fatalf("decode reply: %v", derr)
		}
		parsed, perr := breezy.ParseDataBlock(body)
		if perr != nil {
			d.t.Fatalf("parse reply %x: %v", body, perr)
		}
		out = append(out, rawReply{fn: rfn, params: parsed})
	}
	if len(out) == 0 {
		d.t.Fatalf("no reply (timeout) for fn=0x%02x block=%x", fn, block)
	}
	return out
}

func fmtIDs(ids []breezy.ParamID) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = fmt.Sprintf("0x%04X", uint16(id))
	}
	return fmt.Sprint(s)
}

// assertSubset logs the request/reply IDs and fails if any reply contains an
// ID outside the request. Extras are named explicitly in the failure.
func assertSubset(t *testing.T, label string, req []breezy.ParamID, replies []rawReply) {
	t.Helper()
	want := map[breezy.ParamID]bool{}
	for _, id := range req {
		want[id] = true
	}
	t.Logf("%s: request %s, %d reply datagram(s)", label, fmtIDs(req), len(replies))
	for i, r := range replies {
		got := r.ids()
		var extra, missing, unsup []breezy.ParamID
		seen := map[breezy.ParamID]bool{}
		for _, id := range got {
			seen[id] = true
			if !want[id] {
				extra = append(extra, id)
			}
		}
		for _, id := range req {
			if !seen[id] {
				missing = append(missing, id)
			}
		}
		sort.Slice(extra, func(a, b int) bool { return extra[a] < extra[b] })
		for _, p := range r.params {
			if p.Unsupported {
				unsup = append(unsup, p.ID)
			}
		}
		t.Logf("%s: reply %d fn=0x%02x ids=%s unsupported=%s missing=%s", label, i, r.fn, fmtIDs(got), fmtIDs(unsup), fmtIDs(missing))
		if len(extra) > 0 {
			t.Errorf("%s: reply %d contains IDs outside the request: %s", label, i, fmtIDs(extra))
		}
	}
}

func (d *rawDev) read(label string, ids []breezy.ParamID) map[breezy.ParamID][]byte {
	d.t.Helper()
	replies := d.exchange(breezy.FuncRead, breezy.BuildReadDataBlock(ids))
	assertSubset(d.t, label, ids, replies)
	out := map[breezy.ParamID][]byte{}
	for _, r := range replies {
		for _, p := range r.params {
			if !p.Unsupported {
				out[p.ID] = p.Value
			}
		}
	}
	return out
}

func (d *rawDev) write(label string, writes []breezy.ParamWrite) {
	d.t.Helper()
	ids := make([]breezy.ParamID, len(writes))
	for i, w := range writes {
		ids[i] = w.ID
	}
	replies := d.exchange(breezy.FuncWriteWithReply, breezy.BuildWriteDataBlock(writes))
	assertSubset(d.t, label, ids, replies)
}

// pollerReadIDs mirrors cmd/breezyd/main.go defaultReadIDs (a main package,
// not importable): IDs spanning pages 0, 1 and 3, so the request carries
// FF page-switch markers.
var pollerReadIDs = []breezy.ParamID{
	0x0001, 0x0002, 0x0007, 0x000B,
	0x000F, 0x0011, 0x0019, 0x001A,
	0x001F, 0x0020, 0x0021, 0x0022,
	0x0024, 0x0025, 0x0027,
	0x003A, 0x003B, 0x003C, 0x003D, 0x003E, 0x003F,
	0x0044, 0x004A, 0x004B,
	0x0063, 0x0064, 0x0068,
	0x007E, 0x007F, 0x0081, 0x0083, 0x0084, 0x0086, 0x0088,
	0x00B7, 0x00B9,
	0x0129,
	0x0302, 0x0303, 0x030B, 0x0315, 0x031F, 0x0320,
}

// Read-only: the poller's full batch (pages 0/1/3), in one packet and in two
// halves.
func TestIntegration_ReplyIDs_ReadBatchAcrossPages(t *testing.T) {
	d := newRawDev(t)
	got := d.read("full-batch", pollerReadIDs)
	t.Logf("full-batch: %d of %d IDs returned values", len(got), len(pollerReadIDs))
	half := len(pollerReadIDs) / 2
	d.read("first-half", pollerReadIDs[:half])
	d.read("second-half", pollerReadIDs[half:])
}

// Read-only: a read that includes IDs the unit may not support (FD marker
// path). Candidates are probed; the test logs which came back as FD.
func TestIntegration_ReplyIDs_ReadUnsupported(t *testing.T) {
	d := newRawDev(t)
	candidates := []breezy.ParamID{0x0025, 0x0009, 0x0010, 0x0012, 0x0013, 0x0030, 0x0100, 0x0200, 0x0300}
	replies := d.exchange(breezy.FuncRead, breezy.BuildReadDataBlock(candidates))
	assertSubset(t, "unsupported-probe", candidates, replies)
	n := 0
	for _, r := range replies {
		for _, p := range r.params {
			if p.Unsupported {
				n++
			}
		}
	}
	t.Logf("unsupported-probe: %d FD unsupported markers seen", n)
}

// Writes: power, timer, speed preset and the SetSpeedManual packet, with a
// timer active across the 0x0002 writes. Restores power/speed/manual-pct/timer
// on cleanup and verifies the restore by readback.
func TestIntegration_ReplyIDs_Writes(t *testing.T) {
	d := newRawDev(t)
	c := newClient(t)

	ids := []breezy.ParamID{0x0001, 0x0002, 0x0007, 0x0044}
	orig := d.read("orig-state", ids)
	for _, id := range ids {
		if len(orig[id]) != 1 {
			t.Fatalf("cannot snapshot 0x%04X: %x", uint16(id), orig[id])
		}
	}
	t.Logf("original: power=%d speed_mode=%d timer=%d manual_pct=%d", orig[0x0001][0], orig[0x0002][0], orig[0x0007][0], orig[0x0044][0])

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Timer off, then manual pct, then speed mode, then power: the
		// original values, in dependency order. Power-off also clears timer.
		steps := [][]breezy.ParamWrite{
			{{ID: 0x0007, Value: []byte{0}}},
			{{ID: 0x0044, Value: []byte{orig[0x0044][0]}}},
			{{ID: 0x0002, Value: []byte{orig[0x0002][0]}}},
			{{ID: 0x0001, Value: []byte{orig[0x0001][0]}}},
		}
		for _, s := range steps {
			if err := c.WriteParams(ctx, s); err != nil {
				t.Errorf("cleanup WriteParams(%v): %v", s, err)
			}
		}
		// Let the unit settle before verifying (fan-settle window).
		time.Sleep(settleWait)
		vctx, vcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer vcancel()
		got, err := c.ReadParams(vctx, ids)
		if err != nil {
			t.Errorf("cleanup verify read: %v", err)
			return
		}
		for _, id := range ids {
			want := orig[id][0]
			// The timer is cleared by the 0x0002 / power writes; the
			// original may have been a timer, which we do not re-arm.
			if id == 0x0007 {
				want = 0
			}
			if len(got[id]) != 1 || got[id][0] != want {
				t.Errorf("cleanup verify: 0x%04X = %x, want %02x (original %02x)", uint16(id), got[id], want, orig[id][0])
			}
		}
		t.Logf("restored: power=%d speed_mode=%d timer=%d manual_pct=%d", got[0x0001][0], got[0x0002][0], got[0x0007][0], got[0x0044][0])
	})

	// Power on (single param), then start the night timer.
	d.write("power-on", []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}})
	d.write("timer-night", []breezy.ParamWrite{{ID: 0x0007, Value: []byte{1}}})
	if got := d.read("timer-active", []breezy.ParamID{0x0007}); len(got[0x0007]) != 1 || got[0x0007][0] != 1 {
		t.Logf("timer readback after write: %x (expected 01)", got[0x0007])
	}

	// 0x0002 write while a timer is active, alone (no 0x0007 in the packet):
	// firmware clears 0x0007 as a side effect, the case most likely to leak
	// an extra ID into the reply.
	d.write("speed-mode-preset1-timer-active", []breezy.ParamWrite{{ID: 0x0002, Value: []byte{1}}})
	after := d.read("timer-after-0002", []breezy.ParamID{0x0007})
	t.Logf("0x0007 after 0x0002 write: %x", after[0x0007])

	// Re-arm timer, then the multi-param SetSpeedPreset packet (ops.go).
	d.write("timer-night-again", []breezy.ParamWrite{{ID: 0x0007, Value: []byte{1}}})
	d.write("SetSpeedPreset-packet", []breezy.ParamWrite{
		{ID: 0x0002, Value: []byte{1}},
		{ID: 0x0007, Value: []byte{0}},
	})

	// The SetSpeedManual packet (ops.go), at the minimum speed.
	d.write("SetSpeedManual-packet", []breezy.ParamWrite{
		{ID: 0x0044, Value: []byte{10}},
		{ID: 0x0002, Value: []byte{0xFF}},
		{ID: 0x0007, Value: []byte{0}},
	})

	// Power-off packet (ops.go Power(false)).
	d.write("Power-off-packet", []breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0}},
		{ID: 0x0007, Value: []byte{0}},
	})

	// Wait out the fan-settle window before reading fan-sensitive params.
	time.Sleep(settleWait)
	d.read("post-settle-fan", []breezy.ParamID{0x004A, 0x004B, 0x0084})
}
