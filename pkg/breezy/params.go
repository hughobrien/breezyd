// This file defines the Breezy parameter registry: the static map from
// every documented parameter ID to its name, value type, unit, and
// capability flags, plus typed Decode/Encode helpers.
//
// The registry is the source of truth for the daemon's poller, the CLI's
// pretty-printer, and anything else that needs to understand parameter
// semantics. The cross-check test in params_test.go reads
// docs/superpowers/specs/2026-05-03-param-map.md and verifies that every
// row with a documented (bold-named) entry in that doc has a matching
// entry here. Update both files in lockstep.
package breezy

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ValueType identifies the on-the-wire encoding of a parameter's value.
// Decoders dispatch on this; encoders may refuse to encode types that are
// read-only or otherwise structurally awkward to round-trip.
type ValueType int

const (
	// TypeRaw is the catch-all for parameters whose contents we do not
	// model in detail (e.g. variable-length fault lists, schedule slots).
	TypeRaw ValueType = iota
	TypeUint8
	TypeUint16
	TypeInt16
	TypeIPv4
	TypeASCII
	// TypeTimeOfDay is a 3-byte [sec, min, hr] little-endian wall clock.
	TypeTimeOfDay
	// TypeDate is a 4-byte [day_of_month, day_of_week, month, year-2000].
	TypeDate
	// TypeDuration is a 2-byte [min, hr] LE timer-style duration.
	TypeDuration
	// TypeRemainingTime is a 4-byte [min, hr, day_lo, day_hi] LE odometer.
	TypeRemainingTime
	// TypeFirmwareMeta is a 6-byte firmware-version + build-date blob.
	TypeFirmwareMeta
	// TypeAlertBitmap is the 5-byte per-sensor over-threshold flag array.
	TypeAlertBitmap
	// TypeWriteOnly marks a "trigger" parameter — write 1 to execute, no
	// meaningful read value.
	TypeWriteOnly
)

// String returns a stable lowercase identifier for a value type, suitable
// for log/diagnostic output.
func (t ValueType) String() string {
	switch t {
	case TypeRaw:
		return "raw"
	case TypeUint8:
		return "uint8"
	case TypeUint16:
		return "uint16"
	case TypeInt16:
		return "int16"
	case TypeIPv4:
		return "ipv4"
	case TypeASCII:
		return "ascii"
	case TypeTimeOfDay:
		return "time_of_day"
	case TypeDate:
		return "date"
	case TypeDuration:
		return "duration"
	case TypeRemainingTime:
		return "remaining_time"
	case TypeFirmwareMeta:
		return "firmware_meta"
	case TypeAlertBitmap:
		return "alert_bitmap"
	case TypeWriteOnly:
		return "write_only"
	default:
		return fmt.Sprintf("ValueType(%d)", int(t))
	}
}

// Capabilities is a bitmask of operations the device supports for a
// parameter, mirroring the vendor manual's R/W/INC/DEC columns.
type Capabilities uint8

const (
	CapRead Capabilities = 1 << iota
	CapWrite
	CapInc
	CapDec
)

// Convenience combinations.
const (
	CapReadWrite Capabilities = CapRead | CapWrite
	CapAll       Capabilities = CapRead | CapWrite | CapInc | CapDec
)

func (c Capabilities) CanRead() bool  { return c&CapRead != 0 }
func (c Capabilities) CanWrite() bool { return c&CapWrite != 0 }
func (c Capabilities) CanInc() bool   { return c&CapInc != 0 }
func (c Capabilities) CanDec() bool   { return c&CapDec != 0 }

// Param describes a single parameter in the registry.
type Param struct {
	ID          ParamID
	Name        string
	Type        ValueType
	Unit        string
	Caps        Capabilities
	Description string
}

// Decode interprets raw little-endian bytes for this parameter according
// to its Type and returns a typed Value. It returns an error when the
// byte length doesn't match the expected size for the type.
func (p Param) Decode(raw []byte) (Value, error) {
	return decodeValue(p.Type, raw)
}

// Encode renders v back to little-endian wire bytes for this parameter.
// It returns an error when v's concrete type doesn't match the Param's
// declared Type or when the type isn't safely round-trippable (e.g.
// firmware metadata, raw, write-only).
func (p Param) Encode(v Value) ([]byte, error) {
	return encodeValue(p.Type, v)
}

// paramTable is the single source of truth for the registry. The
// cross-check test in params_test.go verifies it stays in sync with
// docs/superpowers/specs/2026-05-03-param-map.md — update both together.
var paramTable = []Param{
	// Page 0.
	{0x0001, "power", TypeUint8, "", CapAll, "Turn the unit on/off (0=off, 1=on, 2=invert)"},
	{0x0002, "speed_mode", TypeUint8, "", CapAll, "Speed preset 1-3 or 255=manual percentage mode"},
	{0x0007, "timer", TypeUint8, "", CapAll, "Active special-mode (0=off, 1=night, 2=turbo)"},
	{0x000B, "timer_countdown", TypeTimeOfDay, "", CapRead, "Time remaining in active special mode"},
	{0x000F, "humidity_sensor_control", TypeUint8, "", CapReadWrite, "Humidity sensor control (0=off, 1=on, 2=invert)"},
	{0x0011, "co2_sensor_control", TypeUint8, "", CapReadWrite, "CO2 sensor control (0=off, 1=on, 2=invert)"},
	{0x0019, "humidity_threshold", TypeUint8, "%", CapAll, "Humidity threshold setpoint (40-80 RH%)"},
	{0x001A, "co2_threshold", TypeUint16, "ppm", CapAll, "CO2 threshold setpoint (400-2000 ppm)"},
	{0x001F, "temp_outdoor", TypeInt16, "0.1°C", CapRead, "Outdoor air temperature"},
	{0x0020, "temp_supply", TypeInt16, "0.1°C", CapRead, "Supply air temperature, post-reheater"},
	{0x0021, "temp_exhaust_inlet", TypeInt16, "0.1°C", CapRead, "Exhaust air at inlet (room return)"},
	{0x0022, "temp_exhaust_outlet", TypeInt16, "0.1°C", CapRead, "Exhaust air at outlet (post-recovery)"},
	{0x0024, "rtc_battery_mv", TypeUint16, "mV", CapRead, "RTC backup battery voltage"},
	{0x0025, "humidity", TypeUint8, "%", CapRead, "Current room humidity"},
	{0x0027, "co2_or_eco2", TypeUint16, "ppm", CapRead, "Indoor CO2 (eCO2 derived from VOC sensor)"},
	{0x003A, "preset1_supply_pct", TypeUint8, "%", CapAll, "Supply fan speed % at preset 1 (10-100)"},
	{0x003B, "preset1_extract_pct", TypeUint8, "%", CapAll, "Extract fan speed % at preset 1"},
	{0x003C, "preset2_supply_pct", TypeUint8, "%", CapAll, "Supply fan speed % at preset 2"},
	{0x003D, "preset2_extract_pct", TypeUint8, "%", CapAll, "Extract fan speed % at preset 2"},
	{0x003E, "preset3_supply_pct", TypeUint8, "%", CapAll, "Supply fan speed % at preset 3"},
	{0x003F, "preset3_extract_pct", TypeUint8, "%", CapAll, "Extract fan speed % at preset 3"},
	{0x0044, "speed_manual_pct", TypeUint8, "%", CapAll, "Manual fan speed % (10-100, firmware floor 10)"},
	{0x004A, "fan_supply_rpm", TypeUint16, "rpm", CapRead, "Live supply-fan speed"},
	{0x004B, "fan_extract_rpm", TypeUint16, "rpm", CapRead, "Live extract-fan speed"},
	{0x0063, "filter_timeout_days", TypeUint16, "days", CapAll, "Filter replacement interval (0 or 70-365 days)"},
	{0x0064, "filter_remaining", TypeRemainingTime, "", CapRead, "Filter-change countdown remaining"},
	{0x0065, "reset_filter_timer", TypeWriteOnly, "", CapWrite, "Reset filter countdown back to filter_timeout_days"},
	{0x0068, "heater_control", TypeUint8, "", CapReadWrite, "User toggle for the electric reheater (0/1/2)"},
	{0x006F, "rtc_time", TypeTimeOfDay, "", CapReadWrite, "Live wall clock"},
	{0x0070, "rtc_calendar", TypeDate, "", CapReadWrite, "Live calendar date"},
	{0x0072, "weekly_schedule_mode", TypeUint8, "", CapReadWrite, "Weekly schedule on/off/invert"},
	{0x0077, "schedule_settings", TypeRaw, "", CapReadWrite, "Per-day-per-period schedule entry (special access)"},
	{0x007C, "device_id_search", TypeASCII, "", CapRead, "16-char device ID"},
	{0x007D, "protocol_password", TypeASCII, "", CapReadWrite, "Device protocol password (leaks plaintext)"},
	{0x007E, "motor_running_hours", TypeRemainingTime, "", CapRead, "Lifetime motor operation odometer"},
	{0x007F, "fault_warning_list", TypeRaw, "", CapRead, "Variable-length list of active faults/warnings"},
	{0x0080, "reset_faults", TypeWriteOnly, "", CapWrite, "Clear current faults/warnings"},
	{0x0081, "heater_running", TypeUint8, "", CapRead, "Heater currently energized (may be on for frost protection)"},
	{0x0083, "fault_indicator", TypeUint8, "", CapRead, "Top-level severity (0=none, 1=alarm, 2=warning)"},
	{0x0084, "air_quality_status", TypeAlertBitmap, "", CapRead, "Per-sensor over-threshold flags"},
	{0x0085, "cloud_mgmt_allowed", TypeUint8, "", CapReadWrite, "Allow management via vendor cloud (0/1/2)"},
	{0x0086, "firmware_metadata", TypeFirmwareMeta, "", CapRead, "Firmware version and build date"},
	{0x0087, "factory_reset_all", TypeWriteOnly, "", CapWrite, "Reset ALL parameters to factory defaults"},
	{0x0088, "filter_status", TypeUint8, "", CapRead, "Filter clean/soiled flag"},
	{0x0094, "wifi_operating_mode", TypeUint8, "", CapAll, "WiFi mode (1=client, 2=AP)"},
	{0x0095, "wifi_ssid_client", TypeASCII, "", CapReadWrite, "WiFi SSID for client mode (leaks plaintext)"},
	{0x0096, "wifi_password_client", TypeASCII, "", CapReadWrite, "WiFi WPA passphrase for client mode (leaks plaintext)"},
	{0x0099, "wifi_encryption_type", TypeUint8, "", CapReadWrite, "WiFi encryption mode (48=open, 50/51/52=WPA variants)"},
	{0x009A, "wifi_channel", TypeUint8, "", CapAll, "Configured WiFi channel for AP mode (1-13)"},
	{0x009B, "wifi_dhcp_mode", TypeUint8, "", CapReadWrite, "0=static, 1=DHCP, 2=invert"},
	{0x009C, "wifi_static_ip", TypeIPv4, "", CapReadWrite, "Static IP (used when wifi_dhcp_mode=0)"},
	{0x009D, "wifi_static_subnet", TypeIPv4, "", CapReadWrite, "Static subnet mask"},
	{0x009E, "wifi_static_gateway", TypeIPv4, "", CapReadWrite, "Static default gateway"},
	{0x00A0, "wifi_apply_settings", TypeWriteOnly, "", CapWrite, "Commit pending WiFi changes and exit setup mode"},
	{0x00A2, "wifi_cancel_setup", TypeWriteOnly, "", CapWrite, "Discard pending WiFi changes and exit setup mode"},
	{0x00A3, "wifi_active_ip", TypeIPv4, "", CapRead, "Currently active IP on the network"},
	{0x00B7, "fan_rotation_direction", TypeUint8, "", CapAll, "Airflow mode (0=ventilation, 1=regen, 2=supply, 3=extract)"},
	{0x00B9, "device_type", TypeUint16, "", CapRead, "Device type (17=Breezy 160, 20/22/24=other variants)"},

	// Page 0x01.
	{0x0129, "recovery_efficiency", TypeUint8, "%", CapRead, "Heat-recovery efficiency (0-100%)"},
	{0x012A, "factory_reset_fan_speeds", TypeWriteOnly, "", CapWrite, "Reset all fan-speed settings to factory defaults"},

	// Page 0x03.
	{0x0302, "night_duration", TypeDuration, "", CapReadWrite, "Night-timer duration"},
	{0x0303, "turbo_duration", TypeDuration, "", CapReadWrite, "Turbo-timer duration"},
	{0x0306, "schedule_active_speed", TypeUint8, "", CapRead, "Speed level the schedule is currently calling for (0-3)"},
	{0x030B, "frost_protection_active", TypeUint8, "", CapRead, "Frost protection (0=inactive, 1=active)"},
	{0x0315, "voc_sensor_control", TypeUint8, "", CapReadWrite, "VOC sensor control (0=off, 1=on, 2=invert)"},
	{0x031F, "voc_threshold", TypeUint16, "", CapAll, "VOC index setpoint (50-250)"},
	{0x0320, "voc_index", TypeUint16, "", CapRead, "Live VOC index (0-500)"},

	// Page 0x04 (display config).
	{0x0400, "brightness_pct", TypeUint8, "%", CapAll, "Manual screen brightness (1-100)"},
	{0x0401, "sound_emitter_enabled", TypeUint8, "", CapReadWrite, "Front-panel beeper (0=off, 1=on, 2=invert)"},
	{0x0402, "brightness_mode", TypeUint8, "", CapReadWrite, "0=auto (light sensor), 1=manual, 2=invert"},
	{0x0403, "temp_on_screen_mode", TypeUint8, "", CapAll, "Temperature shown on-screen (0=alternating, 1=supply, 2=extract)"},
	{0x0404, "air_quality_on_screen_mode", TypeUint8, "", CapAll, "Air-quality reading shown on-screen (0=alternating, 1=CO2, 2=VOC)"},
	{0x0405, "digital_indicators_mode", TypeUint8, "", CapAll, "Digital area display (0=alternating, 1=time, 2=temp+humidity)"},
	{0x0406, "time_in_standby_enabled", TypeUint8, "", CapReadWrite, "Show clock when device is in standby (0=off, 1=on)"},
	{0x0407, "all_indication_mode", TypeUint8, "", CapReadWrite, "Display master switch (0=off, 1=on, 2=window via 0x0408/0x0409)"},
	{0x0408, "indication_off_window_start", TypeDuration, "hh:mm", CapReadWrite, "Display-off window start (when all_indication_mode=2)"},
	{0x0409, "indication_off_window_end", TypeDuration, "hh:mm", CapReadWrite, "Display-off window end"},
}

var (
	byID   = map[ParamID]Param{}
	byName = map[string]Param{}
)

func init() {
	for _, p := range paramTable {
		if existing, dup := byID[p.ID]; dup {
			panic(fmt.Sprintf("breezy: duplicate ParamID 0x%04X (%q vs %q)", p.ID, existing.Name, p.Name))
		}
		key := strings.ToLower(p.Name)
		if existing, dup := byName[key]; dup {
			panic(fmt.Sprintf("breezy: duplicate Param Name %q (0x%04X vs 0x%04X)", p.Name, existing.ID, p.ID))
		}
		byID[p.ID] = p
		byName[key] = p
	}
}

// LookupByID returns the Param registered for id, or zero+false if no
// such parameter is registered.
func LookupByID(id ParamID) (Param, bool) {
	p, ok := byID[id]
	return p, ok
}

// LookupByName returns the Param registered under name (case-insensitive),
// or zero+false if no such parameter is registered.
func LookupByName(name string) (Param, bool) {
	p, ok := byName[strings.ToLower(name)]
	return p, ok
}

// AllParams returns every registered Param sorted by ID. The slice is a
// freshly-allocated copy; callers may mutate it without affecting the
// registry.
func AllParams() []Param {
	out := make([]Param, len(paramTable))
	copy(out, paramTable)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Value is the typed result of decoding a parameter's bytes. Concrete
// implementations are defined below; callers can type-switch on the
// concrete type or simply call String() for a human-friendly form.
type Value interface {
	// String returns a human-readable rendering of the value. Implementations
	// avoid leaking raw bytes; for opaque blobs prefer RawValue.
	String() string
}

// Errors surfaced by Decode/Encode.
var (
	ErrSize         = errors.New("breezy: wrong byte length for parameter type")
	ErrCodecUnsupported = errors.New("breezy: codec not supported for this type")
	ErrTypeMismatch = errors.New("breezy: value type does not match parameter")
)

// Uint8Value is a single-byte unsigned integer.
type Uint8Value uint8

func (v Uint8Value) String() string { return fmt.Sprintf("%d", uint8(v)) }

// Uint16Value is a 2-byte unsigned little-endian integer.
type Uint16Value uint16

func (v Uint16Value) String() string { return fmt.Sprintf("%d", uint16(v)) }

// Int16Value is a 2-byte signed little-endian integer. The Breezy uses
// the sentinels -32768 ("no sensor") and +32767 ("short circuit") on
// temperature reads.
type Int16Value int16

func (v Int16Value) String() string {
	switch int16(v) {
	case -32768:
		return "no sensor"
	case 32767:
		return "short circuit"
	}
	return fmt.Sprintf("%d", int16(v))
}

// IPv4Value is a 4-byte IP address, big-endian on the wire (the device
// stores it dotted-quad order).
type IPv4Value [4]byte

func (v IPv4Value) String() string { return fmt.Sprintf("%d.%d.%d.%d", v[0], v[1], v[2], v[3]) }

// ASCIIValue is an ASCII string parameter (device ID, password, SSID).
type ASCIIValue string

func (v ASCIIValue) String() string { return string(v) }

// TimeOfDayValue is a 3-byte [sec, min, hr] LE wall clock.
type TimeOfDayValue struct {
	Hour   uint8
	Minute uint8
	Second uint8
}

func (v TimeOfDayValue) String() string {
	return fmt.Sprintf("%02d:%02d:%02d", v.Hour, v.Minute, v.Second)
}

// DateValue is a 4-byte [day, day_of_week, month, year-2000] LE date.
type DateValue struct {
	Day        uint8
	DayOfWeek  uint8 // 1..7
	Month      uint8 // 1..12
	Year       uint8 // year - 2000 (so 25 == 2025)
}

func (v DateValue) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", 2000+int(v.Year), v.Month, v.Day)
}

// DurationValue is a 2-byte [min, hr] LE timer duration.
type DurationValue struct {
	Hour   uint8
	Minute uint8
}

func (v DurationValue) String() string { return fmt.Sprintf("%02d:%02d", v.Hour, v.Minute) }

// RemainingTimeValue is a 4-byte [min, hr, day_lo, day_hi] LE odometer
// (filter remaining, motor running hours).
type RemainingTimeValue struct {
	Days    uint16
	Hours   uint8
	Minutes uint8
}

func (v RemainingTimeValue) String() string {
	return fmt.Sprintf("%dd %dh %dm", v.Days, v.Hours, v.Minutes)
}

// FirmwareMetaValue captures the 6-byte firmware metadata: major.minor
// version plus build date.
type FirmwareMetaValue struct {
	Major uint8
	Minor uint8
	Date  time.Time // built date at midnight UTC
}

func (v FirmwareMetaValue) String() string {
	return fmt.Sprintf("%d.%d (%s)", v.Major, v.Minor, v.Date.Format("2006-01-02"))
}

// AlertBitmapValue is the 5-byte per-sensor over-threshold bitmap. Bytes
// 2 and 3 are reserved; the meaningful flags are at indices 0 (RH), 1
// (CO2), and 4 (VOC).
type AlertBitmapValue struct {
	RH  bool
	CO2 bool
	VOC bool
}

func (v AlertBitmapValue) String() string {
	flags := make([]string, 0, 3)
	if v.RH {
		flags = append(flags, "rh")
	}
	if v.CO2 {
		flags = append(flags, "co2")
	}
	if v.VOC {
		flags = append(flags, "voc")
	}
	if len(flags) == 0 {
		return "ok"
	}
	return strings.Join(flags, ",")
}

// RawValue is the catch-all for un-modeled byte blobs. Its String form
// is space-less hex.
type RawValue []byte

func (v RawValue) String() string {
	if len(v) == 0 {
		return ""
	}
	out := make([]byte, 0, len(v)*2)
	const hex = "0123456789abcdef"
	for _, b := range v {
		out = append(out, hex[b>>4], hex[b&0x0F])
	}
	return string(out)
}

// decodeValue is the dispatcher behind Param.Decode.
func decodeValue(t ValueType, raw []byte) (Value, error) {
	switch t {
	case TypeUint8:
		if len(raw) != 1 {
			return nil, fmt.Errorf("%w: uint8 expects 1 byte, got %d", ErrSize, len(raw))
		}
		return Uint8Value(raw[0]), nil

	case TypeUint16:
		if len(raw) != 2 {
			return nil, fmt.Errorf("%w: uint16 expects 2 bytes, got %d", ErrSize, len(raw))
		}
		return Uint16Value(binary.LittleEndian.Uint16(raw)), nil

	case TypeInt16:
		if len(raw) != 2 {
			return nil, fmt.Errorf("%w: int16 expects 2 bytes, got %d", ErrSize, len(raw))
		}
		return Int16Value(int16(binary.LittleEndian.Uint16(raw))), nil

	case TypeIPv4:
		if len(raw) != 4 {
			return nil, fmt.Errorf("%w: ipv4 expects 4 bytes, got %d", ErrSize, len(raw))
		}
		var ip IPv4Value
		copy(ip[:], raw)
		return ip, nil

	case TypeASCII:
		// Trim the device's NUL padding (it pads short strings to a fixed
		// slot in some firmwares). Empty strings are still legal.
		s := strings.TrimRight(string(raw), "\x00")
		return ASCIIValue(s), nil

	case TypeTimeOfDay:
		if len(raw) != 3 {
			return nil, fmt.Errorf("%w: time_of_day expects 3 bytes, got %d", ErrSize, len(raw))
		}
		return TimeOfDayValue{Second: raw[0], Minute: raw[1], Hour: raw[2]}, nil

	case TypeDate:
		if len(raw) != 4 {
			return nil, fmt.Errorf("%w: date expects 4 bytes, got %d", ErrSize, len(raw))
		}
		return DateValue{Day: raw[0], DayOfWeek: raw[1], Month: raw[2], Year: raw[3]}, nil

	case TypeDuration:
		if len(raw) != 2 {
			return nil, fmt.Errorf("%w: duration expects 2 bytes, got %d", ErrSize, len(raw))
		}
		return DurationValue{Minute: raw[0], Hour: raw[1]}, nil

	case TypeRemainingTime:
		if len(raw) != 4 {
			return nil, fmt.Errorf("%w: remaining_time expects 4 bytes, got %d", ErrSize, len(raw))
		}
		days := uint16(raw[2]) | uint16(raw[3])<<8
		return RemainingTimeValue{Minutes: raw[0], Hours: raw[1], Days: days}, nil

	case TypeFirmwareMeta:
		if len(raw) != 6 {
			return nil, fmt.Errorf("%w: firmware_meta expects 6 bytes, got %d", ErrSize, len(raw))
		}
		year := int(uint16(raw[4]) | uint16(raw[5])<<8)
		date := time.Date(year, time.Month(raw[3]), int(raw[2]), 0, 0, 0, 0, time.UTC)
		return FirmwareMetaValue{Major: raw[0], Minor: raw[1], Date: date}, nil

	case TypeAlertBitmap:
		if len(raw) != 5 {
			return nil, fmt.Errorf("%w: alert_bitmap expects 5 bytes, got %d", ErrSize, len(raw))
		}
		return AlertBitmapValue{
			RH:  raw[0] != 0,
			CO2: raw[1] != 0,
			VOC: raw[4] != 0,
		}, nil

	case TypeWriteOnly:
		// A read of a write-only parameter is meaningless, but if we got
		// bytes we hand them back as raw rather than panicking.
		out := make(RawValue, len(raw))
		copy(out, raw)
		return out, nil

	case TypeRaw:
		out := make(RawValue, len(raw))
		copy(out, raw)
		return out, nil
	}
	return nil, fmt.Errorf("%w: unknown ValueType %d", ErrCodecUnsupported, int(t))
}

// encodeValue is the dispatcher behind Param.Encode.
func encodeValue(t ValueType, v Value) ([]byte, error) {
	switch t {
	case TypeUint8:
		u, ok := v.(Uint8Value)
		if !ok {
			return nil, fmt.Errorf("%w: want Uint8Value, got %T", ErrTypeMismatch, v)
		}
		return []byte{byte(u)}, nil

	case TypeUint16:
		u, ok := v.(Uint16Value)
		if !ok {
			return nil, fmt.Errorf("%w: want Uint16Value, got %T", ErrTypeMismatch, v)
		}
		out := make([]byte, 2)
		binary.LittleEndian.PutUint16(out, uint16(u))
		return out, nil

	case TypeInt16:
		s, ok := v.(Int16Value)
		if !ok {
			return nil, fmt.Errorf("%w: want Int16Value, got %T", ErrTypeMismatch, v)
		}
		out := make([]byte, 2)
		binary.LittleEndian.PutUint16(out, uint16(int16(s)))
		return out, nil

	case TypeIPv4:
		ip, ok := v.(IPv4Value)
		if !ok {
			return nil, fmt.Errorf("%w: want IPv4Value, got %T", ErrTypeMismatch, v)
		}
		out := make([]byte, 4)
		copy(out, ip[:])
		return out, nil

	case TypeASCII:
		s, ok := v.(ASCIIValue)
		if !ok {
			return nil, fmt.Errorf("%w: want ASCIIValue, got %T", ErrTypeMismatch, v)
		}
		return []byte(s), nil

	case TypeDuration:
		d, ok := v.(DurationValue)
		if !ok {
			return nil, fmt.Errorf("%w: want DurationValue, got %T", ErrTypeMismatch, v)
		}
		return []byte{d.Minute, d.Hour}, nil

	case TypeTimeOfDay, TypeDate, TypeRemainingTime, TypeFirmwareMeta,
		TypeAlertBitmap, TypeWriteOnly, TypeRaw:
		// These types are either read-only on the wire, structurally
		// awkward to round-trip, or require the caller to construct raw
		// bytes deliberately. Refuse here so a confused caller can't
		// silently corrupt device state.
		return nil, fmt.Errorf("%w: %s", ErrCodecUnsupported, t.String())
	}
	return nil, fmt.Errorf("%w: unknown ValueType %d", ErrCodecUnsupported, int(t))
}
