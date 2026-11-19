// SPDX-License-Identifier: GPL-3.0-or-later

package promwrite

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	dto "github.com/prometheus/client_model/go"
)

// Writer is what a Queue sends to (a *Client).
type Writer interface {
	Write(ctx context.Context, series []Series) error
}

// Queue holds samples until the receiver takes them. Add from anywhere; Flush
// sends everything queued in one write. After a failure the receiver may get
// over, the samples stay for the next Flush; a write it refused is dropped,
// as is anything older than MaxAge, which bounds the backlog while a receiver
// is down. (VictoriaMetrics takes any sample inside its retention; Prometheus
// without an out-of-order window refuses ones older than about an hour.)
type Queue struct {
	W      Writer
	MaxAge time.Duration // 6h if zero
	Now    func() time.Time

	flush sync.Mutex // one Flush at a time keeps each series in time order

	mu     sync.Mutex
	order  []string // label keys in the order first seen
	series map[string]*Series
	errors uint64
}

func key(labels map[string]string) string {
	names := make([]string, 0, len(labels))
	for n := range labels {
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		b.WriteString(n)
		b.WriteByte(0)
		b.WriteString(labels[n])
		b.WriteByte(0)
	}
	return b.String()
}

// Add queues series. Keep each series' samples in time order across calls:
// Prometheus refuses older ones, and the whole write with them.
func (q *Queue) Add(series ...Series) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.series == nil {
		q.series = map[string]*Series{}
	}
	for _, s := range series {
		k := key(s.Labels)
		have, ok := q.series[k]
		if !ok {
			have = &Series{Labels: s.Labels}
			q.series[k] = have
			q.order = append(q.order, k)
		}
		have.Samples = append(have.Samples, s.Samples...)
	}
}

// Len is the number of queued samples.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, s := range q.series {
		n += len(s.Samples)
	}
	return n
}

// Errors is the number of failed writes so far.
func (q *Queue) Errors() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.errors
}

// Flush sends what is queued. It returns the write's error, if any.
func (q *Queue) Flush(ctx context.Context) error {
	q.flush.Lock()
	defer q.flush.Unlock()
	now := time.Now
	if q.Now != nil {
		now = q.Now
	}
	maxAge := q.MaxAge
	if maxAge == 0 {
		maxAge = 6 * time.Hour
	}
	cutoff := now().Add(-maxAge)

	q.mu.Lock()
	order, taken := q.order, q.series
	q.order, q.series = nil, nil
	q.mu.Unlock()
	var out []Series
	for _, k := range order {
		s := taken[k]
		i := 0
		for i < len(s.Samples) && s.Samples[i].T.Before(cutoff) {
			i++
		}
		if i < len(s.Samples) {
			out = append(out, Series{Labels: s.Labels, Samples: s.Samples[i:]})
		}
	}
	if len(out) == 0 {
		return nil
	}
	err := q.W.Write(ctx, out)
	if err == nil {
		return nil
	}
	var refused *RefusedError
	q.mu.Lock()
	defer q.mu.Unlock()
	q.errors++
	if errors.As(err, &refused) {
		return err
	}
	// Put them back in front of anything added meanwhile.
	newer, newerOrder := q.series, q.order
	q.series, q.order = map[string]*Series{}, nil
	for _, s := range out {
		k := key(s.Labels)
		q.series[k] = &Series{Labels: s.Labels, Samples: s.Samples}
		q.order = append(q.order, k)
	}
	for _, k := range newerOrder {
		if have, ok := q.series[k]; ok {
			have.Samples = append(have.Samples, newer[k].Samples...)
		} else {
			q.series[k] = newer[k]
			q.order = append(q.order, k)
		}
	}
	return err
}

// FromFamilies turns gathered gauges, counters and untyped metrics into
// series with one sample at t. keep (nil keeps all) sees each metric's labels;
// extra labels are added to every series. pushed_<target> labels, which mark
// scraped copies (see Target.Label), are left off.
func FromFamilies(fams []*dto.MetricFamily, t time.Time, keep func(labels map[string]string) bool, extra map[string]string) []Series {
	var out []Series
	for _, f := range fams {
		for _, m := range f.GetMetric() {
			var v float64
			switch f.GetType() {
			case dto.MetricType_GAUGE:
				v = m.GetGauge().GetValue()
			case dto.MetricType_COUNTER:
				v = m.GetCounter().GetValue()
			case dto.MetricType_UNTYPED:
				v = m.GetUntyped().GetValue()
			default:
				continue // histograms and summaries are not pushed
			}
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				if !strings.HasPrefix(l.GetName(), "pushed_") {
					labels[l.GetName()] = l.GetValue()
				}
			}
			if keep != nil && !keep(labels) {
				continue
			}
			for k, x := range extra {
				labels[k] = x
			}
			labels["__name__"] = f.GetName()
			out = append(out, Series{Labels: labels, Samples: []Sample{{T: t, V: v}}})
		}
	}
	return out
}
