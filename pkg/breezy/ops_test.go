// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/matryer/is"
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
func (r *recordingClient) IsLocal() bool { return false }

func TestOps_Power(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	is.NoErr(Power(context.Background(), c, true))
	want := []ParamWrite{{ID: 0x0001, Value: []byte{1}}}
	is.Equal(c.writes[0], want) // Power(true): single write, no cascade

	c = &recordingClient{}
	is.NoErr(Power(context.Background(), c, false))
	is.Equal(c.writes[0][0].Value[0], byte(0)) // Power(false) writes value 0
}

// TestPowerOff_ClearsTimer verifies the server-side mirror of the
// firmware invariant probed 2026-05-12: writing 0x0001=0 also clears
// 0x0007 (timer) at the firmware level. Power(false) emits both writes
// in one packet so the daemon's cache and MemClient stay coherent
// without waiting for the next poll. Power(true) does NOT emit a
// timer-clear (probe showed no firmware coupling in that direction).
func TestPowerOff_ClearsTimer(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	is.NoErr(Power(context.Background(), c, false))
	is.Equal(len(c.writes), 1) // one atomic packet
	pkt := c.writes[0]
	sawPowerOff := false
	sawTimerClear := false
	for _, w := range pkt {
		switch w.ID {
		case ParamID(0x0001):
			is.Equal(w.Value, []byte{0})
			sawPowerOff = true
		case ParamID(0x0007):
			is.Equal(w.Value, []byte{0})
			sawTimerClear = true
		}
	}
	is.True(sawPowerOff)   // 0x0001=0 must be in the packet
	is.True(sawTimerClear) // 0x0007=0 must be alongside it
}

// TestPowerOn_NoTimerClear locks the asymmetry: powering on does NOT
// touch 0x0007 (firmware probe found no coupling). Without this pin,
// a careless extension of Power could silently start clearing timers
// on every power-on, which would surprise users who'd preset a timer
// just before flipping the unit on.
func TestPowerOn_NoTimerClear(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	is.NoErr(Power(context.Background(), c, true))
	pkt := c.writes[0]
	for _, w := range pkt {
		is.True(w.ID != ParamID(0x0007)) // Power(true) must NOT write 0x0007
	}
}

func TestOps_SetSpeedPreset(t *testing.T) {
	is := is.New(t)
	for _, preset := range []int{1, 2, 3} {
		c := &recordingClient{}
		is.NoErr(SetSpeedPreset(context.Background(), c, preset))
		is.Equal(len(c.writes), 1)    // one packet
		is.Equal(len(c.writes[0]), 2) // two writes: 0x0002 + 0x0007
		pkt := c.writes[0]
		is.Equal(pkt[0].ID, ParamID(0x0002))
		is.Equal(int(pkt[0].Value[0]), preset)
	}
	for _, bad := range []int{0, 4, -1, 255} {
		c := &recordingClient{}
		err := SetSpeedPreset(context.Background(), c, bad)
		is.True(errors.Is(err, ErrInvalidArg)) // bad preset must yield ErrInvalidArg
		is.Equal(len(c.writes), 0)             // bad preset should not have issued writes
	}
}

// TestSetSpeedPreset_ClearsTimer verifies the server-side mirror of the
// firmware's "any 0x0002 write also clears 0x0007" rule — SetSpeedPreset
// must write {0x0007, 0} alongside the preset selection so the daemon's
// cache and MemClient stay coherent without waiting for the next poll.
func TestSetSpeedPreset_ClearsTimer(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	is.NoErr(SetSpeedPreset(context.Background(), c, 2))
	is.Equal(len(c.writes), 1) // one atomic packet
	pkt := c.writes[0]
	sawTimerClear := false
	for _, w := range pkt {
		if w.ID == ParamID(0x0007) {
			is.Equal(w.Value, []byte{0}) // timer-clear must be 0
			sawTimerClear = true
		}
	}
	is.True(sawTimerClear) // 0x0007=0 must be present alongside the 0x0002 write
}

func TestOps_SetPresetSpeed(t *testing.T) {
	is := is.New(t)
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
		is.NoErr(SetPresetSpeed(context.Background(), rc, c.preset, c.supplyPct, c.extPct))
		is.Equal(len(rc.writes), 1)    // one packet
		is.Equal(len(rc.writes[0]), 2) // two writes in packet
		w := rc.writes[0]
		is.Equal(w[0].ID, c.supplyID)
		is.Equal(int(w[0].Value[0]), c.supplyPct)
		is.Equal(w[1].ID, c.extID)
		is.Equal(int(w[1].Value[0]), c.extPct)
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
		is.True(errors.Is(err, ErrInvalidArg)) // bad.why
		is.Equal(len(rc.writes), 0)            // bad.why: should not have issued writes
	}
}

func TestOps_SetSpeedManual_PacketOrder(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	is.NoErr(SetSpeedManual(context.Background(), c, 30))
	is.Equal(len(c.writes), 1) // one packet
	pkt := c.writes[0]
	is.Equal(len(pkt), 3)                // three writes in packet: 0x0044, 0x0002, 0x0007
	is.Equal(pkt[0].ID, ParamID(0x0044)) // first write must be 0x0044 (manual_pct)
	is.Equal(pkt[0].Value[0], byte(30))
	is.Equal(pkt[1].ID, ParamID(0x0002))  // second write must be 0x0002 (speed_mode)
	is.Equal(pkt[1].Value[0], byte(0xFF)) // 0xFF is the manual flag
	is.Equal(pkt[2].ID, ParamID(0x0007))  // third write must be 0x0007 (timer-clear)
	is.Equal(pkt[2].Value[0], byte(0))
}

// TestSetSpeedManual_ClearsTimer verifies the server-side mirror of the
// firmware's "any 0x0002 write also clears 0x0007" rule — SetSpeedManual
// must write {0x0007, 0} alongside the speed-mode flip so the daemon's
// cache and MemClient stay coherent without waiting for the next poll.
func TestSetSpeedManual_ClearsTimer(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	is.NoErr(SetSpeedManual(context.Background(), c, 45))
	is.Equal(len(c.writes), 1) // one atomic packet
	pkt := c.writes[0]
	sawTimerClear := false
	for _, w := range pkt {
		if w.ID == ParamID(0x0007) {
			is.Equal(w.Value, []byte{0}) // timer-clear must be 0
			sawTimerClear = true
		}
	}
	is.True(sawTimerClear) // 0x0007=0 must be present alongside the 0x0044+0x0002 writes
}

func TestOps_SetSpeedManual_RangeReject(t *testing.T) {
	is := is.New(t)
	for _, bad := range []int{0, 5, 9, 101, 200} {
		c := &recordingClient{}
		err := SetSpeedManual(context.Background(), c, bad)
		is.True(errors.Is(err, ErrInvalidArg)) // bad manual value must yield ErrInvalidArg
		is.Equal(len(c.writes), 0)             // bad manual value should not issue writes
	}
}

func TestOps_SetMode(t *testing.T) {
	is := is.New(t)
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
		is.NoErr(SetMode(context.Background(), c, in))
		is.Equal(c.writes[0][0].Value[0], want)
	}
	c := &recordingClient{}
	err := SetMode(context.Background(), c, "auto")
	is.True(errors.Is(err, ErrInvalidArg)) // "auto" is not a valid mode
}

func TestOps_SetHeater(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	is.NoErr(SetHeater(context.Background(), c, true))
	is.Equal(c.writes[0][0].ID, ParamID(0x0068))
	is.Equal(c.writes[0][0].Value[0], byte(1))
	c2 := &recordingClient{}
	is.NoErr(SetHeater(context.Background(), c2, false))
	is.Equal(c2.writes[0][0].ID, ParamID(0x0068))
	is.Equal(c2.writes[0][0].Value[0], byte(0))
}

func TestOps_SetTimer(t *testing.T) {
	is := is.New(t)
	cases := map[string]byte{
		"off":   0,
		"night": 1,
		"turbo": 2,
		"OFF":   0,
		"Turbo": 2,
	}
	for in, want := range cases {
		c := &recordingClient{}
		is.NoErr(SetTimer(context.Background(), c, in))
		is.Equal(len(c.writes), 1)    // one packet
		is.Equal(len(c.writes[0]), 1) // one write in packet
		got := c.writes[0][0]
		is.Equal(got.ID, ParamID(0x0007))
		is.Equal(got.Value[0], want)
	}
	for _, bad := range []string{"sleep", "boost", "auto", ""} {
		c := &recordingClient{}
		err := SetTimer(context.Background(), c, bad)
		is.True(errors.Is(err, ErrInvalidArg)) // bad timer value must yield ErrInvalidArg
		is.Equal(len(c.writes), 0)             // bad timer value should not issue writes
	}
}

func TestOps_SetThreshold(t *testing.T) {
	is := is.New(t)
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
		is.NoErr(SetThreshold(context.Background(), rc, c.kind, c.value))
		is.Equal(len(rc.writes), 1)    // one packet
		is.Equal(len(rc.writes[0]), 1) // one write in packet
		got := rc.writes[0][0]
		is.Equal(got.ID, c.id)
		is.Equal(got.Value, c.bytes)
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
		is.True(errors.Is(err, ErrInvalidArg)) // c.why
		is.Equal(len(rc.writes), 0)            // c.why: should not have issued writes
	}
}

func TestOps_SetTimerDuration(t *testing.T) {
	is := is.New(t)
	cases := []struct {
		mode    string
		hours   int
		minutes int
		wantID  ParamID
		wantVal []byte
	}{
		{"night", 1, 30, 0x0302, []byte{30, 1}},
		{"turbo", 0, 5, 0x0303, []byte{5, 0}},
		{"NIGHT", 8, 0, 0x0302, []byte{0, 8}},
		{"Turbo", 0, 1, 0x0303, []byte{1, 0}},
		{"night", 23, 59, 0x0302, []byte{59, 23}},
	}
	for _, tc := range cases {
		c := &recordingClient{}
		is.NoErr(SetTimerDuration(context.Background(), c, tc.mode, tc.hours, tc.minutes))
		is.Equal(len(c.writes), 1)    // one packet
		is.Equal(len(c.writes[0]), 1) // one write
		got := c.writes[0][0]
		is.Equal(got.ID, tc.wantID)
		is.Equal(got.Value, tc.wantVal)
	}
}

func TestOps_SetTimerDuration_RangeReject(t *testing.T) {
	is := is.New(t)
	bad := []struct {
		mode string
		h, m int
	}{
		{"night", -1, 0},
		{"night", 24, 0},
		{"night", 0, -1},
		{"night", 0, 60},
		{"night", 0, 0},
		{"turbo", 0, 0},
		{"sleep", 1, 0},
		{"", 1, 0},
	}
	for _, b := range bad {
		c := &recordingClient{}
		err := SetTimerDuration(context.Background(), c, b.mode, b.h, b.m)
		is.True(errors.Is(err, ErrInvalidArg)) // invalid duration must yield ErrInvalidArg
		is.Equal(len(c.writes), 0)             // no writes on invalid input
	}
}

func TestSetThresholdConfig_ValueOnly(t *testing.T) {
	is := is.New(t)
	rec := &recordingClient{}
	v := 65
	is.NoErr(SetThresholdConfig(context.Background(), rec, "humidity", &v, nil))
	is.Equal(len(rec.writes), 1)
	is.Equal(len(rec.writes[0]), 1)
	is.Equal(rec.writes[0][0].ID, ParamID(0x0019))
	is.Equal(rec.writes[0][0].Value[0], byte(65))
}

func TestSetThresholdConfig_EnabledOnly(t *testing.T) {
	is := is.New(t)
	rec := &recordingClient{}
	enable := false
	is.NoErr(SetThresholdConfig(context.Background(), rec, "co2", nil, &enable))
	is.Equal(len(rec.writes), 1)
	is.Equal(len(rec.writes[0]), 1)
	is.Equal(rec.writes[0][0].ID, ParamID(0x0011))
	is.Equal(rec.writes[0][0].Value[0], byte(0))
}

func TestSetThresholdConfig_Both(t *testing.T) {
	is := is.New(t)
	rec := &recordingClient{}
	v := 200
	enable := true
	is.NoErr(SetThresholdConfig(context.Background(), rec, "voc", &v, &enable))
	// One WriteParams call carrying both writes (atomic from device's POV).
	is.Equal(len(rec.writes), 1)
	is.Equal(len(rec.writes[0]), 2)
	// Order is value-then-enable per implementation; just assert presence by ID.
	idsSeen := map[ParamID]bool{}
	for _, w := range rec.writes[0] {
		idsSeen[w.ID] = true
	}
	is.True(idsSeen[0x031F]) // value write present
	is.True(idsSeen[0x0315]) // enable write present
}

func TestSetThresholdConfig_Neither(t *testing.T) {
	is := is.New(t)
	rec := &recordingClient{}
	err := SetThresholdConfig(context.Background(), rec, "humidity", nil, nil)
	is.True(errors.Is(err, ErrInvalidArg))
	is.Equal(len(rec.writes), 0)
}

func TestSetThresholdConfig_OutOfRange(t *testing.T) {
	is := is.New(t)
	rec := &recordingClient{}
	v := 90 // humidity max is 80
	err := SetThresholdConfig(context.Background(), rec, "humidity", &v, nil)
	is.True(errors.Is(err, ErrInvalidArg))
}

func TestSetThresholdConfig_UnknownKind(t *testing.T) {
	is := is.New(t)
	rec := &recordingClient{}
	v := 50
	err := SetThresholdConfig(context.Background(), rec, "temperature", &v, nil)
	is.True(errors.Is(err, ErrInvalidArg))
}

func TestOps_ResetFilter(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	is.NoErr(ResetFilter(context.Background(), c))
	is.Equal(c.writes[0][0].ID, ParamID(0x0065))
	is.Equal(c.writes[0][0].Value[0], byte(1))
}

func TestOps_ResetFaults(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	is.NoErr(ResetFaults(context.Background(), c))
	is.Equal(c.writes[0][0].ID, ParamID(0x0080))
	is.Equal(c.writes[0][0].Value[0], byte(1))
}

func TestOps_SetRTC(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	t0 := time.Date(2026, 5, 4, 10, 30, 45, 0, time.UTC) // Monday
	is.NoErr(SetRTC(context.Background(), c, t0))
	is.Equal(len(c.writes), 1)    // one packet
	is.Equal(len(c.writes[0]), 2) // two writes
	pkt := c.writes[0]
	is.Equal(pkt[0].ID, ParamID(0x006F)) // rtc_time
	is.Equal(pkt[0].Value, []byte{45, 30, 10})
	is.Equal(pkt[1].ID, ParamID(0x0070)) // rtc_calendar
	is.Equal(pkt[1].Value, []byte{4, 1, 5, 26})
}

func TestOps_SetRTC_SundayDoW(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{}
	t0 := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC) // Sunday
	is.NoErr(SetRTC(context.Background(), c, t0))
	dow := c.writes[0][1].Value[1]
	is.Equal(dow, byte(7)) // Sunday is ISO dow=7
}

func TestOps_SetRTC_YearOutOfRange(t *testing.T) {
	is := is.New(t)
	for _, year := range []int{1999, 2256, 1900} {
		c := &recordingClient{}
		t0 := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		err := SetRTC(context.Background(), c, t0)
		is.True(errors.Is(err, ErrInvalidArg)) // out-of-range year must yield ErrInvalidArg
		is.Equal(len(c.writes), 0)             // out-of-range year should not issue writes
	}
}

func TestOps_GetFirmware(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{
				0x0086: {1, 5, 0x0F, 0x05, 0xEA, 0x07}, // 1.05, 2026-05-15
			}, nil
		},
	}
	fw, err := GetFirmware(context.Background(), c)
	is.NoErr(err)
	is.Equal(fw.Major, uint8(1))
	is.Equal(fw.Minor, uint8(5))
	wantDate := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	is.True(fw.Date.Equal(wantDate)) // firmware date matches 2026-05-15
}

func TestOps_GetFirmware_Unsupported(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{}, nil
		},
	}
	_, err := GetFirmware(context.Background(), c)
	is.True(err != nil) // expected error when 0x0086 is missing
}

func TestOps_GetEfficiency(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{0x0129: {72}}, nil
		},
	}
	got, err := GetEfficiency(context.Background(), c)
	is.NoErr(err)
	is.Equal(got, 72)
}

func TestOps_GetEfficiency_WrongSize(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{0x0129: {72, 0}}, nil // 2 bytes, wrong
		},
	}
	_, err := GetEfficiency(context.Background(), c)
	is.True(err != nil) // expected error for wrong-sized 0x0129
}

func TestOps_GetFaults_PairsAndOddTrailing(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{
				// Three pairs (code, kind) + one odd trailing byte.
				0x007F: {17, 0, 22, 1, 99, 5, 0xAA},
			}, nil
		},
	}
	got, err := GetFaults(context.Background(), c)
	is.NoErr(err)
	want := []FaultCode{
		{Code: 17, Kind: "alarm"},
		{Code: 22, Kind: "warning"},
		{Code: 99, Kind: "unknown(5)"},
	}
	is.Equal(got, want)
}

func TestOps_GetFaults_Empty(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{0x007F: {}}, nil
		},
	}
	got, err := GetFaults(context.Background(), c)
	is.NoErr(err)
	is.True(got != nil) // GetFaults must return [], not nil, on empty fault list
	is.Equal(len(got), 0)
}

func TestOps_GetFaults_Missing(t *testing.T) {
	is := is.New(t)
	c := &recordingClient{
		reads: func(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
			return map[ParamID][]byte{}, nil
		},
	}
	got, err := GetFaults(context.Background(), c)
	is.NoErr(err)
	is.True(got != nil) // GetFaults must return [], not nil, when 0x007F absent
}

func TestOps_GetStatus_RoundTrip(t *testing.T) {
	is := is.New(t)
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
	is.NoErr(err)
	is.Equal(s.Name, "playroom")
	is.Equal(s.ID, "BREEZYID")
	is.Equal(s.IP, "192.168.1.1")
	is.Equal(s.Configured["power"], true)
	is.Equal(s.Configured["speed_mode"], "preset1")
	is.Equal(s.Configured["airflow_mode"], "regeneration")
	is.Equal(s.Sensors["humidity_pct"], 42)
	is.Equal(s.LastPoll, "") // LastPoll must be empty in standalone path
}
