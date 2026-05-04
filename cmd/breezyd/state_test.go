package main

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/hughobrien/twinfresh/pkg/breezy"
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
