// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/matryer/is"
)

// ----- helpers -----

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex decode %q: %v", s, err)
	}
	return b
}

func sumChecksum(b []byte) uint16 {
	var s uint16
	for _, x := range b {
		s += uint16(x)
	}
	return s
}

// ----- EncodeRequest -----

func TestEncodeRequest_GoldenReadUnitType(t *testing.T) {
	is := is.New(t)
	// Read param 0xB9 (unit_type) from the playroom unit with default password.
	got := EncodeRequest("BREEZY00000000A0", "1111", FuncRead, []byte{0xB9})

	// Hand-computed expected wire bytes:
	//   FD FD 02 10 <16 ASCII id> 04 31 31 31 31 01 B9 <ck_lo> <ck_hi>
	want := mustHex(t, "fdfd0210425245455a5930303030303030304130043131313101b95605")
	is.Equal(got, want) // EncodeRequest must match the golden wire bytes

	// Independently verify the checksum: sum of bytes [2:-2] LE-stored at end.
	cs := sumChecksum(got[2 : len(got)-2])
	gotCS := uint16(got[len(got)-2]) | uint16(got[len(got)-1])<<8
	is.Equal(cs, gotCS) // checksum self-check
}

func TestEncodeRequest_EmptyPassword(t *testing.T) {
	is := is.New(t)
	got := EncodeRequest("BREEZY00000000A0", "", FuncRead, []byte{0x01})
	// SIZE_PWD = 0; PWD block is empty; FUNC=0x01 then DATA=0x01.
	is.Equal(got[20], byte(0x00)) // SIZE_PWD must be 0 for empty password
	is.Equal(got[21], byte(FuncRead))
	is.Equal(got[22], byte(0x01))
	cs := sumChecksum(got[2 : len(got)-2])
	gotCS := uint16(got[len(got)-2]) | uint16(got[len(got)-1])<<8
	is.Equal(cs, gotCS)
}

// ----- DecodeResponse golden -----

// TestDecodeResponse_GoldenReadUnitType decodes a captured controller
// response to "read unit_type". The DATA block is FE 02 B9 11 00, i.e.
// param 0x00B9 with 2-byte value [0x11, 0x00] (= unit type 17 = Breezy 160).
func TestDecodeResponse_GoldenReadUnitType(t *testing.T) {
	is := is.New(t)
	// Synthesised by encoding a response with the same shape, then
	// independently checksum-verified against the live capture.
	frame := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse,
		mustHex(t, "fe02b91100"))
	// Sanity: frame should end with stored checksum 0x6c 0x06.
	is.Equal(frame[len(frame)-2], byte(0x6c))
	is.Equal(frame[len(frame)-1], byte(0x06))

	fn, data, err := DecodeResponse(frame, "BREEZY00000000A0", "1111")
	is.NoErr(err)
	is.Equal(fn, byte(FuncResponse))
	want := mustHex(t, "fe02b91100")
	is.Equal(data, want)

	// And ParseDataBlock surfaces the value.
	pvs, err := ParseDataBlock(data)
	is.NoErr(err)
	is.Equal(len(pvs), 1)
	is.Equal(pvs[0].ID, ParamID(0x00B9))
	is.Equal(pvs[0].Value, []byte{0x11, 0x00})
	is.True(!pvs[0].Unsupported) // Unsupported should be false
}

func TestDecodeResponse_AuthFailure(t *testing.T) {
	is := is.New(t)
	// A captured-shape wrong-password response: function 0x07 with
	// 2-byte payload "01 31".
	frame := EncodeRequest("BREEZY00000000A0", "1111", FuncAuthFailure, []byte{0x01, 0x31})
	_, _, err := DecodeResponse(frame, "BREEZY00000000A0", "1111")
	is.True(errors.Is(err, ErrAuth)) // expected ErrAuth
}

func TestDecodeResponse_BadHeader(t *testing.T) {
	is := is.New(t)
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	bad := append([]byte{}, good...)
	bad[0] = 0xAB
	_, _, err := DecodeResponse(bad, "BREEZY00000000A0", "1111")
	is.True(errors.Is(err, ErrBadHeader)) // expected ErrBadHeader
}

func TestDecodeResponse_BadChecksum(t *testing.T) {
	is := is.New(t)
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	bad := append([]byte{}, good...)
	bad[len(bad)-1] ^= 0xFF
	_, _, err := DecodeResponse(bad, "BREEZY00000000A0", "1111")
	is.True(errors.Is(err, ErrChecksum)) // expected ErrChecksum
}

func TestDecodeResponse_IDMismatch(t *testing.T) {
	is := is.New(t)
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	_, _, err := DecodeResponse(good, "BREEZYNOTTHISONE", "1111")
	is.True(errors.Is(err, ErrIDMismatch)) // expected ErrIDMismatch
}

func TestDecodeResponse_PwdMismatch(t *testing.T) {
	is := is.New(t)
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	_, _, err := DecodeResponse(good, "BREEZY00000000A0", "2222")
	is.True(errors.Is(err, ErrPwdMismatch)) // expected ErrPwdMismatch
}

func TestDecodeResponse_Truncated(t *testing.T) {
	is := is.New(t)
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	_, _, err := DecodeResponse(good[:10], "BREEZY00000000A0", "1111")
	is.True(errors.Is(err, ErrTruncated)) // expected ErrTruncated
}

// TestDecodeDiscoveryResponse_RealWireFormat is a regression test for the
// discover-empty-result bug uncovered on 2026-05-04 via tcpdump against
// three real Breezy 160 units: real firmware replies to a wildcard
// discovery request with the device's OWN 16-byte ID in the frame header
// and SIZE_PWD=0, NOT echoing the wildcard ID + password the client sent.
//
// The strict DecodeResponse rejects such replies with ErrIDMismatch /
// ErrPwdMismatch. DecodeDiscoveryResponse must accept them.
func TestDecodeDiscoveryResponse_RealWireFormat(t *testing.T) {
	is := is.New(t)
	// Build a frame that mimics what real firmware sent: device ID in
	// the header is the unit's own ID, password slot is empty, FUNC=
	// FuncResponse (0x06), DATA carries 0x00B9 (unit type) and 0x007C
	// (device ID echoed in the data block).
	realID := "0025001A5646570E"

	// Hand-build the response data block: 0x007C param value (16 bytes)
	// + 0x00B9 param value (2 bytes uint16 = 17 = unit type "Breezy 160").
	//
	// Multi-byte values use the FE <size> <id> <bytes...> framing; the
	// 16-byte ID is multi-byte, the 2-byte unit type is also multi-byte.
	dataBlock := []byte{
		// FE 10 7C <16-byte ID>
		0xFE, 0x10, 0x7C,
	}
	dataBlock = append(dataBlock, []byte(realID)...)
	dataBlock = append(dataBlock,
		// FE 02 B9 11 00  (unit type 17, little-endian)
		0xFE, 0x02, 0xB9, 0x11, 0x00,
	)

	// Frame: real ID + empty password.
	frame := EncodeRequest(realID, "", FuncResponse, dataBlock)

	gotID, fn, body, err := DecodeDiscoveryResponse(frame)
	is.NoErr(err)
	is.Equal(gotID, realID)
	is.Equal(fn, byte(FuncResponse))
	parsed, err := ParseDataBlock(body)
	is.NoErr(err)
	var sawID, sawType bool
	for _, p := range parsed {
		switch p.ID {
		case 0x007C:
			if string(p.Value) == realID {
				sawID = true
			}
		case 0x00B9:
			if len(p.Value) == 2 && p.Value[0] == 0x11 && p.Value[1] == 0x00 {
				sawType = true
			}
		}
	}
	is.True(sawID && sawType) // data block must contain 0x007C and 0x00B9

	// Belt-and-braces: confirm the strict DecodeResponse REJECTS the
	// same frame, so DecodeDiscoveryResponse is the only valid path.
	_, _, err = DecodeResponse(frame, DefaultDeviceID, "huffpuff")
	is.True(err != nil) // strict DecodeResponse should have rejected this real-wire frame
}

// ----- Round-trip property test -----

func TestRoundTrip_EncodeDecode(t *testing.T) {
	cases := []struct {
		devID string
		pwd   string
		fn    byte
		data  []byte
	}{
		{"BREEZY00000000A0", "1111", FuncRead, []byte{0xB9}},
		{"BREEZY00000000A0", "testpwd", FuncResponse, mustHex(t, "fe02b91100")},
		{"DEFAULT_DEVICEID", "", FuncRead, []byte{0x7C}},
		{"BREEZY00000000A1", "abc", FuncWriteWithReply, mustHex(t, "9b02fe04700485374201")},
		{"BREEZY00000000A0", "12345678", FuncResponse, nil},
	}
	for _, c := range cases {
		t.Run(c.devID+"/"+c.pwd, func(t *testing.T) {
			is := is.New(t)
			pkt := EncodeRequest(c.devID, c.pwd, c.fn, c.data)
			fn, data, err := DecodeResponse(pkt, c.devID, c.pwd)
			is.NoErr(err)
			is.Equal(fn, c.fn)
			if len(c.data) == 0 && len(data) == 0 {
				// nil/empty equivalence
			} else {
				is.True(bytes.Equal(data, c.data))
			}
		})
	}
}

// ----- BuildReadDataBlock -----

func TestBuildReadDataBlock(t *testing.T) {
	cases := []struct {
		name string
		ids  []ParamID
		want string
	}{
		{"empty", nil, ""},
		{"single low", []ParamID{0x01}, "01"},
		{"two low", []ParamID{0x01, 0xB9}, "01b9"},
		{"single high", []ParamID{0x0104}, "ff0104"},
		{"mixed (spec example)", []ParamID{0x0001, 0x0104, 0x0240}, "01ff0104ff0240"},
		{"low high low", []ParamID{0x01, 0x0301, 0x02}, "01ff0301ff0002"},
		{"all on page 3", []ParamID{0x0301, 0x0306, 0x0320}, "ff03010620"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			is := is.New(t)
			got := BuildReadDataBlock(c.ids)
			want := mustHex(t, c.want)
			is.True(bytes.Equal(got, want)) // bytes.Equal treats nil and []byte{} as equal; DeepEqual doesn't
		})
	}
}

// ----- BuildWriteDataBlock -----

func TestBuildWriteDataBlock(t *testing.T) {
	cases := []struct {
		name   string
		writes []ParamWrite
		want   string
	}{
		{
			"single 1-byte",
			[]ParamWrite{{ID: 0x9B, Value: []byte{0x02}}},
			"9b02",
		},
		{
			"single multi-byte",
			[]ParamWrite{{ID: 0x70, Value: []byte{0x04, 0x85, 0x37, 0x42}}},
			"fe047004853742",
		},
		{
			// Spec example: write 0x009B = 0x02 (1 byte) and
			// 0x0070 = 0x42378504 (4-byte LE).
			"spec mixed example",
			[]ParamWrite{
				{ID: 0x9B, Value: []byte{0x02}},
				{ID: 0x70, Value: []byte{0x04, 0x85, 0x37, 0x42}},
			},
			"9b02fe047004853742",
		},
		{
			"high page write",
			[]ParamWrite{
				{ID: 0x0315, Value: []byte{0x00}},
			},
			"ff031500",
		},
		{
			"page boundary",
			[]ParamWrite{
				{ID: 0x9B, Value: []byte{0x02}},
				{ID: 0x0315, Value: []byte{0x00}},
				{ID: 0x44, Value: []byte{0x1E}},
			},
			"9b02ff031500ff00441e",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			is := is.New(t)
			got := BuildWriteDataBlock(c.writes)
			want := mustHex(t, c.want)
			is.Equal(got, want)
		})
	}
}

// ----- ParseDataBlock -----

func TestParseDataBlock(t *testing.T) {
	type pv = ParamValue
	cases := []struct {
		name string
		data string
		want []pv
	}{
		{"empty", "", nil},
		{
			"single 1-byte",
			"01b9",
			[]pv{{ID: 0x01, Value: []byte{0xB9}}},
		},
		{
			"single FE multi-byte (spec golden)",
			"fe02b91100",
			[]pv{{ID: 0x00B9, Value: []byte{0x11, 0x00}}},
		},
		{
			"FD unsupported",
			"fd2b",
			[]pv{{ID: 0x002B, Unsupported: true}},
		},
		{
			"FF page change then 1-byte",
			"ff03150a",
			[]pv{{ID: 0x0315, Value: []byte{0x0A}}},
		},
		{
			"FF then FD on high page",
			"ff03fd05",
			[]pv{{ID: 0x0305, Unsupported: true}},
		},
		{
			"mixed: low 1-byte, FE multi-byte, FD, FF + 1-byte",
			// 01 02              -> param 0x0001 = 0x02
			// fe 04 70 04853742  -> param 0x0070 = 4-byte LE
			// fd 2b              -> param 0x002B unsupported
			// ff 03              -> switch to high page 0x03
			// 15 0a              -> param 0x0315 = 0x0A
			"0102fe047004853742fd2bff03150a",
			[]pv{
				{ID: 0x0001, Value: []byte{0x02}},
				{ID: 0x0070, Value: []byte{0x04, 0x85, 0x37, 0x42}},
				{ID: 0x002B, Unsupported: true},
				{ID: 0x0315, Value: []byte{0x0A}},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			is := is.New(t)
			data := mustHex(t, c.data)
			got, err := ParseDataBlock(data)
			is.NoErr(err)
			is.Equal(len(got), len(c.want))
			for i := range got {
				is.Equal(got[i].ID, c.want[i].ID)
				is.Equal(got[i].Unsupported, c.want[i].Unsupported)
				is.Equal(got[i].Value, c.want[i].Value)
			}
		})
	}
}

func TestParseDataBlock_Errors(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"FE without size", "fe"},
		{"FE size without bytes", "fe0470"}, // FE 04 70, then 0 of 4 bytes
		{"FF without hi", "ff"},
		{"FD without id", "fd"},
		{"implicit value missing byte", "01"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			is := is.New(t)
			_, err := ParseDataBlock(mustHex(t, c.data))
			is.True(errors.Is(err, ErrInvalidData)) // expected ErrInvalidData
		})
	}
}

// TestParseDataBlock_FCMarker verifies that an FC <new_func> marker
// embedded mid-block surfaces ErrUnexpectedFuncChange, and that any
// entries decoded *before* the FC are still returned. Real firmware has
// never been observed emitting one; if that ever changes we want to hear
// about it loudly rather than silently dropping the rest of the packet.
func TestParseDataBlock_FCMarker(t *testing.T) {
	is := is.New(t)
	// 01 02   -> param 0x0001 = 0x02   (one valid entry before FC)
	// FC 03   -> change FUNC to 0x03   (the unexpected marker)
	// 04 05   -> would-be param 0x0004 (never reached)
	out, err := ParseDataBlock(mustHex(t, "0102fc030405"))
	is.True(errors.Is(err, ErrUnexpectedFuncChange)) // expected ErrUnexpectedFuncChange
	is.Equal(len(out), 1)
	is.Equal(out[0].ID, ParamID(0x0001))
	is.Equal(out[0].Value, []byte{0x02})
}

// TestParseDataBlock_FCMarkerTruncated verifies that FC at the very end
// of a block (without a following byte) still yields an error — but is
// classified as malformed-data, not as a soft "unexpected FC" notice.
func TestParseDataBlock_FCMarkerTruncated(t *testing.T) {
	is := is.New(t)
	_, err := ParseDataBlock(mustHex(t, "fc"))
	is.True(errors.Is(err, ErrInvalidData)) // expected ErrInvalidData for truncated FC
}

// ----- Reserved low-byte param IDs (panic on FC/FD/FE/FF) -----

func TestBuildReadDataBlock_PanicsOnReservedID(t *testing.T) {
	cases := []ParamID{0x00FC, 0x00FD, 0x00FE, 0x00FF, 0x01FC, 0x03FF}
	for _, id := range cases {
		id := id
		t.Run("read_"+hex.EncodeToString([]byte{byte(id >> 8), byte(id)}), func(t *testing.T) {
			is := is.New(t)
			defer func() {
				r := recover()
				is.True(r != nil) // BuildReadDataBlock must panic on reserved id
				msg, _ := r.(string)
				if msg == "" {
					return
				}
				is.True(bytes.Contains([]byte(msg), []byte("reserved"))) // panic message should mention 'reserved'
			}()
			_ = BuildReadDataBlock([]ParamID{id})
		})
	}
}

func TestBuildWriteDataBlock_PanicsOnReservedID(t *testing.T) {
	is := is.New(t)
	defer func() {
		r := recover()
		is.True(r != nil) // BuildWriteDataBlock must panic on reserved id
	}()
	_ = BuildWriteDataBlock([]ParamWrite{{ID: 0x00FE, Value: []byte{0x01}}})
}

func TestBuildDataBlock_NoPanicOnNormalIDs(t *testing.T) {
	is := is.New(t)
	// Sanity: high byte FF is fine, only the low byte matters.
	defer func() {
		r := recover()
		is.True(r == nil) // no panic expected on legal IDs
	}()
	_ = BuildReadDataBlock([]ParamID{0xFF00, 0x00FB, 0xFF01})
	_ = BuildWriteDataBlock([]ParamWrite{{ID: 0xFF00, Value: []byte{0x01}}})
}

// ----- Round-trip Build* + ParseDataBlock for write-style data -----

func TestBuildAndParseWriteRoundtrip(t *testing.T) {
	is := is.New(t)
	writes := []ParamWrite{
		{ID: 0x9B, Value: []byte{0x02}},
		{ID: 0x70, Value: []byte{0x04, 0x85, 0x37, 0x42}},
		{ID: 0x0315, Value: []byte{0x00}},
	}
	data := BuildWriteDataBlock(writes)
	parsed, err := ParseDataBlock(data)
	is.NoErr(err)
	want := []ParamValue{
		{ID: 0x009B, Value: []byte{0x02}},
		{ID: 0x0070, Value: []byte{0x04, 0x85, 0x37, 0x42}},
		{ID: 0x0315, Value: []byte{0x00}},
	}
	is.Equal(parsed, want) // round-trip Build->Parse must preserve writes
}
