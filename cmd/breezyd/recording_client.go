// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// recordingClient wraps a breezy.DeviceClient with a per-write callback.
// The callback fires after every successful WriteParams; failed writes
// do not record. This lets handlers call pkg/breezy/ops without each
// one remembering to invoke h.recordWrite — the wrapper does it.
//
// Close is intentionally absent: recordingClient satisfies
// breezy.DeviceClient (ReadParams + WriteParams only), not
// cmd/breezyd.HandlerClient (which adds Close). Callers must hold the
// underlying HandlerClient — typically the value returned by h.dial —
// and defer its Close directly:
//
//	rc, raw, err := h.dialRecording(name)
//	if err != nil { ... }
//	defer raw.Close()
type recordingClient struct {
	inner  breezy.DeviceClient
	record func([]breezy.ParamWrite)
}

// Compile-time check that recordingClient satisfies breezy.DeviceClient.
// Without this, a future change to the interface would silently break
// call sites instead of failing here.
var _ breezy.DeviceClient = (*recordingClient)(nil)

// newRecordingClient wraps inner with a write-callback.
func newRecordingClient(inner breezy.DeviceClient, record func([]breezy.ParamWrite)) *recordingClient {
	return &recordingClient{inner: inner, record: record}
}

// IsLocal delegates to the inner client.
func (r *recordingClient) IsLocal() bool { return r.inner.IsLocal() }

// ReadParams delegates without recording.
func (r *recordingClient) ReadParams(ctx context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
	return r.inner.ReadParams(ctx, ids)
}

// WriteParams writes via the inner client and records the writes on
// success. On error, record is not called.
func (r *recordingClient) WriteParams(ctx context.Context, writes []breezy.ParamWrite) error {
	if err := r.inner.WriteParams(ctx, writes); err != nil {
		return err
	}
	if r.record != nil {
		r.record(writes)
	}
	return nil
}
