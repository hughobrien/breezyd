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
// daemon's recording wrapper, and the CLI's standalone directBackend
// all substitute their own implementations.
type DeviceClient interface {
	ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error)
	WriteParams(ctx context.Context, writes []ParamWrite) error
	// IsLocal reports whether the client is in-process (no network I/O).
	// Used to gate UDP-protocol-specific behaviour like the fan-settle
	// suppression window in the poller. UDP *Client returns false;
	// *MemClient returns true.
	IsLocal() bool
}

// ErrInvalidArg is the sentinel for ops that reject caller-supplied
// arguments before any UDP traffic. Callers can errors.Is against this
// to map to a "bad_request" HTTP status, CLI exit code 2, etc.
var ErrInvalidArg = errors.New("breezy: invalid argument")

// Power turns the device on or off (parameter 0x0001).
//
// Powering off also writes 0x0007=0 (timer off) in the same packet. The
// firmware always clears the timer autonomously on a 1→0 power
// transition (probed 2026-05-12 against a Twinfresh Elite 160 running
// firmware 0.11) — encoding that invariant here keeps the daemon's
// cache coherent without waiting for the next poll, and matches the
// *MemClient* (in-process) backend's behavior so Playwright tests see
// the same effect as production.
//
// Powering on is unconditional — no implicit cascade. Activating a
// timer via SetTimer requires power=on as a prerequisite, but that
// coupling lives in the caller (the /timer handler) rather than here.
func Power(ctx context.Context, c DeviceClient, on bool) error {
	if on {
		return c.WriteParams(ctx, []ParamWrite{{ID: 0x0001, Value: []byte{1}}})
	}
	return c.WriteParams(ctx, []ParamWrite{
		{ID: 0x0001, Value: []byte{0}},
		{ID: 0x0007, Value: []byte{0}},
	})
}

// SetSpeedPreset selects a numbered fan preset (1, 2, or 3) via 0x0002.
//
// Also writes 0x0007=0 (special-mode/timer off) in the same packet. The
// firmware always clears 0x0007 autonomously on any 0x0002 write — we
// encode that invariant here so the daemon's cache (and any non-firmware
// backend like *MemClient) stays coherent without waiting for the next
// poll to catch up.
func SetSpeedPreset(ctx context.Context, c DeviceClient, preset int) error {
	if preset < 1 || preset > 3 {
		return fmt.Errorf("%w: preset must be 1-3, got %d", ErrInvalidArg, preset)
	}
	return c.WriteParams(ctx, []ParamWrite{
		{ID: 0x0002, Value: []byte{byte(preset)}},
		{ID: 0x0007, Value: []byte{0}},
	})
}

// SetPresetSpeed writes the per-preset supply and extract percentages
// for one of the three numbered presets. Each preset has its own pair of
// stored percentages (preset 1: 0x3A/0x3B, preset 2: 0x3C/0x3D, preset
// 3: 0x3E/0x3F). Both percentages are uint8, range 10..100.
//
// Editing the currently-active preset takes effect immediately on the
// running fan; editing an inactive preset only updates its stored
// configuration and takes effect when that preset is selected.
func SetPresetSpeed(ctx context.Context, c DeviceClient, preset, supply, extract int) error {
	if preset < 1 || preset > 3 {
		return fmt.Errorf("%w: preset must be 1-3, got %d", ErrInvalidArg, preset)
	}
	if supply < 10 || supply > 100 {
		return fmt.Errorf("%w: supply percent must be 10-100, got %d", ErrInvalidArg, supply)
	}
	if extract < 10 || extract > 100 {
		return fmt.Errorf("%w: extract percent must be 10-100, got %d", ErrInvalidArg, extract)
	}
	supplyID := ParamID(0x003A + (preset-1)*2)
	extractID := ParamID(0x003B + (preset-1)*2)
	return c.WriteParams(ctx, []ParamWrite{
		{ID: supplyID, Value: []byte{byte(supply)}},
		{ID: extractID, Value: []byte{byte(extract)}},
	})
}

// SetSpeedManual sets manual fan speed to pct% (10..100) and switches
// the device into manual mode in a single packet. Order matters per the
// vendor manual: write 0x0044 (percentage) BEFORE 0x0002 (manual flag),
// so the firmware doesn't briefly interpret the flag against a stale
// value.
//
// Also writes 0x0007=0 (special-mode/timer off) in the same packet. The
// firmware always clears 0x0007 autonomously on any 0x0002 write — we
// encode that invariant here so the daemon's cache (and any non-firmware
// backend like *MemClient) stays coherent without waiting for the next
// poll to catch up.
func SetSpeedManual(ctx context.Context, c DeviceClient, pct int) error {
	if pct < 10 || pct > 100 {
		return fmt.Errorf("%w: manual percent must be 10-100, got %d", ErrInvalidArg, pct)
	}
	return c.WriteParams(ctx, []ParamWrite{
		{ID: 0x0044, Value: []byte{byte(pct)}},
		{ID: 0x0002, Value: []byte{0xFF}},
		{ID: 0x0007, Value: []byte{0}},
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

// SetTimer activates one of the special-mode timers (parameter 0x0007):
// "off" stops any active timer, "night" enters the configured-duration
// quiet/low-fan mode, "turbo" enters the configured-duration boost mode.
// The active duration is the device-side configured value (0x0302/0x0303);
// this op only flips the mode byte. Mode strings are case-insensitive.
func SetTimer(ctx context.Context, c DeviceClient, mode string) error {
	var val byte
	switch strings.ToLower(mode) {
	case "off":
		val = 0
	case "night":
		val = 1
	case "turbo":
		val = 2
	default:
		return fmt.Errorf("%w: timer mode must be one of off/night/turbo, got %q", ErrInvalidArg, mode)
	}
	return c.WriteParams(ctx, []ParamWrite{{ID: 0x0007, Value: []byte{val}}})
}

// SetThresholdConfig writes one or both of: the per-sensor over-threshold
// setpoint and the per-sensor enable flag (the firmware's "trigger fan
// boost when this sensor is over its threshold"). At least one of value
// and enabled must be non-nil; otherwise ErrInvalidArg with no write.
// Both writes (when supplied) land in a single WriteParams call so the
// device sees them atomically.
//
// Kinds (case-insensitive):
//   - "humidity": value 40..80 RH%; enable flag at 0x000F
//   - "co2":      value 400..2000 ppm step 10; enable flag at 0x0011
//   - "voc":      value 50..250 index; enable flag at 0x0315
//
// Out-of-range values and unknown kinds return ErrInvalidArg with no write.
func SetThresholdConfig(ctx context.Context, c DeviceClient, kind string, value *int, enabled *bool) error {
	if value == nil && enabled == nil {
		return fmt.Errorf("%w: at least one of value or enabled must be supplied", ErrInvalidArg)
	}
	enableByte := func(b bool) byte {
		if b {
			return 1
		}
		return 0
	}
	var writes []ParamWrite
	switch strings.ToLower(kind) {
	case "humidity":
		if value != nil {
			if *value < 40 || *value > 80 {
				return fmt.Errorf("%w: humidity threshold must be 40..80, got %d", ErrInvalidArg, *value)
			}
			writes = append(writes, ParamWrite{ID: 0x0019, Value: []byte{byte(*value)}})
		}
		if enabled != nil {
			writes = append(writes, ParamWrite{ID: 0x000F, Value: []byte{enableByte(*enabled)}})
		}
	case "co2":
		if value != nil {
			if *value < 400 || *value > 2000 {
				return fmt.Errorf("%w: co2 threshold must be 400..2000, got %d", ErrInvalidArg, *value)
			}
			if *value%10 != 0 {
				return fmt.Errorf("%w: co2 threshold must be a multiple of 10, got %d", ErrInvalidArg, *value)
			}
			writes = append(writes, ParamWrite{ID: 0x001A, Value: []byte{byte(*value), byte(*value >> 8)}})
		}
		if enabled != nil {
			writes = append(writes, ParamWrite{ID: 0x0011, Value: []byte{enableByte(*enabled)}})
		}
	case "voc":
		if value != nil {
			if *value < 50 || *value > 250 {
				return fmt.Errorf("%w: voc threshold must be 50..250, got %d", ErrInvalidArg, *value)
			}
			writes = append(writes, ParamWrite{ID: 0x031F, Value: []byte{byte(*value), byte(*value >> 8)}})
		}
		if enabled != nil {
			writes = append(writes, ParamWrite{ID: 0x0315, Value: []byte{enableByte(*enabled)}})
		}
	default:
		return fmt.Errorf("%w: threshold kind must be one of humidity/co2/voc, got %q", ErrInvalidArg, kind)
	}
	return c.WriteParams(ctx, writes)
}

// SetThreshold writes only the per-sensor over-threshold setpoint. Kept
// as a one-line wrapper for callers that don't touch the enable flag.
func SetThreshold(ctx context.Context, c DeviceClient, kind string, value int) error {
	return SetThresholdConfig(ctx, c, kind, &value, nil)
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

// FaultCode is a single entry in the device's active fault list. Code is
// the raw fault number; Kind is "alarm" (level 0), "warning" (level 1),
// or "unknown(<n>)" for unrecognised severity bytes.
type FaultCode struct {
	Code int    `json:"code"`
	Kind string `json:"kind"`
}

// StatusParamIDs is the canonical set of parameter IDs that GetStatus
// reads in one batched ReadParams call. Mirrors the daemon poller's
// defaultReadIDs() in cmd/breezyd/main.go; keep them in sync until the
// daemon migrates to this constant.
//
// Do not mutate this slice. Callers that need a private copy should
// allocate one (e.g. append([]ParamID(nil), StatusParamIDs...)).
var StatusParamIDs = []ParamID{
	// Page 0 (most params).
	0x0001, 0x0002, 0x0007, 0x000B,
	0x000F, 0x0011, 0x0019, 0x001A,
	0x001F, 0x0020, 0x0021, 0x0022,
	0x0024, 0x0025, 0x0027,
	0x003A, 0x003B, 0x003C, 0x003D, 0x003E, 0x003F,
	0x0044, 0x004A, 0x004B,
	0x0063, 0x0064, 0x0068,
	0x007E, 0x007F, 0x0081, 0x0083, 0x0084, 0x0086, 0x0088,
	0x00B7, 0x00B9,
	// Page 1.
	0x0129,
	// Page 3.
	0x030B, 0x0315, 0x031F, 0x0320,
}

// GetFirmware reads 0x0086 and decodes it as a FirmwareMetaValue.
func GetFirmware(ctx context.Context, c DeviceClient) (FirmwareMetaValue, error) {
	out, err := c.ReadParams(ctx, []ParamID{0x0086})
	if err != nil {
		return FirmwareMetaValue{}, err
	}
	raw, ok := out[0x0086]
	if !ok {
		return FirmwareMetaValue{}, fmt.Errorf("ops.GetFirmware: device replied unsupported for 0x0086")
	}
	v, err := decodeValue(TypeFirmwareMeta, raw)
	if err != nil {
		return FirmwareMetaValue{}, fmt.Errorf("ops.GetFirmware: %w", err)
	}
	fv, ok := v.(FirmwareMetaValue)
	if !ok {
		return FirmwareMetaValue{}, fmt.Errorf("ops.GetFirmware: unexpected decoded type %T", v)
	}
	return fv, nil
}

// GetEfficiency reads 0x0129 and returns it as an int (0..100).
func GetEfficiency(ctx context.Context, c DeviceClient) (int, error) {
	out, err := c.ReadParams(ctx, []ParamID{0x0129})
	if err != nil {
		return 0, err
	}
	raw, ok := out[0x0129]
	if !ok || len(raw) != 1 {
		return 0, fmt.Errorf("ops.GetEfficiency: missing or wrong-sized 0x0129")
	}
	return int(raw[0]), nil
}

// GetFaults reads 0x007F and decodes pairs of (code, kind). An odd
// trailing byte is ignored (matches the daemon's existing parsing).
// Returns an empty slice (not nil) when the parameter is absent.
func GetFaults(ctx context.Context, c DeviceClient) ([]FaultCode, error) {
	out, err := c.ReadParams(ctx, []ParamID{0x007F})
	if err != nil {
		return nil, err
	}
	faults := []FaultCode{}
	raw, ok := out[0x007F]
	if !ok {
		return faults, nil
	}
	for i := 0; i+1 < len(raw); i += 2 {
		var kind string
		switch raw[i+1] {
		case 0:
			kind = "alarm"
		case 1:
			kind = "warning"
		default:
			kind = fmt.Sprintf("unknown(%d)", raw[i+1])
		}
		faults = append(faults, FaultCode{Code: int(raw[i]), Kind: kind})
	}
	return faults, nil
}

// GetStatus issues one batched ReadParams for the canonical status set
// and returns the decoded Status. lastPoll is nil — callers that want a
// timestamp (the daemon, building from a cached snapshot) should call
// BuildStatus directly with their own values + last-poll time.
func GetStatus(ctx context.Context, c DeviceClient, name, id, ip string) (Status, error) {
	values, err := c.ReadParams(ctx, StatusParamIDs)
	if err != nil {
		return Status{}, err
	}
	return BuildStatus(values, name, id, ip, nil), nil
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
