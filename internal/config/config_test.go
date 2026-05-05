// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes contents to a tmp file with mode 0600 by default.
func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoad_HappyPath(t *testing.T) {
	path := writeConfig(t, `
[daemon]
listen        = "127.0.0.1:9876"
poll_interval = "30s"
discovery     = "on-start"

[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"

[devices.bedroom]
id       = "BREEZY00000000A1"
password = "secret"
ip       = "192.168.1.42"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Daemon.Listen != "127.0.0.1:9876" {
		t.Errorf("Listen = %q", cfg.Daemon.Listen)
	}
	if cfg.Daemon.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v", cfg.Daemon.PollInterval)
	}
	if cfg.Daemon.Discovery != "on-start" {
		t.Errorf("Discovery = %q", cfg.Daemon.Discovery)
	}
	if len(cfg.Devices) != 2 {
		t.Fatalf("len(Devices) = %d, want 2", len(cfg.Devices))
	}
	pr, ok := cfg.Devices["playroom"]
	if !ok {
		t.Fatal("playroom missing")
	}
	if pr.ID != "BREEZY00000000A0" || pr.Password != "testpwd" || pr.IP != "" {
		t.Errorf("playroom = %+v", pr)
	}
	br, ok := cfg.Devices["bedroom"]
	if !ok {
		t.Fatal("bedroom missing")
	}
	if br.ID != "BREEZY00000000A1" || br.Password != "secret" || br.IP != "192.168.1.42" {
		t.Errorf("bedroom = %+v", br)
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	// daemon table omitted entirely
	path := writeConfig(t, `
[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Listen is intentionally NOT defaulted by Load — the CLI uses the
	// empty string to mean "no daemon configured → standalone mode". The
	// daemon applies its own default after Load returns.
	if cfg.Daemon.Listen != "" {
		t.Errorf("Listen default = %q, want empty (no-daemon sentinel)", cfg.Daemon.Listen)
	}
	if cfg.Daemon.PollInterval != 30*time.Second {
		t.Errorf("PollInterval default = %v, want 30s", cfg.Daemon.PollInterval)
	}
	if cfg.Daemon.Discovery != "on-start" {
		t.Errorf("Discovery default = %q, want on-start", cfg.Daemon.Discovery)
	}
}

func TestLoad_DaemonPasswordInherited(t *testing.T) {
	// Devices without an explicit `password` should inherit
	// [daemon].password. Devices with their own password keep it.
	path := writeConfig(t, `
[daemon]
password = "fleetpwd"

[devices.bedroom]
id = "BREEZY00000000A0"

[devices.office]
id       = "BREEZY00000000A1"
password = "override"

[devices.playroom]
id = "BREEZY00000000A2"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Daemon.Password != "fleetpwd" {
		t.Errorf("Daemon.Password = %q, want fleetpwd", cfg.Daemon.Password)
	}
	if got := cfg.Devices["bedroom"].Password; got != "fleetpwd" {
		t.Errorf("bedroom inherited = %q, want fleetpwd", got)
	}
	if got := cfg.Devices["office"].Password; got != "override" {
		t.Errorf("office override = %q, want override", got)
	}
	if got := cfg.Devices["playroom"].Password; got != "fleetpwd" {
		t.Errorf("playroom inherited = %q, want fleetpwd", got)
	}
}

func TestLoad_DaemonPasswordAbsentLeavesEmpty(t *testing.T) {
	// When [daemon].password is unset, devices without their own
	// password stay empty (current behaviour preserved).
	path := writeConfig(t, `
[devices.bedroom]
id = "BREEZY00000000A0"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Daemon.Password != "" {
		t.Errorf("Daemon.Password = %q, want empty", cfg.Daemon.Password)
	}
	if got := cfg.Devices["bedroom"].Password; got != "" {
		t.Errorf("bedroom.Password = %q, want empty (no inheritance)", got)
	}
}

func TestLoad_EmptyDevicesAccepted(t *testing.T) {
	path := writeConfig(t, `
[daemon]
listen        = "127.0.0.1:9876"
poll_interval = "30s"
discovery     = "off"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Devices) != 0 {
		t.Errorf("expected zero devices, got %d", len(cfg.Devices))
	}
}

func TestLoad_ReservedNamesRejected(t *testing.T) {
	for _, name := range []string{"ls", "discover", "daemon-url", "param", "LS", "Discover", "Daemon-URL", "Param"} {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, `
[devices.`+name+`]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error for reserved name %q, got nil", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q must mention offending name %q", err.Error(), name)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "reserved") {
				t.Errorf("error %q should mention 'reserved'", err.Error())
			}
		})
	}
}

func TestLoad_WorldReadableRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for mode 0644, got nil")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Errorf("error should mention 0600: %v", err)
	}
	if !strings.Contains(err.Error(), "644") {
		t.Errorf("error should mention actual mode 644: %v", err)
	}
}

func TestLoad_GroupReadableRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(`
[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for mode 0640, got nil")
	}
	if !strings.Contains(err.Error(), "0600") {
		t.Errorf("error should mention 0600: %v", err)
	}
}

func TestLoad_BadDeviceIDLength(t *testing.T) {
	path := writeConfig(t, `
[devices.playroom]
id       = "TOOSHORT"
password = "testpwd"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for short id, got nil")
	}
	if !strings.Contains(err.Error(), "playroom") {
		t.Errorf("error should mention device name: %v", err)
	}
	if !strings.Contains(err.Error(), "16") {
		t.Errorf("error should mention required length 16: %v", err)
	}
	if !strings.Contains(err.Error(), "8") {
		t.Errorf("error should mention actual length 8: %v", err)
	}
}

func TestLoad_BadDeviceIDLong(t *testing.T) {
	path := writeConfig(t, `
[devices.playroom]
id       = "BREEZY00000000A0XTRA"
password = "testpwd"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for too-long id, got nil")
	}
	if !strings.Contains(err.Error(), "playroom") {
		t.Errorf("error should mention device name: %v", err)
	}
}

func TestLoad_BadDiscoveryValue(t *testing.T) {
	cases := []string{
		`discovery = "sometimes"`,
		`discovery = "periodic:"`,
		`discovery = "periodic:notaduration"`,
		`discovery = "ON-START"`, // case-sensitive
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			path := writeConfig(t, `
[daemon]
`+line+`
`)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("expected error for %q", line)
			}
			if !strings.Contains(strings.ToLower(err.Error()), "discovery") &&
				!strings.Contains(strings.ToLower(err.Error()), "periodic") {
				t.Errorf("error should mention discovery: %v", err)
			}
		})
	}
}

func TestLoad_GoodPeriodicDiscovery(t *testing.T) {
	path := writeConfig(t, `
[daemon]
discovery = "periodic:5m"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Daemon.Discovery != "periodic:5m" {
		t.Errorf("Discovery = %q", cfg.Daemon.Discovery)
	}
}

func TestLoad_BadPollInterval(t *testing.T) {
	path := writeConfig(t, `
[daemon]
poll_interval = "notaduration"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "poll_interval") {
		t.Errorf("error should mention poll_interval: %v", err)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "doesnotexist.toml")
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestLoad_BadTOMLSyntax(t *testing.T) {
	path := writeConfig(t, `this is not valid toml = = =`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestWriteDefault_FreshTempDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "breezyd.toml")
	if err := WriteDefault(path); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %#o, want 0600", mode)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(body)
	for _, want := range []string{
		// [daemon] block is commented out so new users land in standalone mode.
		`# [daemon]`,
		`# listen        = "127.0.0.1:9876"`,
		`# poll_interval = "30s"`,
		`# discovery     = "on-start"`,
		`# [devices.playroom]`,
		`breezy discover`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("default config missing %q", want)
		}
	}
	// Roundtrip: the bootstrap output must Load cleanly. This is the
	// strongest single regression guard against a future template edit
	// that breaks `breezyd` first-run.
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load on WriteDefault output failed: %v", err)
	}
	if cfg.Daemon.Listen != "" {
		t.Errorf("Daemon.Listen = %q, want empty (standalone sentinel)", cfg.Daemon.Listen)
	}
}

func TestWriteDefault_CreatesParent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "breezyd.toml")
	if err := WriteDefault(path); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	parentSt, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat parent: %v", err)
	}
	if !parentSt.IsDir() {
		t.Fatal("parent is not a directory")
	}
	if mode := parentSt.Mode().Perm(); mode != 0o700 {
		t.Errorf("parent mode = %#o, want 0700", mode)
	}
}

func TestWriteDefault_RefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "breezyd.toml")
	const sentinel = "user-supplied content; do not clobber"
	if err := os.WriteFile(path, []byte(sentinel), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := WriteDefault(path)
	if err == nil {
		t.Fatal("WriteDefault returned nil; expected ErrConfigExists")
	}
	if !errors.Is(err, ErrConfigExists) {
		t.Errorf("err = %v, want errors.Is(_, ErrConfigExists)", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after refusal: %v", err)
	}
	if string(got) != sentinel {
		t.Errorf("WriteDefault clobbered existing file; got %q", string(got))
	}
}

func TestLoad_HomekitDisabledByDefault(t *testing.T) {
	path := writeConfig(t, `
[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Homekit.Enabled {
		t.Error("Homekit.Enabled defaults to false")
	}
}

func TestLoad_HomekitEnabledDefaults(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := writeConfig(t, `
[homekit]
enabled = true

[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Homekit.Enabled {
		t.Error("Enabled = false, want true")
	}
	if cfg.Homekit.BridgeName != "breezyd" {
		t.Errorf("BridgeName = %q, want default 'breezyd'", cfg.Homekit.BridgeName)
	}
	if !strings.HasSuffix(cfg.Homekit.StateDir, "/breezyd/homekit") {
		t.Errorf("StateDir = %q, want path ending in /breezyd/homekit", cfg.Homekit.StateDir)
	}
}

func TestLoad_HomekitBridgeNameTooLong(t *testing.T) {
	path := writeConfig(t, `
[homekit]
enabled     = true
bridge_name = "this-name-is-way-too-long-for-the-32-char-limit"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for long bridge_name")
	}
	if !strings.Contains(err.Error(), "32 chars") {
		t.Errorf("error should mention 32 char limit: %v", err)
	}
}

func TestLoad_HomekitBadPort(t *testing.T) {
	path := writeConfig(t, `
[homekit]
enabled = true
port    = 80
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for low port")
	}
	if !strings.Contains(err.Error(), "1024-65535") {
		t.Errorf("error should mention port range: %v", err)
	}
}

func TestLoad_HomekitTildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	path := writeConfig(t, `
[homekit]
enabled   = true
state_dir = "~/.config/breezyd/homekit"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(home, ".config", "breezyd", "homekit")
	if cfg.Homekit.StateDir != want {
		t.Errorf("StateDir = %q, want %q", cfg.Homekit.StateDir, want)
	}
}
