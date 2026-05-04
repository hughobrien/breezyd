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

	"github.com/hughobrien/twinfresh/internal/config"
	"github.com/hughobrien/twinfresh/pkg/breezy"
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
)

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
		return fmt.Errorf("config: %w", err)
	}

	listen := cfg.Daemon.Listen
	if *flagAddr != "" {
		listen = *flagAddr
	}

	devices := buildDeviceMap(cfg)

	if cfg.Daemon.Discovery == "on-start" {
		if err := runDiscovery(parent, devices); err != nil {
			slog.Warn("discovery failed", "err", err)
		}
	}

	state := NewState()
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)

	rootCtx, rootCancel := context.WithCancel(parent)
	defer rootCancel()

	pollers := startPollers(rootCtx, devices, cfg.Daemon.PollInterval, state, metrics)

	handler := &Handler{
		State:         state,
		Devices:       devices,
		Pollers:       pollers,
		ClientFactory: makeClientFactory(devices),
	}

	mux := http.NewServeMux()
	mux.Handle("/healthz", handler)
	mux.Handle("/v1/", handler)
	mux.Handle("/metrics", metricsHandler(reg, metrics, state, devices))

	srv := &http.Server{
		Addr:              listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
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
	rootCancel()
	return runErr
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
// IP, returning a name->Poller map for the HTTP handler's
// NoticeWrite plumbing. Devices without an IP are logged and skipped
// — they'll come online if periodic discovery later finds them
// (currently main only does on-start discovery; periodic is a
// near-term TODO that just needs a ticker).
//
// We pass `parent` rather than spawning fresh goroutines per device
// from main() so a top-level cancel propagates to every poller.
func startPollers(
	parent context.Context,
	devices map[string]DeviceConfig,
	interval time.Duration,
	state *State,
	metrics *Metrics,
) map[string]*Poller {
	pollers := map[string]*Poller{}
	var wg sync.WaitGroup

	for name, d := range devices {
		if d.IP == "" {
			slog.Warn("no IP for device; skipping until discovery succeeds", "name", name)
			continue
		}
		// Capture loop vars for the closure.
		devName := name
		devID := d.ID

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
		}
		pollers[devName] = p

		wg.Add(1)
		go func() {
			defer wg.Done()
			p.Run(parent)
		}()
	}

	// Detach a goroutine that just waits for everyone to exit; the
	// returned wait isn't useful to the caller (main blocks on
	// rootCancel + shutdown anyway). Keeping wg here is a safety net
	// in case future code wants per-Poller lifecycle reporting.
	go func() { wg.Wait() }()

	return pollers
}

// makeClientFactory returns the ClientFactory the HTTP handler hands
// to each per-request UDP dial. We close over a pointer to the
// devices map so future discovery-driven IP updates are picked up;
// for now devices is read-only after main() finishes startup, but
// keeping the indirection avoids re-plumbing later.
func makeClientFactory(devices map[string]DeviceConfig) func(name string) (HandlerClient, error) {
	return func(name string) (HandlerClient, error) {
		d, ok := devices[name]
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
//
// This is deliberately cheap: Update() is a few map lookups and gauge
// sets per device, dwarfed by the protobuf encode promhttp does
// afterward.
func metricsHandler(reg *prometheus.Registry, m *Metrics, state *State, devices map[string]DeviceConfig) http.Handler {
	inner := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range state.Devices() {
			snap, ok := state.Get(name)
			if !ok {
				continue
			}
			d, ok := devices[name]
			if !ok {
				// Device removed since last poll; skip rather than
				// emit a series with an empty id label.
				continue
			}
			m.Update(name, d.ID, snap)
		}
		inner.ServeHTTP(w, r)
	})
}

// runDiscovery sends one wildcard probe and updates the IP of any
// configured device that answers. Unknown responders are logged at
// INFO so the operator sees them once on next startup (the firmware
// also lets the user check the device's screen for the ID, but
// surfacing it via the daemon log saves a trip to the appliance).
//
// Errors from net.ListenPacket are returned; per-target errors and
// "no devices answered" are silently OK.
func runDiscovery(parent context.Context, devices map[string]DeviceConfig) error {
	slog.Info("running discovery", "timeout", discoveryTimeout)
	ctx, cancel := context.WithTimeout(parent, discoveryTimeout)
	defer cancel()

	found, err := breezy.Discover(ctx)
	if err != nil {
		return err
	}

	knownByID := map[string]string{}
	for name, d := range devices {
		knownByID[d.ID] = name
	}

	for _, f := range found {
		name, ok := knownByID[f.DeviceID]
		if !ok {
			slog.Info("discovered unconfigured device — add a [devices.NAME] block to control it",
				"id", f.DeviceID, "ip", f.IP, "type", f.UnitType)
			continue
		}
		d := devices[name]
		newIP := fmt.Sprintf("%s:4000", f.IP)
		if d.IP == newIP {
			slog.Debug("device already at known IP", "name", name, "ip", f.IP)
		} else {
			slog.Info("discovered configured device", "name", name, "ip", f.IP, "previous", d.IP)
			d.IP = newIP
			devices[name] = d
		}
	}
	slog.Info("discovery complete", "found", len(found))
	return nil
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
		0x0044, 0x004A, 0x004B,
		0x0063, 0x0064, 0x0068,
		0x007E, 0x0081, 0x0083, 0x0084, 0x0086, 0x0088,
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
