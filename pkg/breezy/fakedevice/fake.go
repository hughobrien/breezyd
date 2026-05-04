// Package fakedevice provides an in-process UDP server that speaks the
// Vents Breezy FDFD/02 protocol from a captured parameter snapshot. It's
// used by daemon and client tests to exercise the full HTTP -> state ->
// UDP -> param-decode path without real hardware.
//
// The server is intentionally minimal: it answers reads from an in-memory
// map of values seeded from a JSON snapshot, applies writes to that map,
// echoes wrong-password requests with FUNC=0x07, and silently drops
// requests whose deviceID echo doesn't match (matching real device
// behavior). It does NOT model firmware quirks like retries, ramping, or
// timing.
package fakedevice

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"

	"github.com/hughobrien/twinfresh/pkg/breezy"
)

// Server is an in-process UDP fake of a Breezy ERV.
type Server struct {
	deviceID string
	password string

	mu     sync.Mutex
	values map[breezy.ParamID][]byte // per-param value bytes (LE)
	closed bool

	conn *net.UDPConn
	done chan struct{}
}

// snapshot is the on-disk JSON shape of the parameter map. Keys are
// uppercase hex of the full uint16 ParamID, zero-padded to 4 chars.
// Values are hex of the value bytes (LE) — i.e. just the bytes that go
// after the FE/<id_low> framing in a response, without any framing prefix.
type snapshot struct {
	Params map[string]string `json:"params"`
}

// NewServer loads the snapshot from snapshotPath and starts a UDP listener
// on 127.0.0.1 with an ephemeral port. The deviceID must be exactly 16
// ASCII bytes (Breezy convention). password may be empty.
func NewServer(snapshotPath, deviceID, password string) (*Server, error) {
	if len(deviceID) != 16 {
		return nil, fmt.Errorf("fakedevice: deviceID must be 16 bytes, got %d", len(deviceID))
	}
	if len(password) > 8 {
		return nil, fmt.Errorf("fakedevice: password must be <= 8 bytes, got %d", len(password))
	}

	values, err := loadSnapshot(snapshotPath)
	if err != nil {
		return nil, err
	}

	addr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("fakedevice: ResolveUDPAddr: %w", err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("fakedevice: ListenUDP: %w", err)
	}

	s := &Server{
		deviceID: deviceID,
		password: password,
		values:   values,
		conn:     conn,
		done:     make(chan struct{}),
	}
	go s.serve()
	return s, nil
}

func loadSnapshot(path string) (map[breezy.ParamID][]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fakedevice: read snapshot: %w", err)
	}
	var snap snapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("fakedevice: parse snapshot: %w", err)
	}
	out := make(map[breezy.ParamID][]byte, len(snap.Params))
	for k, v := range snap.Params {
		idU, err := strconv.ParseUint(k, 16, 16)
		if err != nil {
			return nil, fmt.Errorf("fakedevice: bad param key %q: %w", k, err)
		}
		// Empty value or "fd<id>" means "unsupported" — skip; absence in
		// the map is the canonical "unsupported" state.
		if v == "" {
			continue
		}
		val, err := hex.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("fakedevice: bad value for %q: %w", k, err)
		}
		// Defensive: an old format might encode "fd<id>" as the value;
		// treat as unsupported.
		if len(val) == 2 && val[0] == 0xFD {
			continue
		}
		out[breezy.ParamID(idU)] = val
	}
	return out, nil
}

// Addr returns the listener address as "host:port".
func (s *Server) Addr() string {
	return s.conn.LocalAddr().String()
}

// Close shuts down the listener. Multiple Close calls are safe.
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	err := s.conn.Close()
	<-s.done
	return err
}

func (s *Server) serve() {
	defer close(s.done)
	buf := make([]byte, 2048)
	for {
		n, peer, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			// On Close, the read returns with an error; we treat any
			// error as terminal — the test harness just creates a new
			// Server if it needs to retry.
			return
		}
		// Copy req into a fresh slice — handle may run synchronously here,
		// but defensive copying lets us add concurrency later without
		// re-thinking aliasing on buf.
		req := make([]byte, n)
		copy(req, buf[:n])
		s.handle(req, peer)
	}
}

// handle parses one request and dispatches based on FUNC. Errors that
// aren't auth failures (bad header, bad ID, checksum, etc.) are silently
// dropped — that's how real devices behave.
func (s *Server) handle(req []byte, peer *net.UDPAddr) {
	fn, data, err := breezy.DecodeResponse(req, s.deviceID, s.password)
	if err != nil {
		switch {
		case errors.Is(err, breezy.ErrPwdMismatch):
			// Request used a password our codec didn't accept. Mirror
			// firmware: emit FUNC=0x07. Echo the *client's* password
			// back so the client can decode the response.
			clientPwd, ok := extractRequestPassword(req)
			if !ok {
				return
			}
			s.sendAuthFailure(peer, clientPwd)
			return
		case errors.Is(err, breezy.ErrIDMismatch):
			// Real device behavior: silently drop.
			return
		default:
			// Bad header / checksum / truncated — drop.
			return
		}
	}

	switch fn {
	case breezy.FuncRead:
		s.handleRead(data, peer)
	case breezy.FuncWriteWithReply:
		s.handleWrite(data, peer, true)
	case breezy.FuncWriteNoResponse:
		s.handleWrite(data, peer, false)
	default:
		// Unknown function — drop.
	}
}

// extractRequestPassword pulls the SIZE_PWD-prefixed password from a
// well-formed request frame. Returns ("", false) if the frame is too
// short or the size byte is out of range. Used only to echo the client's
// password back in an auth-failure response.
func extractRequestPassword(raw []byte) (string, bool) {
	// Layout: 2 magic + 1 type + 1 size_id + 16 id + 1 size_pwd + ... pwd ...
	const sizePwdIdx = 20
	if len(raw) <= sizePwdIdx {
		return "", false
	}
	sizePwd := int(raw[sizePwdIdx])
	if sizePwd > 8 {
		return "", false
	}
	pwdStart := sizePwdIdx + 1
	pwdEnd := pwdStart + sizePwd
	if pwdEnd > len(raw) {
		return "", false
	}
	return string(raw[pwdStart:pwdEnd]), true
}

// sendAuthFailure replies with FUNC=0x07 and a 2-byte payload (01 00).
// The second byte's exact value isn't load-bearing for callers; we use 0.
// The password echoed in the response must match what the client sent so
// the codec on the client side accepts the frame.
func (s *Server) sendAuthFailure(peer *net.UDPAddr, clientPwd string) {
	resp := breezy.EncodeRequest(s.deviceID, clientPwd, breezy.FuncAuthFailure, []byte{0x01, 0x00})
	_, _ = s.conn.WriteToUDP(resp, peer)
}

// handleRead parses the request data block (which is a sequence of param
// IDs framed via FF for high pages and bare bytes for the page-0 IDs),
// looks up each value, and emits a response.
func (s *Server) handleRead(reqData []byte, peer *net.UDPAddr) {
	ids := parseReadDataBlock(reqData)
	respData := s.buildResponseDataBlock(ids)
	resp := breezy.EncodeRequest(s.deviceID, s.password, breezy.FuncResponse, respData)
	_, _ = s.conn.WriteToUDP(resp, peer)
}

// handleWrite parses the request data block (which is a sequence of param
// writes framed like a write request), applies them to the in-memory
// state, and — if reply is true — echoes the updated values back.
func (s *Server) handleWrite(reqData []byte, peer *net.UDPAddr, reply bool) {
	writes, err := breezy.ParseDataBlock(reqData)
	if err != nil {
		// Malformed write — drop.
		return
	}

	ids := make([]breezy.ParamID, 0, len(writes))
	s.mu.Lock()
	for _, w := range writes {
		if w.Unsupported {
			// FD in a write request — nonsensical, skip.
			continue
		}
		// Copy the value to avoid aliasing the request buffer.
		val := make([]byte, len(w.Value))
		copy(val, w.Value)
		s.values[w.ID] = val
		ids = append(ids, w.ID)
	}
	s.mu.Unlock()

	if !reply {
		return
	}
	respData := s.buildResponseDataBlock(ids)
	resp := breezy.EncodeRequest(s.deviceID, s.password, breezy.FuncResponse, respData)
	_, _ = s.conn.WriteToUDP(resp, peer)
}

// parseReadDataBlock walks the request data block of a FUNC=0x01 read,
// resolving FF <hi> page transitions to surface a flat slice of full
// uint16 ParamIDs. We can't reuse ParseDataBlock because that one expects
// every byte that isn't FF/FD/FE to be a 1-byte VALUE pair (id + byte) —
// in a read request we have just the id_low.
func parseReadDataBlock(data []byte) []breezy.ParamID {
	if len(data) == 0 {
		return nil
	}
	out := make([]breezy.ParamID, 0, len(data))
	curHi := byte(0x00)
	for i := 0; i < len(data); {
		b := data[i]
		switch b {
		case 0xFF:
			if i+1 >= len(data) {
				return out
			}
			curHi = data[i+1]
			i += 2
		case 0xFC:
			// Function-change command in a request: skip the new func byte.
			if i+1 >= len(data) {
				return out
			}
			i += 2
		default:
			out = append(out, breezy.ParamID(uint16(curHi)<<8|uint16(b)))
			i++
		}
	}
	return out
}

// buildResponseDataBlock emits a response data block for the given IDs.
// For each ID:
//   - 1-byte value: <id_low> <value>
//   - multi-byte value: FE <size> <id_low> <bytes...>
//   - missing: FD <id_low>
//
// Page transitions are emitted via FF <hi>. The default page at the start
// is 0x00.
func (s *Server) buildResponseDataBlock(ids []breezy.ParamID) []byte {
	if len(ids) == 0 {
		return nil
	}
	out := make([]byte, 0, len(ids)*3)
	curHi := byte(0x00)
	first := true

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		hi := byte(id >> 8)
		lo := byte(id & 0xFF)
		if first || hi != curHi {
			if hi != curHi || (first && hi != 0x00) {
				out = append(out, 0xFF, hi)
				curHi = hi
			}
			first = false
		}
		val, ok := s.values[id]
		if !ok {
			out = append(out, 0xFD, lo)
			continue
		}
		switch len(val) {
		case 0:
			// Treat zero-length as unsupported — an empty value is
			// indistinguishable from "no value" on the wire.
			out = append(out, 0xFD, lo)
		case 1:
			out = append(out, lo, val[0])
		default:
			if len(val) > 0xFF {
				// Defensive: snapshot shouldn't have values this large.
				out = append(out, 0xFD, lo)
				continue
			}
			out = append(out, 0xFE, byte(len(val)), lo)
			out = append(out, val...)
		}
	}
	return out
}
