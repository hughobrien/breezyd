// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads and validates the Breezy daemon's TOML configuration.
//
// The config file lives at ~/.config/breezy/config.toml and is shared by both
// the daemon (which reads everything) and the CLI (which reads device entries
// for standalone mode and [daemon].listen to detect whether daemon mode is
// configured). The loader enforces mode 0600 because the file contains device
// passwords in plaintext — the underlying UDP protocol leaks the password back
// over the LAN unauthenticated, so encrypting the config would not improve the
// threat model, but at least we keep other local users from reading it.
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

// deviceNameRe enforces that device names are valid JS identifier segments.
// Device names appear as datastar signal-path segments in the dashboard
// (e.g. $detailsOpen.<name>.sensors). Names with hyphens/dots would silently
// break datastar's path parsing. See docs/superpowers/specs/2026-05-11-dashboard-bugfixes-design.md.
var deviceNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Config is the top-level config structure.
type Config struct {
	Daemon  Daemon
	Homekit Homekit
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
	// Password is the fleet-wide protocol password used by the daemon's
	// startup/periodic discovery probes and inherited by any
	// [devices.NAME] block that omits its own `password`. Empty means
	// no fleet default — discovery falls back to the factory wildcard
	// "1111", and devices without their own password are treated as
	// "no password set" (which the firmware will reject at poll time).
	Password string
}

// Homekit holds HomeKit bridge settings. The bridge is opt-in via
// Enabled; defaults for BridgeName and StateDir are applied by Load
// when Enabled is true.
type Homekit struct {
	// Enabled controls whether the daemon starts the HAP server.
	Enabled bool
	// BridgeName is shown in iOS during pairing. Default "breezyd".
	BridgeName string
	// Port is the TCP port the HAP server binds to. 0 = ephemeral.
	Port int
	// StateDir holds pairing keys + the generated PIN. Default
	// $XDG_STATE_HOME/breezyd/homekit. Delete to factory-reset
	// pairing.
	StateDir string
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
	Homekit rawHomekit           `toml:"homekit"`
	Devices map[string]rawDevice `toml:"devices"`
}

type rawHomekit struct {
	Enabled    bool   `toml:"enabled"`
	BridgeName string `toml:"bridge_name"`
	Port       int    `toml:"port"`
	StateDir   string `toml:"state_dir"`
}

type rawDaemon struct {
	Listen       string `toml:"listen"`
	PollInterval string `toml:"poll_interval"`
	Discovery    string `toml:"discovery"`
	Password     string `toml:"password"`
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

	var raw rawConfig
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}

	// The mode-0600 check is there to protect passwords. Skip it for
	// password-free files so a world-readable system fallback (e.g.
	// /etc/breezy/config.toml written by the NixOS module to point the
	// CLI at the daemon) can sit at 0644 without tripping the loader.
	hasPasswords := raw.Daemon.Password != ""
	if !hasPasswords {
		for _, d := range raw.Devices {
			if d.Password != "" {
				hasPasswords = true
				break
			}
		}
	}
	if hasPasswords {
		// 0o077 catches group + other rwx bits.
		if mode := st.Mode().Perm(); mode&0o077 != 0 {
			return nil, fmt.Errorf("config file %s contains passwords and must be mode 0600 (currently %#o); "+
				"run: chmod 600 %s", path, mode, path)
		}
	}

	cfg := &Config{
		Daemon: Daemon{
			Listen:    raw.Daemon.Listen,
			Discovery: raw.Daemon.Discovery,
			Password:  raw.Daemon.Password,
		},
		Devices: make(map[string]Device, len(raw.Devices)),
	}

	cfg.Homekit = Homekit{
		Enabled:    raw.Homekit.Enabled,
		BridgeName: raw.Homekit.BridgeName,
		Port:       raw.Homekit.Port,
		StateDir:   raw.Homekit.StateDir,
	}
	if cfg.Homekit.Enabled {
		if cfg.Homekit.BridgeName == "" {
			cfg.Homekit.BridgeName = "breezyd"
		}
		if len(cfg.Homekit.BridgeName) > 32 {
			return nil, fmt.Errorf("config: homekit bridge_name must be <= 32 chars, got %d", len(cfg.Homekit.BridgeName))
		}
		if cfg.Homekit.Port != 0 && (cfg.Homekit.Port < 1024 || cfg.Homekit.Port > 65535) {
			return nil, fmt.Errorf("config: homekit port must be 0 or 1024-65535, got %d", cfg.Homekit.Port)
		}
		expanded, err := expandStateDir(cfg.Homekit.StateDir)
		if err != nil {
			return nil, fmt.Errorf("config: homekit state_dir: %w", err)
		}
		cfg.Homekit.StateDir = expanded
	}

	// Defaults.
	// NOTE: Listen is intentionally NOT defaulted here. The CLI uses the
	// empty-string sentinel to mean "no daemon configured → use standalone
	// mode". The daemon applies its own default after Load returns.
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
		// Device names appear as datastar signal-path segments in the dashboard
		// (e.g. $detailsOpen.<name>.sensors). Restrict to a JS-identifier-safe
		// shape so paths don't accidentally tokenise into arithmetic or nested
		// access. See docs/superpowers/specs/2026-05-11-dashboard-bugfixes-design.md.
		if !deviceNameRe.MatchString(name) {
			return nil, fmt.Errorf("config: device name %q must be a valid identifier "+
				"(letters/digits/underscore; starts non-digit)", name)
		}
		if len(rd.ID) != 16 {
			return nil, fmt.Errorf("config: device %q: id must be exactly 16 "+
				"ASCII chars, got %d (%q)", name, len(rd.ID), rd.ID)
		}
		pw := rd.Password
		if pw == "" {
			pw = cfg.Daemon.Password
		}
		cfg.Devices[name] = Device{
			ID:       rd.ID,
			Password: pw,
			IP:       rd.IP,
		}
	}

	return cfg, nil
}

// expandStateDir applies ~/$HOME and $XDG_STATE_HOME defaults for
// the homekit state directory. An empty input returns the default
// $XDG_STATE_HOME/breezyd/homekit (or $HOME/.local/state/... when
// XDG_STATE_HOME is unset).
func expandStateDir(in string) (string, error) {
	if in == "" {
		if x := os.Getenv("XDG_STATE_HOME"); x != "" {
			return filepath.Join(x, "breezyd", "homekit"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state", "breezyd", "homekit"), nil
	}
	if strings.HasPrefix(in, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, in[2:]), nil
	}
	return in, nil
}

// defaultConfigTemplate is the content WriteDefault writes for a fresh
// install. The [daemon] block is commented out so new users land in
// standalone mode (CLI talks UDP directly to each device) without
// needing to run breezyd first. Uncomment the block to enable daemon
// mode (polling, caching, /metrics, the web dashboard).
//
// The single example device block is also commented out so the file
// passes Load on the immediate next run without doing anything
// dangerous; the user has to add at least one real device to get
// useful behaviour.
const defaultConfigTemplate = `# breezyd configuration. See:
#   https://github.com/hughobrien/breezyd#configuration
#
# This file must remain mode 0600 — the daemon refuses to start otherwise.

# Uncomment the [daemon] block below to run the breezyd daemon and have
# the CLI talk to it over HTTP (enables caching, polling, /metrics, and
# the embedded dashboard). Without it, the CLI talks UDP directly to
# each configured device — that's the default and is fine for ad-hoc
# use. The CLI checks whether [daemon] listen = "..." is set; if that
# line is absent or commented, standalone mode is used automatically.
#
# [daemon]
# listen        = "127.0.0.1:9876"
# poll_interval = "30s"
# discovery     = "on-start"   # "on-start" | "off" | "periodic:<duration>"
# # Optional fleet-wide protocol password. Used for the daemon's wildcard
# # discovery probes (works around firmware that drops mismatched wildcards)
# # and inherited by any [devices.NAME] block that omits its own password.
# password      = "your-protocol-password"

# One [devices.<name>] block per Breezy unit. Run ` + "`breezy discover`" + ` to
# find device IDs on your LAN, then uncomment one of the blocks below
# and fill in your values. The ip line is optional in daemon mode (on-
# start discovery resolves it); in standalone mode the ip is required.
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
