// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads and validates the Breezy daemon's TOML configuration.
//
// The config file lives at ~/.config/breezy/config.toml and is shared by both
// the daemon (which reads everything) and the CLI (which only needs the
// daemon's listen address). The loader enforces mode 0600 because the file
// contains device passwords in plaintext — the underlying UDP protocol leaks
// the password back over the LAN unauthenticated, so encrypting the config
// would not improve the threat model, but at least we keep other local users
// from reading it.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// reservedNames are global CLI verbs that cannot be used as device names,
// because `breezy <name> ...` would be ambiguous with the verb form.
// Comparison is case-insensitive.
var reservedNames = map[string]bool{
	"ls":         true,
	"discover":   true,
	"daemon-url": true,
	"param":      true,
}

// periodicRe matches the `periodic:<duration>` discovery form.
var periodicRe = regexp.MustCompile(`^periodic:(.+)$`)

// Config is the top-level config structure.
type Config struct {
	Daemon  Daemon
	Devices map[string]Device
}

// Daemon holds daemon-wide settings.
type Daemon struct {
	// Listen is the host:port the HTTP server binds to.
	Listen string
	// PollInterval is how often the poller refreshes device state.
	PollInterval time.Duration
	// Discovery is one of "on-start", "off", or "periodic:<duration>".
	Discovery string
}

// Device is a single configured device.
type Device struct {
	// ID is the 16-char ASCII device id (uppercase hex in practice, but we
	// don't validate the alphabet — the device can echo whatever it wants).
	ID string
	// Password is the 4-char protocol password.
	Password string
	// IP is optional — if set, discovery is skipped for this device.
	IP string
}

// rawConfig mirrors the on-disk TOML structure. We decode poll_interval as a
// raw string and parse it into a time.Duration ourselves so we can return a
// nicer error message than the TOML library's generic decode failure.
type rawConfig struct {
	Daemon  rawDaemon            `toml:"daemon"`
	Devices map[string]rawDevice `toml:"devices"`
}

type rawDaemon struct {
	Listen       string `toml:"listen"`
	PollInterval string `toml:"poll_interval"`
	Discovery    string `toml:"discovery"`
}

type rawDevice struct {
	ID       string `toml:"id"`
	Password string `toml:"password"`
	IP       string `toml:"ip"`
}

// Load reads, validates, and returns the config at path. Returns an error
// matching os.ErrNotExist when the file is absent so callers can distinguish
// "no config" from "bad config".
func Load(path string) (*Config, error) {
	st, err := os.Stat(path)
	if err != nil {
		// Pass through os.ErrNotExist & friends unwrapped so errors.Is works.
		return nil, err
	}

	// Refuse to load if anyone besides the owner can read or write the file.
	// 0o077 catches group + other rwx bits.
	if mode := st.Mode().Perm(); mode&0o077 != 0 {
		return nil, fmt.Errorf("config file %s must be mode 0600 (currently %#o); "+
			"run: chmod 600 %s", path, mode, path)
	}

	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	cfg := &Config{
		Daemon: Daemon{
			Listen:    raw.Daemon.Listen,
			Discovery: raw.Daemon.Discovery,
		},
		Devices: make(map[string]Device, len(raw.Devices)),
	}

	// Defaults.
	if cfg.Daemon.Listen == "" {
		cfg.Daemon.Listen = "127.0.0.1:9876"
	}
	if cfg.Daemon.Discovery == "" {
		cfg.Daemon.Discovery = "on-start"
	}

	// poll_interval: parse raw string, default to 30s if absent.
	if raw.Daemon.PollInterval == "" {
		cfg.Daemon.PollInterval = 30 * time.Second
	} else {
		d, err := time.ParseDuration(raw.Daemon.PollInterval)
		if err != nil {
			return nil, fmt.Errorf("config: invalid poll_interval %q: %w",
				raw.Daemon.PollInterval, err)
		}
		cfg.Daemon.PollInterval = d
	}

	// discovery: validate against the three allowed forms.
	switch cfg.Daemon.Discovery {
	case "on-start", "off":
		// ok
	default:
		m := periodicRe.FindStringSubmatch(cfg.Daemon.Discovery)
		if m == nil {
			return nil, fmt.Errorf("config: invalid discovery %q: "+
				"must be \"on-start\", \"off\", or \"periodic:<duration>\"",
				cfg.Daemon.Discovery)
		}
		if _, err := time.ParseDuration(m[1]); err != nil {
			return nil, fmt.Errorf("config: invalid discovery %q: "+
				"periodic duration %q is not a valid Go duration: %w",
				cfg.Daemon.Discovery, m[1], err)
		}
	}

	// Devices: copy + validate.
	for name, rd := range raw.Devices {
		if reservedNames[strings.ToLower(name)] {
			return nil, fmt.Errorf("config: device name %q is reserved "+
				"(collides with global CLI verb)", name)
		}
		if len(rd.ID) != 16 {
			return nil, fmt.Errorf("config: device %q: id must be exactly 16 "+
				"ASCII chars, got %d (%q)", name, len(rd.ID), rd.ID)
		}
		cfg.Devices[name] = Device{
			ID:       rd.ID,
			Password: rd.Password,
			IP:       rd.IP,
		}
	}

	return cfg, nil
}

// defaultConfigTemplate is the content WriteDefault writes for a fresh
// install. It carries the same [daemon] values that Load defaults to
// when fields are absent — duplicating them here is intentional, since
// the file is meant to be a starting point the user reads and edits.
//
// The single example device block is commented out so the file passes
// Load on the immediate next run without doing anything dangerous; the
// user has to add at least one real device to get useful behaviour.
const defaultConfigTemplate = `# breezyd configuration. See:
#   https://github.com/hughobrien/breezyd#configuration
#
# This file must remain mode 0600 — the daemon refuses to start otherwise.

[daemon]
listen        = "127.0.0.1:9876"
poll_interval = "30s"
discovery     = "on-start"   # "on-start" | "off" | "periodic:<duration>"

# One [devices.<name>] block per Breezy unit. Run ` + "`breezy discover`" + ` to
# find device IDs on your LAN, then uncomment one of the blocks below
# and fill in your values. The ip line is optional — if omitted, on-start
# discovery resolves it.
#
# [devices.playroom]
# id       = "BREEZY00000000A0"
# password = "your-protocol-password"
# ip       = "192.168.1.148"
`

// ErrConfigExists is returned by WriteDefault when the target path
// already exists. Callers can use errors.Is to distinguish this from
// other write failures and avoid clobbering a real config.
var ErrConfigExists = errors.New("config: file already exists")

// WriteDefault writes a starter config to path with mode 0600. The
// parent directory is created (mode 0700) if missing. The write is
// atomic: content is staged in a sibling tempfile and renamed into
// place, so a crash mid-write doesn't leave a half-formed file that
// the loader would reject.
//
// If path already exists, WriteDefault returns ErrConfigExists without
// touching the existing file. Callers that want to overwrite must
// handle that themselves.
func WriteDefault(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrConfigExists, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("config: stat %s: %w", path, err)
	}

	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("config: mkdir %s: %w", dir, err)
		}
	}

	// Stage in a sibling temp file with the same dir so rename is atomic.
	tmp, err := os.CreateTemp(dir, ".breezyd-config-*.tmp")
	if err != nil {
		return fmt.Errorf("config: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.WriteString(defaultConfigTemplate); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: write temp: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("config: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("config: close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("config: rename temp -> %s: %w", path, err)
	}
	return nil
}
