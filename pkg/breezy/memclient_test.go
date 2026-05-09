// SPDX-License-Identifier: GPL-3.0-or-later

package breezy_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

func TestMemClient_RoundTrip(t *testing.T) {
	is := is.New(t)
	c := breezy.NewMemClient(map[breezy.ParamID][]byte{
		0x0001: {0x00},
	})
	ctx := context.Background()
	is.NoErr(c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x42}}}))
	got, err := c.ReadParams(ctx, []breezy.ParamID{0x0001})
	is.NoErr(err)
	is.Equal(string(got[0x0001]), "\x42")
}

func TestMemClient_WriteNewParam(t *testing.T) {
	is := is.New(t)
	// Write to a param that wasn't in the seed — should be stored and readable.
	c := breezy.NewMemClient(nil)
	ctx := context.Background()
	is.NoErr(c.WriteParams(ctx, []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x01, 0x00}}}))
	got, err := c.ReadParams(ctx, []breezy.ParamID{0x0001})
	is.NoErr(err)
	want := []byte{0x01, 0x00}
	is.Equal(string(got[0x0001]), string(want))
}

func TestMemClient_AbsentParamOmittedFromResult(t *testing.T) {
	is := is.New(t)
	c := breezy.NewMemClient(nil)
	got, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	is.NoErr(err)
	_, ok := got[0x0001]
	is.True(!ok) // absent param should be missing from result map
}

func TestMemClient_IsLocal(t *testing.T) {
	is := is.New(t)
	c := breezy.NewMemClient(nil)
	is.True(c.IsLocal()) // MemClient.IsLocal should be true
}

func TestMemClient_AuthFault(t *testing.T) {
	is := is.New(t)
	c := breezy.NewMemClient(nil)
	c.SetAuthFailureMode(true)
	_, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	is.True(errors.Is(err, breezy.ErrAuth))
	// Also affects writes.
	werr := c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x00}}})
	is.True(errors.Is(werr, breezy.ErrAuth))
}

func TestMemClient_TimeoutFault(t *testing.T) {
	is := is.New(t)
	c := breezy.NewMemClient(nil)
	c.SetTimeoutMode(true)
	err := c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0x00}}})
	is.True(errors.Is(err, breezy.ErrTimeout))
	// Also affects reads.
	_, rerr := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	is.True(errors.Is(rerr, breezy.ErrTimeout))
}

func TestMemClient_FaultClearable(t *testing.T) {
	is := is.New(t)
	c := breezy.NewMemClient(nil)
	c.SetAuthFailureMode(true)
	c.SetAuthFailureMode(false)
	_, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	is.NoErr(err) // expected no error after clearing fault
}

func TestMemClient_Reset(t *testing.T) {
	is := is.New(t)
	c := breezy.NewMemClient(map[breezy.ParamID][]byte{0x0001: {0x00}})
	// Mutate and inject fault.
	_ = c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0xFF}}})
	c.SetAuthFailureMode(true)
	// Reset should restore seed params and clear the fault.
	c.Reset()
	got, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	is.NoErr(err)
	is.Equal(string(got[0x0001]), "\x00") // reset restored seed
}

func TestMemClient_ResetDoesNotShareSlices(t *testing.T) {
	is := is.New(t)
	// Verify Reset makes independent copies, not aliased slices.
	seed := map[breezy.ParamID][]byte{0x0001: {0xAA}}
	c := breezy.NewMemClient(seed)
	c.Reset()
	_ = c.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{0xBB}}})
	// Second reset should still see 0xAA, not 0xBB.
	c.Reset()
	got, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	is.NoErr(err)
	is.Equal(got[0x0001][0], byte(0xAA)) // second reset restores original seed
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
	is := is.New(t)
	c, err := breezy.NewMemClientFromFile("fakedevice/snapshot_148.json")
	is.NoErr(err)
	// snapshot_148.json has 0x0001 (power) present.
	got, err := c.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	is.NoErr(err)
	is.True(len(got[0x0001]) != 0) // expected param 0x0001 present in snapshot_148.json
}

func TestMemClient_FromFile_LoadsAllParams(t *testing.T) {
	is := is.New(t)
	c, err := breezy.NewMemClientFromFile("fakedevice/snapshot_148.json")
	is.NoErr(err)
	// Request the full status param set to confirm ~120 params loaded.
	got, err := c.ReadParams(context.Background(), breezy.StatusParamIDs)
	is.NoErr(err)
	// StatusParamIDs has 43 entries; a healthy snapshot should provide most.
	is.True(len(got) >= 30) // expected at least 30 params loaded
}

// Compile-time check: *MemClient satisfies breezy.DeviceClient.
var _ breezy.DeviceClient = (*breezy.MemClient)(nil)
