// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

// TestRegistryMatchesMarkdown is the most important test in this file.
// It parses the param map markdown and asserts that every documented row
// (rows whose name is bold-marked, e.g. **power**) has a matching entry
// in the registry. Rows marked _undocumented_ are skipped intentionally
// — the registry deliberately does not expose those.
//
// If this fails, either the markdown grew a row the registry missed, or
// the registry has a name that drifted from the doc. Fix both files in
// lockstep.
func TestRegistryMatchesMarkdown(t *testing.T) {
	is := is.New(t)
	path := filepath.Join("..", "..", "docs", "superpowers", "specs", "2026-05-03-param-map.md")
	raw, err := os.ReadFile(path)
	is.NoErr(err) // read param map

	// Match table rows of the form:
	//   | 0xNN  | **name_with_underscores**  | ...
	// The regex deliberately rejects rows with underscored italics like
	// `_undocumented_` because those are NOT meant for the registry.
	re := regexp.MustCompile(`(?m)^\|\s*(0x[0-9A-Fa-f]+)\s*\|\s*\*\*([a-z][a-z0-9_]*)\*\*`)
	matches := re.FindAllStringSubmatch(string(raw), -1)
	is.True(len(matches) > 0) // no documented param rows matched in markdown — regex may need updating

	seen := make(map[ParamID]bool, len(matches))
	for _, m := range matches {
		idStr, name := m[1], m[2]
		idVal, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(idStr), "0x"), 16, 16)
		if err != nil {
			t.Errorf("bad id %q in markdown: %v", idStr, err)
			continue
		}
		id := ParamID(idVal)
		if seen[id] {
			t.Errorf("markdown lists id %s more than once", idStr)
			continue
		}
		seen[id] = true

		p, ok := LookupByID(id)
		if !ok {
			t.Errorf("markdown documents %s as %q but registry has no entry for that id", idStr, name)
			continue
		}
		if p.Name != name {
			t.Errorf("name mismatch for %s: markdown=%q registry=%q", idStr, name, p.Name)
		}
	}

	// Inverse direction: every registry entry should appear in the markdown.
	for _, p := range AllParams() {
		if !seen[p.ID] {
			t.Errorf("registry has 0x%04X (%q) but markdown has no documented row for it", uint16(p.ID), p.Name)
		}
	}

	t.Logf("cross-checked %d documented params against the registry", len(seen))
}

func TestLookupByID(t *testing.T) {
	is := is.New(t)
	p, ok := LookupByID(0x0001)
	is.True(ok) // 0x0001 (power) must be registered
	is.Equal(p.Name, "power")
	is.Equal(p.Type, TypeUint8)
	is.True(p.Caps.CanRead() && p.Caps.CanWrite() && p.Caps.CanInc() && p.Caps.CanDec()) // 0x0001 should be CapAll

	_, ok = LookupByID(0xDEAD)
	is.True(!ok) // LookupByID(0xDEAD) must not return ok for unregistered id
}

func TestLookupByName_CaseInsensitive(t *testing.T) {
	is := is.New(t)
	for _, name := range []string{"power", "POWER", "Power", "pOwEr"} {
		p, ok := LookupByName(name)
		is.True(ok) // case-insensitive lookup must find power
		is.Equal(p.ID, ParamID(0x0001))
	}

	_, ok := LookupByName("not_a_real_param")
	is.True(!ok) // LookupByName for unknown name must not return ok
}

func TestAllParams_SortedByID(t *testing.T) {
	is := is.New(t)
	all := AllParams()
	is.True(len(all) > 0) // AllParams returned empty slice
	for i := 1; i < len(all); i++ {
		is.True(all[i-1].ID < all[i].ID) // AllParams must be sorted by ID
	}

	// Verify it's a copy — mutating shouldn't affect subsequent calls.
	all[0].Name = "MUTATED"
	again := AllParams()
	is.True(again[0].Name != "MUTATED") // AllParams must return a copy, not the underlying slice
}

func TestCapabilities_Bits(t *testing.T) {
	is := is.New(t)
	is.True(CapRead.CanRead())   // CapRead.CanRead must be true
	is.True(!CapRead.CanWrite()) // CapRead.CanWrite must be false
	is.True(CapReadWrite.CanRead())
	is.True(CapReadWrite.CanWrite())
	all := CapAll
	is.True(all.CanRead() && all.CanWrite() && all.CanInc() && all.CanDec()) // CapAll must include every bit
	var none Capabilities
	is.True(!none.CanRead() && !none.CanWrite() && !none.CanInc() && !none.CanDec()) // zero Capabilities must report no capabilities
}

func TestWriteOnlyParams_HaveOnlyWrite(t *testing.T) {
	is := is.New(t)
	want := []string{
		"reset_filter_timer",
		"reset_faults",
		"factory_reset_all",
		"wifi_apply_settings",
		"wifi_cancel_setup",
	}
	for _, name := range want {
		p, ok := LookupByName(name)
		is.True(ok)                  // write-only param must be registered: name
		is.True(p.Caps&CapRead == 0) // write-only param must not have CapRead: name
		is.True(p.Caps.CanWrite())   // write-only param must have CapWrite: name
		is.Equal(p.Type, TypeWriteOnly)
	}
}

// --- Decode/Encode tests ---------------------------------------------------

func TestDecode_Uint8(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x0001)
	v, err := p.Decode([]byte{1})
	is.NoErr(err)
	u, ok := v.(Uint8Value)
	is.True(ok) // decode must return Uint8Value
	is.Equal(uint8(u), uint8(1))
	is.Equal(v.String(), "1")

	_, err = p.Decode([]byte{1, 2})
	is.True(errors.Is(err, ErrSize)) // wrong-size uint8 must yield ErrSize
}

func TestDecode_Uint16_DeviceType(t *testing.T) {
	is := is.New(t)
	// 0xB9 device_type, raw bytes 11 00 -> 17 (Breezy 160).
	p, ok := LookupByID(0x00B9)
	is.True(ok) // 0x00B9 (device_type) must be registered
	v, err := p.Decode([]byte{0x11, 0x00})
	is.NoErr(err)
	is.Equal(v.String(), "17")
}

func TestDecode_Uint8_RecoveryEfficiency(t *testing.T) {
	is := is.New(t)
	// 0x0129 recovery_efficiency, raw byte 0x5F -> 95.
	p, ok := LookupByID(0x0129)
	is.True(ok) // 0x0129 must be registered
	v, err := p.Decode([]byte{0x5F})
	is.NoErr(err)
	is.Equal(v.String(), "95")
}

func TestDecode_Uint16_VOCIndex(t *testing.T) {
	is := is.New(t)
	// 0x0320 voc_index, raw 5E 01 -> 350.
	p, ok := LookupByID(0x0320)
	is.True(ok) // 0x0320 must be registered
	v, err := p.Decode([]byte{0x5E, 0x01})
	is.NoErr(err)
	is.Equal(v.String(), "350")
}

func TestDecode_Int16_TempAndSentinels(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x001F) // temp_outdoor

	// Normal positive: 215 (= 21.5°C in 0.1°C units), bytes D7 00.
	v, err := p.Decode([]byte{0xD7, 0x00})
	is.NoErr(err)
	is.Equal(v.String(), "215")

	// Normal negative: -50 (= -5.0°C), bytes CE FF.
	v, err = p.Decode([]byte{0xCE, 0xFF})
	is.NoErr(err)
	is.Equal(v.String(), "-50")

	// "no sensor" sentinel = -32768, bytes 00 80.
	v, err = p.Decode([]byte{0x00, 0x80})
	is.NoErr(err)
	is.Equal(v.String(), "no sensor")

	// "short circuit" sentinel = +32767, bytes FF 7F.
	v, err = p.Decode([]byte{0xFF, 0x7F})
	is.NoErr(err)
	is.Equal(v.String(), "short circuit")
}

func TestDecode_IPv4(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x00A3) // wifi_active_ip
	v, err := p.Decode([]byte{192, 168, 1, 148})
	is.NoErr(err)
	is.Equal(v.String(), "192.168.1.148")

	_, err = p.Decode([]byte{1, 2, 3})
	is.True(errors.Is(err, ErrSize)) // wrong-size ipv4 must yield ErrSize
}

func TestDecode_ASCII_TrimsNul(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x007C) // device_id_search
	raw := append([]byte("BREEZY00000000A0"), 0x00, 0x00)
	v, err := p.Decode(raw)
	is.NoErr(err)
	is.Equal(v.String(), "BREEZY00000000A0")
}

func TestDecode_TimeOfDay(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x006F) // rtc_time
	// [sec=30, min=15, hr=14] -> 14:15:30
	v, err := p.Decode([]byte{30, 15, 14})
	is.NoErr(err)
	is.Equal(v.String(), "14:15:30")
}

func TestDecode_Date(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x0070) // rtc_calendar
	// [day=3, dow=6, month=5, year=26] -> 2026-05-03
	v, err := p.Decode([]byte{3, 6, 5, 26})
	is.NoErr(err)
	is.Equal(v.String(), "2026-05-03")
}

func TestDecode_Duration(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x0302) // night_duration
	// [min=0, hr=8] -> 08:00
	v, err := p.Decode([]byte{0x00, 0x08})
	is.NoErr(err)
	is.Equal(v.String(), "08:00")
}

func TestDecode_RemainingTime_FilterRemaining(t *testing.T) {
	is := is.New(t)
	// 0x64 filter_remaining, bytes 21 09 59 00:
	//   min=0x21=33, hr=0x09=9, day_lo=0x59=89, day_hi=0x00 -> 89 days
	p, _ := LookupByID(0x0064)
	v, err := p.Decode([]byte{0x21, 0x09, 0x59, 0x00})
	is.NoErr(err)
	is.Equal(v.String(), "89d 9h 33m")
}

func TestDecode_FirmwareMeta(t *testing.T) {
	is := is.New(t)
	// 0x86 firmware_metadata, bytes 00 0B 15 03 E9 07:
	//   major=0, minor=0x0B=11, day=0x15=21, month=0x03, year=0x07E9=2025.
	p, _ := LookupByID(0x0086)
	v, err := p.Decode([]byte{0x00, 0x0B, 0x15, 0x03, 0xE9, 0x07})
	is.NoErr(err)
	is.Equal(v.String(), "0.11 (2025-03-21)")
	fw, ok := v.(FirmwareMetaValue)
	is.True(ok) // decoded value must be FirmwareMetaValue
	wantDate := time.Date(2025, time.March, 21, 0, 0, 0, 0, time.UTC)
	is.True(fw.Date.Equal(wantDate)) // firmware date must equal 2025-03-21
}

func TestDecode_AlertBitmap(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x0084) // air_quality_status
	// All zero -> "ok".
	v, err := p.Decode([]byte{0, 0, 0, 0, 0})
	is.NoErr(err)
	is.Equal(v.String(), "ok")

	// RH and VOC over: bytes [1, 0, 0, 0, 1].
	v, err = p.Decode([]byte{1, 0, 0, 0, 1})
	is.NoErr(err)
	is.Equal(v.String(), "rh,voc")

	// All three set.
	v, err = p.Decode([]byte{1, 1, 0, 0, 1})
	is.NoErr(err)
	is.Equal(v.String(), "rh,co2,voc")
}

func TestDecode_Raw(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x007F) // fault_warning_list
	v, err := p.Decode([]byte{0xDE, 0xAD, 0xBE, 0xEF})
	is.NoErr(err)
	is.Equal(v.String(), "deadbeef")
}

// --- Encode round-trip tests ----------------------------------------------

func TestEncode_RoundTrip(t *testing.T) {
	cases := []struct {
		name string
		id   ParamID
		raw  []byte
	}{
		{"uint8", 0x0001, []byte{1}},
		{"uint16", 0x001A, []byte{0xD0, 0x07}}, // 2000 ppm
		{"int16", 0x001F, []byte{0xD7, 0x00}},  // 215
		{"int16-neg", 0x001F, []byte{0xCE, 0xFF}},
		{"ipv4", 0x00A3, []byte{192, 168, 1, 148}},
		{"ascii", 0x007D, []byte("testpwd")},
		{"duration", 0x0302, []byte{0x00, 0x08}},
		// Time-of-day at 0x006F (rtc_time): [sec, min, hr] = 30, 36, 22.
		{"time_of_day", 0x006F, []byte{0x1E, 0x24, 0x16}},
		// Date at 0x0070 (rtc_calendar): [day, dow, month, year-2000] = 3, 7, 5, 26.
		{"date", 0x0070, []byte{0x03, 0x07, 0x05, 0x1A}},
		// RemainingTime at 0x0064 (filter_remaining): [min, hr, day_lo, day_hi].
		{"remaining_time_small", 0x0064, []byte{0x1D, 0x0D, 0x59, 0x00}},
		{"remaining_time_big_days", 0x0064, []byte{0x00, 0x00, 0x00, 0x01}}, // 256 days
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			p, ok := LookupByID(tc.id)
			is.True(ok) // tc.id must be registered
			v, err := p.Decode(tc.raw)
			is.NoErr(err)
			out, err := p.Encode(v)
			is.NoErr(err)
			is.True(bytesEqual(out, tc.raw)) // round-trip must preserve bytes
		})
	}
}

func TestEncode_RefusesNonRoundTrippable(t *testing.T) {
	is := is.New(t)
	// firmware_metadata, alert_bitmap, write_only, raw should refuse to
	// encode for safety. (TimeOfDay/Date/RemainingTime are now supported
	// — see TestEncode_RoundTrip — because rtc_time/rtc_calendar are
	// CapReadWrite and the daemon's RTC handler needs a working encode.)
	cases := []struct {
		id ParamID
		v  Value
	}{
		{0x0086, FirmwareMetaValue{Major: 0, Minor: 11}},
		{0x0084, AlertBitmapValue{}},
		{0x0065, Uint8Value(1)}, // write-only param: not encodable via Encode (caller should use raw byte path)
		{0x007F, RawValue{0x01}},
	}
	for _, tc := range cases {
		p, ok := LookupByID(tc.id)
		is.True(ok) // tc.id must be registered
		_, err := p.Encode(tc.v)
		is.True(errors.Is(err, ErrCodecUnsupported)) // non-round-trippable encode must yield ErrCodecUnsupported
	}
}

func TestEncode_TypeMismatch(t *testing.T) {
	is := is.New(t)
	p, _ := LookupByID(0x0001) // uint8 power
	_, err := p.Encode(Uint16Value(1))
	is.True(errors.Is(err, ErrTypeMismatch))
}

func TestValueType_StringStable(t *testing.T) {
	// Make sure no two ValueTypes share a String() — useful when printing
	// a registry dump.
	seen := map[string]ValueType{}
	for t1 := ValueType(0); t1 <= TypeWriteOnly; t1++ {
		s := t1.String()
		if other, dup := seen[s]; dup {
			tFatal := s + " duplicated by " + ValueType(other).String()
			panic(tFatal)
		}
		seen[s] = t1
	}
}

// bytesEqual is a small helper to keep test output readable; we don't pull
// in reflect.DeepEqual because byte-slice diffs render poorly.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRegistry_NoStaleNames is a defensive sanity check: every registered
// name should be lowercase snake_case (so case-insensitive lookups behave
// predictably).
func TestRegistry_NoStaleNames(t *testing.T) {
	re := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	names := make([]string, 0, len(paramTable))
	for _, p := range paramTable {
		if !re.MatchString(p.Name) {
			t.Errorf("0x%04X has non-snake_case name %q", uint16(p.ID), p.Name)
		}
		names = append(names, p.Name)
	}
	sort.Strings(names)
	for i := 1; i < len(names); i++ {
		if names[i-1] == names[i] {
			t.Errorf("duplicate name %q in registry", names[i])
		}
	}
}
