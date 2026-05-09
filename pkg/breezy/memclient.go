// SPDX-License-Identifier: GPL-3.0-or-later

package breezy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
)

// MemClient is an in-process DeviceClient backed by a parameter-byte map.
// Production code paths use *Client (UDP); MemClient is for UI dev and
// Playwright tests that don't need the wire path. Reads return a snapshot
// of the configured params; writes mutate them in place.
//
// Fault-injection knobs (SetAuthFailureMode, SetTimeoutMode, Reset) make
// MemClient a drop-in for the previous fakedevice.Server admin surface.
type MemClient struct {
	mu            sync.RWMutex
	params        map[ParamID][]byte
	initial       map[ParamID][]byte // for Reset()
	forceAuth     bool
	forceTimeout  bool
}

// NewMemClient builds a MemClient with the given initial param bytes.
// The map is copied; subsequent edits to the caller's map don't leak
// into the client. A nil seed is valid and produces an empty client.
func NewMemClient(seed map[ParamID][]byte) *MemClient {
	p := make(map[ParamID][]byte, len(seed))
	snap := make(map[ParamID][]byte, len(seed))
	for k, v := range seed {
		b := append([]byte(nil), v...)
		p[k] = b
		snap[k] = append([]byte(nil), v...)
	}
	return &MemClient{params: p, initial: snap}
}

// IsLocal returns true: MemClient is an in-process client with no network I/O.
func (m *MemClient) IsLocal() bool { return true }

// Close is a no-op; MemClient holds no network resources.
func (m *MemClient) Close() error { return nil }

// ReadParams returns the stored bytes for the requested IDs. Absent IDs are
// omitted from the map (matching *Client semantics for unsupported params).
// Returns ErrAuth or ErrTimeout if fault-injection is active.
func (m *MemClient) ReadParams(ctx context.Context, ids []ParamID) (map[ParamID][]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.forceAuth {
		return nil, ErrAuth
	}
	if m.forceTimeout {
		return nil, ErrTimeout
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make(map[ParamID][]byte, len(ids))
	for _, id := range ids {
		if b, ok := m.params[id]; ok {
			out[id] = append([]byte(nil), b...)
		}
		// Absent ID: omit from map — callers detect missing-key form.
	}
	return out, nil
}

// WriteParams stores the supplied values, replacing any prior bytes for each
// written ID. Returns ErrAuth or ErrTimeout if fault-injection is active.
func (m *MemClient) WriteParams(ctx context.Context, writes []ParamWrite) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.forceAuth {
		return ErrAuth
	}
	if m.forceTimeout {
		return ErrTimeout
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, w := range writes {
		m.params[w.ID] = append([]byte(nil), w.Value...)
	}
	return nil
}

// SetAuthFailureMode toggles whether every subsequent ReadParams/WriteParams
// call returns ErrAuth. Pass false to restore normal operation.
func (m *MemClient) SetAuthFailureMode(force bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forceAuth = force
}

// SetTimeoutMode toggles whether every subsequent ReadParams/WriteParams
// call returns ErrTimeout. Pass false to restore normal operation.
func (m *MemClient) SetTimeoutMode(force bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.forceTimeout = force
}

// SetParamValue overwrites one param's bytes. Used by the test admin surface
// to stage specific device states mid-test.
func (m *MemClient) SetParamValue(id ParamID, value []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.params[id] = append([]byte(nil), value...)
}

// Reset restores params to the construction-time snapshot and clears any
// active fault-injection flags.
func (m *MemClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := make(map[ParamID][]byte, len(m.initial))
	for k, v := range m.initial {
		p[k] = append([]byte(nil), v...)
	}
	m.params = p
	m.forceAuth = false
	m.forceTimeout = false
}

// NewMemClientFromFile builds a MemClient seeded from a fakedevice JSON
// snapshot file. The file format matches pkg/breezy/fakedevice/snapshot_*.json:
// a JSON object with a "params" key whose values are 4-hex-digit ParamIDs
// mapped to hex-encoded little-endian value bytes.
func NewMemClientFromFile(path string) (*MemClient, error) {
	seed, err := loadSnapshotJSON(path)
	if err != nil {
		return nil, fmt.Errorf("memclient: %w", err)
	}
	return NewMemClient(seed), nil
}

// snapshotFile is the on-disk JSON shape shared with fakedevice.
type snapshotFile struct {
	Params map[string]string `json:"params"`
}

// loadSnapshotJSON parses a fakedevice-format JSON snapshot. It is a local
// copy of fakedevice.LoadSnapshotJSON, duplicated here to avoid an import
// cycle (pkg/breezy/fakedevice already imports pkg/breezy).
func loadSnapshotJSON(path string) (map[ParamID][]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	var snap snapshotFile
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}
	out := make(map[ParamID][]byte, len(snap.Params))
	for k, v := range snap.Params {
		idU, err := strconv.ParseUint(k, 16, 16)
		if err != nil {
			return nil, fmt.Errorf("bad param key %q: %w", k, err)
		}
		// Empty value means "unsupported" — omit from map.
		if v == "" {
			continue
		}
		val, err := hex.DecodeString(v)
		if err != nil {
			return nil, fmt.Errorf("bad value for %q: %w", k, err)
		}
		// Old format: "fd<id>" marker means unsupported — omit.
		if len(val) == 2 && val[0] == 0xFD {
			continue
		}
		out[ParamID(idU)] = val
	}
	return out, nil
}
