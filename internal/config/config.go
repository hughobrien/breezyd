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
	"fmt"
	"os"
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
