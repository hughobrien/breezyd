// SPDX-License-Identifier: GPL-3.0-or-later

package breezy_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

func TestMemClient_RoundTrip(t *testing.T) {
	c := breezy.NewMemClient(map[breezy.ParamID][]byte{
		0x0001: {0x00},
	})
	ctx := context.Background()
	if err := c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x42}}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := c.ReadParams(ctx, []breezy.ParamID{0x0001})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got[0x0001]) != "\x42" {
		t.Fatalf("got %x want 42", got[0x0001])
	}
}

func TestMemClient_WriteNewParam(t *testing.T) {
	// Write to a param that wasn't in the seed — should be stored and readable.
	c := breezy.NewMemClient(nil)
	ctx := context.Background()
	if err := c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x01, 0x00}}}); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := c.ReadParams(ctx, []breezy.ParamID{0x0001})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := []byte{0x01, 0x00}
	if string(got[0x0001]) != string(want) {
		t.Fatalf("got %x want %x", got[0x0001], want)
	}
}

func TestMemClient_AbsentParamOmittedFromResult(t *testing.T) {
	c := breezy.NewMemClient(nil)
	got, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, ok := got[0x0001]; ok {
		t.Fatal("absent param should be missing from result map")
	}
}

func TestMemClient_IsLocal(t *testing.T) {
	c := breezy.NewMemClient(nil)
	if !c.IsLocal() {
		t.Fatal("MemClient.IsLocal should be true")
	}
}

func TestMemClient_AuthFault(t *testing.T) {
	c := breezy.NewMemClient(nil)
	c.SetAuthFailureMode(true)
	_, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	if !errors.Is(err, breezy.ErrAuth) {
		t.Fatalf("got %v want ErrAuth", err)
	}
	// Also affects writes.
	werr := c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x00}}})
	if !errors.Is(werr, breezy.ErrAuth) {
		t.Fatalf("write: got %v want ErrAuth", werr)
	}
}

func TestMemClient_TimeoutFault(t *testing.T) {
	c := breezy.NewMemClient(nil)
	c.SetTimeoutMode(true)
	err := c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x00}}})
	if !errors.Is(err, breezy.ErrTimeout) {
		t.Fatalf("got %v want ErrTimeout", err)
	}
	// Also affects reads.
	_, rerr := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	if !errors.Is(rerr, breezy.ErrTimeout) {
		t.Fatalf("read: got %v want ErrTimeout", rerr)
	}
}

func TestMemClient_FaultClearable(t *testing.T) {
	c := breezy.NewMemClient(nil)
	c.SetAuthFailureMode(true)
	c.SetAuthFailureMode(false)
	if _, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001}); err != nil {
		t.Fatalf("expected no error after clearing fault, got %v", err)
	}
}

func TestMemClient_Reset(t *testing.T) {
	c := breezy.NewMemClient(map[breezy.ParamID][]byte{0x0001: {0x00}})
	// Mutate and inject fault.
	_ = c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0xFF}}})
	c.SetAuthFailureMode(true)
	// Reset should restore seed params and clear the fault.
	c.Reset()
	got, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	if err != nil {
		t.Fatalf("read after reset: %v", err)
	}
	if string(got[0x0001]) != "\x00" {
		t.Fatalf("reset didn't restore: got %x", got[0x0001])
	}
}

func TestMemClient_ResetDoesNotShareSlices(t *testing.T) {
	// Verify Reset makes independent copies, not aliased slices.
	seed := map[breezy.ParamID][]byte{0x0001: {0xAA}}
	c := breezy.NewMemClient(seed)
	c.Reset()
	_ = c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0xBB}}})
	// Second reset should still see 0xAA, not 0xBB.
	c.Reset()
	got, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[0x0001][0] != 0xAA {
		t.Fatalf("after second reset, got %x want AA", got[0x0001])
	}
}

func TestMemClient_Concurrency(t *testing.T) {
	c := breezy.NewMemClient(map[breezy.ParamID][]byte{0x0001: {0x00}})
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
		}()
		go func() {
			defer wg.Done()
			_ = c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x01}}})
		}()
	}
	wg.Wait()
}

func TestMemClient_FromFile(t *testing.T) {
	c, err := breezy.NewMemClientFromFile("fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("from file: %v", err)
	}
	// snapshot_148.json has 0x0001 (power) present.
	got, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got[0x0001]) == 0 {
		t.Fatal("expected param 0x0001 present in snapshot_148.json")
	}
}

func TestMemClient_FromFile_LoadsAllParams(t *testing.T) {
	c, err := breezy.NewMemClientFromFile("fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("from file: %v", err)
	}
	// Request the full status param set to confirm ~120 params loaded.
	got, err := c.ReadParams(context.Background(), breezy.StatusParamIDs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// StatusParamIDs has 43 entries; a healthy snapshot should provide most.
	if len(got) < 30 {
		t.Fatalf("expected at least 30 params loaded, got %d", len(got))
	}
}

// Compile-time check: *MemClient satisfies breezy.DeviceClient.
var _ breezy.DeviceClient = (*breezy.MemClient)(nil)
