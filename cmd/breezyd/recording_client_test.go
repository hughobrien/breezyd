// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

// stubInner is a breezy.DeviceClient where ReadParams and WriteParams
// are configurable per test.
type stubInner struct {
	readResp   map[breezy.ParamID][]byte
	readErr    error
	writeErr   error
	writeCalls [][]breezy.ParamWrite
}

func (s *stubInner) ReadParams(_ context.Context, _ []breezy.ParamID) (map[breezy.ParamID][]byte, error) {
	return s.readResp, s.readErr
}
func (s *stubInner) WriteParams(_ context.Context, ws []breezy.ParamWrite) error {
	s.writeCalls = append(s.writeCalls, ws)
	return s.writeErr
}
func (s *stubInner) IsLocal() bool { return false }

func TestRecordingClient_WriteSuccessFiresCallback(t *testing.T) {
	is := is.New(t)
	inner := &stubInner{}
	var recorded [][]breezy.ParamWrite
	rc := newRecordingClient(inner, func(ws []breezy.ParamWrite) { recorded = append(recorded, ws) })

	ws := []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}}
	is.NoErr(rc.WriteParams(context.Background(), ws))
	is.Equal(len(recorded), 1) // one callback fires per successful write
	is.Equal(recorded[0], ws)  // callback receives the writes verbatim
}

func TestRecordingClient_WriteFailureSuppressesCallback(t *testing.T) {
	is := is.New(t)
	inner := &stubInner{writeErr: errors.New("boom")}
	called := false
	rc := newRecordingClient(inner, func([]breezy.ParamWrite) { called = true })

	err := rc.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}})
	is.True(err != nil)     // inner write failure must propagate
	is.Equal(called, false) // callback must NOT fire on inner write failure
}

func TestRecordingClient_ReadDoesNotRecord(t *testing.T) {
	is := is.New(t)
	inner := &stubInner{readResp: map[breezy.ParamID][]byte{0x0001: {1}}}
	called := false
	rc := newRecordingClient(inner, func([]breezy.ParamWrite) { called = true })

	_, err := rc.ReadParams(context.Background(), []breezy.ParamID{0x0001})
	is.NoErr(err)
	is.Equal(called, false) // callback must NEVER fire on reads
}

func TestRecordingClient_NilCallback(t *testing.T) {
	is := is.New(t)
	inner := &stubInner{}
	rc := newRecordingClient(inner, nil)
	is.NoErr(rc.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}}))
}
