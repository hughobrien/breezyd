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
	"github.com/matryer/is"
)

func TestHomekit_PinPersists(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	is.NoErr(os.MkdirAll(dir, 0o700))
	pin1, err := loadOrGeneratePin(dir)
	is.NoErr(err)
	is.Equal(len(pin1), 8)
	is.True(!weakPins[pin1]) // generated pin must not be in weak list
	pin2, err := loadOrGeneratePin(dir)
	is.NoErr(err)
	is.Equal(pin1, pin2) // PIN must persist across runs
}

func TestHomekit_PinFileMode(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	is.NoErr(os.MkdirAll(dir, 0o700))
	_, err := loadOrGeneratePin(dir)
	is.NoErr(err)
	st, err := os.Stat(filepath.Join(dir, "pin.txt"))
	is.NoErr(err)
	is.Equal(st.Mode().Perm(), os.FileMode(0o600))
}

func TestHomekit_PinFormat(t *testing.T) {
	is := is.New(t)
	is.Equal(formatPinDisplay("12345678"), "1234-5678")
}

func TestHomekit_StartDisabledIsNoop(t *testing.T) {
	is := is.New(t)
	h := &Handler{}
	stop, err := h.StartHomekit(context.Background(), config.Homekit{Enabled: false}, nil)
	is.NoErr(err)
	is.True(stop != nil) // stop must not be nil even when disabled
	is.NoErr(stop())
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
	is := is.New(t)
	const (
		devID   = "TESTID0000000001"
		devPwd  = "1111"
		devName = "playroom"
		wantHum = 65.0
	)

	// Start fakedevice so the DeviceConfig has a valid IP.
	srv, err := fakedevice.NewServer(homekitSnapshotPath(t), devID, devPwd)
	is.NoErr(err)
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
	is.NoErr(err)
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
	is.True(ok) // accessory must be registered in homekitAccessories

	is.Equal(a.Humidity.CurrentRelativeHumidity.Value(), wantHum)
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
