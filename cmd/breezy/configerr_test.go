package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matryer/is"
)

// brokenHome points HOME at a temp dir holding a config that exists but
// fails to load, and returns the config path.
func brokenHome(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "breezy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodDevice = `
[devices.playroom]
id = "0011223344556677"
password = "1111"
ip = "127.0.0.1:1"
`

// A config that exists but fails to load must be reported, not turned
// into "unknown device", an empty table, or a silent mode switch.
func TestBrokenConfigIsReported(t *testing.T) {
	cases := map[string]struct {
		body string
		mode os.FileMode
	}{
		"bad toml":   {"[devices.playroom\n", 0o600},
		"wrong mode": {goodDevice, 0o644},
		"daemon listen, bad id": {"[daemon]\nlisten = \"127.0.0.1:9\"\n" +
			"[devices.playroom]\nid = \"0123456789abcdef0\"\npassword = \"1\"\nip = \"1.2.3.4\"\n", 0o600},
	}
	for name, tc := range cases {
		for _, args := range [][]string{{"playroom", "status"}, {"ls"}, {"daemon-url"}} {
			t.Run(name+"/"+args[0], func(t *testing.T) {
				is := is.New(t)
				p := brokenHome(t, tc.body, tc.mode)
				var stdout, stderr bytes.Buffer
				code := run(args, &stdout, &stderr, nil)
				is.Equal(code, 2)
				is.True(strings.HasPrefix(stderr.String(), "error: "))
				is.True(strings.Contains(stderr.String(), p))
				is.True(!strings.Contains(stderr.String(), "unknown device"))
				is.Equal(stdout.String(), "")
			})
		}
	}
}

// Commands that never consult the config keep working with a broken one,
// and --daemon URL does not need the config.
func TestBrokenConfigIgnoredWhenUnneeded(t *testing.T) {
	for _, args := range [][]string{
		{"version"}, {"--version"}, {"help"}, {"param"},
		{"--daemon", "http://x:1", "daemon-url"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			is := is.New(t)
			brokenHome(t, "[devices.playroom\n", 0o600)
			var stdout, stderr bytes.Buffer
			is.Equal(run(args, &stdout, &stderr, nil), 0)
			is.Equal(stderr.String(), "")
		})
	}
}
