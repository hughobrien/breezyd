// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// recordingClient implements DeviceClient for tests: it captures writes and
// optionally delegates reads.
type recordingClient struct {
	writes   [][]ParamWrite
	reads    func(context.Context, []ParamID) (map[ParamID][]byte, error)
	writeErr error
}

func (r *recordingClient) ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
	if r.reads == nil {
		return map[ParamID][]byte{}, nil
	}
	return r.reads(ctx, ids)
}
func (r *recordingClient) WriteParams(ctx context.Context, ws []ParamWrite) error {
	r.writes = append(r.writes, ws)
	return r.writeErr
}

func TestOps_Power(t *testing.T) {
	c := &recordingClient{}
	if err := Power(context.Background(), c, true); err != nil {
		t.Fatalf("Power(true): %v", err)
	}
	want := []ParamWrite{{ID: 0x0001, Value: []byte{1}}}
	if !reflect.DeepEqual(c.writes[0], want) {
		t.Errorf("got %v, want %v", c.writes[0], want)
	}

	c = &recordingClient{}
	if err := Power(context.Background(), c, false); err != nil {
		t.Fatalf("Power(false): %v", err)
	}
	if c.writes[0][0].Value[0] != 0 {
		t.Errorf("Power(false): want value 0, got %d", c.writes[0][0].Value[0])
	}
}

func TestOps_SetSpeedPreset(t *testing.T) {
	for _, preset := range []int{1, 2, 3} {
		c := &recordingClient{}
		if err := SetSpeedPreset(context.Background(), c, preset); err != nil {
			t.Errorf("SetSpeedPreset(%d): %v", preset, err)
			continue
		}
		got := c.writes[0][0].Value[0]
		if int(got) != preset {
			t.Errorf("preset %d: wrote 0x%02x, want 0x%02x", preset, got, preset)
		}
	}
	for _, bad := range []int{0, 4, -1, 255} {
		c := &recordingClient{}
		err := SetSpeedPreset(context.Background(), c, bad)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("preset %d: expected ErrInvalidArg, got %v", bad, err)
		}
		if len(c.writes) != 0 {
			t.Errorf("preset %d: should not have issued any writes", bad)
		}
	}
}

func TestOps_SetSpeedManual_PacketOrder(t *testing.T) {
	c := &recordingClient{}
	if err := SetSpeedManual(context.Background(), c, 30); err != nil {
		t.Fatalf("SetSpeedManual(30): %v", err)
	}
	if len(c.writes) != 1 {
		t.Fatalf("expected 1 packet, got %d", len(c.writes))
	}
	pkt := c.writes[0]
	if len(pkt) != 2 {
		t.Fatalf("expected 2 writes in packet, got %d", len(pkt))
	}
	if pkt[0].ID != 0x0044 {
		t.Errorf("first write must be 0x0044 (manual_pct), got 0x%04X", uint16(pkt[0].ID))
	}
	if pkt[0].Value[0] != 30 {
		t.Errorf("manual_pct value: want 30, got %d", pkt[0].Value[0])
	}
	if pkt[1].ID != 0x0002 {
		t.Errorf("second write must be 0x0002 (speed_mode), got 0x%04X", uint16(pkt[1].ID))
	}
	if pkt[1].Value[0] != 0xFF {
		t.Errorf("speed_mode value: want 0xFF (manual flag), got 0x%02X", pkt[1].Value[0])
	}
}

func TestOps_SetSpeedManual_RangeReject(t *testing.T) {
	for _, bad := range []int{0, 5, 9, 101, 200} {
		c := &recordingClient{}
		err := SetSpeedManual(context.Background(), c, bad)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("manual %d: expected ErrInvalidArg, got %v", bad, err)
		}
		if len(c.writes) != 0 {
			t.Errorf("manual %d: should not have issued any writes", bad)
		}
	}
}

func TestOps_SetMode(t *testing.T) {
	cases := map[string]byte{
		"ventilation":  0,
		"regeneration": 1,
		"supply":       2,
		"extract":      3,
		"VENTILATION":  0,
		"Regeneration": 1,
	}
	for in, want := range cases {
		c := &recordingClient{}
		if err := SetMode(context.Background(), c, in); err != nil {
			t.Errorf("SetMode(%q): %v", in, err)
			continue
		}
		got := c.writes[0][0].Value[0]
		if got != want {
			t.Errorf("SetMode(%q): wrote 0x%02X, want 0x%02X", in, got, want)
		}
	}
	c := &recordingClient{}
	err := SetMode(context.Background(), c, "auto")
	if !errors.Is(err, ErrInvalidArg) {
		t.Errorf("SetMode(\"auto\"): expected ErrInvalidArg, got %v", err)
	}
}

func TestOps_SetHeater(t *testing.T) {
	c := &recordingClient{}
	if err := SetHeater(context.Background(), c, true); err != nil {
		t.Fatalf("SetHeater(true): %v", err)
	}
	if c.writes[0][0].ID != 0x0068 || c.writes[0][0].Value[0] != 1 {
		t.Errorf("SetHeater(true): unexpected write %+v", c.writes[0][0])
	}
	c2 := &recordingClient{}
	if err := SetHeater(context.Background(), c2, false); err != nil {
		t.Fatalf("SetHeater(false): %v", err)
	}
	if c2.writes[0][0].ID != 0x0068 || c2.writes[0][0].Value[0] != 0 {
		t.Errorf("SetHeater(false): unexpected write %+v", c2.writes[0][0])
	}
}

func TestOps_ResetFilter(t *testing.T) {
	c := &recordingClient{}
	if err := ResetFilter(context.Background(), c); err != nil {
		t.Fatalf("ResetFilter: %v", err)
	}
	if c.writes[0][0].ID != 0x0065 || c.writes[0][0].Value[0] != 1 {
		t.Errorf("ResetFilter: unexpected write %+v", c.writes[0][0])
	}
}

func TestOps_ResetFaults(t *testing.T) {
	c := &recordingClient{}
	if err := ResetFaults(context.Background(), c); err != nil {
		t.Fatalf("ResetFaults: %v", err)
	}
	if c.writes[0][0].ID != 0x0080 || c.writes[0][0].Value[0] != 1 {
		t.Errorf("ResetFaults: unexpected write %+v", c.writes[0][0])
	}
}

func TestOps_SetRTC(t *testing.T) {
	c := &recordingClient{}
	t0 := time.Date(2026, 5, 4, 10, 30, 45, 0, time.UTC) // Monday
	if err := SetRTC(context.Background(), c, t0); err != nil {
		t.Fatalf("SetRTC: %v", err)
	}
	if len(c.writes) != 1 || len(c.writes[0]) != 2 {
		t.Fatalf("expected one packet with two writes, got %v", c.writes)
	}
	pkt := c.writes[0]
	if pkt[0].ID != 0x006F {
		t.Errorf("first write must be 0x006F (rtc_time), got 0x%04X", uint16(pkt[0].ID))
	}
	if !reflect.DeepEqual(pkt[0].Value, []byte{45, 30, 10}) {
		t.Errorf("rtc_time bytes: want [45 30 10], got %v", pkt[0].Value)
	}
	if pkt[1].ID != 0x0070 {
		t.Errorf("second write must be 0x0070 (rtc_calendar), got 0x%04X", uint16(pkt[1].ID))
	}
	if !reflect.DeepEqual(pkt[1].Value, []byte{4, 1, 5, 26}) {
		t.Errorf("rtc_calendar bytes: want [4 1 5 26], got %v", pkt[1].Value)
	}
}

func TestOps_SetRTC_SundayDoW(t *testing.T) {
	c := &recordingClient{}
	t0 := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC) // Sunday
	if err := SetRTC(context.Background(), c, t0); err != nil {
		t.Fatalf("SetRTC: %v", err)
	}
	dow := c.writes[0][1].Value[1]
	if dow != 7 {
		t.Errorf("Sunday: want dow=7 (ISO), got %d", dow)
	}
}

func TestOps_SetRTC_YearOutOfRange(t *testing.T) {
	for _, year := range []int{1999, 2256, 1900} {
		c := &recordingClient{}
		t0 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		err := SetRTC(context.Background(), c, t0)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("year %d: expected ErrInvalidArg, got %v", year, err)
		}
		if len(c.writes) != 0 {
			t.Errorf("year %d: should not have issued any writes", year)
		}
	}
}
