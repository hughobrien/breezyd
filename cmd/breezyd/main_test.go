// Smoke test for the daemon's main(): boot run() against an
// in-process fakedevice, hit /v1/devices, /metrics, and a write
// endpoint, then trigger graceful shutdown.
//
// We don't try to model multi-device topology or discovery here —
// the goal is to confirm the wiring (cfg → state → poller → http →
// metrics) holds together end-to-end.
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
)

// mainSnapshotPath returns the absolute path to the captured device
// snapshot used by every fake device in the test suite.
func mainSnapshotPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../pkg/breezy/fakedevice/snapshot_148.json")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	return p
}

// writeTempConfig writes a minimal TOML config wired to fakeAddr at
// the given listen address and returns the file path. Mode 0600 is
// enforced because the loader refuses anything looser.
func writeTempConfig(t *testing.T, listen, fakeAddr, deviceID, password string) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	body := fmt.Sprintf(`[daemon]
listen        = "%s"
poll_interval = "100ms"
discovery     = "off"

[devices.playroom]
id       = "%s"
password = "%s"
ip       = "%s"
`, listen, deviceID, password, fakeAddr)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfgPath
}

// freeListenAddr binds an ephemeral 127.0.0.1 TCP port, immediately
// closes it, and returns the addr string. There's a small TOCTOU
// window before the daemon binds, but it's negligible in a single
// test process.
func freeListenAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen ephemeral: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestMainSmoke boots the daemon end-to-end and probes a few endpoints.
func TestMainSmoke(t *testing.T) {
	const (
		devID = "TESTID0000000001"
		pwd   = "1111"
	)

	srv, err := fakedevice.NewServer(mainSnapshotPath(t), devID, pwd)
	if err != nil {
		t.Fatalf("fakedevice.NewServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	listen := freeListenAddr(t)
	cfgPath := writeTempConfig(t, listen, srv.Addr(), devID, pwd)

	// Override flags for this test. flag.Parse() runs from main()
	// (not run()), so writing through the *flag pointers here is the
	// supported route.
	*flagConfig = cfgPath
	*flagAddr = ""
	*flagLogLevel = "warn"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx) }()

	base := "http://" + listen
	if err := waitForReady(t, base+"/healthz", 3*time.Second); err != nil {
		cancel()
		t.Fatalf("daemon did not become ready: %v", err)
	}

	// Wait briefly for the first poll tick to populate the cache.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body := mustGet(t, base+"/v1/devices")
		if strings.Contains(body, "\"playroom\"") && strings.Contains(body, "last_poll") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Run("v1_devices_lists_playroom", func(t *testing.T) {
		body := mustGet(t, base+"/v1/devices")
		if !strings.Contains(body, "\"playroom\"") {
			t.Errorf("v1/devices missing playroom: %s", body)
		}
	})

	t.Run("metrics_exposes_breezy_up", func(t *testing.T) {
		body := mustGet(t, base+"/metrics")
		if !strings.Contains(body, "breezy_up{") {
			t.Errorf("metrics missing breezy_up gauge")
		}
		if !strings.Contains(body, "device=\"playroom\"") {
			t.Errorf("metrics missing device=\"playroom\" label")
		}
		// Confirm a representative non-trivial gauge has rendered too.
		if !strings.Contains(body, "breezy_temperature_celsius") {
			t.Errorf("metrics missing breezy_temperature_celsius")
		}
	})

	t.Run("post_power_returns_ok", func(t *testing.T) {
		req, _ := http.NewRequest("POST", base+"/v1/devices/playroom/power",
			strings.NewReader(`{"on": true}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			t.Errorf("POST status = %d, body = %s", resp.StatusCode, b)
		}
	})

	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Errorf("run returned error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not exit within 10s of cancellation")
	}
}

func waitForReady(t *testing.T, url string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", url)
}

func mustGet(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", url, err)
	}
	return string(body)
}
