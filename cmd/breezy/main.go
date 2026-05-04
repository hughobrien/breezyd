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
// This file holds only flag parsing, dispatch, and HTTP envelope
// plumbing. Per-verb cmd* functions live in commands.go; the
// human-friendly renderers live in render.go.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hughobrien/breezyd/internal/config"
)

// defaultDaemonURL is the fall-back when no --daemon flag is given and
// no config file exists or the file lacks a [daemon].listen entry.
const defaultDaemonURL = "http://127.0.0.1:9876"

// httpTimeout bounds every HTTP request the CLI issues. The daemon
// itself bounds its UDP work to ~5 s, so 10 s leaves headroom for
// OS-level retries without making a hung daemon look like a hang.
const httpTimeout = 10 * time.Second

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
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It returns the process exit code so
// tests can assert on stdout/stderr without intercepting os.Exit.
func run(args []string, stdout, stderr io.Writer) int {
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

	// Globals.
	switch rest[0] {
	case "ls":
		return cmdLs(daemonURL, stdout, stderr)
	case "discover":
		return cmdDiscover(stdout, stderr)
	case "daemon-url":
		fmt.Fprintln(stdout, daemonURL)
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
		return cmdStatus(daemonURL, name, stdout, stderr)
	case "on":
		return cmdPower(daemonURL, name, true, stdout, stderr)
	case "off":
		return cmdPower(daemonURL, name, false, stdout, stderr)
	case "speed":
		return cmdSpeed(daemonURL, name, vargs, stdout, stderr)
	case "mode":
		return cmdMode(daemonURL, name, vargs, stdout, stderr)
	case "heater":
		return cmdHeater(daemonURL, name, vargs, stdout, stderr)
	case "reset-filter":
		return cmdResetFilter(daemonURL, name, stdout, stderr)
	case "reset-faults":
		return cmdResetFaults(daemonURL, name, stdout, stderr)
	case "faults":
		return cmdFaults(daemonURL, name, stdout, stderr)
	case "firmware":
		return cmdFirmware(daemonURL, name, stdout, stderr)
	case "efficiency":
		return cmdEfficiency(daemonURL, name, stdout, stderr)
	case "rtc":
		return cmdRtc(daemonURL, name, vargs, stdout, stderr)
	case "get":
		return cmdGet(daemonURL, name, vargs, stdout, stderr)
	case "set":
		return cmdSet(daemonURL, name, vargs, stdout, stderr)
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

// ----------------------------------------------------------------------------
// HTTP plumbing
// ----------------------------------------------------------------------------

// httpJSON issues method url with body (if non-nil) marshalled as JSON,
// reads the entire response, and returns the status + raw bytes. The
// caller decodes success/error envelopes — we don't try to be clever
// about non-2xx here so unit tests can assert on whatever shape comes
// back.
func httpJSON(method, url string, body any) (int, []byte, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, nil, fmt.Errorf("encode body: %w", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, &buf)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, raw, err
	}
	return resp.StatusCode, raw, nil
}

// errEnvelope mirrors the daemon's standard error shape.
type errEnvelope struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// renderErr prints an error to stderr in the canonical
// `error: <msg> (<code>)` form and returns exit code 1. It tolerates
// non-envelope responses (raw HTML, empty body, etc.) by falling back
// to "HTTP <status>".
func renderErr(stderr io.Writer, status int, raw []byte, transportErr error) int {
	if transportErr != nil {
		fmt.Fprintf(stderr, "error: %s\n", transportErr)
		return 1
	}
	var e errEnvelope
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		if e.Code == "" {
			fmt.Fprintf(stderr, "error: %s\n", e.Error)
		} else {
			fmt.Fprintf(stderr, "error: %s (%s)\n", e.Error, e.Code)
		}
		return 1
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		fmt.Fprintf(stderr, "error: HTTP %d\n", status)
	} else {
		fmt.Fprintf(stderr, "error: HTTP %d: %s\n", status, body)
	}
	return 1
}

// doSimple is the common path for verbs that POST a JSON body and
// expect either an `{"ok":true}` ack or an error envelope. On success
// it prints a short "ok" line; on failure it routes through renderErr.
func doSimple(method, url string, body any, ack string, stdout, stderr io.Writer) int {
	status, raw, err := httpJSON(method, url, body)
	if err != nil {
		return renderErr(stderr, status, raw, err)
	}
	if status >= 400 {
		return renderErr(stderr, status, raw, nil)
	}
	fmt.Fprintln(stdout, ack)
	return 0
}
