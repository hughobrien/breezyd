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

func TestOps_SetPresetSpeed(t *testing.T) {
	cases := []struct {
		preset            int
		supplyID, extID   ParamID
		supplyPct, extPct int
	}{
		{1, 0x003A, 0x003B, 30, 35},
		{2, 0x003C, 0x003D, 55, 60},
		{3, 0x003E, 0x003F, 100, 100},
	}
	for _, c := range cases {
		rc := &recordingClient{}
		if err := SetPresetSpeed(context.Background(), rc, c.preset, c.supplyPct, c.extPct); err != nil {
			t.Errorf("preset=%d: %v", c.preset, err)
			continue
		}
		if len(rc.writes) != 1 || len(rc.writes[0]) != 2 {
			t.Errorf("preset=%d: want one packet with two writes, got %v", c.preset, rc.writes)
			continue
		}
		w := rc.writes[0]
		if w[0].ID != c.supplyID || int(w[0].Value[0]) != c.supplyPct {
			t.Errorf("preset=%d: supply write = (0x%04X, %d), want (0x%04X, %d)",
				c.preset, uint16(w[0].ID), w[0].Value[0], uint16(c.supplyID), c.supplyPct)
		}
		if w[1].ID != c.extID || int(w[1].Value[0]) != c.extPct {
			t.Errorf("preset=%d: extract write = (0x%04X, %d), want (0x%04X, %d)",
				c.preset, uint16(w[1].ID), w[1].Value[0], uint16(c.extID), c.extPct)
		}
	}

	for _, bad := range []struct {
		preset, supply, extract int
		why                     string
	}{
		{0, 50, 50, "preset below range"},
		{4, 50, 50, "preset above range"},
		{1, 9, 50, "supply below range"},
		{1, 101, 50, "supply above range"},
		{1, 50, 9, "extract below range"},
		{1, 50, 101, "extract above range"},
	} {
		rc := &recordingClient{}
		err := SetPresetSpeed(context.Background(), rc, bad.preset, bad.supply, bad.extract)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("[%s] want ErrInvalidArg, got %v", bad.why, err)
		}
		if len(rc.writes) != 0 {
			t.Errorf("[%s] should not have issued writes", bad.why)
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

func TestOps_SetThreshold(t *testing.T) {
	type happy struct {
		kind  string
		value int
		id    ParamID
		bytes []byte
	}
	for _, c := range []happy{
		{"humidity", 60, 0x0019, []byte{60}},
		{"HUMIDITY", 40, 0x0019, []byte{40}},
		{"humidity", 80, 0x0019, []byte{80}},
		{"co2", 400, 0x001A, []byte{0x90, 0x01}},
		{"co2", 1500, 0x001A, []byte{0xDC, 0x05}},
		{"Co2", 2000, 0x001A, []byte{0xD0, 0x07}},
		{"voc", 50, 0x031F, []byte{0x32, 0x00}},
		{"voc", 250, 0x031F, []byte{0xFA, 0x00}},
		{"VOC", 175, 0x031F, []byte{0xAF, 0x00}},
	} {
		rc := &recordingClient{}
		if err := SetThreshold(context.Background(), rc, c.kind, c.value); err != nil {
			t.Errorf("SetThreshold(%q,%d): %v", c.kind, c.value, err)
			continue
		}
		if len(rc.writes) != 1 || len(rc.writes[0]) != 1 {
			t.Errorf("SetThreshold(%q,%d): want one packet with one write, got %v", c.kind, c.value, rc.writes)
			continue
		}
		got := rc.writes[0][0]
		if got.ID != c.id {
			t.Errorf("SetThreshold(%q,%d): wrote ID 0x%04X, want 0x%04X", c.kind, c.value, uint16(got.ID), uint16(c.id))
		}
		if !reflect.DeepEqual(got.Value, c.bytes) {
			t.Errorf("SetThreshold(%q,%d): wrote bytes %v, want %v", c.kind, c.value, got.Value, c.bytes)
		}
	}

	type bad struct {
		kind  string
		value int
		why   string
	}
	for _, c := range []bad{
		{"humidity", 39, "below range"},
		{"humidity", 81, "above range"},
		{"co2", 399, "below range"},
		{"co2", 2001, "above range"},
		{"co2", 1505, "not a multiple of 10"},
		{"voc", 49, "below range"},
		{"voc", 251, "above range"},
		{"unknown", 100, "unknown kind"},
		{"", 60, "empty kind"},
	} {
		rc := &recordingClient{}
		err := SetThreshold(context.Background(), rc, c.kind, c.value)
		if !errors.Is(err, ErrInvalidArg) {
			t.Errorf("SetThreshold(%q,%d) [%s]: want ErrInvalidArg, got %v", c.kind, c.value, c.why, err)
		}
		if len(rc.writes) != 0 {
			t.Errorf("SetThreshold(%q,%d) [%s]: should not have issued any writes", c.kind, c.value, c.why)
		}
	}
}

func TestSetThresholdConfig_ValueOnly(t *testing.T) {
	rec := &recordingClient{}
	v := 65
	if err := SetThresholdConfig(context.Background(), rec, "humidity", &v, nil); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rec.writes) != 1 || len(rec.writes[0]) != 1 ||
		rec.writes[0][0].ID != 0x0019 || rec.writes[0][0].Value[0] != 65 {
		t.Errorf("writes = %+v; want one packet with one write to 0x0019 byte 65", rec.writes)
	}
}

func TestSetThresholdConfig_EnabledOnly(t *testing.T) {
	rec := &recordingClient{}
	enable := false
	if err := SetThresholdConfig(context.Background(), rec, "co2", nil, &enable); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rec.writes) != 1 || len(rec.writes[0]) != 1 ||
		rec.writes[0][0].ID != 0x0011 || rec.writes[0][0].Value[0] != 0 {
		t.Errorf("writes = %+v; want one packet with one write to 0x0011 byte 0", rec.writes)
	}
}

func TestSetThresholdConfig_Both(t *testing.T) {
	rec := &recordingClient{}
	v := 200
	enable := true
	if err := SetThresholdConfig(context.Background(), rec, "voc", &v, &enable); err != nil {
		t.Fatalf("err: %v", err)
	}
	// One WriteParams call carrying both writes (atomic from device's POV).
	if len(rec.writes) != 1 {
		t.Fatalf("got %d packets, want 1", len(rec.writes))
	}
	if len(rec.writes[0]) != 2 {
		t.Fatalf("got %d writes in packet, want 2", len(rec.writes[0]))
	}
	// Order is value-then-enable per implementation; just assert presence by ID.
	idsSeen := map[ParamID]bool{}
	for _, w := range rec.writes[0] {
		idsSeen[w.ID] = true
	}
	if !idsSeen[0x031F] || !idsSeen[0x0315] {
		t.Errorf("writes = %+v; want both 0x031F and 0x0315", rec.writes[0])
	}
}

func TestSetThresholdConfig_Neither(t *testing.T) {
	rec := &recordingClient{}
	if err := SetThresholdConfig(context.Background(), rec, "humidity", nil, nil); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("err = %v, want ErrInvalidArg", err)
	}
	if len(rec.writes) != 0 {
		t.Errorf("got %d writes after invalid-arg, want 0", len(rec.writes))
	}
}

func TestSetThresholdConfig_OutOfRange(t *testing.T) {
	rec := &recordingClient{}
	v := 90 // humidity max is 80
	if err := SetThresholdConfig(context.Background(), rec, "humidity", &v, nil); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("err = %v, want ErrInvalidArg", err)
	}
}

func TestSetThresholdConfig_UnknownKind(t *testing.T) {
	rec := &recordingClient{}
	v := 50
	if err := SetThresholdConfig(context.Background(), rec, "temperature", &v, nil); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("err = %v, want ErrInvalidArg", err)
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
