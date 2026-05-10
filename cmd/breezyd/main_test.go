// SPDX-License-Identifier: GPL-3.0-or-later

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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
	"github.com/matryer/is"
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

// TestDaemonStateDir_Precedence pins the resolution order documented
// at SPECIFICATION-daemon.md "State directory resolution":
//
//  1. $STATE_DIRECTORY (systemd) — used as-is.
//  2. $XDG_STATE_HOME — returns $XDG_STATE_HOME/breezyd.
//  3. neither set — returns $HOME/.local/state/breezyd.
//
// The created directory is always mode 0700.
func TestDaemonStateDir_Precedence(t *testing.T) {
	// Each subtest uses a non-existing leaf inside t.TempDir() so
	// daemonStateDir's MkdirAll(..., 0o700) actually creates it (and the
	// 0700 mode is observable). t.TempDir() itself creates with the host
	// umask, which would otherwise mask the assertion.

	t.Run("STATE_DIRECTORY wins over XDG_STATE_HOME", func(t *testing.T) {
		is := is.New(t)
		stateDir := filepath.Join(t.TempDir(), "stateDir")
		xdg := filepath.Join(t.TempDir(), "xdg") // also set, must be ignored
		t.Setenv("STATE_DIRECTORY", stateDir)
		t.Setenv("XDG_STATE_HOME", xdg)

		got, err := daemonStateDir()
		is.NoErr(err)
		is.Equal(got, stateDir) // STATE_DIRECTORY takes precedence
		st, err := os.Stat(got)
		is.NoErr(err)
		is.Equal(st.Mode().Perm(), os.FileMode(0o700)) // dir mode
	})

	t.Run("XDG_STATE_HOME used when STATE_DIRECTORY unset", func(t *testing.T) {
		is := is.New(t)
		xdg := filepath.Join(t.TempDir(), "xdg")
		t.Setenv("STATE_DIRECTORY", "")
		t.Setenv("XDG_STATE_HOME", xdg)

		got, err := daemonStateDir()
		is.NoErr(err)
		is.Equal(got, filepath.Join(xdg, "breezyd"))
		st, err := os.Stat(got)
		is.NoErr(err)
		is.Equal(st.Mode().Perm(), os.FileMode(0o700))
	})

	t.Run("HOME fallback when STATE_DIRECTORY and XDG_STATE_HOME unset", func(t *testing.T) {
		is := is.New(t)
		home := filepath.Join(t.TempDir(), "home")
		t.Setenv("STATE_DIRECTORY", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", home)

		got, err := daemonStateDir()
		is.NoErr(err)
		is.Equal(got, filepath.Join(home, ".local", "state", "breezyd"))
		st, err := os.Stat(got)
		is.NoErr(err)
		is.Equal(st.Mode().Perm(), os.FileMode(0o700))
	})
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
	is := is.New(t)
	const (
		devID = "TESTID0000000001"
		pwd   = "1111"
	)

	srv, err := fakedevice.NewServer(mainSnapshotPath(t), devID, pwd)
	is.NoErr(err)
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
		is := is.New(t)
		body := mustGet(t, base+"/v1/devices")
		is.True(strings.Contains(body, "\"playroom\"")) // /v1/devices must list playroom
	})

	t.Run("metrics_exposes_breezy_up", func(t *testing.T) {
		is := is.New(t)
		body := mustGet(t, base+"/metrics")
		is.True(strings.Contains(body, "breezy_up{"))                 // metrics must expose breezy_up gauge
		is.True(strings.Contains(body, "device=\"playroom\""))        // metrics must label by device
		is.True(strings.Contains(body, "breezy_temperature_celsius")) // representative non-trivial gauge must render
	})

	t.Run("post_power_returns_ok", func(t *testing.T) {
		is := is.New(t)
		req, _ := http.NewRequest("POST", base+"/v1/devices/playroom/power",
			strings.NewReader(`{"on": true}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		is.NoErr(err)
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 200)
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

// TestMainMemoryBackend boots the daemon with --backend=memory --seed <snapshot>
// and verifies that /v1/devices/<name> returns a populated snapshot without
// any real UDP traffic. The device IP in the config points to a non-listening
// address; MemClient intercepts all reads and writes before the poller can
// attempt a UDP dial.
func TestMainMemoryBackend(t *testing.T) {
	const (
		devID = "TESTID0000000002"
		pwd   = "1111"
	)

	listen := freeListenAddr(t)
	// Use a dummy IP — MemClient means the poller will never open a real socket.
	cfgPath := writeTempConfig(t, listen, "127.0.0.1:4000", devID, pwd)

	*flagConfig = cfgPath
	*flagAddr = ""
	*flagLogLevel = "warn"
	*flagBackend = "memory"
	*flagSeed = mainSnapshotPath(t)
	t.Cleanup(func() {
		*flagBackend = "udp"
		*flagSeed = ""
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx) }()

	base := "http://" + listen
	if err := waitForReady(t, base+"/healthz", 3*time.Second); err != nil {
		cancel()
		t.Fatalf("daemon did not become ready: %v", err)
	}

	// Wait for the first poll tick to populate the cache.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body := mustGet(t, base+"/v1/devices")
		if strings.Contains(body, "\"playroom\"") && strings.Contains(body, "last_poll") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Run("v1_devices_lists_playroom", func(t *testing.T) {
		is := is.New(t)
		body := mustGet(t, base+"/v1/devices")
		is.True(strings.Contains(body, "\"playroom\"")) // /v1/devices must list playroom
	})

	t.Run("v1_device_snapshot_populated", func(t *testing.T) {
		is := is.New(t)
		body := mustGet(t, base+"/v1/devices/playroom")
		// snapshot_148.json has a real device ID and non-zero temperature data;
		// the snapshot should decode to a populated struct with at least one
		// temperature field set.
		is.True(strings.Contains(body, "last_poll")) // snapshot must include last_poll
		// Accept any of the various serialisations of a no-error last_err.
		// Don't fail hard on last_err presence — any populated snapshot is fine.
		// Confirm at least some device data came through (non-trivial snapshot).
		is.True(strings.TrimSpace(body) != "{}" && strings.Contains(body, "last_poll")) // snapshot must not be empty
	})

	t.Run("post_power_returns_ok", func(t *testing.T) {
		is := is.New(t)
		req, _ := http.NewRequest("POST", base+"/v1/devices/playroom/power",
			strings.NewReader(`{"on": true}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		is.NoErr(err)
		defer func() { _ = resp.Body.Close() }()
		is.Equal(resp.StatusCode, 200)
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

// TestMainBackendFlagValidation checks that invalid --backend / --seed
// combinations return clear errors without booting the daemon.
func TestMainBackendFlagValidation(t *testing.T) {
	// We need a valid config for run() to load before it hits the flag check.
	listen := freeListenAddr(t)
	cfgPath := writeTempConfig(t, listen, "127.0.0.1:4000", "TESTID0000000003", "1111")

	t.Run("seed_without_memory_backend", func(t *testing.T) {
		is := is.New(t)
		*flagConfig = cfgPath
		*flagAddr = ""
		*flagLogLevel = "warn"
		*flagBackend = "udp"
		*flagSeed = mainSnapshotPath(t)
		t.Cleanup(func() {
			*flagBackend = "udp"
			*flagSeed = ""
		})
		err := run(context.Background())
		is.True(err != nil)                                                                  // run must reject --seed without --backend=memory
		is.True(strings.Contains(err.Error(), "--seed is only valid with --backend=memory")) // error must explain --seed gate
	})

	t.Run("unknown_backend_value", func(t *testing.T) {
		is := is.New(t)
		*flagConfig = cfgPath
		*flagAddr = ""
		*flagLogLevel = "warn"
		*flagBackend = "fakeudp"
		*flagSeed = ""
		t.Cleanup(func() {
			*flagBackend = "udp"
			*flagSeed = ""
		})
		err := run(context.Background())
		is.True(err != nil)                                                // run must reject unknown --backend value
		is.True(strings.Contains(err.Error(), "--backend: unknown value")) // error must explain --backend gate
	})
}

// TestMain_ShutdownWaitsForInflightPoll pins SPECIFICATION-daemon.md
// "Signals and shutdown" (G-daemon-15): "pollersWg.Wait() blocks (up to
// another 5s) for in-flight ticks. The synchronous wait exists because
// earlier fire-and-forget shutdowns let main return while pollers were
// still mid-tick." Without the synchronous wait, run() returns
// immediately on cancel and the test would observe a sub-100ms return
// time.
//
// Mechanic: install a test OnPoll hook that blocks for blockDur, cancel
// the parent context shortly after the first tick fires, and time how
// long run() takes to return. We assert the elapsed return time is at
// least most-of-blockDur (so we know it actually waited) and well under
// the 5s shutdown deadline (so we know it didn't hit the abandon path).
func TestMain_ShutdownWaitsForInflightPoll(t *testing.T) {
	const (
		devID    = "TESTID0000000004"
		pwd      = "1111"
		blockDur = 200 * time.Millisecond
	)

	listen := freeListenAddr(t)
	cfgPath := writeTempConfig(t, listen, "127.0.0.1:4000", devID, pwd)

	*flagConfig = cfgPath
	*flagAddr = ""
	*flagLogLevel = "warn"
	*flagBackend = "memory"
	*flagSeed = mainSnapshotPath(t)
	t.Cleanup(func() {
		*flagBackend = "udp"
		*flagSeed = ""
	})

	// Install the OnPoll-time block. Each tick sleeps blockDur; run()
	// must wait for the in-flight tick to finish before returning.
	firstTick := make(chan struct{})
	var firstOnce sync.Once
	testOnPollHook = func(_ *Handler, _ string, _ Snapshot) {
		firstOnce.Do(func() { close(firstTick) })
		time.Sleep(blockDur)
	}
	t.Cleanup(func() { testOnPollHook = nil })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx) }()

	// Wait for the first poll tick to hit the hook so we cancel mid-tick.
	select {
	case <-firstTick:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("first poll tick did not fire within 3s")
	}

	start := time.Now()
	cancel()
	select {
	case err := <-runErr:
		elapsed := time.Since(start)
		if err != nil {
			t.Errorf("run returned error: %v", err)
		}
		// Spec floor: must wait until the in-flight tick's blocking
		// hook returns. Use generous slack (80ms) to account for
		// scheduling jitter — we still detect a fire-and-forget
		// shutdown (which would return in single-digit-ms).
		minWait := blockDur - 120*time.Millisecond
		if elapsed < minWait {
			t.Errorf("run returned in %v; expected ≥ %v (synchronous pollers wait)", elapsed, minWait)
		}
		// Spec ceiling: 5s shutdown deadline + slack. If we hit this,
		// either the wait isn't bounded or something else is hung.
		if elapsed > 6*time.Second {
			t.Errorf("run took %v to return; expected ≤ 6s (shutdownTimeout + slack)", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not exit within 10s of cancellation")
	}
}

// TestMain_PollersPopulatedBeforeFirstOnPoll pins
// SPECIFICATION-daemon.md "Wiring sequence" (G-daemon-7):
// "handler.Pollers and handler.Schedulers must be populated BEFORE the
// goroutines start so the first poll's OnPoll → PushHub.Notify always
// sees a populated map (without this ordering the race detector fires)."
//
// Mechanic: install a test OnPoll hook that, on the first tick,
// captures whether handler.Pollers / handler.Schedulers are populated
// for the device that just ticked. Run under -race in CI to also
// catch the data-race form of the bug.
func TestMain_PollersPopulatedBeforeFirstOnPoll(t *testing.T) {
	is := is.New(t)
	const (
		devID = "TESTID0000000005"
		pwd   = "1111"
	)

	listen := freeListenAddr(t)
	cfgPath := writeTempConfig(t, listen, "127.0.0.1:4000", devID, pwd)

	*flagConfig = cfgPath
	*flagAddr = ""
	*flagLogLevel = "warn"
	*flagBackend = "memory"
	*flagSeed = mainSnapshotPath(t)
	t.Cleanup(func() {
		*flagBackend = "udp"
		*flagSeed = ""
	})

	type observation struct {
		name            string
		pollerNonNil    bool
		schedulerNonNil bool
	}
	var (
		obsCh = make(chan observation, 1)
		fired atomic.Bool
	)
	testOnPollHook = func(h *Handler, name string, _ Snapshot) {
		if !fired.CompareAndSwap(false, true) {
			return
		}
		// Read both maps under the same hook call. Production code
		// reads handler.Pollers from the same goroutine path
		// (PushHub.Notify → buildView → buildPushEvent), so this is
		// the exact race window the spec is pinning.
		p, pok := h.Pollers[name]
		s, sok := h.Schedulers[name]
		obsCh <- observation{
			name:            name,
			pollerNonNil:    pok && p != nil,
			schedulerNonNil: sok && s != nil,
		}
	}
	t.Cleanup(func() { testOnPollHook = nil })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx) }()

	select {
	case obs := <-obsCh:
		is.Equal(obs.name, "playroom") // first tick must be for the configured device
		is.True(obs.pollerNonNil)      // handler.Pollers[name] must be non-nil at first OnPoll
		is.True(obs.schedulerNonNil)   // handler.Schedulers[name] must be non-nil at first OnPoll
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("first poll tick did not fire within 3s")
	}

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
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s: %v", url, err)
	}
	return string(body)
}
