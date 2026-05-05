// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/internal/config"
	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
	"github.com/hughobrien/breezyd/pkg/homekit"
)

func TestHomekit_PinPersists(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pin1, err := loadOrGeneratePin(dir)
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if len(pin1) != 8 {
		t.Errorf("pin length = %d, want 8", len(pin1))
	}
	if weakPins[pin1] {
		t.Errorf("generated pin %q is in weak list", pin1)
	}
	pin2, err := loadOrGeneratePin(dir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if pin1 != pin2 {
		t.Errorf("PIN changed across runs: %q != %q", pin1, pin2)
	}
}

func TestHomekit_PinFileMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := loadOrGeneratePin(dir); err != nil {
		t.Fatalf("generate: %v", err)
	}
	st, err := os.Stat(filepath.Join(dir, "pin.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("pin.txt mode = %#o, want 0600", mode)
	}
}

func TestHomekit_PinFormat(t *testing.T) {
	if got := formatPinDisplay("12345678"); got != "123-45-678" {
		t.Errorf("formatPinDisplay = %q, want '123-45-678'", got)
	}
}

func TestHomekit_StartDisabledIsNoop(t *testing.T) {
	h := &Handler{}
	stop, err := h.StartHomekit(context.Background(), config.Homekit{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("StartHomekit: %v", err)
	}
	if stop == nil {
		t.Fatal("stop is nil")
	}
	if err := stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
}

// homekitSnapshotPath returns the absolute path to the fakedevice snapshot.
func homekitSnapshotPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../pkg/breezy/fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

// TestHomekit_SyncRoundTrip starts a HomeKit bridge against a fakedevice,
// calls SyncHomekit with a snapshot containing humidity=65, and asserts
// the HumiditySensor's CurrentRelativeHumidity characteristic reflects 65.
func TestHomekit_SyncRoundTrip(t *testing.T) {
	const (
		devID   = "TESTID0000000001"
		devPwd  = "1111"
		devName = "playroom"
		wantHum = 65.0
	)

	// Start fakedevice so the DeviceConfig has a valid IP.
	srv, err := fakedevice.NewServer(homekitSnapshotPath(t), devID, devPwd)
	if err != nil {
		t.Fatalf("fakedevice.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	stateDir := t.TempDir()
	devices := NewDeviceRegistry(map[string]DeviceConfig{
		devName: {
			ID:       devID,
			Password: devPwd,
			IP:       srv.Addr(),
		},
	})

	state := NewState()
	h := &Handler{
		State:   state,
		Devices: devices,
	}

	cfg := config.Homekit{
		Enabled:    true,
		BridgeName: "test-bridge",
		StateDir:   stateDir,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stop, err := h.StartHomekit(ctx, cfg, devices.Snapshot())
	if err != nil {
		t.Fatalf("StartHomekit: %v", err)
	}
	t.Cleanup(func() { _ = stop() })

	// Build a snapshot with humidity=65 (param 0x0025 is a single byte).
	snap := Snapshot{
		IP:       srv.Addr(),
		LastPoll: time.Now(),
		Values: map[breezy.ParamID][]byte{
			0x0025: {byte(wantHum)},
		},
	}

	h.SyncHomekit(devName, snap)

	// Retrieve the accessory and check the characteristic.
	a, ok := h.homekitAccessories[devName]
	if !ok {
		t.Fatalf("accessory %q not found in homekitAccessories", devName)
	}

	got := a.Humidity.CurrentRelativeHumidity.Value()
	if got != wantHum {
		t.Errorf("CurrentRelativeHumidity = %v, want %v", got, wantHum)
	}
}

// TestHomekit_SyncNilMap is a no-op guard: SyncHomekit returns early when
// homekitAccessories is nil (HomeKit disabled).
func TestHomekit_SyncNilMap(t *testing.T) {
	h := &Handler{
		Devices: NewDeviceRegistry(map[string]DeviceConfig{
			"x": {ID: "ID", Password: "pw", IP: "127.0.0.1:4000"},
		}),
	}
	// Must not panic.
	h.SyncHomekit("x", Snapshot{})
}

// TestHomekit_SyncUnknownDevice is a no-op guard: SyncHomekit returns early
// when the device name is absent from homekitAccessories.
func TestHomekit_SyncUnknownDevice(t *testing.T) {
	h := &Handler{
		homekitAccessories: make(map[string]*homekit.Accessory),
		Devices: NewDeviceRegistry(map[string]DeviceConfig{
			"x": {ID: "ID", Password: "pw", IP: "127.0.0.1:4000"},
		}),
	}
	// Must not panic — "y" is not in the map.
	h.SyncHomekit("y", Snapshot{})
}
