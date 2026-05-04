// SPDX-License-Identifier: GPL-3.0-or-later

// breezy is the operator CLI for the breezyd daemon. It speaks HTTP to
// the daemon for everything except `discover`, which performs a real
// LAN broadcast for first-time setup.
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
//   - 1 HTTP / daemon error (the daemon's {"error","code"} envelope is
//     decoded and rendered as `error: <msg> (<code>)`)
//   - 2 local usage error (bad args, validation failure before HTTP)
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

// defaultDaemonURL is the fall-back when no --daemon flag is given and
// no config file exists or the file lacks a [daemon].listen entry.
const defaultDaemonURL = "http://127.0.0.1:9876"

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

	daemonURL := resolveDaemonURL(*daemon)
	b := injected
	if b == nil {
		b = newDaemonBackend(daemonURL)
	}
	defer b.Close()

	// Globals.
	switch rest[0] {
	case "ls":
		return cmdLs(b, stdout, stderr)
	case "discover":
		return cmdDiscover(stdout, stderr)
	case "daemon-url":
		fmt.Fprintln(stdout, b.DaemonURLString())
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

const usage = `breezy: control Vents Twinfresh Breezy ERVs via the breezyd daemon

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
  discover              LAN broadcast (bypasses daemon)
  daemon-url            print the URL the CLI would use
  param                 list known parameters (id, type, unit, caps)
`

// resolveDaemonURL chooses the daemon URL according to the precedence:
//
//  1. --daemon flag (override).
//  2. ~/.config/breezy/config.toml [daemon].listen.
//  3. defaultDaemonURL.
//
// Any error reading or parsing the config falls through to the default
// silently — running breezy on a fresh box without a config file should
// still be useful (e.g. `breezy daemon-url`).
func resolveDaemonURL(override string) string {
	if override != "" {
		return normalizeURL(override)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return defaultDaemonURL
	}
	cfg, err := config.Load(filepath.Join(home, ".config", "breezy", "config.toml"))
	if err != nil || cfg == nil || cfg.Daemon.Listen == "" {
		return defaultDaemonURL
	}
	return normalizeURL(cfg.Daemon.Listen)
}

// normalizeURL prepends http:// when the operator wrote bare host:port
// in the config (TOML files commonly do this).
func normalizeURL(addr string) string {
	if strings.Contains(addr, "://") {
		return addr
	}
	return "http://" + addr
}
