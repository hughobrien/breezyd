// SPDX-License-Identifier: GPL-3.0-or-later

// Package main, file backend.go: declares the CLI's backend interface
// and its two implementations. The CLI dispatches every verb through
// `backend`; daemonBackend wraps the existing HTTP plumbing and
// directBackend (added in Task 2) talks UDP via pkg/breezy/ops.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hughobrien/breezyd/internal/config"
	"github.com/hughobrien/breezyd/pkg/breezy"
)

// backend is the CLI's per-verb operation surface. Each method
// corresponds to one CLI verb. Implementations either hit the daemon
// (daemonBackend) or talk UDP directly (directBackend, Task 2).
//
// Methods that return only error are write-style (no useful payload
// beyond success/failure). Methods returning typed values are reads.
type backend interface {
	// Read-style operations.
	Status(ctx context.Context, name string) (breezy.Status, error)
	Faults(ctx context.Context, name string) ([]breezy.FaultCode, error)
	Firmware(ctx context.Context, name string) (version, buildDate string, err error)
	Efficiency(ctx context.Context, name string) (int, error)
	GetParam(ctx context.Context, name string, id breezy.ParamID) ([]byte, error)
	Devices(ctx context.Context) ([]lsRow, error)

	// Write-style operations.
	Power(ctx context.Context, name string, on bool) error
	SpeedPreset(ctx context.Context, name string, preset int) error
	SpeedManual(ctx context.Context, name string, pct int) error
	Mode(ctx context.Context, name string, mode string) error
	Heater(ctx context.Context, name string, on bool) error
	ResetFilter(ctx context.Context, name string) error
	ResetFaults(ctx context.Context, name string) error
	SetRTC(ctx context.Context, name string, t time.Time) error
	SetParam(ctx context.Context, name string, id breezy.ParamID, value []byte) error

	// DaemonURLString returns "" for standalone backends; daemon mode
	// returns the URL so `breezy daemon-url` can render it.
	DaemonURLString() string

	// Close releases any resources held by the backend (open UDP
	// sockets in directBackend; daemonBackend's Close is a no-op).
	Close() error
}

// errEnvelope mirrors the daemon's standard error shape. Lifted from
// main.go so backend.go can decode it.
type errEnvelope struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// daemonBackend implements backend by issuing HTTP requests to a
// running breezyd. It carries forward every behavior of the pre-Phase-2
// CLI: same paths, same JSON shapes, same error envelope handling.
type daemonBackend struct {
	url    string
	client *http.Client
}

// newDaemonBackend builds a daemon-talking backend with a 10s default
// HTTP timeout per request.
func newDaemonBackend(url string) *daemonBackend {
	return &daemonBackend{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *daemonBackend) DaemonURLString() string { return d.url }
func (d *daemonBackend) Close() error            { return nil }

// httpJSON issues method url with body (if non-nil) marshalled as
// JSON, reads the entire response, and returns the status + raw bytes.
// Direct port of main.go's pre-Phase-2 helper.
func (d *daemonBackend) httpJSON(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, nil, fmt.Errorf("encode body: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, d.url+path, &buf)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := d.client.Do(req)
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

// envelopeErr converts an HTTP error response (status >= 400) or a
// transport error into a typed `error`. The caller wraps user-facing
// formatting around this; the rendered output stays the same as the
// pre-Phase-2 CLI's `error: <msg> (<code>)` shape.
func envelopeErr(status int, raw []byte, transportErr error) error {
	if transportErr != nil {
		return transportErr
	}
	var e errEnvelope
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		if e.Code != "" {
			return fmt.Errorf("%s (%s)", e.Error, e.Code)
		}
		return errors.New(e.Error)
	}
	body := strings.TrimSpace(string(raw))
	if body == "" {
		return fmt.Errorf("HTTP %d", status)
	}
	return fmt.Errorf("HTTP %d: %s", status, body)
}

func (d *daemonBackend) Status(ctx context.Context, name string) (breezy.Status, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices/"+name, nil)
	if err != nil || status >= 400 {
		return breezy.Status{}, envelopeErr(status, raw, err)
	}
	var s breezy.Status
	if err := json.Unmarshal(raw, &s); err != nil {
		return breezy.Status{}, fmt.Errorf("decode snapshot: %w", err)
	}
	return s, nil
}

func (d *daemonBackend) Power(ctx context.Context, name string, on bool) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/power", map[string]any{"on": on})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) SpeedPreset(ctx context.Context, name string, preset int) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/speed", map[string]any{"preset": preset})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) SpeedManual(ctx context.Context, name string, pct int) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/speed", map[string]any{"manual": pct})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) Mode(ctx context.Context, name, mode string) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/mode", map[string]any{"mode": mode})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) Heater(ctx context.Context, name string, on bool) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/heater", map[string]any{"on": on})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) ResetFilter(ctx context.Context, name string) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/filter/reset", nil)
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) ResetFaults(ctx context.Context, name string) error {
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/faults/reset", nil)
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) Faults(ctx context.Context, name string) ([]breezy.FaultCode, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices/"+name+"/faults", nil)
	if err != nil || status >= 400 {
		return nil, envelopeErr(status, raw, err)
	}
	var resp struct {
		Faults []breezy.FaultCode `json:"faults"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode faults: %w", err)
	}
	if resp.Faults == nil {
		return []breezy.FaultCode{}, nil
	}
	return resp.Faults, nil
}

func (d *daemonBackend) Firmware(ctx context.Context, name string) (string, string, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices/"+name+"/firmware", nil)
	if err != nil || status >= 400 {
		return "", "", envelopeErr(status, raw, err)
	}
	var resp struct {
		Version   string `json:"version"`
		BuildDate string `json:"build_date"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", fmt.Errorf("decode firmware: %w", err)
	}
	return resp.Version, resp.BuildDate, nil
}

func (d *daemonBackend) Efficiency(ctx context.Context, name string) (int, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices/"+name+"/efficiency", nil)
	if err != nil || status >= 400 {
		return 0, envelopeErr(status, raw, err)
	}
	var resp struct {
		Pct int `json:"recovery_efficiency_pct"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, fmt.Errorf("decode efficiency: %w", err)
	}
	return resp.Pct, nil
}

func (d *daemonBackend) GetParam(ctx context.Context, name string, id breezy.ParamID) ([]byte, error) {
	path := fmt.Sprintf("/v1/devices/%s/params/0x%04X", name, uint16(id))
	status, raw, err := d.httpJSON(ctx, http.MethodGet, path, nil)
	if err != nil || status >= 400 {
		return nil, envelopeErr(status, raw, err)
	}
	var resp struct {
		Hex string `json:"hex"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode param: %w", err)
	}
	b, err := hex.DecodeString(resp.Hex)
	if err != nil {
		return nil, fmt.Errorf("decode hex %q: %w", resp.Hex, err)
	}
	return b, nil
}

func (d *daemonBackend) SetParam(ctx context.Context, name string, id breezy.ParamID, value []byte) error {
	path := fmt.Sprintf("/v1/devices/%s/params/0x%04X", name, uint16(id))
	status, raw, err := d.httpJSON(ctx, http.MethodPost, path, map[string]any{"hex": hex.EncodeToString(value)})
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) SetRTC(ctx context.Context, name string, t time.Time) error {
	body := map[string]any{"time": t.Format(time.RFC3339)}
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/rtc", body)
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}

func (d *daemonBackend) Devices(ctx context.Context) ([]lsRow, error) {
	status, raw, err := d.httpJSON(ctx, http.MethodGet, "/v1/devices", nil)
	if err != nil || status >= 400 {
		return nil, envelopeErr(status, raw, err)
	}
	var resp struct {
		Devices []struct {
			Name      string `json:"name"`
			ID        string `json:"id"`
			IP        string `json:"ip"`
			LastPoll  string `json:"last_poll"`
			Power     *bool  `json:"power"`
			Mode      string `json:"airflow_mode"`
			Reachable bool   `json:"reachable"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode device list: %w", err)
	}
	rows := make([]lsRow, 0, len(resp.Devices))
	for _, dev := range resp.Devices {
		rows = append(rows, lsRow{
			Name:      dev.Name,
			ID:        dev.ID,
			IP:        dev.IP,
			LastPoll:  dev.LastPoll,
			Power:     dev.Power,
			Mode:      dev.Mode,
			Reachable: dev.Reachable,
		})
	}
	return rows, nil
}

// Compile-time check that daemonBackend satisfies backend.
var _ backend = (*daemonBackend)(nil)

// directBackend implements backend by opening UDP clients directly to
// each configured device via pkg/breezy/ops. Per-device clients are
// lazy-opened on first use and reused for the rest of the CLI
// invocation; Close releases every open client.
//
// devices is set at construction and treated as read-only thereafter,
// so per-name lookups don't need the mutex. mu protects the clients
// map: lazy-open and Close both acquire it.
type directBackend struct {
	devices map[string]config.Device

	mu      sync.Mutex
	clients map[string]*breezy.Client
}

func newDirectBackend(devices map[string]config.Device) *directBackend {
	return &directBackend{
		devices: devices,
		clients: map[string]*breezy.Client{},
	}
}

func (d *directBackend) DaemonURLString() string { return "" }

func (d *directBackend) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	var firstErr error
	for _, c := range d.clients {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	d.clients = map[string]*breezy.Client{}
	return firstErr
}

// dial returns a *breezy.Client for the named device, opening one and
// caching it on first call. The client is reused for subsequent calls
// within the same CLI invocation.
func (d *directBackend) dial(name string) (*breezy.Client, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c, ok := d.clients[name]; ok {
		return c, nil
	}
	cfg, ok := d.devices[name]
	if !ok {
		return nil, fmt.Errorf("device %q not configured", name)
	}
	if cfg.IP == "" {
		return nil, fmt.Errorf("device %q has no IP configured (run `breezy discover` to find it)", name)
	}
	c, err := breezy.NewClient(cfg.IP, cfg.ID, cfg.Password)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", name, err)
	}
	d.clients[name] = c
	return c, nil
}

func (d *directBackend) Status(ctx context.Context, name string) (breezy.Status, error) {
	c, err := d.dial(name)
	if err != nil {
		return breezy.Status{}, err
	}
	cfg := d.devices[name]
	return breezy.GetStatus(ctx, c, name, cfg.ID, cfg.IP)
}

func (d *directBackend) Power(ctx context.Context, name string, on bool) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.Power(ctx, c, on)
}

func (d *directBackend) SpeedPreset(ctx context.Context, name string, preset int) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetSpeedPreset(ctx, c, preset)
}

func (d *directBackend) SpeedManual(ctx context.Context, name string, pct int) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetSpeedManual(ctx, c, pct)
}

func (d *directBackend) Mode(ctx context.Context, name, mode string) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetMode(ctx, c, mode)
}

func (d *directBackend) Heater(ctx context.Context, name string, on bool) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetHeater(ctx, c, on)
}

func (d *directBackend) ResetFilter(ctx context.Context, name string) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.ResetFilter(ctx, c)
}

func (d *directBackend) ResetFaults(ctx context.Context, name string) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.ResetFaults(ctx, c)
}

func (d *directBackend) Faults(ctx context.Context, name string) ([]breezy.FaultCode, error) {
	c, err := d.dial(name)
	if err != nil {
		return nil, err
	}
	return breezy.GetFaults(ctx, c)
}

func (d *directBackend) Firmware(ctx context.Context, name string) (string, string, error) {
	c, err := d.dial(name)
	if err != nil {
		return "", "", err
	}
	fw, err := breezy.GetFirmware(ctx, c)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf("%d.%02d", fw.Major, fw.Minor), fw.Date.Format("2006-01-02"), nil
}

func (d *directBackend) Efficiency(ctx context.Context, name string) (int, error) {
	c, err := d.dial(name)
	if err != nil {
		return 0, err
	}
	return breezy.GetEfficiency(ctx, c)
}

func (d *directBackend) GetParam(ctx context.Context, name string, id breezy.ParamID) ([]byte, error) {
	c, err := d.dial(name)
	if err != nil {
		return nil, err
	}
	out, err := c.ReadParams(ctx, []breezy.ParamID{id})
	if err != nil {
		return nil, err
	}
	val, ok := out[id]
	if !ok {
		return nil, fmt.Errorf("device replied 'unsupported' for param 0x%04X", uint16(id))
	}
	return val, nil
}

func (d *directBackend) SetParam(ctx context.Context, name string, id breezy.ParamID, value []byte) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return c.WriteParams(ctx, []breezy.ParamWrite{{ID: id, Value: value}})
}

func (d *directBackend) SetRTC(ctx context.Context, name string, t time.Time) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetRTC(ctx, c, t)
}

// Devices returns one row per configured device. Power/Mode/LastPoll are
// zero-valued because standalone has no cache; renderLs already maps
// those to "?" / "never".
func (d *directBackend) Devices(ctx context.Context) ([]lsRow, error) {
	rows := make([]lsRow, 0, len(d.devices))
	for name, cfg := range d.devices {
		rows = append(rows, lsRow{
			Name:      name,
			ID:        cfg.ID,
			IP:        cfg.IP,
			LastPoll:  "",
			Power:     nil,
			Mode:      "",
			Reachable: false,
		})
	}
	return rows, nil
}

// Compile-time check that directBackend satisfies backend.
var _ backend = (*directBackend)(nil)
