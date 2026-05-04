package main

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

func TestState_RoundTrip(t *testing.T) {
	s := NewState()
	now := time.Now().UTC().Truncate(time.Second)
	in := Snapshot{
		IP: "192.168.1.148",
		Values: map[breezy.ParamID][]byte{
			0x01: {0x01},
			0x86: {0x06, 0x10, 0x01, 0x06},
		},
		LastPoll: now,
		LastErr:  nil,
	}
	s.Set("playroom", in)

	got, ok := s.Get("playroom")
	if !ok {
		t.Fatalf("Get returned ok=false after Set")
	}
	if got.IP != in.IP {
		t.Errorf("IP: got %q want %q", got.IP, in.IP)
	}
	if !got.LastPoll.Equal(in.LastPoll) {
		t.Errorf("LastPoll: got %v want %v", got.LastPoll, in.LastPoll)
	}
	if got.LastErr != nil {
		t.Errorf("LastErr: got %v want nil", got.LastErr)
	}
	if !reflect.DeepEqual(got.Values, in.Values) {
		t.Errorf("Values mismatch: got %v want %v", got.Values, in.Values)
	}
}

func TestState_RoundTrip_WithError(t *testing.T) {
	s := NewState()
	wantErr := errors.New("timeout")
	s.Set("a", Snapshot{IP: "10.0.0.1", LastErr: wantErr})

	got, ok := s.Get("a")
	if !ok {
		t.Fatalf("Get returned ok=false")
	}
	if got.LastErr == nil || got.LastErr.Error() != "timeout" {
		t.Errorf("LastErr: got %v want %v", got.LastErr, wantErr)
	}
}

func TestState_Get_Missing(t *testing.T) {
	s := NewState()
	got, ok := s.Get("nonexistent")
	if ok {
		t.Fatalf("Get returned ok=true for missing key")
	}
	if !reflect.DeepEqual(got, Snapshot{}) {
		t.Errorf("expected zero Snapshot, got %+v", got)
	}
}

func TestState_DeepCopy_OnGet(t *testing.T) {
	s := NewState()
	s.Set("a", Snapshot{Values: map[breezy.ParamID][]byte{0x01: {1, 2, 3}}})

	got, _ := s.Get("a")
	got.Values[0x01][0] = 99
	got.Values[0x02] = []byte{42}

	again, _ := s.Get("a")
	if again.Values[0x01][0] == 99 {
		t.Fatal("mutation of returned slice leaked into storage")
	}
	if _, ok := again.Values[0x02]; ok {
		t.Fatal("addition of new key leaked into storage")
	}
}

func TestState_DeepCopy_OnSet(t *testing.T) {
	s := NewState()
	src := map[breezy.ParamID][]byte{0x01: {1, 2, 3}}
	s.Set("a", Snapshot{Values: src})

	// Mutate the original map after Set; must not affect storage.
	src[0x01][0] = 99
	src[0x02] = []byte{42}

	got, _ := s.Get("a")
	if got.Values[0x01][0] == 99 {
		t.Fatal("mutation of caller's slice leaked into storage")
	}
	if _, ok := got.Values[0x02]; ok {
		t.Fatal("addition to caller's map leaked into storage")
	}
}

func TestState_UpdateIP(t *testing.T) {
	s := NewState()
	now := time.Now().UTC()
	s.Set("a", Snapshot{
		IP:       "1.1.1.1",
		Values:   map[breezy.ParamID][]byte{0x01: {1}},
		LastPoll: now,
	})

	s.UpdateIP("a", "2.2.2.2")

	got, ok := s.Get("a")
	if !ok {
		t.Fatalf("Get returned ok=false")
	}
	if got.IP != "2.2.2.2" {
		t.Errorf("IP: got %q want 2.2.2.2", got.IP)
	}
	if !got.LastPoll.Equal(now) {
		t.Errorf("LastPoll mutated: got %v want %v", got.LastPoll, now)
	}
	if !reflect.DeepEqual(got.Values, map[breezy.ParamID][]byte{0x01: {1}}) {
		t.Errorf("Values disturbed by UpdateIP: %v", got.Values)
	}
}

func TestState_UpdateIP_NoExistingSnapshot(t *testing.T) {
	s := NewState()
	s.UpdateIP("a", "10.0.0.5")

	got, ok := s.Get("a")
	if !ok {
		t.Fatalf("Get returned ok=false; UpdateIP should have created snapshot")
	}
	if got.IP != "10.0.0.5" {
		t.Errorf("IP: got %q want 10.0.0.5", got.IP)
	}
	if got.Values != nil {
		t.Errorf("Values: got %v want nil", got.Values)
	}
	if !got.LastPoll.IsZero() {
		t.Errorf("LastPoll: got %v want zero", got.LastPoll)
	}
	if got.LastErr != nil {
		t.Errorf("LastErr: got %v want nil", got.LastErr)
	}
}

func TestState_RecordPoll(t *testing.T) {
	s := NewState()
	snap := Snapshot{
		IP:       "1.2.3.4",
		Values:   map[breezy.ParamID][]byte{0x86: {6, 16, 1, 6}},
		LastPoll: time.Now().UTC(),
	}
	s.RecordPoll("a", snap)

	got, ok := s.Get("a")
	if !ok {
		t.Fatalf("Get returned ok=false after RecordPoll")
	}
	if got.IP != snap.IP {
		t.Errorf("IP mismatch")
	}
	if !reflect.DeepEqual(got.Values, snap.Values) {
		t.Errorf("Values mismatch")
	}
}

func TestState_Devices_Sorted(t *testing.T) {
	s := NewState()
	s.Set("zulu", Snapshot{})
	s.Set("alpha", Snapshot{})
	s.Set("mike", Snapshot{})

	got := s.Devices()
	want := []string{"alpha", "mike", "zulu"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Devices: got %v want %v", got, want)
	}
}

func TestState_Devices_Empty(t *testing.T) {
	s := NewState()
	got := s.Devices()
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestState_Delete(t *testing.T) {
	s := NewState()
	s.Set("a", Snapshot{IP: "1.1.1.1"})
	s.Set("b", Snapshot{IP: "2.2.2.2"})

	s.Delete("a")

	if _, ok := s.Get("a"); ok {
		t.Errorf("a should be gone after Delete")
	}
	if _, ok := s.Get("b"); !ok {
		t.Errorf("b should still be present")
	}

	// Deleting a missing key must not panic.
	s.Delete("nonexistent")
}

func TestState_WriteThrough_FreshSnapshot(t *testing.T) {
	s := NewState()
	s.WriteThrough("a", []breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0x01}},
		{ID: 0x0044, Value: []byte{0x32}},
	})
	got, ok := s.Get("a")
	if !ok {
		t.Fatalf("Get returned ok=false; WriteThrough should have created snapshot")
	}
	if !reflect.DeepEqual(got.Values[0x0001], []byte{0x01}) {
		t.Errorf("0x0001: got %x, want 01", got.Values[0x0001])
	}
	if !reflect.DeepEqual(got.Values[0x0044], []byte{0x32}) {
		t.Errorf("0x0044: got %x, want 32", got.Values[0x0044])
	}
}

func TestState_WriteThrough_PreservesPollMetadata(t *testing.T) {
	s := NewState()
	now := time.Now().UTC().Truncate(time.Second)
	wantErr := errors.New("transport")
	s.Set("a", Snapshot{
		IP: "1.1.1.1",
		Values: map[breezy.ParamID][]byte{
			0x0001: {0x00},        // power off
			0x004A: {0x10, 0x27}, // fan_supply_rpm 10000
		},
		LastPoll: now,
		LastErr:  wantErr,
	})
	s.WriteThrough("a", []breezy.ParamWrite{
		{ID: 0x0001, Value: []byte{0x01}}, // user turns power on
	})
	got, ok := s.Get("a")
	if !ok {
		t.Fatalf("Get returned ok=false")
	}
	if got.IP != "1.1.1.1" {
		t.Errorf("IP changed: %q", got.IP)
	}
	if !got.LastPoll.Equal(now) {
		t.Errorf("LastPoll changed: %v", got.LastPoll)
	}
	if got.LastErr == nil || got.LastErr.Error() != "transport" {
		t.Errorf("LastErr changed: %v", got.LastErr)
	}
	if !reflect.DeepEqual(got.Values[0x0001], []byte{0x01}) {
		t.Errorf("0x0001 not updated: %x", got.Values[0x0001])
	}
	// Existing values for params we didn't write must be preserved.
	if !reflect.DeepEqual(got.Values[0x004A], []byte{0x10, 0x27}) {
		t.Errorf("0x004A not preserved: %x", got.Values[0x004A])
	}
}

func TestState_WriteThrough_DeepCopiesValues(t *testing.T) {
	s := NewState()
	val := []byte{0x42}
	s.WriteThrough("a", []breezy.ParamWrite{{ID: 0x0001, Value: val}})
	val[0] = 0x99
	got, _ := s.Get("a")
	if got.Values[0x0001][0] != 0x42 {
		t.Errorf("WriteThrough did not deep-copy: stored %x", got.Values[0x0001])
	}
}

func TestState_WriteThrough_Empty(t *testing.T) {
	s := NewState()
	s.WriteThrough("a", nil)
	if _, ok := s.Get("a"); ok {
		t.Errorf("empty WriteThrough created a snapshot for missing device")
	}
	s.Set("b", Snapshot{IP: "1.1.1.1"})
	s.WriteThrough("b", []breezy.ParamWrite{})
	got, _ := s.Get("b")
	if got.IP != "1.1.1.1" {
		t.Errorf("empty WriteThrough disturbed existing snapshot: %+v", got)
	}
}

func TestParsePeriodicDiscovery(t *testing.T) {
	cases := []struct {
		in     string
		want   time.Duration
		wantOk bool
	}{
		{"periodic:5m", 5 * time.Minute, true},
		{"periodic:30s", 30 * time.Second, true},
		{"periodic:bogus", 0, false},
		{"on-start", 0, false},
		{"off", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		got, ok := parsePeriodicDiscovery(tc.in)
		if ok != tc.wantOk {
			t.Errorf("parsePeriodicDiscovery(%q) ok = %v, want %v", tc.in, ok, tc.wantOk)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("parsePeriodicDiscovery(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestRunDiscoveryWith_UpdatesIPViaRegistry(t *testing.T) {
	devices := NewDeviceRegistry(map[string]DeviceConfig{
		"playroom": {ID: "TESTID0000000001", Password: "1111", IP: "1.1.1.1:4000"},
	})
	stub := func(ctx context.Context) ([]breezy.Found, error) {
		return []breezy.Found{
			{DeviceID: "TESTID0000000001", IP: "10.0.0.5", UnitType: 17},
		}, nil
	}
	if err := runDiscoveryWith(context.Background(), devices, stub); err != nil {
		t.Fatalf("runDiscoveryWith: %v", err)
	}
	d, _ := devices.Get("playroom")
	if d.IP != "10.0.0.5:4000" {
		t.Errorf("after discovery IP = %q, want 10.0.0.5:4000", d.IP)
	}
}

func TestDeviceRegistry_ConcurrentReadAndUpdate(t *testing.T) {
	r := NewDeviceRegistry(map[string]DeviceConfig{
		"a": {ID: "TESTID0000000001", Password: "1111", IP: "1.1.1.1:4000"},
	})
	var wg sync.WaitGroup
	const N = 200
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			r.UpdateIP("a", "2.2.2.2:4000")
			r.UpdateIP("a", "3.3.3.3:4000")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < N; i++ {
			_, _ = r.Get("a")
			_ = r.Snapshot()
			_ = r.Names()
		}
	}()
	wg.Wait()
}

func TestState_Concurrent(t *testing.T) {
	s := NewState()
	var wg sync.WaitGroup
	const goroutines = 10
	const ops = 1000

	for i := 0; i < goroutines; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				s.Set("a", Snapshot{
					IP:     "1.1.1.1",
					Values: map[breezy.ParamID][]byte{0x01: {1, 2, 3}},
				})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				snap, ok := s.Get("a")
				if ok && snap.Values != nil {
					// touch the bytes — must not race with writers
					for _, v := range snap.Values {
						if len(v) > 0 {
							_ = v[0]
						}
					}
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				s.UpdateIP("a", "2.2.2.2")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < ops; j++ {
				_ = s.Devices()
			}
		}()
	}
	wg.Wait()
}
