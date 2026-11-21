// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/hughobrien/breezyd/internal/promwrite"
	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/prometheus/client_golang/prometheus"
)

// metricPusher sends each successful poll with remote write to every target,
// stamped with the poll's time. With polls every 30 s and a 60 s scrape, the
// scrape kept one poll in two, and two timers drifting against each other
// can read one twice or skip one; pushing keeps every poll exactly once.
//
// Notify is called from the poller's OnPoll, inside the tick and under the
// device's UDP lock, so it only queues: the gather and the write happen on
// Run's goroutine, and a slow or absent target never holds up a fan.
type metricPusher struct {
	m       *Metrics
	data    *prometheus.Registry
	targets promwrite.Pushers
	job     string
	devices *DeviceRegistry
	energy  func(name string) (breezy.EnergyValues, bool)

	polls chan polled
	last  map[string]time.Time // newest sample time pushed, per device
}

type polled struct {
	name string
	snap Snapshot
}

func newMetricPusher(m *Metrics, data *prometheus.Registry, targets promwrite.Pushers, job string,
	devices *DeviceRegistry, energy func(string) (breezy.EnergyValues, bool)) *metricPusher {
	return &metricPusher{m: m, data: data, targets: targets, job: job, devices: devices, energy: energy,
		polls: make(chan polled, 64), last: map[string]time.Time{}}
}

// Collectors expose each target's queue on /metrics.
func (p *metricPusher) Collectors() []prometheus.Collector { return p.targets.Collectors("breezyd") }

// Notify queues one successful poll. It never blocks; if the queue is full
// the poll is dropped (the next one follows within a poll interval).
func (p *metricPusher) Notify(name string, snap Snapshot) {
	select {
	case p.polls <- polled{name, snap}:
	default:
		slog.Warn("push queue full; dropping a poll", "device", name)
	}
}

func (p *metricPusher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-p.polls:
			p.push(ctx, e.name, e.snap)
		}
	}
}

func (p *metricPusher) push(ctx context.Context, name string, snap Snapshot) {
	d, ok := p.devices.Get(name)
	if !ok {
		return
	}
	p.m.Update(name, d.ID, snap)
	if p.energy != nil {
		if ev, ok := p.energy(name); ok {
			p.m.SetEnergy(name, ev)
		}
	}
	fams, err := p.data.Gather()
	if err != nil {
		slog.Warn("gather for push", "err", err)
		return
	}
	// Strictly after the last sample: a Prometheus target refuses an older one.
	t := snap.LastPoll
	if last := p.last[name]; !t.After(last) {
		t = last.Add(time.Millisecond)
	}
	series := promwrite.FromFamilies(fams, t, func(l map[string]string) bool { return l["device"] == name },
		map[string]string{"job": p.job})
	if len(series) == 0 {
		return
	}
	p.targets.Add(series...)
	p.last[name] = t
	if err := p.targets.Flush(ctx); err != nil {
		slog.Warn("push", "err", err)
	}
}

// redactURL returns raw as scheme://host[:port]/path, dropping userinfo, query
// and fragment, which can carry credentials. Use it for anything logged.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<invalid url>"
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// redactedTargets formats push targets as name=url,... with every URL redacted.
func redactedTargets(ts promwrite.Targets) string {
	s := make([]string, 0, len(ts))
	for _, t := range ts {
		s = append(s, t.Name+"="+redactURL(t.URL))
	}
	return strings.Join(s, ",")
}
