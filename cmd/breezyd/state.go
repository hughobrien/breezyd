package main

import (
	"sort"
	"sync"
	"time"

	"github.com/hughobrien/twinfresh/pkg/breezy"
)

// Snapshot is the latest known state of a single device. Values holds the raw
// little-endian bytes the device returned for each parameter; decoding is the
// caller's responsibility (see pkg/breezy.Param.Decode).
type Snapshot struct {
	// IP is the current/last-known IP for the device.
	IP string
	// Values are the raw param values (LE bytes), keyed by ParamID, exactly as
	// the device returned them on the most recent successful poll.
	Values map[breezy.ParamID][]byte
	// LastPoll is the wall-clock time of the most recent poll attempt.
	LastPoll time.Time
	// LastErr is the error from the most recent poll attempt, or nil on success.
	LastErr error
}

// State is the in-memory cache of the most recent Snapshot for each configured
// device, keyed by device name. It is safe for concurrent use; readers see
// deep copies of the stored snapshot so they may mutate freely without racing
// the poller's next write.
type State struct {
	mu    sync.RWMutex
	snaps map[string]Snapshot
}

// NewState returns an empty State.
func NewState() *State {
	return &State{snaps: map[string]Snapshot{}}
}

// Get returns a deep copy of the snapshot for name, or the zero Snapshot and
// false if no snapshot exists. Callers may freely mutate the returned
// Snapshot's Values map and byte slices.
func (s *State) Get(name string) (Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snap, ok := s.snaps[name]
	if !ok {
		return Snapshot{}, false
	}
	return cloneSnap(snap), true
}

// Set replaces the snapshot for name atomically. The Values map and its byte
// slices are deep-copied; the caller is free to mutate the source after Set
// returns.
func (s *State) Set(name string, snap Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snaps[name] = cloneSnap(snap)
}

// UpdateIP updates only the IP for name, leaving Values, LastPoll, and LastErr
// untouched. If no snapshot exists for name, one is created with just IP set.
// Used by discovery when a device's IP changes without affecting the cached
// poll data.
func (s *State) UpdateIP(name, ip string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := s.snaps[name]
	snap.IP = ip
	s.snaps[name] = snap
}

// RecordPoll sets all fields of the snapshot atomically. It is equivalent to
// Set but named for clarity at the poller call site.
func (s *State) RecordPoll(name string, snap Snapshot) {
	s.Set(name, snap)
}

// Devices returns a sorted snapshot of the device names currently in the
// cache. The returned slice is independent of internal state and safe to
// iterate without holding the lock.
func (s *State) Devices() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.snaps))
	for name := range s.snaps {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Delete removes the snapshot for name. It is a no-op if no snapshot exists.
// Useful when a device is removed from the config at reload time.
func (s *State) Delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.snaps, name)
}

// cloneSnap returns a deep copy of src: scalar fields are copied by value, and
// the Values map plus each byte slice is duplicated so the result shares no
// mutable state with src.
func cloneSnap(src Snapshot) Snapshot {
	out := src
	if src.Values != nil {
		out.Values = make(map[breezy.ParamID][]byte, len(src.Values))
		for k, v := range src.Values {
			cp := make([]byte, len(v))
			copy(cp, v)
			out.Values[k] = cp
		}
	}
	return out
}
