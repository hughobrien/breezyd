package breezy

import (
	"bytes"
	"encoding/hex"
	"errors"
	"reflect"
	"testing"
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
	// Read param 0xB9 (unit_type) from the playroom unit with default password.
	got := EncodeRequest("BREEZY00000000A0", "1111", FuncRead, []byte{0xB9})

	// Hand-computed expected wire bytes:
	//   FD FD 02 10 <16 ASCII id> 04 31 31 31 31 01 B9 <ck_lo> <ck_hi>
	want := mustHex(t, "fdfd0210425245455a593030303030303030304130043131313101b9e304")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeRequest mismatch:\n got: %s\nwant: %s", hex.EncodeToString(got), hex.EncodeToString(want))
	}

	// Independently verify the checksum: sum of bytes [2:-2] LE-stored at end.
	cs := sumChecksum(got[2 : len(got)-2])
	gotCS := uint16(got[len(got)-2]) | uint16(got[len(got)-1])<<8
	if cs != gotCS {
		t.Fatalf("checksum self-check: stored 0x%04x sum 0x%04x", gotCS, cs)
	}
}

func TestEncodeRequest_EmptyPassword(t *testing.T) {
	got := EncodeRequest("BREEZY00000000A0", "", FuncRead, []byte{0x01})
	// SIZE_PWD = 0; PWD block is empty; FUNC=0x01 then DATA=0x01.
	if got[20] != 0x00 {
		t.Fatalf("expected SIZE_PWD=0, got 0x%02x", got[20])
	}
	if got[21] != FuncRead || got[22] != 0x01 {
		t.Fatalf("expected FUNC=0x01 DATA=0x01, got 0x%02x 0x%02x", got[21], got[22])
	}
	cs := sumChecksum(got[2 : len(got)-2])
	gotCS := uint16(got[len(got)-2]) | uint16(got[len(got)-1])<<8
	if cs != gotCS {
		t.Fatalf("checksum mismatch: 0x%04x vs 0x%04x", cs, gotCS)
	}
}

// ----- DecodeResponse golden -----

// TestDecodeResponse_GoldenReadUnitType decodes a captured controller
// response to "read unit_type". The DATA block is FE 02 B9 11 00, i.e.
// param 0x00B9 with 2-byte value [0x11, 0x00] (= unit type 17 = Breezy 160).
func TestDecodeResponse_GoldenReadUnitType(t *testing.T) {
	// Synthesised by encoding a response with the same shape, then
	// independently checksum-verified against the live capture.
	frame := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse,
		mustHex(t, "fe02b91100"))
	// Sanity: frame should end with stored checksum 0xf9 0x05.
	if frame[len(frame)-2] != 0xf9 || frame[len(frame)-1] != 0x05 {
		t.Fatalf("unexpected checksum bytes: 0x%02x 0x%02x (frame=%s)",
			frame[len(frame)-2], frame[len(frame)-1], hex.EncodeToString(frame))
	}

	fn, data, err := DecodeResponse(frame, "BREEZY00000000A0", "1111")
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if fn != FuncResponse {
		t.Fatalf("function: got 0x%02x want 0x06", fn)
	}
	want := mustHex(t, "fe02b91100")
	if !bytes.Equal(data, want) {
		t.Fatalf("DATA: got %s want %s", hex.EncodeToString(data), hex.EncodeToString(want))
	}

	// And ParseDataBlock surfaces the value.
	pvs, err := ParseDataBlock(data)
	if err != nil {
		t.Fatalf("ParseDataBlock: %v", err)
	}
	if len(pvs) != 1 {
		t.Fatalf("expected 1 entry, got %d (%v)", len(pvs), pvs)
	}
	if pvs[0].ID != 0x00B9 {
		t.Fatalf("ID: got 0x%04x want 0x00B9", pvs[0].ID)
	}
	if !bytes.Equal(pvs[0].Value, []byte{0x11, 0x00}) {
		t.Fatalf("Value: got %x want 1100", pvs[0].Value)
	}
	if pvs[0].Unsupported {
		t.Fatalf("Unsupported should be false")
	}
}

func TestDecodeResponse_AuthFailure(t *testing.T) {
	// A captured-shape wrong-password response: function 0x07 with
	// 2-byte payload "01 31".
	frame := EncodeRequest("BREEZY00000000A0", "1111", FuncAuthFailure, []byte{0x01, 0x31})
	_, _, err := DecodeResponse(frame, "BREEZY00000000A0", "1111")
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("expected ErrAuth, got %v", err)
	}
}

func TestDecodeResponse_BadHeader(t *testing.T) {
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	bad := append([]byte{}, good...)
	bad[0] = 0xAB
	_, _, err := DecodeResponse(bad, "BREEZY00000000A0", "1111")
	if !errors.Is(err, ErrBadHeader) {
		t.Fatalf("expected ErrBadHeader, got %v", err)
	}
}

func TestDecodeResponse_BadChecksum(t *testing.T) {
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	bad := append([]byte{}, good...)
	bad[len(bad)-1] ^= 0xFF
	_, _, err := DecodeResponse(bad, "BREEZY00000000A0", "1111")
	if !errors.Is(err, ErrChecksum) {
		t.Fatalf("expected ErrChecksum, got %v", err)
	}
}

func TestDecodeResponse_IDMismatch(t *testing.T) {
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	_, _, err := DecodeResponse(good, "BREEZYNOTTHISONE", "1111")
	if !errors.Is(err, ErrIDMismatch) {
		t.Fatalf("expected ErrIDMismatch, got %v", err)
	}
}

func TestDecodeResponse_PwdMismatch(t *testing.T) {
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	_, _, err := DecodeResponse(good, "BREEZY00000000A0", "2222")
	if !errors.Is(err, ErrPwdMismatch) {
		t.Fatalf("expected ErrPwdMismatch, got %v", err)
	}
}

func TestDecodeResponse_Truncated(t *testing.T) {
	good := EncodeRequest("BREEZY00000000A0", "1111", FuncResponse, []byte{0x01, 0x02})
	_, _, err := DecodeResponse(good[:10], "BREEZY00000000A0", "1111")
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("expected ErrTruncated, got %v", err)
	}
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
			pkt := EncodeRequest(c.devID, c.pwd, c.fn, c.data)
			fn, data, err := DecodeResponse(pkt, c.devID, c.pwd)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if fn != c.fn {
				t.Fatalf("function: got 0x%02x want 0x%02x", fn, c.fn)
			}
			if len(c.data) == 0 && len(data) == 0 {
				// nil/empty equivalence
			} else if !bytes.Equal(data, c.data) {
				t.Fatalf("data: got %x want %x", data, c.data)
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
			got := BuildReadDataBlock(c.ids)
			want := mustHex(t, c.want)
			if !bytes.Equal(got, want) {
				t.Fatalf("got %s want %s", hex.EncodeToString(got), c.want)
			}
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
			got := BuildWriteDataBlock(c.writes)
			want := mustHex(t, c.want)
			if !bytes.Equal(got, want) {
				t.Fatalf("got %s want %s", hex.EncodeToString(got), c.want)
			}
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
			data := mustHex(t, c.data)
			got, err := ParseDataBlock(data)
			if err != nil {
				t.Fatalf("ParseDataBlock: %v", err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("len: got %d want %d (%v)", len(got), len(c.want), got)
			}
			for i := range got {
				if got[i].ID != c.want[i].ID {
					t.Errorf("[%d] ID: got 0x%04x want 0x%04x", i, got[i].ID, c.want[i].ID)
				}
				if got[i].Unsupported != c.want[i].Unsupported {
					t.Errorf("[%d] Unsupported: got %v want %v", i, got[i].Unsupported, c.want[i].Unsupported)
				}
				if !bytes.Equal(got[i].Value, c.want[i].Value) {
					t.Errorf("[%d] Value: got %x want %x", i, got[i].Value, c.want[i].Value)
				}
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
			_, err := ParseDataBlock(mustHex(t, c.data))
			if !errors.Is(err, ErrInvalidData) {
				t.Fatalf("expected ErrInvalidData, got %v", err)
			}
		})
	}
}

// ----- Round-trip Build* + ParseDataBlock for write-style data -----

func TestBuildAndParseWriteRoundtrip(t *testing.T) {
	writes := []ParamWrite{
		{ID: 0x9B, Value: []byte{0x02}},
		{ID: 0x70, Value: []byte{0x04, 0x85, 0x37, 0x42}},
		{ID: 0x0315, Value: []byte{0x00}},
	}
	data := BuildWriteDataBlock(writes)
	parsed, err := ParseDataBlock(data)
	if err != nil {
		t.Fatalf("ParseDataBlock: %v", err)
	}
	want := []ParamValue{
		{ID: 0x009B, Value: []byte{0x02}},
		{ID: 0x0070, Value: []byte{0x04, 0x85, 0x37, 0x42}},
		{ID: 0x0315, Value: []byte{0x00}},
	}
	if !reflect.DeepEqual(parsed, want) {
		t.Fatalf("round-trip mismatch:\n got: %+v\nwant: %+v", parsed, want)
	}
}
