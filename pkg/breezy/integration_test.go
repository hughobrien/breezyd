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
	"os"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
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
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.ReadParam(ctx, 0x00B9)
	if err != nil {
		t.Fatalf("ReadParam(0x00B9): %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("expected 2-byte device type, got %d bytes: %x", len(raw), raw)
	}

	// Decode via the registry too, to exercise the full path.
	p, ok := breezy.LookupByID(0x00B9)
	if !ok {
		t.Fatal("0x00B9 not in registry")
	}
	v, err := p.Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	typ := binary.LittleEndian.Uint16(raw)
	if typ == 0 {
		t.Fatalf("device type is zero (decoded: %s)", v)
	}
	t.Logf("device type: %d (%s)", typ, v)
}

func TestIntegration_ReadFirmware(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.ReadParam(ctx, 0x0086)
	if err != nil {
		t.Fatalf("ReadParam(0x0086): %v", err)
	}

	p, ok := breezy.LookupByID(0x0086)
	if !ok {
		t.Fatal("0x0086 not in registry")
	}
	v, err := p.Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	fw, ok := v.(breezy.FirmwareMetaValue)
	if !ok {
		t.Fatalf("expected FirmwareMetaValue, got %T", v)
	}
	if fw.Major == 0 && fw.Minor == 0 {
		t.Fatalf("firmware version is 0.0 (raw: %x)", raw)
	}
	t.Logf("firmware: %s", fw)
}

func TestIntegration_ReadRTCBattery(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	raw, err := c.ReadParam(ctx, 0x0024)
	if err != nil {
		t.Fatalf("ReadParam(0x0024): %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("expected 2-byte rtc battery value, got %d bytes: %x", len(raw), raw)
	}
	mv := binary.LittleEndian.Uint16(raw)
	if mv < 1000 || mv > 5000 {
		t.Fatalf("rtc battery out of sane range: %d mV (raw %x)", mv, raw)
	}
	t.Logf("rtc battery: %d mV", mv)
}

func TestIntegration_HighPageRead(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 0x0320 lives on page 3; reading it end-to-end validates that the
	// codec emits the FF page-switch marker and the device honors it.
	raw, err := c.ReadParam(ctx, 0x0320)
	if err != nil {
		t.Fatalf("ReadParam(0x0320): %v", err)
	}
	if len(raw) != 2 {
		t.Fatalf("expected 2-byte voc index, got %d bytes: %x", len(raw), raw)
	}
	voc := binary.LittleEndian.Uint16(raw)
	if voc > 500 {
		t.Fatalf("voc index out of expected range: %d (raw %x)", voc, raw)
	}
	t.Logf("voc index: %d", voc)
}

func TestIntegration_MultiByteWriteRoundtrip(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Save the current night-timer duration so we can restore it.
	orig, err := c.ReadParam(ctx, 0x0302)
	if err != nil {
		t.Fatalf("ReadParam(0x0302) initial: %v", err)
	}
	if len(orig) != 2 {
		t.Fatalf("expected 2-byte duration, got %d bytes: %x", len(orig), orig)
	}
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
	if err := c.WriteParam(ctx, 0x0302, target); err != nil {
		t.Fatalf("WriteParam(0x0302, %x): %v", target, err)
	}

	got, err := c.ReadParam(ctx, 0x0302)
	if err != nil {
		t.Fatalf("ReadParam(0x0302) readback: %v", err)
	}
	if len(got) != 2 || got[0] != target[0] || got[1] != target[1] {
		t.Fatalf("readback mismatch: wrote %x, got %x", target, got)
	}
	t.Logf("multi-byte write+readback ok: %x", got)
}

func TestIntegration_SpeedRoundtrip(t *testing.T) {
	c := newClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	orig, err := c.ReadParam(ctx, 0x0044)
	if err != nil {
		t.Fatalf("ReadParam(0x0044) initial: %v", err)
	}
	if len(orig) != 1 {
		t.Fatalf("expected 1-byte manual %%, got %d bytes: %x", len(orig), orig)
	}
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
	if err := c.WriteParam(ctx, 0x0044, []byte{target}); err != nil {
		t.Fatalf("WriteParam(0x0044, %d): %v", target, err)
	}

	got, err := c.ReadParam(ctx, 0x0044)
	if err != nil {
		t.Fatalf("ReadParam(0x0044) readback: %v", err)
	}
	if len(got) != 1 || got[0] != target {
		t.Fatalf("readback mismatch: wrote %d, got %x", target, got)
	}
	t.Logf("single-byte write+readback ok: %d", got[0])
}

func TestIntegration_DiscoverFindsDevice(t *testing.T) {
	requireIntegration(t)
	expectedID := envOrSkip(t, "BREEZY_TEST_DEVICE_ID")

	// Discovery probes broadcast and waits up to 2s by default; allow a
	// generous outer ceiling here for slow networks.
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	found, err := breezy.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
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
