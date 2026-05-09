// SPDX-License-Identifier: GPL-3.0-or-later

// Package breezy implements the Vents Twinfresh Breezy ERV's UDP/4000
// FDFD/02 protocol. This file contains the wire-frame codec and the
// DATA-block composition / parsing helpers. No I/O happens here — the
// transport (Client) lives in client.go and is layered on top.
package breezy

import (
	"errors"
	"fmt"
)

// ParamID is a Breezy parameter number (0x0000..0xFFFF). The high byte is
// the "page"; the protocol routes IDs >= 0x0100 by injecting an
// FF <hi> command into the DATA block before referring to those params.
type ParamID uint16

// ParamWrite is one parameter write request: a target ID and the value
// to set, in little-endian byte order. A 1-byte Value uses the implicit
// 1-byte default; longer values are framed with FE <size> automatically.
type ParamWrite struct {
	ID    ParamID
	Value []byte
}

// ParamValue is one decoded entry in a response's DATA block. When the
// device returned an "unsupported" marker (FD <id>) for this ID, Value
// will be nil and Unsupported will be true.
type ParamValue struct {
	ID          ParamID
	Value       []byte
	Unsupported bool
}

// Wire-level constants.
const (
	headerByte0 byte = 0xFD
	headerByte1 byte = 0xFD
	protoType   byte = 0x02 // protocol version / packet type
	idSize      byte = 0x10 // 16 ASCII bytes

	// FUNC codes. Public for callers that build raw data blocks.
	FuncRead            byte = 0x01
	FuncWriteNoResponse byte = 0x02
	FuncWriteWithReply  byte = 0x03
	FuncIncrement       byte = 0x04
	FuncDecrement       byte = 0x05
	FuncResponse        byte = 0x06
	FuncAuthFailure     byte = 0x07 // firmware-emitted on bad password (undocumented)

	// DATA-block special command bytes.
	cmdChangeFunc     byte = 0xFC
	cmdNotSupported   byte = 0xFD
	cmdSizePrefix     byte = 0xFE
	cmdChangeHighByte byte = 0xFF
)

// Errors surfaced by DecodeResponse and the helpers.
var (
	ErrBadHeader   = errors.New("breezy: bad header")
	ErrChecksum    = errors.New("breezy: checksum mismatch")
	ErrTruncated   = errors.New("breezy: truncated frame")
	ErrAuth        = errors.New("breezy: authentication failed")
	// ErrTimeout is returned by MemClient (and may be returned by other
	// non-UDP implementations) when a simulated timeout is injected via
	// SetTimeoutMode. The UDP *Client surfaces context.DeadlineExceeded
	// for real timeouts; this sentinel lets callers test timeout handling
	// against in-process clients without real timers.
	ErrTimeout     = errors.New("breezy: operation timed out")
	ErrInvalidData = errors.New("breezy: malformed data block")
	ErrIDMismatch  = errors.New("breezy: response device ID does not match request")
	ErrPwdMismatch = errors.New("breezy: response password does not match request")
	// ErrReservedParamID is returned (via panic in the builders) when a
	// caller passes a ParamID whose low byte is in the protocol's reserved
	// special-command range 0xFC-0xFF. Such IDs would alias to FC/FD/FE/FF
	// inside DATA blocks and silently corrupt traffic. No documented
	// parameter uses this range; hitting it is a caller bug.
	ErrReservedParamID = errors.New("breezy: parameter ID's low byte is in the reserved range 0xFC-0xFF")
	// ErrUnexpectedFuncChange indicates the response DATA block contained an
	// FC <new_func> marker. The protocol allows it but we've never observed
	// real firmware emitting one. Callers can choose to treat it as an error
	// or log it for diagnostics.
	ErrUnexpectedFuncChange = errors.New("breezy: unexpected FC change-function marker in DATA")
)

// isReservedLowByte reports whether lo collides with the protocol's special
// command bytes (FC change-func, FD not-supported, FE size-prefix,
// FF change-page). The high byte is unrestricted because page switches use
// FF <hi>, which never aliases to a value byte.
func isReservedLowByte(lo byte) bool {
	return lo >= cmdChangeFunc // 0xFC..0xFF
}

// EncodeRequest builds a complete FDFD/02 request packet. deviceID must
// be exactly 16 ASCII characters and password must be 0..8 ASCII bytes
// (per the protocol spec). The checksum is computed automatically.
func EncodeRequest(deviceID, password string, function byte, dataBlock []byte) []byte {
	if len(deviceID) != int(idSize) {
		panic(fmt.Sprintf("breezy: deviceID must be %d bytes, got %d", idSize, len(deviceID)))
	}
	if len(password) > 8 {
		panic(fmt.Sprintf("breezy: password must be <= 8 bytes, got %d", len(password)))
	}

	// 2 magic + 1 type + 1 size_id + 16 id + 1 size_pwd + N pwd + 1 func +
	// data + 2 cksum
	buf := make([]byte, 0, 24+len(password)+len(dataBlock))
	buf = append(buf, headerByte0, headerByte1)
	buf = append(buf, protoType)
	buf = append(buf, idSize)
	buf = append(buf, []byte(deviceID)...)
	buf = append(buf, byte(len(password)))
	buf = append(buf, []byte(password)...)
	buf = append(buf, function)
	buf = append(buf, dataBlock...)

	// Checksum: sum of bytes from TYPE (index 2) through end of DATA,
	// stored little-endian.
	var sum uint16
	for _, b := range buf[2:] {
		sum += uint16(b)
	}
	buf = append(buf, byte(sum&0xFF), byte((sum>>8)&0xFF))
	return buf
}

// DecodeResponse parses an FDFD/02 response packet. deviceID and password
// are the values sent in the originating request and must match the
// echoed ID and PWD blocks in the response. If the device replied with
// FUNC=0x07 (auth failure), DecodeResponse returns ErrAuth.
//
// On success, returns (function, dataBlock, nil). The returned slice
// aliases raw — callers must copy if they intend to retain it past the
// next read.
func DecodeResponse(raw []byte, deviceID, password string) (byte, []byte, error) {
	if len(deviceID) != int(idSize) {
		return 0, nil, fmt.Errorf("breezy: deviceID must be %d bytes, got %d", idSize, len(deviceID))
	}
	// Minimum: 2 magic + 1 type + 1 size_id + 16 id + 1 size_pwd + 0 pwd +
	// 1 func + 0 data + 2 cksum = 24 bytes.
	if len(raw) < 24 {
		return 0, nil, ErrTruncated
	}
	if raw[0] != headerByte0 || raw[1] != headerByte1 {
		return 0, nil, ErrBadHeader
	}
	if raw[2] != protoType {
		return 0, nil, fmt.Errorf("%w: TYPE=0x%02x", ErrBadHeader, raw[2])
	}
	if raw[3] != idSize {
		return 0, nil, fmt.Errorf("%w: SIZE_ID=0x%02x", ErrBadHeader, raw[3])
	}

	idStart := 4
	idEnd := idStart + int(idSize) // 20
	if string(raw[idStart:idEnd]) != deviceID {
		return 0, nil, ErrIDMismatch
	}

	sizePwdIdx := idEnd // 20
	sizePwd := int(raw[sizePwdIdx])
	if sizePwd > 8 {
		return 0, nil, fmt.Errorf("%w: SIZE_PWD=%d", ErrBadHeader, sizePwd)
	}
	pwdStart := sizePwdIdx + 1
	pwdEnd := pwdStart + sizePwd
	// Frame must have room for FUNC + 2 cksum bytes after PWD.
	if pwdEnd+1+2 > len(raw) {
		return 0, nil, ErrTruncated
	}
	if string(raw[pwdStart:pwdEnd]) != password {
		return 0, nil, ErrPwdMismatch
	}

	funcIdx := pwdEnd
	function := raw[funcIdx]
	dataStart := funcIdx + 1
	dataEnd := len(raw) - 2 // checksum is last 2 bytes
	if dataEnd < dataStart {
		return 0, nil, ErrTruncated
	}

	// Verify checksum: sum of bytes [2 : len-2].
	var sum uint16
	for _, b := range raw[2:dataEnd] {
		sum += uint16(b)
	}
	want := uint16(raw[dataEnd]) | uint16(raw[dataEnd+1])<<8
	if sum != want {
		return function, nil, ErrChecksum
	}

	if function == FuncAuthFailure {
		return function, raw[dataStart:dataEnd], ErrAuth
	}
	return function, raw[dataStart:dataEnd], nil
}

// DecodeDiscoveryResponse is the relaxed decoder for replies to a wildcard
// (DEFAULT_DEVICEID) discovery request. Real Breezy firmware echoes the
// device's *own* 16-character ID in the frame header and SIZE_PWD=0 — not
// the wildcard ID and password we sent. The strict DecodeResponse rejects
// these as ErrIDMismatch / ErrPwdMismatch, so the discovery code path must
// not use it.
//
// This decoder validates header magic, protocol type, ID-size byte, and
// the trailing checksum, then extracts the device's own 16-byte ID from
// the frame, the function code, and the DATA block. Auth-failure replies
// (FUNC=0x07) surface as ErrAuth in the same way DecodeResponse does.
//
// On success returns (frameDeviceID, function, dataBlock, nil). The
// returned slices alias raw — copy if you intend to retain them past the
// next read.
func DecodeDiscoveryResponse(raw []byte) (string, byte, []byte, error) {
	if len(raw) < 24 {
		return "", 0, nil, ErrTruncated
	}
	if raw[0] != headerByte0 || raw[1] != headerByte1 {
		return "", 0, nil, ErrBadHeader
	}
	if raw[2] != protoType {
		return "", 0, nil, fmt.Errorf("%w: TYPE=0x%02x", ErrBadHeader, raw[2])
	}
	if raw[3] != idSize {
		return "", 0, nil, fmt.Errorf("%w: SIZE_ID=0x%02x", ErrBadHeader, raw[3])
	}

	idStart := 4
	idEnd := idStart + int(idSize) // 20
	frameID := string(raw[idStart:idEnd])

	sizePwdIdx := idEnd
	sizePwd := int(raw[sizePwdIdx])
	if sizePwd > 8 {
		return "", 0, nil, fmt.Errorf("%w: SIZE_PWD=%d", ErrBadHeader, sizePwd)
	}
	pwdEnd := sizePwdIdx + 1 + sizePwd
	// FUNC + at least 0 bytes data + 2 cksum after PWD.
	if pwdEnd+1+2 > len(raw) {
		return "", 0, nil, ErrTruncated
	}

	funcIdx := pwdEnd
	function := raw[funcIdx]
	dataStart := funcIdx + 1
	dataEnd := len(raw) - 2
	if dataEnd < dataStart {
		return "", 0, nil, ErrTruncated
	}

	var sum uint16
	for _, b := range raw[2:dataEnd] {
		sum += uint16(b)
	}
	want := uint16(raw[dataEnd]) | uint16(raw[dataEnd+1])<<8
	if sum != want {
		return "", function, nil, ErrChecksum
	}

	if function == FuncAuthFailure {
		return frameID, function, raw[dataStart:dataEnd], ErrAuth
	}
	return frameID, function, raw[dataStart:dataEnd], nil
}

// BuildReadDataBlock composes the DATA block for a read request covering
// arbitrary parameter IDs. Pages (high bytes) are switched transparently
// with FF <hi> commands as needed. The default page at packet start is
// 0x00 — an FF prefix is emitted for the first ID iff its high byte is
// non-zero.
//
// Panics with ErrReservedParamID if any id's low byte is in the reserved
// 0xFC-0xFF range (such IDs would alias to the protocol's special-command
// bytes inside DATA and silently corrupt traffic). No documented parameter
// uses this range; the panic surfaces caller bugs loudly.
func BuildReadDataBlock(ids []ParamID) []byte {
	if len(ids) == 0 {
		return nil
	}
	out := make([]byte, 0, len(ids)*2)
	curHi := byte(0x00)
	first := true
	for _, id := range ids {
		hi := byte(id >> 8)
		lo := byte(id & 0xFF)
		if isReservedLowByte(lo) {
			panic(fmt.Sprintf("%s: 0x%04X", ErrReservedParamID.Error(), uint16(id)))
		}
		if first || hi != curHi {
			if hi != curHi || (first && hi != 0x00) {
				out = append(out, cmdChangeHighByte, hi)
				curHi = hi
			}
			first = false
		}
		out = append(out, lo)
	}
	return out
}

// BuildWriteDataBlock composes the DATA block for a write request. For
// 1-byte values, emits <id_low> <value>. For multi-byte values, emits
// FE <size> <id_low> <bytes...>. Pages are switched transparently as
// in BuildReadDataBlock.
//
// Panics with ErrReservedParamID if any write's ID has a low byte in the
// reserved 0xFC-0xFF range — see BuildReadDataBlock for the rationale.
func BuildWriteDataBlock(writes []ParamWrite) []byte {
	if len(writes) == 0 {
		return nil
	}
	out := make([]byte, 0, len(writes)*3)
	curHi := byte(0x00)
	first := true
	for _, w := range writes {
		hi := byte(w.ID >> 8)
		lo := byte(w.ID & 0xFF)
		if isReservedLowByte(lo) {
			panic(fmt.Sprintf("%s: 0x%04X", ErrReservedParamID.Error(), uint16(w.ID)))
		}
		if first || hi != curHi {
			if hi != curHi || (first && hi != 0x00) {
				out = append(out, cmdChangeHighByte, hi)
				curHi = hi
			}
			first = false
		}
		switch len(w.Value) {
		case 0:
			// A zero-length write isn't meaningful in this protocol —
			// emitting just the ID would alias to a read. Skip.
			continue
		case 1:
			out = append(out, lo, w.Value[0])
		default:
			if len(w.Value) > 0xFF {
				// Defensive — protocol size field is 1 byte. Caller
				// would have to be doing something weird to hit this.
				panic(fmt.Sprintf("breezy: write value too large (%d bytes) for FE size prefix", len(w.Value)))
			}
			out = append(out, cmdSizePrefix, byte(len(w.Value)), lo)
			out = append(out, w.Value...)
		}
	}
	return out
}

// ParseDataBlock decodes a response's DATA block into a sequence of
// (id, value) entries. Recognises:
//   - FE <size> <id_lo> <bytes...> for explicit-size values
//   - FF <hi>                    to switch high page
//   - FD <id_lo>                 device's "parameter not supported" marker
//
// Anything else is treated as a 1-byte value: <id_lo> <byte>.
//
// The default page at the start of the block is 0x00, matching the
// wire convention.
func ParseDataBlock(data []byte) ([]ParamValue, error) {
	if len(data) == 0 {
		return nil, nil
	}
	out := make([]ParamValue, 0, 4)
	curHi := byte(0x00)

	for i := 0; i < len(data); {
		b := data[i]
		switch b {
		case cmdChangeHighByte:
			if i+1 >= len(data) {
				return nil, fmt.Errorf("%w: FF without high byte", ErrInvalidData)
			}
			curHi = data[i+1]
			i += 2

		case cmdNotSupported:
			if i+1 >= len(data) {
				return nil, fmt.Errorf("%w: FD without param id", ErrInvalidData)
			}
			lo := data[i+1]
			out = append(out, ParamValue{
				ID:          ParamID(uint16(curHi)<<8 | uint16(lo)),
				Unsupported: true,
			})
			i += 2

		case cmdSizePrefix:
			if i+2 >= len(data) {
				return nil, fmt.Errorf("%w: FE without size and id", ErrInvalidData)
			}
			size := int(data[i+1])
			lo := data[i+2]
			valStart := i + 3
			valEnd := valStart + size
			if valEnd > len(data) {
				return nil, fmt.Errorf("%w: FE %d truncated (need %d bytes)", ErrInvalidData, size, size)
			}
			val := make([]byte, size)
			copy(val, data[valStart:valEnd])
			out = append(out, ParamValue{
				ID:    ParamID(uint16(curHi)<<8 | uint16(lo)),
				Value: val,
			})
			i = valEnd

		case cmdChangeFunc:
			// FC <new_func>: the protocol allows a packet to switch FUNC
			// for the rest of its DATA. We've never observed real firmware
			// emitting this; surfacing it as an error means we'd hear about
			// it if the assumption breaks. The decoded prefix (entries
			// already in `out`) is returned alongside the error so callers
			// can still see what came before the FC.
			//
			// TODO: if a real-world device is observed using FC, relax this
			// to a "soft" sentinel (e.g. a flag on the returned slice) and
			// teach Client.exec how to handle the function switch.
			if i+1 >= len(data) {
				return out, fmt.Errorf("%w: FC without new func", ErrInvalidData)
			}
			return out, fmt.Errorf("%w: FC at offset %d switching to FUNC=0x%02x",
				ErrUnexpectedFuncChange, i, data[i+1])

		default:
			// Implicit 1-byte value: <id_lo> <value>.
			if i+1 >= len(data) {
				return nil, fmt.Errorf("%w: implicit 1-byte param 0x%02x missing value", ErrInvalidData, b)
			}
			lo := b
			val := []byte{data[i+1]}
			out = append(out, ParamValue{
				ID:    ParamID(uint16(curHi)<<8 | uint16(lo)),
				Value: val,
			})
			i += 2
		}
	}
	return out, nil
}
