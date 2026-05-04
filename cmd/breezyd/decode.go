// SPDX-License-Identifier: GPL-3.0-or-later

package main

// Snapshot decode helpers shared by server.go (HTTP JSON shape) and
// metrics.go (Prometheus gauges). These are tiny — a few lines each —
// but were duplicated across both files; lifting them here keeps the
// "is the byte length right?" decision in one place.

import (
	"encoding/binary"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// uint8At returns the single byte stored at id, or (0, false) if the
// value is missing or wrong-sized.
func uint8At(snap Snapshot, id breezy.ParamID) (uint8, bool) {
	raw, ok := snap.Values[id]
	if !ok || len(raw) != 1 {
		return 0, false
	}
	return raw[0], true
}

// uint16At returns the LE 2-byte value at id.
func uint16At(snap Snapshot, id breezy.ParamID) (uint16, bool) {
	raw, ok := snap.Values[id]
	if !ok || len(raw) != 2 {
		return 0, false
	}
	return binary.LittleEndian.Uint16(raw), true
}

// int16At returns the LE 2-byte signed value at id.
func int16At(snap Snapshot, id breezy.ParamID) (int16, bool) {
	v, ok := uint16At(snap, id)
	if !ok {
		return 0, false
	}
	return int16(v), true
}
