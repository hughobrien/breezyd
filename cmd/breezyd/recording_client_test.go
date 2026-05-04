// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
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

func TestRecordingClient_WriteSuccessFiresCallback(t *testing.T) {
	inner := &stubInner{}
	var recorded [][]breezy.ParamWrite
	rc := newRecordingClient(inner, func(ws []breezy.ParamWrite) { recorded = append(recorded, ws) })

	ws := []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}}
	if err := rc.WriteParams(context.Background(), ws); err != nil {
		t.Fatalf("WriteParams: %v", err)
	}
	if len(recorded) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(recorded))
	}
	if !reflect.DeepEqual(recorded[0], ws) {
		t.Errorf("callback got %v, want %v", recorded[0], ws)
	}
}

func TestRecordingClient_WriteFailureSuppressesCallback(t *testing.T) {
	inner := &stubInner{writeErr: errors.New("boom")}
	called := false
	rc := newRecordingClient(inner, func([]breezy.ParamWrite) { called = true })

	err := rc.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}})
	if err == nil {
		t.Fatal("expected error from inner, got nil")
	}
	if called {
		t.Error("callback must NOT fire on inner write failure")
	}
}

func TestRecordingClient_ReadDoesNotRecord(t *testing.T) {
	inner := &stubInner{readResp: map[breezy.ParamID][]byte{0x0001: {1}}}
	called := false
	rc := newRecordingClient(inner, func([]breezy.ParamWrite) { called = true })

	if _, err := rc.ReadParams(context.Background(), []breezy.ParamID{0x0001}); err != nil {
		t.Fatalf("ReadParams: %v", err)
	}
	if called {
		t.Error("callback must NEVER fire on reads")
	}
}

func TestRecordingClient_NilCallback(t *testing.T) {
	inner := &stubInner{}
	rc := newRecordingClient(inner, nil)
	if err := rc.WriteParams(context.Background(), []breezy.ParamWrite{{ID: 0x0001, Value: []byte{1}}}); err != nil {
		t.Fatalf("WriteParams with nil callback should succeed silently, got: %v", err)
	}
}
