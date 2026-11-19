// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/hughobrien/breezyd/internal/promwrite"
	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/prometheus/client_golang/prometheus"
)

// metricPusher sends each successful poll to Prometheus with remote write,
// stamped with the poll's time. With polls every 30 s and a 60 s scrape, the
// scrape kept one poll in two, and two timers drifting against each other
// can read one twice or skip one; pushing keeps every poll exactly once.
//
// Notify is called from the poller's OnPoll, inside the tick and under the
// device's UDP lock, so it only queues: the gather and the write happen on
// Run's goroutine, and a slow or absent Prometheus never holds up a fan.
type metricPusher struct {
	m       *Metrics
	data    *prometheus.Registry
	q       *promwrite.Queue
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

func newMetricPusher(m *Metrics, data *prometheus.Registry, w promwrite.Writer, job string,
	devices *DeviceRegistry, energy func(string) (breezy.EnergyValues, bool)) *metricPusher {
	return &metricPusher{m: m, data: data, q: &promwrite.Queue{W: w}, job: job, devices: devices, energy: energy,
		polls: make(chan polled, 64), last: map[string]time.Time{}}
}

// Collectors expose the pusher's queue on /metrics.
func (p *metricPusher) Collectors() []prometheus.Collector {
	return []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "breezyd_push_queued_samples", Help: "Samples waiting to be pushed to Prometheus."},
			func() float64 { return float64(p.q.Len()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{Name: "breezyd_push_errors_total", Help: "Failed writes to Prometheus."},
			func() float64 { return float64(p.q.Errors()) }),
	}
}

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
	// Strictly after the last sample: Prometheus refuses an older one.
	t := snap.LastPoll
	if last := p.last[name]; !t.After(last) {
		t = last.Add(time.Millisecond)
	}
	series := promwrite.FromFamilies(fams, t, func(l map[string]string) bool { return l["device"] == name },
		map[string]string{"job": p.job})
	if len(series) == 0 {
		return
	}
	p.q.Add(series...)
	p.last[name] = t
	if err := p.q.Flush(ctx); err != nil {
		slog.Warn("push to prometheus", "err", err, "queued", p.q.Len())
	}
}
