// SPDX-License-Identifier: GPL-3.0-or-later

// breezy is the operator CLI for Vents Twinfresh Breezy ERVs. By
// default it talks UDP directly to each configured device (standalone
// mode). When the user opts in via --daemon URL or [daemon].listen in
// ~/.config/breezy/config.toml, it talks HTTP to the breezyd daemon
// instead. `discover` always issues UDP directly to the LAN —
// broadcasting by default, or unicast to positional IP arguments
// (`breezy discover 192.168.1.148 ...`) when the network drops
// broadcasts. Independent of mode.
//
// The CLI surface is "subject before verb" so per-device verbs read
// naturally:
//
//	breezy playroom status
//	breezy playroom speed manual:30
//	breezy playroom mode regeneration
//
// Globals (`ls`, `discover`, `daemon-url`, `param`) are detected by checking
// the first arg against a small reserved-name set.
//
// Exit codes:
//   - 0 success
//   - 1 backend error: in daemon mode this is an HTTP error (the
//     daemon's {"error","code"} envelope is decoded and rendered as
//     `error: <msg> (<code>)`); in standalone mode it's a UDP /
//     protocol error rendered as `error: <msg>`.
//   - 2 local usage error (bad args, validation failure before any I/O)
//
// This file holds only flag parsing, dispatch, and backend construction.
// Per-verb cmd* functions live in commands.go; the human-friendly
// renderers live in render.go; backend implementations live in backend.go.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hughobrien/breezyd/internal/config"
)

// discoverTimeout bounds the LAN broadcast in `breezy discover`.
const discoverTimeout = 3 * time.Second

// Build metadata. These are populated by goreleaser via -ldflags at build
// time; an unbuilt local binary reports "dev" / "none" / "unknown" so
// `breezy --version` is always meaningful.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, nil))
}

// run is the testable entry point. It returns the process exit code so
// tests can assert on stdout/stderr without intercepting os.Exit.
//
// If injected is non-nil, it overrides the backend that run() would
// otherwise construct from flags + config. Tests pass a directBackend
// pointed at a fakedevice; production passes nil. Plumbing the seam
// through the parameter rather than a package-level variable keeps
// run() safe to invoke from parallel tests in the future.
func run(args []string, stdout, stderr io.Writer, injected backend) int {
	fs := flag.NewFlagSet("breezy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	daemon := fs.String("daemon", "", "daemon URL (overrides config)")
	versionFlag := fs.Bool("version", false, "print version information and exit")

	if err := fs.Parse(args); err != nil {
		// flag prints its own message + usage; we just need the right code.
		return 2
	}

	if *versionFlag {
		fmt.Fprintf(stdout, "breezy %s (commit %s, built %s)\n", version, commit, date)
		return 0
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return 2
	}

	b := injected
	if b == nil {
		cfg := loadConfig()
		var err error
		b, err = resolveBackend(*daemon, cfg)
		if err != nil {
			fmt.Fprintf(stderr, "error: %s\n", err)
			return 1
		}
	}
	defer b.Close()

	// Globals.
	switch rest[0] {
	case "ls":
		return cmdLs(b, stdout, stderr)
	case "discover":
		return cmdDiscover(rest[1:], stdout, stderr)
	case "daemon-url":
		url := b.DaemonURLString()
		if url == "" {
			fmt.Fprintln(stdout, "(standalone — no daemon)")
		} else {
			fmt.Fprintln(stdout, url)
		}
		return 0
	case "param":
		return cmdParam(stdout)
	case "version":
		fmt.Fprintf(stdout, "breezy %s (commit %s, built %s)\n", version, commit, date)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}

	// Per-device verbs: `breezy <name> <verb> [args...]`.
	if len(rest) < 2 {
		fmt.Fprintln(stderr, "usage: breezy <name> <verb> [args]")
		return 2
	}

	name, verb, vargs := rest[0], rest[1], rest[2:]

	switch verb {
	case "status":
		return cmdStatus(b, name, stdout, stderr)
	case "on":
		return cmdPower(b, name, true, stdout, stderr)
	case "off":
		return cmdPower(b, name, false, stdout, stderr)
	case "speed":
		return cmdSpeed(b, name, vargs, stdout, stderr)
	case "mode":
		return cmdMode(b, name, vargs, stdout, stderr)
	case "heater":
		return cmdHeater(b, name, vargs, stdout, stderr)
	case "reset-filter":
		return cmdResetFilter(b, name, stdout, stderr)
	case "reset-faults":
		return cmdResetFaults(b, name, stdout, stderr)
	case "faults":
		return cmdFaults(b, name, stdout, stderr)
	case "firmware":
		return cmdFirmware(b, name, stdout, stderr)
	case "efficiency":
		return cmdEfficiency(b, name, stdout, stderr)
	case "rtc":
		return cmdRtc(b, name, vargs, stdout, stderr)
	case "get":
		return cmdGet(b, name, vargs, stdout, stderr)
	case "set":
		return cmdSet(b, name, vargs, stdout, stderr)
	}

	fmt.Fprintf(stderr, "unknown verb: %s\n", verb)
	return 2
}

const usage = `breezy: control Vents Twinfresh Breezy ERVs

Usage:
  breezy [--daemon URL] <name> <verb> [args]
  breezy [--daemon URL] <global>

Per-device verbs:
  status                show structured snapshot
  on | off              power on/off
  speed <1|2|3>         select preset
  speed manual:<pct>    manual % (10..100)
  mode <ventilation|regeneration|supply|extract>
  heater on|off
  reset-filter          reset filter timer
  reset-faults          clear active faults
  faults                list active faults
  firmware              show firmware version + build date
  efficiency            recovery efficiency %
  rtc                   show device clock
  rtc set <RFC3339>     set device clock
  get <param>           raw read (hex 0x25, "25", or registry name e.g. humidity)
  set <param> <hex>     raw write

Globals:
  ls                    one-line summary of every configured device
  discover [-p PWD] [ip...]  LAN broadcast (or unicast to each IP if given);
                        -p overrides the factory-default discovery password
  daemon-url            print the URL the CLI would use
  param                 list known parameters (id, type, unit, caps)
`

// loadConfig reads ~/.config/breezy/config.toml. Errors silently fall
// through to nil — running breezy on a fresh box without a config
// should still produce useful behavior (standalone mode).
func loadConfig() *config.Config {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	cfg, err := config.Load(filepath.Join(home, ".config", "breezy", "config.toml"))
	if err != nil {
		return nil
	}
	return cfg
}

// resolveBackend picks a backend based on the precedence:
//
//  1. --daemon URL flag (explicit override).
//  2. ~/.config/breezy/config.toml [daemon].listen.
//  3. Standalone (direct UDP via pkg/breezy/ops).
//
// There is no fallback URL: if neither a flag nor config opts in to
// daemon mode, we go standalone. The user's choice is honoured —
// daemon-mode-but-unreachable surfaces as a clear HTTP error from the
// first request, not a silent fall-through.
func resolveBackend(override string, cfg *config.Config) (backend, error) {
	if override != "" {
		return newDaemonBackend(normalizeURL(override)), nil
	}
	if cfg != nil && cfg.Daemon.Listen != "" {
		return newDaemonBackend(normalizeURL(cfg.Daemon.Listen)), nil
	}
	devices := map[string]config.Device{}
	if cfg != nil {
		devices = cfg.Devices
	}
	return newDirectBackend(devices), nil
}

// normalizeURL prepends http:// when the operator wrote bare host:port
// in the config (TOML files commonly do this).
func normalizeURL(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}
