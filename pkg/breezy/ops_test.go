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

func TestOps_SetTimer(t *testing.T) {
	cases := map[string]byte{
		"off":   0,
		"night": 1,
		"turbo": 2,
		"OFF":   0,
		"Turbo": 2,
	}
	for in, want := range cases {
		c := &recordingClient{}
		if err := SetTimer(context.Background(), c, in); err != nil {
			t.Errorf("SetTimer(%q): %v", in, err)
			continue
		}
		if len(c.writes) != 1 || len(c.writes[0]) != 1 {
			t.Errorf("SetTimer(%q): expected one packet with one write, got %v", in, c.writes)
			continue
		}
		got := c.writes[0][0]
		if got.ID != 0x0007 {
			t.Errorf("SetTimer(%q): wrote ID 0x%04X, want 0x0007", in, uint16(got.ID))
		}
		if got.Value[0] != want {
			t.Errorf("SetTimer(%q): wrote 0x%02X, want 0x%02X", in, got.Value[0], want)
		}
	}
	for _, bad := range []string{"sleep", "boost", "auto", ""} {
		c := &recordingClient{}
		err := SetTimer(context.Background(), c, bad)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("SetTimer(%q): expected ErrInvalidArg, got %v", bad, err)
		}
		if len(c.writes) != 0 {
			t.Errorf("SetTimer(%q): should not have issued any writes", bad)
		}
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

func TestOps_GetFirmware(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{
				0x0086: {1, 5, 0x0F, 0x05, 0xEA, 0x07}, // 1.05, 2026-05-15
			}, nil
		},
	}
	fw, err := GetFirmware(context.Background(), c)
	if err != nil {
		t.Fatalf("GetFirmware: %v", err)
	}
	if fw.Major != 1 || fw.Minor != 5 {
		t.Errorf("version: want 1.5, got %d.%d", fw.Major, fw.Minor)
	}
	wantDate := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	if !fw.Date.Equal(wantDate) {
		t.Errorf("date: want %v, got %v", wantDate, fw.Date)
	}
}

func TestOps_GetFirmware_Unsupported(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{}, nil
		},
	}
	_, err := GetFirmware(context.Background(), c)
	if err == nil {
		t.Fatal("expected error when 0x0086 is missing, got nil")
	}
}

func TestOps_GetEfficiency(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{0x0129: {72}}, nil
		},
	}
	got, err := GetEfficiency(context.Background(), c)
	if err != nil {
		t.Fatalf("GetEfficiency: %v", err)
	}
	if got != 72 {
		t.Errorf("efficiency: want 72, got %d", got)
	}
}

func TestOps_GetEfficiency_WrongSize(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{0x0129: {72, 0}}, nil // 2 bytes, wrong
		},
	}
	_, err := GetEfficiency(context.Background(), c)
	if err == nil {
		t.Fatal("expected error for wrong-sized 0x0129, got nil")
	}
}

func TestOps_GetFaults_PairsAndOddTrailing(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{
				// Three pairs (code, kind) + one odd trailing byte.
				0x007F: {17, 0, 22, 1, 99, 5, 0xAA},
			}, nil
		},
	}
	got, err := GetFaults(context.Background(), c)
	if err != nil {
		t.Fatalf("GetFaults: %v", err)
	}
	want := []FaultCode{
		{Code: 17, Kind: "alarm"},
		{Code: 22, Kind: "warning"},
		{Code: 99, Kind: "unknown(5)"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestOps_GetFaults_Empty(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{0x007F: {}}, nil
		},
	}
	got, err := GetFaults(context.Background(), c)
	if err != nil {
		t.Fatalf("GetFaults: %v", err)
	}
	if got == nil {
		t.Errorf("GetFaults must return [], not nil, on empty fault list")
	}
	if len(got) != 0 {
		t.Errorf("expected empty slice, got %v", got)
	}
}

func TestOps_GetFaults_Missing(t *testing.T) {
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{}, nil
		},
	}
	got, err := GetFaults(context.Background(), c)
	if err != nil {
		t.Fatalf("GetFaults: %v", err)
	}
	if got == nil {
		t.Errorf("GetFaults must return [], not nil, when 0x007F absent")
	}
}

func TestOps_GetStatus_RoundTrip(t *testing.T) {
	values := map[ParamID][]byte{
		0x0001: {1},
		0x0002: {0x01},
		0x00B7: {1},
		0x0025: {42},
	}
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			out := map[ParamID][]byte{}
			for _, id := range ids {
				if v, ok := values[id]; ok {
					out[id] = v
				}
			}
			return out, nil
		},
	}
	s, err := GetStatus(context.Background(), c, "playroom", "BREEZYID", "192.168.1.1")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if s.Name != "playroom" || s.ID != "BREEZYID" || s.IP != "192.168.1.1" {
		t.Errorf("identity fields wrong: %+v", s)
	}
	if s.Configured["power"] != true {
		t.Errorf("power: want true, got %v", s.Configured["power"])
	}
	if s.Configured["speed_mode"] != "preset1" {
		t.Errorf("speed_mode: want preset1, got %v", s.Configured["speed_mode"])
	}
	if s.Configured["airflow_mode"] != "regeneration" {
		t.Errorf("airflow_mode: want regeneration, got %v", s.Configured["airflow_mode"])
	}
	if s.Sensors["humidity_pct"] != 42 {
		t.Errorf("humidity_pct: want 42, got %v", s.Sensors["humidity_pct"])
	}
	if s.LastPoll != "" {
		t.Errorf("LastPoll must be empty in standalone path, got %q", s.LastPoll)
	}
}
