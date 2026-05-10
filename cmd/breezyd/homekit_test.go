// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
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

// TestHomekit_PinRegeneratesWeakSeed pins that an existing pin.txt holding
// a weak PIN (one of brutella/hap's invalid set) gets regenerated rather
// than reused. Security-relevant: a deployed daemon that silently accepted
// a weak PIN would expose a default-pairing-code attack surface.
func TestHomekit_PinRegeneratesWeakSeed(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.txt")
	is.NoErr(os.WriteFile(pinPath, []byte("12345678"), 0o600))

	pin, err := loadOrGeneratePin(dir)
	is.NoErr(err)
	is.True(pin != "12345678") // weak seed must be replaced
	is.True(!weakPins[pin])    // replacement must not also be weak

	raw, err := os.ReadFile(pinPath)
	is.NoErr(err)
	is.Equal(string(raw), pin) // file now reflects the regenerated PIN
}

// TestHomekit_PinRegeneratesMalformed pins that an existing pin.txt holding
// a malformed value (non-8-digit) gets regenerated.
func TestHomekit_PinRegeneratesMalformed(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	pinPath := filepath.Join(dir, "pin.txt")
	is.NoErr(os.WriteFile(pinPath, []byte("abc"), 0o600))

	pin, err := loadOrGeneratePin(dir)
	is.NoErr(err)
	is.Equal(len(pin), 8)   // regenerated PIN is the canonical 8-digit form
	is.True(!weakPins[pin]) // and not weak

	raw, err := os.ReadFile(pinPath)
	is.NoErr(err)
	is.Equal(string(raw), pin) // file persisted to new value
}

// TestHomekit_AccessoryAidIsLexOrdered pins that StartHomekit sorts device
// names before adding accessories to the bridge, so brutella/hap's
// sequential aid assignment (server.go::add: aid=1 for bridge, then 2,3,4...
// in children slice order) produces alphabetical aids regardless of map
// iteration order. Without the sort, iOS Home tiles would re-bind to the
// wrong device on every restart.
func TestHomekit_AccessoryAidIsLexOrdered(t *testing.T) {
	is := is.New(t)
	const (
		devID  = "TESTID0000000001"
		devPwd = "1111"
	)
	srv, err := fakedevice.NewServer(homekitSnapshotPath(t), devID, devPwd)
	is.NoErr(err)
	t.Cleanup(func() { _ = srv.Close() })

	// Five devices in non-alphabetical insertion order. With sort.Strings in
	// place, aids land alphabetically: alpha < bravo < mike < tango < zulu.
	// Without it, map iteration randomisation gives ~1/120 odds of producing
	// alphabetical aids by chance.
	devices := map[string]DeviceConfig{
		"zulu":  {ID: devID, Password: devPwd, IP: srv.Addr()},
		"alpha": {ID: devID, Password: devPwd, IP: srv.Addr()},
		"mike":  {ID: devID, Password: devPwd, IP: srv.Addr()},
		"tango": {ID: devID, Password: devPwd, IP: srv.Addr()},
		"bravo": {ID: devID, Password: devPwd, IP: srv.Addr()},
	}

	state := NewState()
	h := &Handler{
		State:   state,
		Devices: NewDeviceRegistry(devices),
	}

	cfg := config.Homekit{
		Enabled:    true,
		BridgeName: "test-bridge",
		StateDir:   t.TempDir(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	stop, err := h.StartHomekit(ctx, cfg, devices)
	is.NoErr(err)
	t.Cleanup(func() { _ = stop() })

	a := h.homekitAccessories
	is.True(a["alpha"].Id < a["bravo"].Id)
	is.True(a["bravo"].Id < a["mike"].Id)
	is.True(a["mike"].Id < a["tango"].Id)
	is.True(a["tango"].Id < a["zulu"].Id)
}

// newHomekitWriteTestHandler builds a Handler wired to a fakedevice via a
// real UDP ClientFactory, plus a freshly-constructed Accessory with the
// write callbacks registered. Returns the handler, the registered callback
// set, and the device name so individual subtests can fire each callback
// and assert the post-state on Handler.State (recordingClient writes
// through to State on every successful WriteParams).
func newHomekitWriteTestHandler(t *testing.T) (*Handler, homekitWriteCallbacks, string) {
	t.Helper()
	const (
		devID   = "TESTID0000000001"
		devPwd  = "1111"
		devName = "playroom"
	)
	srv, err := fakedevice.NewServer(homekitSnapshotPath(t), devID, devPwd)
	if err != nil {
		t.Fatalf("fakedevice.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	state := NewState()
	devices := NewDeviceRegistry(map[string]DeviceConfig{
		devName: {ID: devID, Password: devPwd, IP: srv.Addr()},
	})
	h := &Handler{
		State:   state,
		Devices: devices,
	}
	h.ClientFactory = func(name string) (HandlerClient, error) {
		d, ok := h.Devices.Get(name)
		if !ok {
			return nil, fmt.Errorf("unknown device %q", name)
		}
		return breezy.NewClient(d.IP, d.ID, d.Password,
			breezy.WithRetries(0), breezy.WithTimeout(500*time.Millisecond))
	}

	a := homekit.NewBreezyAccessory(devName, devID, srv.Addr())
	cb := registerWriteCallbacks(h, devName, a)
	return h, cb, devName
}

// TestHomekit_WriteCallbacks pins every OnValueRemoteUpdate callback
// registered by registerWriteCallbacks. Each subtest fires the callback
// with a representative input and asserts the expected param IDs and
// bytes landed in Handler.State (via the recordingClient write-through).
//
// Together with the read-path coverage in pkg/homekit/sync_test.go, this
// pins the entire iOS-write surface — the only path through which the
// HAP bridge mutates device state.
func TestHomekit_WriteCallbacks(t *testing.T) {
	t.Run("active=1 → Power(true)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.active(1)
		snap, ok := h.State.Get(name)
		is.True(ok)
		is.Equal(snap.Values[0x0001], []byte{0x01}) // Active=1 writes Power on
	})

	t.Run("active=0 → Power(false)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.active(0)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0001], []byte{0x00}) // Active=0 writes Power off
	})

	t.Run("targetState=1 → SetSpeedPreset(1)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.targetState(1)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0002], []byte{0x01}) // TargetState=1 (Auto) → preset 1 byte at 0x0002
	})

	t.Run("targetState=0 is a no-op (manual mode)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.targetState(0)
		snap, ok := h.State.Get(name)
		// State may be empty (no writes) or have nothing at 0x0002.
		if ok {
			_, hasPreset := snap.Values[0x0002]
			is.True(!hasPreset) // TargetState=0 must not write to 0x0002
		}
	})

	t.Run("rotationSpeed=50 → SetSpeedManual(50)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.rotationSpeed(50.0)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0044], []byte{50})   // pct byte
		is.Equal(snap.Values[0x0002], []byte{0xFF}) // manual flag
	})

	t.Run("rotationSpeed=5 clamps to 10", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.rotationSpeed(5.0)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0044], []byte{10}) // below-range value clamps to 10
	})

	t.Run("rotationSpeed=150 clamps to 100", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.rotationSpeed(150.0)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0044], []byte{100}) // above-range value clamps to 100
	})

	t.Run("supplyOnly=true → SetMode(supply)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.supplyOnly(true)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x00B7], []byte{0x02}) // supply mode byte
	})

	t.Run("supplyOnly=false → SetMode(regeneration)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.supplyOnly(false)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x00B7], []byte{0x01}) // both off → regeneration
	})

	t.Run("extractOnly=true → SetMode(extract)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.extractOnly(true)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x00B7], []byte{0x03}) // extract mode byte
	})

	t.Run("heater=true → SetHeater(true)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.heater(true)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0068], []byte{0x01}) // heater on
	})

	t.Run("heater=false → SetHeater(false)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.heater(false)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0068], []byte{0x00}) // heater off
	})

	t.Run("night=true → SetTimer(night)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.night(true)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0007], []byte{0x01}) // night = 1
	})

	t.Run("night=false → SetTimer(off)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.night(false)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0007], []byte{0x00}) // off = 0
	})

	t.Run("turbo=true → SetTimer(turbo)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.turbo(true)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0007], []byte{0x02}) // turbo = 2
	})

	t.Run("turbo=false → SetTimer(off)", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.turbo(false)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0007], []byte{0x00}) // off = 0
	})

	t.Run("resetFilter → ResetFilter", func(t *testing.T) {
		is := is.New(t)
		h, cb, name := newHomekitWriteTestHandler(t)
		cb.resetFilter(1)
		snap, _ := h.State.Get(name)
		is.Equal(snap.Values[0x0065], []byte{0x01}) // filter reset writes 1 to 0x0065
	})
}
