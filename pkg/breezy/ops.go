// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DeviceClient is the minimal subset of *breezy.Client that pkg/breezy/ops
// requires. The concrete *breezy.Client satisfies it; tests, the
// daemon's recording wrapper, and the future standalone CLI backend
// all substitute their own implementations.
type DeviceClient interface {
	ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error)
	WriteParams(ctx context.Context, writes []ParamWrite) error
}

// ErrInvalidArg is the sentinel for ops that reject caller-supplied
// arguments before any UDP traffic. Callers can errors.Is against this
// to map to a "bad_request" HTTP status, CLI exit code 2, etc.
var ErrInvalidArg = errors.New("breezy: invalid argument")

// Power turns the device on or off (parameter 0x0001).
func Power(ctx context.Context, c DeviceClient, on bool) error {
	val := byte(0)
	if on {
		val = 1
	}
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0001, Value: []byte{val}}})
}

// SetSpeedPreset selects a numbered fan preset (1, 2, or 3) via 0x0002.
func SetSpeedPreset(ctx context.Context, c DeviceClient, preset int) error {
	if preset < 1 || preset > 3 {
		return fmt.Errorf("%w: preset must be 1-3, got %d", ErrInvalidArg, preset)
	}
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0002, Value: []byte{byte(preset)}}})
}

// SetSpeedManual sets manual fan speed to pct% (10..100) and switches
// the device into manual mode in a single packet. Order matters per the
// vendor manual: write 0x0044 (percentage) BEFORE 0x0002 (manual flag),
// so the firmware doesn't briefly interpret the flag against a stale
// value.
func SetSpeedManual(ctx context.Context, c DeviceClient, pct int) error {
	if pct < 10 || pct > 100 {
		return fmt.Errorf("%w: manual percent must be 10-100, got %d", ErrInvalidArg, pct)
	}
	return c.WriteParams(ctx, []ParamWrite{
		{ID: 0x0044, Value: []byte{byte(pct)}},
		{ID: 0x0002, Value: []byte{0xFF}},
	})
}

// SetMode sets the airflow mode via 0x00B7. Accepts case-insensitive
// "ventilation"/"regeneration"/"supply"/"extract".
func SetMode(ctx context.Context, c DeviceClient, mode string) error {
	var val byte
	switch strings.ToLower(mode) {
	case "ventilation":
		val = 0
	case "regeneration":
		val = 1
	case "supply":
		val = 2
	case "extract":
		val = 3
	default:
		return fmt.Errorf("%w: mode must be one of ventilation/regeneration/supply/extract, got %q", ErrInvalidArg, mode)
	}
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x00B7, Value: []byte{val}}})
}

// SetHeater toggles the auxiliary reheater (0x0068). The firmware may
// also activate the heater autonomously for frost protection; this op
// only controls the user-facing toggle.
func SetHeater(ctx context.Context, c DeviceClient, on bool) error {
	val := byte(0)
	if on {
		val = 1
	}
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0068, Value: []byte{val}}})
}

// ResetFilter writes 1 to 0x0065, resetting the filter-replacement
// countdown back to the configured filter_timeout_days.
func ResetFilter(ctx context.Context, c DeviceClient) error {
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0065, Value: []byte{1}}})
}

// ResetFaults writes 1 to 0x0080, clearing the active fault list.
func ResetFaults(ctx context.Context, c DeviceClient) error {
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0080, Value: []byte{1}}})
}

// SetRTC sets the device's wall clock and calendar from t. Writes
// 0x006F (time_of_day, [sec, min, hr]) and 0x0070 (date,
// [day, dow, month, year-2000]) in one packet. Day-of-week follows
// ISO-8601 (Monday=1, Sunday=7).
func SetRTC(ctx context.Context, c DeviceClient, t time.Time) error {
	year := t.Year() - 2000
	if year < 0 || year > 255 {
		return fmt.Errorf("%w: year %d is outside the RTC range 2000-2255", ErrInvalidArg, t.Year())
	}
	tv := TimeOfDayValue{Hour: uint8(t.Hour()), Minute: uint8(t.Minute()), Second: uint8(t.Second())}
	timeBytes, err := encodeValue(TypeTimeOfDay, tv)
	if err != nil {
		return fmt.Errorf("ops.SetRTC: encode time: %w", err)
	}
	dow := uint8(t.Weekday())
	if dow == 0 {
		dow = 7 // Sunday: time.Weekday returns 0; ISO calls it 7.
	}
	dv := DateValue{Day: uint8(t.Day()), DayOfWeek: dow, Month: uint8(t.Month()), Year: uint8(year)}
	dateBytes, err := encodeValue(TypeDate, dv)
	if err != nil {
		return fmt.Errorf("ops.SetRTC: encode date: %w", err)
	}
	return c.WriteParams(ctx, []ParamWrite{
		{ID: 0x006F, Value: timeBytes},
		{ID: 0x0070, Value: dateBytes},
	})
}
