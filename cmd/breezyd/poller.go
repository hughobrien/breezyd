// SPDX-License-Identifier: GPL-3.0-or-later

// The per-device polling goroutine that keeps the State cache fresh. One
// Poller maps to one configured device; it batches the configured ReadIDs
// into protocol packets, writes a Snapshot per tick (success or failure),
// and exposes NoticeWrite so the HTTP handler can suppress fan-RPM reads
// while a just-issued speed change is still settling.
package main

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// fanSettleDuration is how long after a fan-affecting write we suppress
// reads of the live fan-RPM and air-quality params, to avoid recording
// in-flight values as if they were the new steady-state.
const fanSettleDuration = 12 * time.Second

// pollBatchSize bounds the number of param IDs per Read packet. The
// FDFD/02 protocol is limited to ~256 bytes per packet; 30 IDs leaves
// generous headroom for response framing (FE/size/id-low + bytes per
// param can run several bytes each, plus FF page-switch markers).
const pollBatchSize = 30

// fanWriteIDs is the set of write targets that affect live fan speed.
// A write to any of these triggers the settle window during which we
// skip the params in fanSensitiveReads.
var fanWriteIDs = map[breezy.ParamID]bool{
	0x0002: true, // speed_mode
	0x0007: true, // timer (entering/leaving night|turbo ramps fans)
	0x0044: true, // speed_manual_pct
	0x00B7: true, // fan_rotation_direction
	// Editing the currently-active preset ramps the running fan immediately,
	// so its supply/extract pcts get the same settle treatment as 0x44.
	0x003A: true, 0x003B: true, // preset1 supply/extract
	0x003C: true, 0x003D: true, // preset2 supply/extract
	0x003E: true, 0x003F: true, // preset3 supply/extract
}

// fanSensitiveReads is the set of params we skip during the settle window
// because their values are meaningful only after the fans have stabilised.
var fanSensitiveReads = map[breezy.ParamID]bool{
	0x004A: true, // fan_supply_rpm
	0x004B: true, // fan_extract_rpm
	0x0084: true, // air_quality_status (sensors react to flow)
}

// PollerClient is the subset of breezy.Client the Poller needs. Tests
// inject an in-process fake; production code injects a real *breezy.Client.
type PollerClient interface {
	ReadParams(ctx context.Context, ids []breezy.ParamID) (map[breezy.ParamID][]byte, error)
	Close() error
	// IsLocal reports whether the client is in-process (e.g. MemClient).
	// The fan-settle suppression is skipped for local clients because writes
	// land instantly and there is no firmware settle delay to wait out.
	IsLocal() bool
}

// Poller drives one device's polling loop and updates State.
type Poller struct {
	// Name is the device label used as the State cache key and in metric
	// callbacks. Must be unique across the daemon.
	Name string
	// IP is the address (with port) of the device, e.g. "192.168.1.148:4000".
	// It's recorded in each Snapshot so consumers know where the data came
	// from after a discovery-driven IP move.
	IP string
	// DeviceID is the 16-byte FDFD/02 device ID.
	DeviceID string
	// Password is the device protocol password (<=8 bytes).
	Password string
	// Interval is the wall-clock period between ticks.
	Interval time.Duration
	// State is the cache the poller writes Snapshots into. Must be non-nil.
	State *State
	// ReadIDs is the full set of params this poller reads on each tick.
	// The slice is split into batches of pollBatchSize across separate
	// protocol packets.
	ReadIDs []breezy.ParamID
	// OnError is invoked once per tick error with the device Name and a
	// classification ("checksum"/"auth"/"timeout"/"other"). Optional.
	OnError func(name, kind string)
	// OnPoll is called after every successful tick (i.e. when the recorded
	// Snapshot has no LastErr) with the device Name and the Snapshot that
	// was just written to State. Use this to push fresh data into subsystems
	// (e.g. the HomeKit bridge) without polling the State cache. Optional;
	// a nil OnPoll is a no-op.
	OnPoll func(name string, snap Snapshot)
	// OnTick is called after every tick (success OR failure) with the
	// device Name and the Snapshot that was just written to State. Use
	// this for consumers that need to learn about failed ticks too —
	// e.g. PushHub.Notify so the dashboard's $lastPollAge / $stale
	// signals advance under sustained UDP timeouts. Optional; a nil
	// OnTick is a no-op. OnPoll fires only on success and is the right
	// hook for HomeKit characteristic sync and Energy accumulation.
	OnTick func(name string, snap Snapshot)
	// Energy tracks accumulated heat-recovery and fan-power energy for this
	// device. When non-nil, Tick is called after each successful pollOnce.
	// Nil for tests and standalone mode that don't need energy tracking.
	Energy *EnergyTracker

	// NewClient builds the breezy client used for the next tick. Tests
	// inject a stub; production code leaves it nil and the poller dials
	// a real *breezy.Client itself.
	NewClient func() (PollerClient, error)
	// Now overrides time.Now for tests of the settle window. Optional.
	Now func() time.Time

	mu             sync.Mutex
	settleDeadline time.Time
	lastClient     PollerClient // set by dial(); nil until first tick

	// udpMu serialises ALL UDP traffic to this device — both the poller's
	// own tick and any HTTP handler issuing a write/read. CLAUDE.md's
	// design intent is "breezyd serialises traffic per device behind a
	// sync.Mutex"; this is that mutex. Without it, the poll and a write
	// could interleave at the UDP packet level (separate Client instances,
	// independent sockets), and the poll's response could overwrite a
	// just-WriteThrough'd cache value with the device's pre-write reading.
	udpMu sync.Mutex
}

// LockUDP acquires the per-device UDP serialisation mutex and returns an
// unlock function. Callers (the tick loop and HTTP handlers) MUST hold
// this mutex around any UDP traffic to this device, so the device sees a
// strictly sequential request stream.
func (p *Poller) LockUDP() func() {
	p.udpMu.Lock()
	return p.udpMu.Unlock
}

// NoticeWrite is called by the HTTP handler whenever it issues a write to
// a fan-affecting parameter. It schedules the next fanSettleDuration of
// ticks to skip params whose values are unstable while the fans ramp.
// Writes to non-fan params are no-ops. Local clients (MemClient) skip the
// suppression entirely — writes land instantly with no firmware settle delay.
func (p *Poller) NoticeWrite(id breezy.ParamID) {
	if !fanWriteIDs[id] {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastClient != nil && p.lastClient.IsLocal() {
		return
	}
	p.settleDeadline = p.now().Add(fanSettleDuration)
}

// Run blocks until ctx is done, ticking at p.Interval. The first tick
// fires immediately so callers see fresh data without waiting an interval.
func (p *Poller) Run(ctx context.Context) {
	p.tick(ctx)
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}

// tick performs one full poll cycle: build the per-tick ID list (settle
// filter applied), open a client, read each batch, record one Snapshot.
//
// Failed-tick semantics: the prior Snapshot's LastPoll and Values are
// carried forward across both the dial-failure and read-failure branches.
// LastPoll therefore reflects the most recent SUCCESSFUL poll — see
// SPECIFICATION-daemon.md "failed-poll cache semantics" and the
// SPECIFICATION-web.md "Card states" stale-window definition.
func (p *Poller) tick(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	ids := p.idsForThisTick()

	// Hold udpMu for the entire tick — dial, read batches, close — so
	// concurrent HTTP handler writes can't interleave at the UDP layer.
	unlock := p.LockUDP()
	defer unlock()

	// (1) Read the prior snapshot once at the top of the tick. Get returns
	// a deep copy on hit and a zero Snapshot on miss; either is a safe
	// carry-forward source for failed-tick branches below.
	prev, _ := p.State.Get(p.Name)

	client, err := p.dial()
	if err != nil {
		p.recordErr(err)
		// (2) Dial-failure branch: carry prev.Values and prev.LastPoll
		// forward (was: empty Values + p.now()).
		p.State.RecordPoll(p.Name, Snapshot{
			IP:       p.IP,
			Values:   prev.Values,
			LastPoll: prev.LastPoll,
			LastErr:  err,
		})
		return
	}
	defer func() { _ = client.Close() }()

	values := make(map[breezy.ParamID][]byte, len(ids))
	var lastErr error
	for start := 0; start < len(ids); start += pollBatchSize {
		end := start + pollBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		got, err := client.ReadParams(ctx, batch)
		if err != nil {
			lastErr = err
			p.recordErr(err)
			continue
		}
		for k, v := range got {
			values[k] = v
		}
	}

	// If every read in this tick failed, preserve the last successful
	// Snapshot's Values so the dashboard keeps showing last-known-good
	// (marked stale by LastErr) rather than dropping to unreachable.
	// This matters most for the in-process MemClient backend, where a
	// forced ErrTimeout returns instantly and would otherwise overwrite
	// good state on the very first failed tick.
	if lastErr != nil && len(values) == 0 && len(prev.Values) > 0 {
		values = prev.Values
	}

	// (3) Read-failure branch: LastPoll holds at prev.LastPoll on failure;
	// success advances it to p.now().
	lastPoll := p.now()
	if lastErr != nil {
		lastPoll = prev.LastPoll
	}
	snap := Snapshot{
		IP:       p.IP,
		Values:   values,
		LastPoll: lastPoll,
		LastErr:  lastErr,
	}
	p.State.RecordPoll(p.Name, snap)
	if p.OnTick != nil {
		p.OnTick(p.Name, snap)
	}
	if lastErr == nil {
		if p.Energy != nil {
			p.Energy.Tick(values, time.Now())
		}
		if p.OnPoll != nil {
			p.OnPoll(p.Name, snap)
		}
	}
}

// dial returns a PollerClient for the upcoming tick. If NewClient is set
// it's used directly; otherwise the poller builds a real *breezy.Client.
// The returned client is stored in p.lastClient (under p.mu) so that
// NoticeWrite can gate settle-window suppression on IsLocal.
func (p *Poller) dial() (PollerClient, error) {
	var (
		c   PollerClient
		err error
	)
	if p.NewClient != nil {
		c, err = p.NewClient()
	} else {
		c, err = breezy.NewClient(p.IP, p.DeviceID, p.Password)
	}
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.lastClient = c
	p.mu.Unlock()
	return c, nil
}

// idsForThisTick returns ReadIDs filtered through the settle window: if
// we're still inside one, params in fanSensitiveReads are dropped.
func (p *Poller) idsForThisTick() []breezy.ParamID {
	p.mu.Lock()
	deadline := p.settleDeadline
	p.mu.Unlock()

	if !deadline.IsZero() && p.now().Before(deadline) {
		out := make([]breezy.ParamID, 0, len(p.ReadIDs))
		for _, id := range p.ReadIDs {
			if fanSensitiveReads[id] {
				continue
			}
			out = append(out, id)
		}
		return out
	}
	return p.ReadIDs
}

// now returns the current wall clock, honouring p.Now for tests.
func (p *Poller) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// recordErr classifies err and invokes OnError. Safe to call with a nil
// callback (no-op) so callers don't have to nil-check.
func (p *Poller) recordErr(err error) {
	if p.OnError == nil || err == nil {
		return
	}
	p.OnError(p.Name, classifyErr(err))
}

// classifyErr maps a poll error into one of the metric labels documented
// in the daemon spec. Order matters: ErrAuth must outrank generic
// net-level errors so a wrong-password device is reported as such even
// if the failure path also looks transport-y.
func classifyErr(err error) string {
	switch {
	case errors.Is(err, breezy.ErrChecksum):
		return "checksum"
	case errors.Is(err, breezy.ErrAuth):
		return "auth"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "other"
}
