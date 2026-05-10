// SPDX-License-Identifier: GPL-3.0-or-later

// breezyd is the long-running daemon that owns the UDP/4000 conversation
// with each Vents Breezy ERV. main() wires together:
//
//   - config (internal/config)
//   - state cache (state.go)
//   - per-device poller goroutine (poller.go)
//   - HTTP API + Prometheus /metrics (server.go, metrics.go)
//
// Discovery on-start is best-effort: a configured device whose IP
// changed since last boot is updated; an unconfigured device is logged
// once. The daemon does NOT auto-add unconfigured devices — passwords
// must be supplied by the operator.
//
// Graceful shutdown: SIGINT/SIGTERM cancels the root context (stopping
// every poller) and shuts down the HTTP server with a 5s deadline.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hughobrien/breezyd/internal/config"
	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Discovery defaults — kept here rather than buried in config so the
// timeout is easy to find when tuning for slow networks.
const discoveryTimeout = 3 * time.Second

// shutdownTimeout bounds how long Shutdown waits for in-flight HTTP
// requests to finish before forcibly closing connections.
const shutdownTimeout = 5 * time.Second

var (
	flagConfig   = flag.String("config", defaultConfigPath(), "config file path")
	flagAddr     = flag.String("addr", "", "listen address (overrides config)")
	flagLogLevel = flag.String("log-level", "info", "log level (debug|info|warn|error)")
	flagVersion  = flag.Bool("version", false, "print version information and exit")
	flagBackend  = flag.String("backend", "udp", "client backend (udp|memory)")
	flagSeed     = flag.String("seed", "", "fakedevice JSON snapshot for --backend=memory")
)

// Build metadata. These are populated by goreleaser via -ldflags at build
// time; an unbuilt local binary reports "dev" / "none" / "unknown" so
// `breezyd --version` is always meaningful.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// testOnPollHook is a process-level test seam. When non-nil, the composed
// poll fan-out calls it on every tick BEFORE SyncHomekit / PushHub.Notify,
// passing the live Handler so wiring-order tests can assert
// handler.Pollers / handler.Schedulers are populated, and so blocking
// shutdown-wait tests can sleep here. Tests must reset to nil via
// t.Cleanup; production code never touches it.
var testOnPollHook func(h *Handler, name string, snap Snapshot)

// defaultConfigPath returns ~/.config/breezy/config.toml. When the
// home dir can't be determined we fall back to a relative path so
// running breezyd in a sandbox without HOME still produces a useful
// "file not found" error rather than panicking.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "config.toml"
	}
	return filepath.Join(home, ".config", "breezy", "config.toml")
}

func main() {
	flag.Parse()
	if *flagVersion {
		fmt.Printf("breezyd %s (commit %s, built %s)\n", version, commit, date)
		return
	}
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "breezyd:", err)
		os.Exit(1)
	}
}

// run is the testable variant of main: instead of os.Exit it returns
// an error, and instead of installing signal handlers itself it
// derives the root context from the caller's. The smoke test in
// main_test.go drives this directly.
func run(parent context.Context) error {
	setupLogging(*flagLogLevel)

	cfg, err := config.Load(*flagConfig)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if werr := config.WriteDefault(*flagConfig); werr != nil {
				return fmt.Errorf("config: bootstrap: %w", werr)
			}
			return fmt.Errorf("breezyd: no config file existed at %s — wrote a default; "+
				"edit it to add at least one [devices.<name>] block, then run breezyd again",
				*flagConfig)
		}
		return fmt.Errorf("config: %w", err)
	}

	// Validate --backend and --seed before doing any further setup.
	if *flagSeed != "" && *flagBackend != "memory" {
		return fmt.Errorf("--seed is only valid with --backend=memory")
	}
	if *flagBackend != "udp" && *flagBackend != "memory" {
		return fmt.Errorf("--backend: unknown value %q (allowed: udp, memory)", *flagBackend)
	}

	// Build one MemClient per configured device when backend=memory. All
	// callers (poller, handler) share the same instance per device so
	// writes from handlers are immediately visible to poller reads.
	var memClients map[string]*breezy.MemClient
	if *flagBackend == "memory" {
		memClients = make(map[string]*breezy.MemClient, len(cfg.Devices))
		for name := range cfg.Devices {
			if *flagSeed != "" {
				c, err := breezy.NewMemClientFromFile(*flagSeed)
				if err != nil {
					return fmt.Errorf("device %q: %w", name, err)
				}
				memClients[name] = c
			} else {
				memClients[name] = breezy.NewMemClient(nil)
			}
		}
	}

	listen := cfg.Daemon.Listen
	if listen == "" {
		listen = "127.0.0.1:9876" // hardcoded default when [daemon].listen is absent; --addr still overrides below
	}
	if *flagAddr != "" {
		listen = *flagAddr
	}

	devices := NewDeviceRegistry(buildDeviceMap(cfg))

	if cfg.Daemon.Discovery == "on-start" {
		if err := runDiscovery(parent, devices, cfg.Daemon.Password); err != nil {
			slog.Warn("discovery failed", "err", err)
		}
	}

	state := NewState()
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	rootCtx, rootCancel := context.WithCancel(parent)
	defer rootCancel()

	handler := &Handler{
		State:         state,
		Devices:       devices,
		ClientFactory: makeClientFactory(devices, memClients),
		PollInterval:  cfg.Daemon.PollInterval,
	}
	// Render closure captures handler so PushHub can build a structured
	// PushEvent for any (name, snap) tuple — both the poll path and the
	// post-write path drive it.
	handler.PushHub = NewPushHub(func(name string, _ Snapshot) (*PushEvent, error) {
		view, ok := handler.viewFor(name)
		if !ok {
			return nil, fmt.Errorf("no snapshot for %s", name)
		}
		return buildPushEvent(name, view)
	})

	stateDir, err := daemonStateDir()
	if err != nil {
		slog.Warn("energy: could not create state dir; energy tracking will not persist", "err", err)
	}
	// Compose poll-side fan-out: HomeKit characteristics first, then the
	// browser push hub. Both are independent and idempotent; either firing
	// in isolation is safe.
	onPoll := func(name string, snap Snapshot) {
		// testOnPollHook is the run-level test seam: when a test sets it,
		// it fires synchronously on every successful poll tick (with handler
		// attached so wiring-order assertions can read handler.Pollers).
		// Production leaves it nil and incurs zero cost.
		if hook := testOnPollHook; hook != nil {
			hook(handler, name, snap)
		}
		handler.SyncHomekit(name, snap)
	}
	onTick := func(name string, snap Snapshot) {
		// PushHub.Notify must fire on EVERY tick (success or failure) so
		// the dashboard's $lastPollAge / $stale signals advance under
		// sustained UDP timeouts (SPECIFICATION-web.md "Card states").
		handler.PushHub.Notify(name, snap)
	}
	pollers, schedulers, pollersWg, startPollerGoroutines := startPollers(
		rootCtx, devices.Snapshot(), cfg.Daemon.PollInterval,
		stateDir, state, metrics, onPoll, onTick,
		handler.scheduleDial, memClients,
	)
	// Set the maps on the handler BEFORE spawning the goroutines so the
	// onPoll → PushHub.Notify → buildView path always sees populated
	// Pollers/Schedulers. Without this ordering, the race detector
	// fires on the first tick.
	handler.Pollers = pollers
	handler.Schedulers = schedulers
	startPollerGoroutines()

	// Periodic discovery: parse "periodic:<duration>" and tick a goroutine
	// that refreshes IPs when devices move on the network. on-start is
	// already done above; off is a no-op; anything else has been validated
	// by the config loader.
	if d, ok := parsePeriodicDiscovery(cfg.Daemon.Discovery); ok {
		go runPeriodicDiscovery(rootCtx, devices, d, cfg.Daemon.Password)
	}

	homekitStop, err := handler.StartHomekit(rootCtx, cfg.Homekit, devices.Snapshot())
	if err != nil {
		return fmt.Errorf("homekit: %w", err)
	}
	defer homekitStop() //nolint:errcheck

	mux := http.NewServeMux()
	mux.Handle("/healthz", handler)
	mux.Handle("/v1/", handler)
	mux.Handle("/metrics", metricsHandler(reg, metrics, state, devices, pollers))
	mux.Handle("/", handler)

	srv := &http.Server{
		Addr:    listen,
		Handler: mux,
		// Bound every phase of the request lifecycle. Every endpoint we
		// serve is short-lived (cache-backed JSON, a small HTML page, or
		// a small Prometheus dump), so generous-but-finite timeouts both
		// protect against slow-loris-style misuse and ensure a wedged
		// connection eventually frees its goroutine.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("breezyd listening", "addr", listen)
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	// Block until shutdown signal or server failure.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)

	var runErr error
	select {
	case <-parent.Done():
		slog.Info("parent context cancelled; shutting down")
	case <-sig:
		slog.Info("shutdown signal received")
	case err := <-serveErr:
		if err != nil {
			runErr = err
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http server shutdown error", "err", err)
	}
	// Cancel pollers and wait synchronously for them to drain. This used
	// to be a fire-and-forget goroutine, which let main return while
	// pollers were still mid-tick and (under -race) racey against the
	// global slog state at process teardown.
	rootCancel()
	pollersDone := make(chan struct{})
	go func() {
		pollersWg.Wait()
		close(pollersDone)
	}()
	select {
	case <-pollersDone:
	case <-time.After(shutdownTimeout):
		slog.Warn("pollers did not exit within shutdown deadline; abandoning")
	}
	return runErr
}

// daemonStateDir returns the base directory where the daemon stores
// per-device state files (e.g. energy counters). Precedence:
//
//  1. $STATE_DIRECTORY — set by systemd when StateDirectory = "breezyd"
//     is in the unit (the NixOS module's canonical case). Survives
//     ProtectSystem=strict because systemd pre-creates and chowns it.
//  2. $XDG_STATE_HOME/breezyd — for direct-run / development cases.
//  3. $HOME/.local/state/breezyd — XDG fallback when XDG_STATE_HOME is
//     unset.
//
// The directory is created (mode 0700) if it does not exist. On error
// the path is returned alongside the error so callers can decide whether
// to abort or continue without persistence.
func daemonStateDir() (string, error) {
	// Systemd-managed deployments (the NixOS module is the canonical
	// example) set STATE_DIRECTORY to the writable path that survives
	// ProtectSystem=strict. Honour it first; otherwise fall back to
	// XDG_STATE_HOME for direct-run / development cases.
	if dir := os.Getenv("STATE_DIRECTORY"); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return dir, fmt.Errorf("daemonStateDir: mkdir %s: %w", dir, err)
		}
		return dir, nil
	}
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("daemonStateDir: home dir: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	dir = filepath.Join(dir, "breezyd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return dir, fmt.Errorf("daemonStateDir: mkdir %s: %w", dir, err)
	}
	return dir, nil
}

// buildDeviceMap converts internal/config.Device entries into the
// daemon's local DeviceConfig form, normalising IPs to include the
// default port 4000 when the operator omitted it.
func buildDeviceMap(cfg *config.Config) map[string]DeviceConfig {
	devices := make(map[string]DeviceConfig, len(cfg.Devices))
	for name, d := range cfg.Devices {
		ip := d.IP
		if ip != "" && !strings.Contains(ip, ":") {
			ip = ip + ":4000"
		}
		devices[name] = DeviceConfig{
			ID:       d.ID,
			Password: d.Password,
			IP:       ip,
		}
	}
	return devices
}

// startPollers launches one goroutine per configured device with an
// IP, returning a name->Poller map and a name->Scheduler map for the
// HTTP handler's plumbing, plus a *sync.WaitGroup the caller blocks
// on at shutdown. Devices without an IP are logged and skipped —
// they'll come online when (and if) periodic discovery finds them.
//
// onPoll, when non-nil, is set on each Poller and called after every
// successful tick. Pass h.SyncHomekit to push fresh snapshots into the
// HomeKit bridge after every poll.
//
// scheduleDialFor is called once per device to produce the Dial
// closure wired into each Scheduler. Pass handler.scheduleDial.
//
// We pass `parent` rather than spawning fresh goroutines per device
// from main() so a top-level cancel propagates to every poller and
// scheduler.
func startPollers(
	parent context.Context,
	devices map[string]DeviceConfig,
	interval time.Duration,
	stateDir string,
	state *State,
	metrics *Metrics,
	onPoll func(name string, snap Snapshot),
	onTick func(name string, snap Snapshot),
	scheduleDialFor func(name string) func(ctx context.Context) (breezy.DeviceClient, HandlerClient, error),
	memClients map[string]*breezy.MemClient,
) (map[string]*Poller, map[string]*Scheduler, *sync.WaitGroup, func()) {
	pollers := map[string]*Poller{}
	schedulers := map[string]*Scheduler{}
	wg := &sync.WaitGroup{}
	var startFns []func()

	for name, d := range devices {
		if d.IP == "" {
			slog.Warn("no IP for device; skipping until discovery succeeds", "name", name)
			continue
		}
		devName := name
		devID := d.ID

		tr := &EnergyTracker{
			Device:   devName,
			StateDir: stateDir,
		}
		tr.Load() // always returns nil; missing/malformed handled internally with slog.Warn

		p := &Poller{
			Name:     devName,
			IP:       d.IP,
			DeviceID: d.ID,
			Password: d.Password,
			Interval: interval,
			State:    state,
			ReadIDs:  defaultReadIDs(),
			OnError: func(n, kind string) {
				metrics.RecordPollError(n, devID, kind)
				slog.Debug("poll error", "device", n, "kind", kind)
			},
			OnPoll: onPoll,
			OnTick: onTick,
			Energy: tr,
		}
		if mc, ok := memClients[devName]; ok {
			p.NewClient = func() (PollerClient, error) { return mc, nil }
		}
		pollers[devName] = p

		sch := &Scheduler{
			Device:   devName,
			StateDir: stateDir,
			LockUDP:  p.LockUDP,
			Dial:     scheduleDialFor(devName),
		}
		sch.Load() // always returns nil; missing/malformed handled internally with slog.Warn
		schedulers[devName] = sch

		startFns = append(startFns, func() {
			wg.Add(2)
			go func() { defer wg.Done(); p.Run(parent) }()
			go func() { defer wg.Done(); sch.Run(parent) }()
		})
	}

	start := func() {
		for _, fn := range startFns {
			fn()
		}
	}
	return pollers, schedulers, wg, start
}

// makeClientFactory returns the ClientFactory the HTTP handler hands
// to each per-request dial. When memClients is non-nil (i.e. --backend=memory),
// the factory returns the pre-built MemClient for the named device instead of
// opening a UDP connection. The registry is consulted on every request so
// periodic-discovery IP updates take effect immediately without bouncing the
// connection (UDP path only).
func makeClientFactory(devices *DeviceRegistry, memClients map[string]*breezy.MemClient) func(name string) (HandlerClient, error) {
	return func(name string) (HandlerClient, error) {
		if mc, ok := memClients[name]; ok {
			return mc, nil
		}
		d, ok := devices.Get(name)
		if !ok {
			return nil, fmt.Errorf("unknown device %q", name)
		}
		if d.IP == "" {
			return nil, fmt.Errorf("device %q has no IP yet", name)
		}
		return breezy.NewClient(d.IP, d.ID, d.Password)
	}
}

// metricsHandler wraps promhttp.HandlerFor with a pre-scrape gauge
// refresh: every cached snapshot is poured into the Metrics
// collectors before serving, so /metrics never returns yesterday's
// numbers without at least an updated breezy_last_poll_timestamp.
// Energy gauges are also refreshed by walking the poller map.
//
// This is deliberately cheap: Update() is a few map lookups and gauge
// sets per device, dwarfed by the protobuf encode promhttp does
// afterward.
func metricsHandler(reg *prometheus.Registry, m *Metrics, state *State, devices *DeviceRegistry, pollers map[string]*Poller) http.Handler {
	inner := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range state.Devices() {
			snap, ok := state.Get(name)
			if !ok {
				continue
			}
			d, ok := devices.Get(name)
			if !ok {
				// Device removed since last poll; skip rather than
				// emit a series with an empty id label.
				continue
			}
			m.Update(name, d.ID, snap)
		}
		for name, p := range pollers {
			if p != nil && p.Energy != nil {
				m.SetEnergy(name, p.Energy.Snapshot())
			}
		}
		inner.ServeHTTP(w, r)
	})
}

// discoverFunc is the shape of the discovery probe. main wires
// breezy.Discover; tests inject a stub via runDiscoveryWith. Keeping the
// indirection here keeps runDiscovery testable without standing up a
// real UDP fake on each test.
type discoverFunc func(ctx context.Context) ([]breezy.Found, error)

// defaultDiscover is the production discoverFunc.
var defaultDiscover discoverFunc = breezy.Discover

// defaultDiscoverWithPassword is the production password-bearing
// discoverFunc; held as a package-level var so tests can swap it to
// observe which branch runDiscovery selects based on cfg.Daemon.Password.
var defaultDiscoverWithPassword = breezy.DiscoverWithPassword

// runDiscovery sends one wildcard probe and updates the IP of any
// configured device that answers. Unknown responders are logged at
// INFO so the operator sees them once on next startup. Errors from
// net.ListenPacket are returned; per-target errors and "no devices
// answered" are silently OK.
//
// password is the wildcard probe password — typically cfg.Daemon.Password.
// When empty, the factory-default "1111" is used (matches breezy.Discover).
// When set, breezy.DiscoverWithPassword is called instead, which works
// around firmware variants that silently drop wildcard requests with a
// password mismatch despite the spec saying discovery is unauthenticated.
func runDiscovery(parent context.Context, devices *DeviceRegistry, password string) error {
	fn := defaultDiscover
	if password != "" && password != breezy.DefaultDiscoveryPassword {
		fn = func(ctx context.Context) ([]breezy.Found, error) {
			return defaultDiscoverWithPassword(ctx, password)
		}
	}
	return runDiscoveryWith(parent, devices, fn)
}

// runDiscoveryWith is runDiscovery's testable form: tests inject a stub
// discover that returns deterministic Found values without UDP.
func runDiscoveryWith(parent context.Context, devices *DeviceRegistry, discover discoverFunc) error {
	if discover == nil {
		discover = defaultDiscover
	}
	slog.Info("running discovery", "timeout", discoveryTimeout)
	ctx, cancel := context.WithTimeout(parent, discoveryTimeout)
	defer cancel()

	found, err := discover(ctx)
	if err != nil {
		return err
	}

	// Snapshot by-ID map under the registry lock once.
	snap := devices.Snapshot()
	knownByID := map[string]string{}
	for name, d := range snap {
		knownByID[d.ID] = name
	}

	for _, f := range found {
		name, ok := knownByID[f.DeviceID]
		if !ok {
			slog.Info("discovered unconfigured device — add a [devices.NAME] block to control it",
				"id", f.DeviceID, "ip", f.IP, "type", f.UnitType)
			continue
		}
		newIP := fmt.Sprintf("%s:4000", f.IP)
		prev, _ := devices.UpdateIP(name, newIP)
		if prev == newIP {
			slog.Debug("device already at known IP", "name", name, "ip", f.IP)
		} else {
			slog.Info("discovered configured device", "name", name, "ip", f.IP, "previous", prev)
		}
	}
	slog.Info("discovery complete", "found", len(found))
	return nil
}

// parsePeriodicDiscovery returns the duration encoded in a
// "periodic:<go-duration>" config value. Returns (0, false) for any other
// shape — the config loader has already validated the format, so a
// false here just means the operator chose "on-start" or "off".
func parsePeriodicDiscovery(s string) (time.Duration, bool) {
	const prefix = "periodic:"
	if !strings.HasPrefix(s, prefix) {
		return 0, false
	}
	d, err := time.ParseDuration(s[len(prefix):])
	if err != nil {
		return 0, false
	}
	return d, true
}

// runPeriodicDiscovery ticks every interval, calling runDiscovery.
// Cancellation via ctx exits cleanly. Errors from each tick are logged
// at WARN; the loop continues so a transient network issue doesn't
// permanently disable IP refresh.
func runPeriodicDiscovery(ctx context.Context, devices *DeviceRegistry, interval time.Duration, password string) {
	slog.Info("periodic discovery enabled", "interval", interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := runDiscovery(ctx, devices, password); err != nil {
				slog.Warn("periodic discovery tick failed", "err", err)
			}
		}
	}
}

// defaultReadIDs is the set of params each poller reads on every
// tick. Drawn from the spec's metric list plus the structured
// snapshot fields (`/v1/devices/{name}` reads from the cache).
//
// The order matters: the poller batches in groups of pollBatchSize,
// and the FDFD/02 protocol switches "page" via 0xFF markers when
// reading params from page 0x01/0x03/etc. Sorting by ID minimises
// page transitions per packet.
func defaultReadIDs() []breezy.ParamID {
	return []breezy.ParamID{
		// Page 0 (most params).
		0x0001, 0x0002, 0x0007, 0x000B,
		0x000F, 0x0011, 0x0019, 0x001A,
		0x001F, 0x0020, 0x0021, 0x0022,
		0x0024, 0x0025, 0x0027,
		0x003A, 0x003B, 0x003C, 0x003D, 0x003E, 0x003F,
		0x0044, 0x004A, 0x004B,
		0x0063, 0x0064, 0x0068,
		0x007E, 0x007F, 0x0081, 0x0083, 0x0084, 0x0086, 0x0088,
		0x00B7, 0x00B9,
		// Page 1.
		0x0129,
		// Page 3.
		0x030B, 0x0315, 0x031F, 0x0320,
	}
}

// setupLogging installs a slog text handler at the requested level.
// Unknown levels fall through to info silently — we don't want a
// typo'd flag to abort the daemon on boot.
func setupLogging(level string) {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})))
}
