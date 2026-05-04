// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"bytes"
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

// genDeviceID draws a 16-character ASCII device ID from [0-9A-Za-z].
func genDeviceID(t *rapid.T) string {
	return rapid.StringMatching(`[0-9A-Za-z]{16}`).Draw(t, "deviceID")
}

// genPassword draws a 0..8 character ASCII password from [0-9A-Za-z].
func genPassword(t *rapid.T) string {
	return rapid.StringMatching(`[0-9A-Za-z]{0,8}`).Draw(t, "password")
}

// TestProp_EncodeDecodeRoundTrip exercises the FDFD/02 frame codec:
// EncodeRequest's output should round-trip through DecodeResponse and
// recover the function byte and DATA block byte-for-byte.
//
// We deliberately exclude FUNC=0x07 (auth-failure), which DecodeResponse
// surfaces as ErrAuth.
func TestProp_EncodeDecodeRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		devid := genDeviceID(t)
		pwd := genPassword(t)
		fn := rapid.SampledFrom([]byte{
			FuncRead,
			FuncWriteNoResponse,
			FuncWriteWithReply,
			FuncIncrement,
			FuncDecrement,
			FuncResponse,
		}).Draw(t, "fn")
		data := rapid.SliceOfN(rapid.Byte(), 0, 200).Draw(t, "data")

		raw := EncodeRequest(devid, pwd, fn, data)
		gotFn, gotData, err := DecodeResponse(raw, devid, pwd)
		if err != nil {
			t.Fatalf("decode failed: %v", err)
		}
		if gotFn != fn {
			t.Fatalf("fn mismatch: got %#x want %#x", gotFn, fn)
		}
		if !bytes.Equal(gotData, data) {
			t.Fatalf("data mismatch:\n got: %x\nwant: %x", gotData, data)
		}
	})
}

// TestProp_AuthFailureSurfaced ensures DecodeResponse surfaces ErrAuth
// (and only ErrAuth) when the frame's FUNC byte is 0x07. The frame is
// otherwise well-formed.
func TestProp_AuthFailureSurfaced(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		devid := genDeviceID(t)
		pwd := genPassword(t)
		data := rapid.SliceOfN(rapid.Byte(), 0, 200).Draw(t, "data")

		raw := EncodeRequest(devid, pwd, FuncAuthFailure, data)
		gotFn, _, err := DecodeResponse(raw, devid, pwd)
		if err != ErrAuth {
			t.Fatalf("expected ErrAuth, got %v", err)
		}
		if gotFn != FuncAuthFailure {
			t.Fatalf("fn: got %#x want %#x", gotFn, FuncAuthFailure)
		}
	})
}

// genParamID draws a ParamID covering the low page (0x00) and high pages
// 0x01..0x04 that appear in real packets. The low byte is constrained to
// [0x00, 0xFB] because the protocol reserves 0xFC..0xFF inside DATA as
// command bytes (see frame.go and the param-map spec) — the codec has no
// way to disambiguate a literal 0xFF in a low-byte position from a
// page-switch command. Real parameters never use those low bytes.
func genParamID(t *rapid.T) ParamID {
	hi := rapid.IntRange(0, 4).Draw(t, "hi")
	lo := rapid.IntRange(0, 0xFB).Draw(t, "lo")
	return ParamID(uint16(hi)<<8 | uint16(lo))
}

// genParamWrite draws a ParamWrite with a 1..4 byte arbitrary value.
// Zero-length values are excluded because BuildWriteDataBlock skips them
// (see frame.go), making them irrelevant to the round-trip property.
func genParamWrite(t *rapid.T) ParamWrite {
	id := genParamID(t)
	sz := rapid.IntRange(1, 4).Draw(t, "size")
	val := rapid.SliceOfN(rapid.Byte(), sz, sz).Draw(t, "value")
	return ParamWrite{ID: id, Value: val}
}

// TestProp_BuildWriteParseRoundTrip verifies that BuildWriteDataBlock and
// ParseDataBlock are inverses on well-formed input: every (ID, Value)
// pair survives in order, and no entries are flagged Unsupported (since
// every input was a valid write, never an FD marker).
func TestProp_BuildWriteParseRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		writes := rapid.SliceOf(rapid.Custom(genParamWrite)).Draw(t, "writes")

		block := BuildWriteDataBlock(writes)
		parsed, err := ParseDataBlock(block)
		if err != nil {
			t.Fatalf("ParseDataBlock returned error: %v", err)
		}
		if len(parsed) != len(writes) {
			t.Fatalf("entry count: got %d want %d\nblock: %x", len(parsed), len(writes), block)
		}
		for i := range writes {
			if parsed[i].Unsupported {
				t.Fatalf("entry %d: unexpectedly flagged Unsupported", i)
			}
			if parsed[i].ID != writes[i].ID {
				t.Fatalf("entry %d: ID got %#x want %#x", i, parsed[i].ID, writes[i].ID)
			}
			if !bytes.Equal(parsed[i].Value, writes[i].Value) {
				t.Fatalf("entry %d: Value got %x want %x", i, parsed[i].Value, writes[i].Value)
			}
		}
	})
}

// parseReadBlock walks the bytes emitted by BuildReadDataBlock back into
// a list of ParamIDs, asserting structural invariants along the way.
// Returns the ordered list of visited IDs, or an error describing where
// the structure broke.
func parseReadBlock(data []byte) ([]ParamID, error) {
	out := make([]ParamID, 0)
	curHi := byte(0x00)
	for i := 0; i < len(data); {
		b := data[i]
		if b == cmdChangeHighByte {
			if i+1 >= len(data) {
				return nil, errInvalidf("FF without high byte at %d", i)
			}
			newHi := data[i+1]
			if newHi == curHi {
				return nil, errInvalidf("redundant FF %#x at offset %d (high byte unchanged)", newHi, i)
			}
			curHi = newHi
			i += 2
			continue
		}
		// Otherwise, b is a low byte — emit a ParamID at the current page.
		out = append(out, ParamID(uint16(curHi)<<8|uint16(b)))
		i++
	}
	return out, nil
}

// errInvalidf is a tiny helper to keep parseReadBlock readable.
func errInvalidf(format string, args ...any) error {
	return fmt.Errorf(format, args...)
}

// TestProp_BuildReadStructural verifies the structural invariant of
// BuildReadDataBlock: the emitted bytes parse cleanly back into the
// exact input ParamID list, with no redundant page switches.
func TestProp_BuildReadStructural(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ids := rapid.SliceOf(rapid.Custom(genParamID)).Draw(t, "ids")

		block := BuildReadDataBlock(ids)
		gotIDs, err := parseReadBlock(block)
		if err != nil {
			t.Fatalf("structural parse failed: %v\nblock: %x\nids: %v", err, block, ids)
		}
		if len(gotIDs) != len(ids) {
			t.Fatalf("id count: got %d want %d\nblock: %x", len(gotIDs), len(ids), block)
		}
		for i := range ids {
			if gotIDs[i] != ids[i] {
				t.Fatalf("id[%d]: got %#x want %#x\nblock: %x", i, gotIDs[i], ids[i], block)
			}
		}
	})
}

// expectedPageSwitches counts the number of FF <hi> commands the codec
// should emit for the given input. Definition: a transition exists at
// position i iff (i == 0 && hi(i) != 0) OR (i > 0 && hi(i) != hi(i-1)).
// Empty input emits zero switches.
func expectedPageSwitches[T interface{ ParamID | ParamWrite }](items []T, getHi func(T) byte) int {
	if len(items) == 0 {
		return 0
	}
	count := 0
	prev := byte(0x00)
	for i, it := range items {
		hi := getHi(it)
		if i == 0 {
			if hi != 0x00 {
				count++
				prev = hi
			}
			continue
		}
		if hi != prev {
			count++
			prev = hi
		}
	}
	return count
}

// countPageSwitches counts FF bytes that act as page-switch commands at
// the top level (i.e. not consumed as part of an FE size-prefix payload).
// For BuildReadDataBlock output, every FF byte is a page switch. For
// BuildWriteDataBlock output, FE <size> <id_lo> <bytes...> means we must
// skip <size> bytes after the FE size header — those bytes are the
// value payload, not commands.
func countPageSwitches(block []byte, hasFEPrefix bool) int {
	count := 0
	for i := 0; i < len(block); {
		b := block[i]
		switch {
		case b == cmdChangeHighByte:
			count++
			i += 2 // FF <hi>
		case hasFEPrefix && b == cmdSizePrefix:
			// FE <size> <id_lo> <size bytes of value>
			if i+2 >= len(block) {
				return -1 // malformed; let caller surface
			}
			size := int(block[i+1])
			i += 3 + size
		default:
			// 1-byte value entry: <id_lo> <value> for writes,
			// or just <id_lo> for reads.
			if hasFEPrefix {
				i += 2
			} else {
				i++
			}
		}
	}
	return count
}

// TestProp_BuildReadMinimalSwitches checks that BuildReadDataBlock emits
// exactly the minimum required number of FF page-switch commands, given
// the input ID sequence. Catches both spurious switches and missing ones.
func TestProp_BuildReadMinimalSwitches(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ids := rapid.SliceOf(rapid.Custom(genParamID)).Draw(t, "ids")

		block := BuildReadDataBlock(ids)
		got := countPageSwitches(block, false)
		want := expectedPageSwitches(ids, func(id ParamID) byte { return byte(id >> 8) })
		if got != want {
			t.Fatalf("page switches: got %d want %d\nids: %v\nblock: %x", got, want, ids, block)
		}
	})
}

// TestProp_BuildWriteMinimalSwitches is the analogous minimality check
// for BuildWriteDataBlock.
func TestProp_BuildWriteMinimalSwitches(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		writes := rapid.SliceOf(rapid.Custom(genParamWrite)).Draw(t, "writes")

		block := BuildWriteDataBlock(writes)
		got := countPageSwitches(block, true)
		want := expectedPageSwitches(writes, func(w ParamWrite) byte { return byte(w.ID >> 8) })
		if got != want {
			t.Fatalf("page switches: got %d want %d\nwrites: %v\nblock: %x", got, want, writes, block)
		}
	})
}
