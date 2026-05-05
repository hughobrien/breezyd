// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hughobrien/breezyd/internal/config"
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
