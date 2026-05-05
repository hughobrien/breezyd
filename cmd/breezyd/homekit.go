// SPDX-License-Identifier: GPL-3.0-or-later

// HomeKit bridge lifecycle for breezyd. StartHomekit is the single entry
// point: it is a no-op when cfg.Enabled is false, otherwise it generates
// or reloads a PIN, builds the brutella/hap bridge and per-device
// accessories, registers write callbacks on every writable characteristic,
// and runs the HAP server on a goroutine.
//
// HAP server lifecycle is tied to the returned stop() function: calling
// it cancels the server's context and waits for ListenAndServe to return.
// The caller (main.go) should call stop() as part of its graceful-shutdown
// sequence.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/brutella/hap"
	hapaccessory "github.com/brutella/hap/accessory"
	"github.com/hughobrien/breezyd/internal/config"
	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/homekit"
)

// weakPins mirrors hap.InvalidPins so we can check during generation
// without depending on the library's unexported validation path.
// The list is drawn from the brutella/hap source (server.go:536-549).
var weakPins = map[string]bool{
	"00000000": true,
	"11111111": true,
	"22222222": true,
	"33333333": true,
	"44444444": true,
	"55555555": true,
	"66666666": true,
	"77777777": true,
	"88888888": true,
	"99999999": true,
	"12345678": true,
	"87654321": true,
}

// StartHomekit boots the HAP bridge when cfg.Enabled is true.
// Returns a no-op stop function and nil error when disabled.
// On success the returned stop() function cancels the server context
// and waits for ListenAndServe to return.
func (h *Handler) StartHomekit(ctx context.Context, cfg config.Homekit, devices map[string]DeviceConfig) (stop func() error, err error) {
	noop := func() error { return nil }
	if !cfg.Enabled {
		return noop, nil
	}

	// Ensure the state directory exists before writing the PIN.
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("homekit: mkdir state dir %s: %w", cfg.StateDir, err)
	}

	pin, err := loadOrGeneratePin(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("homekit: pin: %w", err)
	}
	slog.Info("HomeKit PIN", "pin", formatPinDisplay(pin))

	// Build the bridge accessory.
	bridge := hapaccessory.NewBridge(hapaccessory.Info{
		Name:         cfg.BridgeName,
		Manufacturer: "Vents",
		Model:        "breezyd",
	})

	// Build per-device accessories and stash them. Iterate device names in
	// sorted order so brutella/hap assigns the same Accessory ID to the
	// same device on every daemon restart — Go's map iteration is randomised,
	// and HAP's add() at server.go:270 hands out aids sequentially in slice
	// order. iOS Home caches the (aid → tile) mapping locally, so a swap on
	// restart would have the "Office" tile driving the bedroom unit.
	h.homekitAccessories = make(map[string]*homekit.Accessory, len(devices))
	names := make([]string, 0, len(devices))
	for name := range devices {
		names = append(names, name)
	}
	sort.Strings(names)
	children := make([]*hapaccessory.A, 0, len(devices))
	for _, name := range names {
		d := devices[name]
		a := homekit.NewBreezyAccessory(name, d.ID, d.IP)
		h.homekitAccessories[name] = a
		children = append(children, a.A)
		registerWriteCallbacks(h, name, a)
	}

	// Build the HAP store and server.
	store := hap.NewFsStore(cfg.StateDir)
	server, err := hap.NewServer(store, bridge.A, children...)
	if err != nil {
		return nil, fmt.Errorf("homekit: new server: %w", err)
	}
	server.Pin = pin
	if cfg.Port != 0 {
		server.Addr = fmt.Sprintf(":%d", cfg.Port)
	}

	hapCtx, hapCancel := context.WithCancel(ctx)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if serveErr := server.ListenAndServe(hapCtx); serveErr != nil && !errors.Is(serveErr, context.Canceled) {
			slog.Error("HAP server exited", "err", serveErr)
		}
	}()

	return func() error {
		hapCancel()
		wg.Wait()
		return nil
	}, nil
}

// SyncHomekit translates a Snapshot into a breezy.Status and pushes it
// into the HomeKit accessory for name. It is a no-op when the HomeKit
// bridge is disabled (h.homekitAccessories == nil) or the device name
// is not registered in the map. Safe to call concurrently from multiple
// goroutines as long as each call targets a different name; callers must
// serialise concurrent calls for the same name.
func (h *Handler) SyncHomekit(name string, snap Snapshot) {
	if h.homekitAccessories == nil {
		return
	}
	a, ok := h.homekitAccessories[name]
	if !ok {
		return
	}

	cfg, ok := h.Devices.Get(name)
	if !ok {
		return
	}

	var lastPoll *time.Time
	if !snap.LastPoll.IsZero() {
		t := snap.LastPoll
		lastPoll = &t
	}

	status := breezy.BuildStatus(snap.Values, name, cfg.ID, cfg.IP, lastPoll)
	homekit.Sync(a, status)
}

// loadOrGeneratePin reads pin.txt from stateDir if it exists and is an
// 8-digit string; otherwise generates a fresh non-weak PIN, writes it
// to pin.txt (mode 0600), and returns it.
func loadOrGeneratePin(stateDir string) (string, error) {
	pinPath := filepath.Join(stateDir, "pin.txt")

	raw, err := os.ReadFile(pinPath)
	if err == nil {
		pin := strings.TrimSpace(string(raw))
		if len(pin) == 8 && isDigits(pin) && !weakPins[pin] {
			return pin, nil
		}
		// Existing file is malformed or weak — regenerate.
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read pin.txt: %w", err)
	}

	pin, err := generatePin()
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(pinPath, []byte(pin), 0o600); err != nil {
		return "", fmt.Errorf("write pin.txt: %w", err)
	}
	return pin, nil
}

// generatePin generates a random 8-digit string, retrying until it
// passes the weak-pin check. In practice a non-weak PIN is produced on
// the first or second try.
func generatePin() (string, error) {
	max := big.NewInt(100_000_000) // 10^8; gives 00000000..99999999
	for {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("generate pin: %w", err)
		}
		pin := fmt.Sprintf("%08d", n.Int64())
		if !weakPins[pin] {
			return pin, nil
		}
	}
}

// formatPinDisplay formats an 8-digit PIN as "XXXX-XXXX" for the log.
// The raw 8-digit value is what brutella/hap (and iOS) expect; the dash
// is purely cosmetic so the operator can read it back at a glance.
func formatPinDisplay(pin string) string {
	if len(pin) != 8 {
		return pin
	}
	return pin[:4] + "-" + pin[4:]
}

// isDigits reports whether s consists entirely of ASCII digits.
func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// registerWriteCallbacks wires HAP remote-write callbacks on all writable
// characteristics for the given device accessory. Failures are logged at
// ERROR (not panicked) so a single device error doesn't crash the bridge.
func registerWriteCallbacks(h *Handler, name string, a *homekit.Accessory) {
	// Active (power on/off): HAP Active is int 0=inactive, 1=active.
	a.AirPurifier.Active.OnValueRemoteUpdate(func(v int) {
		rc, raw, unlock, err := h.dialRecording(name)
		if err != nil {
			slog.Error("homekit power dial", "device", name, "err", err)
			return
		}
		defer unlock()
		defer raw.Close()
		ctx := context.Background()
		if err := breezy.Power(ctx, rc, v != 0); err != nil {
			slog.Error("homekit power write", "device", name, "err", err)
		}
	})

	// TargetAirPurifierState: 0=manual, 1=auto (preset).
	// Map to SetSpeedPreset(1) for auto; leave speed alone for manual.
	a.AirPurifier.TargetAirPurifierState.OnValueRemoteUpdate(func(v int) {
		if v == 0 {
			// Client chose Manual — no preset to set; the RotationSpeed
			// callback drives the actual speed.
			return
		}
		rc, raw, unlock, err := h.dialRecording(name)
		if err != nil {
			slog.Error("homekit mode dial", "device", name, "err", err)
			return
		}
		defer unlock()
		defer raw.Close()
		ctx := context.Background()
		if err := breezy.SetSpeedPreset(ctx, rc, 1); err != nil {
			slog.Error("homekit preset write", "device", name, "err", err)
		}
	})

	// RotationSpeed: float64 percentage, clamp 10..100, then SetSpeedManual.
	a.RotationSpeed.OnValueRemoteUpdate(func(v float64) {
		pct := int(v)
		if pct < 10 {
			pct = 10
		}
		if pct > 100 {
			pct = 100
		}
		rc, raw, unlock, err := h.dialRecording(name)
		if err != nil {
			slog.Error("homekit speed dial", "device", name, "err", err)
			return
		}
		defer unlock()
		defer raw.Close()
		ctx := context.Background()
		if err := breezy.SetSpeedManual(ctx, rc, pct); err != nil {
			slog.Error("homekit speed write", "device", name, "err", err)
		}
	})

	// SupplyOnly switch: on → supply mode; off → check ExtractOnly.
	a.SupplyOnly.On.OnValueRemoteUpdate(func(v bool) {
		switchAirflow(h, name, a, v, false)
	})

	// ExtractOnly switch: on → extract mode; off → check SupplyOnly.
	a.ExtractOnly.On.OnValueRemoteUpdate(func(v bool) {
		switchAirflow(h, name, a, false, v)
	})

	// Heater switch.
	a.Heater.On.OnValueRemoteUpdate(func(v bool) {
		rc, raw, unlock, err := h.dialRecording(name)
		if err != nil {
			slog.Error("homekit heater dial", "device", name, "err", err)
			return
		}
		defer unlock()
		defer raw.Close()
		if err := breezy.SetHeater(context.Background(), rc, v); err != nil {
			slog.Error("homekit heater write", "device", name, "err", err)
		}
	})

	// Night / Turbo switches share the same mutually-exclusive timer.
	a.Night.On.OnValueRemoteUpdate(func(v bool) {
		mode := "off"
		if v {
			mode = "night"
		}
		switchTimer(h, name, a, mode)
	})
	a.Turbo.On.OnValueRemoteUpdate(func(v bool) {
		mode := "off"
		if v {
			mode = "turbo"
		}
		switchTimer(h, name, a, mode)
	})

	// ResetFilter: any remote write means "I changed the filter, reset
	// the counter." HAP Apple spec writes 1 on the reset gesture.
	a.ResetFilter.OnValueRemoteUpdate(func(_ int) {
		rc, raw, unlock, err := h.dialRecording(name)
		if err != nil {
			slog.Error("homekit filter-reset dial", "device", name, "err", err)
			return
		}
		defer unlock()
		defer raw.Close()
		if err := breezy.ResetFilter(context.Background(), rc); err != nil {
			slog.Error("homekit filter-reset write", "device", name, "err", err)
		}
	})
}

// switchTimer wires the Night and Turbo switches to the special-mode
// timer (0x0007). The two switches are mutually exclusive — entering one
// always cancels the other; turning either off cancels the timer entirely.
func switchTimer(h *Handler, name string, a *homekit.Accessory, mode string) {
	a.Night.On.SetValue(mode == "night")
	a.Turbo.On.SetValue(mode == "turbo")
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		slog.Error("homekit timer dial", "device", name, "err", err)
		return
	}
	defer unlock()
	defer raw.Close()
	if err := breezy.SetTimer(context.Background(), rc, mode); err != nil {
		slog.Error("homekit timer write", "device", name, "mode", mode, "err", err)
	}
}

// switchAirflow implements the mutual-exclusion logic for the Supply Only
// and Extract Only switches:
//
//   - supply=true  → mode "supply",      ExtractOnly forced off
//   - extract=true → mode "extract",     SupplyOnly forced off
//   - both false   → mode "regeneration"
//
// Both true is not a valid call (the individual callbacks only set one at a
// time); we treat it as "supply" to avoid an undefined state.
func switchAirflow(h *Handler, name string, a *homekit.Accessory, supply, extract bool) {
	var mode string
	switch {
	case supply:
		mode = "supply"
		a.SupplyOnly.On.SetValue(true)
		a.ExtractOnly.On.SetValue(false)
	case extract:
		mode = "extract"
		a.SupplyOnly.On.SetValue(false)
		a.ExtractOnly.On.SetValue(true)
	default:
		mode = "regeneration"
		a.SupplyOnly.On.SetValue(false)
		a.ExtractOnly.On.SetValue(false)
	}

	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		slog.Error("homekit airflow dial", "device", name, "err", err)
		return
	}
	defer unlock()
	defer raw.Close()
	ctx := context.Background()
	if err := breezy.SetMode(ctx, rc, mode); err != nil {
		slog.Error("homekit airflow write", "device", name, "mode", mode, "err", err)
	}
}
