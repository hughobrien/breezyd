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
// recordingClient does not implement Close: the underlying client's
// Close (e.g. *breezy.Client.Close) is still available via the original
// reference. Handlers that need to close the client should hold onto
// the inner *breezy.Client and `defer raw.Close()` before wrapping.
type recordingClient struct {
	inner  breezy.DeviceClient
	record func([]breezy.ParamWrite)
}

// newRecordingClient wraps inner with a write-callback.
func newRecordingClient(inner breezy.DeviceClient, record func([]breezy.ParamWrite)) *recordingClient {
	return &recordingClient{inner: inner, record: record}
}

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
