// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/internal/promwrite"
	"github.com/matryer/is"
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
	is := is.New(t)
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
	is.NoErr(err)
	is.Equal(cfg.Daemon.Listen, "127.0.0.1:9876")
	is.Equal(cfg.Daemon.PollInterval, 30*time.Second)
	is.Equal(cfg.Daemon.Discovery, "on-start")
	is.Equal(len(cfg.Devices), 2)

	pr, ok := cfg.Devices["playroom"]
	is.True(ok) // playroom present
	is.Equal(pr.ID, "BREEZY00000000A0")
	is.Equal(pr.Password, "testpwd")
	is.Equal(pr.IP, "")

	br, ok := cfg.Devices["bedroom"]
	is.True(ok) // bedroom present
	is.Equal(br.ID, "BREEZY00000000A1")
	is.Equal(br.Password, "secret")
	is.Equal(br.IP, "192.168.1.42")
}

func TestLoad_AllowedHosts(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `
[daemon]
allowed_hosts = ["breezy.example.com", "breezy.lan"]
`)
	cfg, err := Load(path)
	is.NoErr(err)
	is.Equal(cfg.Daemon.AllowedHosts, []string{"breezy.example.com", "breezy.lan"})

	path = writeConfig(t, "[devices.a]\nid = \"BREEZY00000000A0\"\npassword = \"p\"\n")
	cfg, err = Load(path)
	is.NoErr(err)
	is.Equal(len(cfg.Daemon.AllowedHosts), 0)
}

func TestLoad_DefaultsApplied(t *testing.T) {
	is := is.New(t)
	// daemon table omitted entirely
	path := writeConfig(t, `
[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
	cfg, err := Load(path)
	is.NoErr(err)
	// Listen is intentionally NOT defaulted by Load — the CLI uses the
	// empty string to mean "no daemon configured → standalone mode". The
	// daemon applies its own default after Load returns.
	is.Equal(cfg.Daemon.Listen, "")
	is.Equal(cfg.Daemon.PollInterval, 30*time.Second)
	is.Equal(cfg.Daemon.Discovery, "on-start")
}

func TestLoad_DaemonPasswordInherited(t *testing.T) {
	is := is.New(t)
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
	is.NoErr(err)
	is.Equal(cfg.Daemon.Password, "fleetpwd")
	is.Equal(cfg.Devices["bedroom"].Password, "fleetpwd")
	is.Equal(cfg.Devices["office"].Password, "override")
	is.Equal(cfg.Devices["playroom"].Password, "fleetpwd")
}

func TestLoad_DaemonPasswordAbsentLeavesEmpty(t *testing.T) {
	is := is.New(t)
	// When [daemon].password is unset, devices without their own
	// password stay empty (current behaviour preserved).
	path := writeConfig(t, `
[devices.bedroom]
id = "BREEZY00000000A0"
`)
	cfg, err := Load(path)
	is.NoErr(err)
	is.Equal(cfg.Daemon.Password, "")
	is.Equal(cfg.Devices["bedroom"].Password, "")
}

func TestLoad_EmptyDevicesAccepted(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `
[daemon]
listen        = "127.0.0.1:9876"
poll_interval = "30s"
discovery     = "off"
`)
	cfg, err := Load(path)
	is.NoErr(err)
	is.Equal(len(cfg.Devices), 0)
}

// TestLoad_InvalidIdentifierNamesRejected pins that device names with hyphens
// or leading digits are rejected. They appear as datastar signal-path segments
// ($detailsOpen.<name>.sensors) and would silently break signal parsing. The
// restriction is enforced post-TOML-parse so it applies to any syntactically
// valid TOML key that fails the JS-identifier regex.
func TestLoad_InvalidIdentifierNamesRejected(t *testing.T) {
	// Use TOML quoted keys so the TOML parser accepts the names; the new
	// validation fires on the parsed name string.
	cases := []struct {
		name    string
		tomlKey string
	}{
		{"hyphen", `"my-device"`},
		{"leading digit", `"1start"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			path := writeConfig(t, `
[devices.`+tc.tomlKey+`]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
			_, err := Load(path)
			is.True(err != nil)                                        // error returned for invalid identifier
			is.True(strings.Contains(err.Error(), "valid identifier")) // mentions identifier requirement
		})
	}
}

func TestLoad_ReservedNamesRejected(t *testing.T) {
	for _, name := range []string{"ls", "discover", "daemon-url", "param", "LS", "Discover", "Daemon-URL", "Param"} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			path := writeConfig(t, `
[devices.`+name+`]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
			_, err := Load(path)
			is.True(err != nil)                                                 // error returned for reserved name
			is.True(strings.Contains(err.Error(), name))                        // mentions offending name
			is.True(strings.Contains(strings.ToLower(err.Error()), "reserved")) // mentions 'reserved'
		})
	}
}

func TestLoad_WorldReadableRejected(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	err := os.WriteFile(path, []byte(`
[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`), 0o644)
	is.NoErr(err)
	_, err = Load(path)
	is.True(err != nil)                            // mode 0644 rejected
	is.True(strings.Contains(err.Error(), "0600")) // error mentions 0600
	is.True(strings.Contains(err.Error(), "644"))  // error mentions actual mode
}

func TestLoad_GroupReadableRejected(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	err := os.WriteFile(path, []byte(`
[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`), 0o640)
	is.NoErr(err)
	_, err = Load(path)
	is.True(err != nil)                            // mode 0640 rejected
	is.True(strings.Contains(err.Error(), "0600")) // error mentions 0600
}

func TestLoad_PasswordFreeWorldReadableAccepted(t *testing.T) {
	is := is.New(t)
	// A system fallback like /etc/breezy/config.toml with only
	// [daemon].listen (no passwords) must be loadable at mode 0644 so
	// every user on the host can read it.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	err := os.WriteFile(path, []byte(`
[daemon]
listen = "127.0.0.1:9876"
`), 0o644)
	is.NoErr(err)
	cfg, err := Load(path)
	is.NoErr(err)
	is.Equal(cfg.Daemon.Listen, "127.0.0.1:9876")
}

func TestLoad_DaemonPasswordTriggersModeCheck(t *testing.T) {
	is := is.New(t)
	// [daemon].password counts as a secret too — a config with only a
	// fleet-wide password and no [devices] still requires mode 0600.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	err := os.WriteFile(path, []byte(`
[daemon]
password = "fleetpwd"
`), 0o644)
	is.NoErr(err)
	_, err = Load(path)
	is.True(err != nil)                            // daemon-password at 0644 rejected
	is.True(strings.Contains(err.Error(), "0600")) // error mentions 0600
}

func TestLoad_BadDeviceIDLength(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `
[devices.playroom]
id       = "TOOSHORT"
password = "testpwd"
`)
	_, err := Load(path)
	is.True(err != nil)                                // short id rejected
	is.True(strings.Contains(err.Error(), "playroom")) // mentions device name
	is.True(strings.Contains(err.Error(), "16"))       // mentions required length 16
	is.True(strings.Contains(err.Error(), "8"))        // mentions actual length 8
}

func TestLoad_BadDeviceIDLong(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `
[devices.playroom]
id       = "BREEZY00000000A0XTRA"
password = "testpwd"
`)
	_, err := Load(path)
	is.True(err != nil)                                // too-long id rejected
	is.True(strings.Contains(err.Error(), "playroom")) // mentions device name
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
			is := is.New(t)
			path := writeConfig(t, `
[daemon]
`+line+`
`)
			_, err := Load(path)
			is.True(err != nil) // bad discovery rejected
			lower := strings.ToLower(err.Error())
			is.True(strings.Contains(lower, "discovery") || strings.Contains(lower, "periodic")) // mentions discovery/periodic
		})
	}
}

func TestLoad_GoodPeriodicDiscovery(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `
[daemon]
discovery = "periodic:5m"
`)
	cfg, err := Load(path)
	is.NoErr(err)
	is.Equal(cfg.Daemon.Discovery, "periodic:5m")
}

func TestLoad_BadPollInterval(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `
[daemon]
poll_interval = "notaduration"
`)
	_, err := Load(path)
	is.True(err != nil)                                                      // bad poll_interval rejected
	is.True(strings.Contains(strings.ToLower(err.Error()), "poll_interval")) // mentions poll_interval
}

func TestLoad_MissingFile(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "doesnotexist.toml")
	_, err := Load(path)
	is.True(err != nil)                     // missing file rejected
	is.True(errors.Is(err, os.ErrNotExist)) // wraps os.ErrNotExist
}

func TestLoad_BadTOMLSyntax(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `this is not valid toml = = =`)
	_, err := Load(path)
	is.True(err != nil) // bad TOML rejected
}

func TestWriteDefault_FreshTempDir(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "breezyd.toml")
	is.NoErr(WriteDefault(path))

	st, err := os.Stat(path)
	is.NoErr(err)
	is.Equal(st.Mode().Perm(), os.FileMode(0o600))

	body, err := os.ReadFile(path)
	is.NoErr(err)
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
		is.True(strings.Contains(got, want)) // default config contains expected line
	}
	// Roundtrip: the bootstrap output must Load cleanly. This is the
	// strongest single regression guard against a future template edit
	// that breaks `breezyd` first-run.
	cfg, err := Load(path)
	is.NoErr(err)
	is.Equal(cfg.Daemon.Listen, "")
}

func TestWriteDefault_CreatesParent(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "breezyd.toml")
	is.NoErr(WriteDefault(path))

	parentSt, err := os.Stat(filepath.Dir(path))
	is.NoErr(err)
	is.True(parentSt.IsDir()) // parent is a directory
	is.Equal(parentSt.Mode().Perm(), os.FileMode(0o700))
}

func TestWriteDefault_RefusesToOverwrite(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "breezyd.toml")
	const sentinel = "user-supplied content; do not clobber"
	is.NoErr(os.WriteFile(path, []byte(sentinel), 0o600))

	err := WriteDefault(path)
	is.True(err != nil)                      // refused to overwrite
	is.True(errors.Is(err, ErrConfigExists)) // wraps ErrConfigExists

	got, err := os.ReadFile(path)
	is.NoErr(err)
	is.Equal(string(got), sentinel) // existing file preserved
}

func TestLoad_HomekitDisabledByDefault(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `
[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
	cfg, err := Load(path)
	is.NoErr(err)
	is.True(!cfg.Homekit.Enabled) // Homekit defaults to false
}

func TestLoad_HomekitEnabledDefaults(t *testing.T) {
	is := is.New(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := writeConfig(t, `
[homekit]
enabled = true

[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
	cfg, err := Load(path)
	is.NoErr(err)
	is.True(cfg.Homekit.Enabled)
	is.Equal(cfg.Homekit.BridgeName, "breezyd")
	is.True(strings.HasSuffix(cfg.Homekit.StateDir, "/breezyd/homekit")) // StateDir ends in /breezyd/homekit
}

func TestLoad_HomekitBridgeNameTooLong(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `
[homekit]
enabled     = true
bridge_name = "this-name-is-way-too-long-for-the-32-char-limit"
`)
	_, err := Load(path)
	is.True(err != nil)                                // long bridge_name rejected
	is.True(strings.Contains(err.Error(), "32 chars")) // mentions 32 char limit
}

func TestLoad_HomekitBadPort(t *testing.T) {
	is := is.New(t)
	path := writeConfig(t, `
[homekit]
enabled = true
port    = 80
`)
	_, err := Load(path)
	is.True(err != nil)                                  // low port rejected
	is.True(strings.Contains(err.Error(), "1024-65535")) // mentions port range
}

func TestLoad_HomekitTildeExpansion(t *testing.T) {
	is := is.New(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	path := writeConfig(t, `
[homekit]
enabled   = true
state_dir = "~/.config/breezyd/homekit"
`)
	cfg, err := Load(path)
	is.NoErr(err)
	want := filepath.Join(home, ".config", "breezyd", "homekit")
	is.Equal(cfg.Homekit.StateDir, want)
}

func TestLoad_Push(t *testing.T) {
	is := is.New(t)
	cfg, err := Load(writeConfig(t, `
[daemon.push]
offsite = "https://vm.example"
bigthink = "http://127.0.0.1:8428"
`))
	is.NoErr(err)
	is.Equal(cfg.Daemon.Push, promwrite.Targets{{Name: "bigthink", URL: "http://127.0.0.1:8428"}, {Name: "offsite", URL: "https://vm.example"}}) // sorted
	is.Equal(cfg.Daemon.PrometheusJob, "breezyd")                                                                                                // job defaults

	cfg, err = Load(writeConfig(t, `
[daemon]
prometheus_job = "vents"
`))
	is.NoErr(err)
	is.Equal(len(cfg.Daemon.Push), 0) // no targets: nothing pushed
	is.Equal(cfg.Daemon.PrometheusJob, "vents")

	_, err = Load(writeConfig(t, `
[daemon.push]
"my-host" = "http://127.0.0.1:8428"
`))
	is.True(err != nil) // not a label name
}

func TestLoad_RejectsBadPollInterval(t *testing.T) {
	for _, v := range []string{"0s", "-5s", "10ms", "999ms"} {
		t.Run(v, func(t *testing.T) {
			is := is.New(t)
			_, err := Load(writeConfig(t, "[daemon]\npoll_interval = \""+v+"\"\n"))
			is.True(err != nil)
			is.True(strings.Contains(err.Error(), "poll_interval"))
			is.True(strings.Contains(err.Error(), v))
		})
	}
	is := is.New(t)
	cfg, err := Load(writeConfig(t, "[daemon]\npoll_interval = \"1s\"\n"))
	is.NoErr(err)
	is.Equal(cfg.Daemon.PollInterval, time.Second)
}

func TestLoad_RejectsBadPeriodicDiscovery(t *testing.T) {
	for _, v := range []string{"periodic:0s", "periodic:-1m", "periodic:10ms"} {
		t.Run(v, func(t *testing.T) {
			is := is.New(t)
			_, err := Load(writeConfig(t, "[daemon]\ndiscovery = \""+v+"\"\n"))
			is.True(err != nil)
			is.True(strings.Contains(err.Error(), "discovery"))
			is.True(strings.Contains(err.Error(), v))
		})
	}
	is := is.New(t)
	_, err := Load(writeConfig(t, "[daemon]\ndiscovery = \"periodic:1s\"\n"))
	is.NoErr(err)
}

func TestLoad_RejectsLongPasswords(t *testing.T) {
	is := is.New(t)
	_, err := Load(writeConfig(t, "[daemon]\npassword = \"123456789\"\n"))
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "[daemon].password"))

	_, err = Load(writeConfig(t, "[devices.a]\nid = \"BREEZY00000000A0\"\npassword = \"123456789\"\n"))
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), `device "a"`))
	is.True(strings.Contains(err.Error(), "password"))

	_, err = Load(writeConfig(t, "[daemon]\npassword = \"12345678\"\n[devices.a]\nid = \"BREEZY00000000A0\"\npassword = \"12345678\"\n"))
	is.NoErr(err)
}

func TestLoad_RejectsHelpAndVersionNames(t *testing.T) {
	for _, n := range []string{"help", "version", "Help", "VERSION"} {
		t.Run(n, func(t *testing.T) {
			is := is.New(t)
			_, err := Load(writeConfig(t, "[devices."+n+"]\nid = \"BREEZY00000000A0\"\n"))
			is.True(err != nil)
			is.True(strings.Contains(err.Error(), n))
			is.True(strings.Contains(err.Error(), "reserved"))
		})
	}
}

func TestLoad_RejectsNonASCIIID(t *testing.T) {
	is := is.New(t)
	// 16 bytes but non-ASCII: "é" is 2 bytes.
	_, err := Load(writeConfig(t, "[devices.a]\nid = \"BREEZY0000000é0\"\n"))
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "id"))
}

func TestLoad_HomekitStateDirPrecedence(t *testing.T) {
	hk := "[homekit]\nenabled = true\n"
	t.Run("STATE_DIRECTORY beats XDG", func(t *testing.T) {
		is := is.New(t)
		t.Setenv("STATE_DIRECTORY", "/var/lib/breezyd")
		t.Setenv("XDG_STATE_HOME", "/x")
		cfg, err := Load(writeConfig(t, hk))
		is.NoErr(err)
		is.Equal(cfg.Homekit.StateDir, "/var/lib/breezyd/homekit")
	})
	t.Run("explicit wins", func(t *testing.T) {
		is := is.New(t)
		t.Setenv("STATE_DIRECTORY", "/var/lib/breezyd")
		cfg, err := Load(writeConfig(t, hk+"state_dir = \"/custom\"\n"))
		is.NoErr(err)
		is.Equal(cfg.Homekit.StateDir, "/custom")
	})
	t.Run("legacy dir kept when it exists and new one does not", func(t *testing.T) {
		is := is.New(t)
		xdg, sd := t.TempDir(), t.TempDir()
		is.NoErr(os.MkdirAll(filepath.Join(xdg, "breezyd", "homekit"), 0o700))
		t.Setenv("STATE_DIRECTORY", sd)
		t.Setenv("XDG_STATE_HOME", xdg)
		cfg, err := Load(writeConfig(t, hk))
		is.NoErr(err)
		is.Equal(cfg.Homekit.StateDir, filepath.Join(xdg, "breezyd", "homekit"))
	})
	t.Run("new dir wins when both exist", func(t *testing.T) {
		is := is.New(t)
		xdg, sd := t.TempDir(), t.TempDir()
		is.NoErr(os.MkdirAll(filepath.Join(xdg, "breezyd", "homekit"), 0o700))
		is.NoErr(os.MkdirAll(filepath.Join(sd, "homekit"), 0o700))
		t.Setenv("STATE_DIRECTORY", sd)
		t.Setenv("XDG_STATE_HOME", xdg)
		cfg, err := Load(writeConfig(t, hk))
		is.NoErr(err)
		is.Equal(cfg.Homekit.StateDir, filepath.Join(sd, "homekit"))
	})
	t.Run("new dir used when legacy absent", func(t *testing.T) {
		is := is.New(t)
		xdg, sd := t.TempDir(), t.TempDir()
		t.Setenv("STATE_DIRECTORY", sd)
		t.Setenv("XDG_STATE_HOME", xdg)
		cfg, err := Load(writeConfig(t, hk))
		is.NoErr(err)
		is.Equal(cfg.Homekit.StateDir, filepath.Join(sd, "homekit"))
	})
	t.Run("XDG fallback", func(t *testing.T) {
		is := is.New(t)
		t.Setenv("STATE_DIRECTORY", "")
		t.Setenv("XDG_STATE_HOME", "/x")
		cfg, err := Load(writeConfig(t, hk))
		is.NoErr(err)
		is.Equal(cfg.Homekit.StateDir, "/x/breezyd/homekit")
	})
}
