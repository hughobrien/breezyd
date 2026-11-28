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
// sends everything queued in chronological chunks of ChunkWindow (15m if zero).
// After a transient failure the samples stay for the next Flush (already-sent
// chunks are not resent); a chunk the receiver refuses is dropped without
// discarding newer chunks, as is anything older than MaxAge. (VictoriaMetrics
// takes any sample inside its retention; Prometheus without an out-of-order
// window refuses ones older than about an hour.)
type Queue struct {
	W           Writer
	MaxAge      time.Duration // 6h if zero
	ChunkWindow time.Duration // 15m if zero
	Now         func() time.Time

	flush sync.Mutex // one Flush at a time keeps each series in time order

	mu     sync.Mutex
	order  []string // label keys in the order first seen
	series map[string]*Series
	errors uint64
}

const defaultChunkWindow = 15 * time.Minute

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
	window := q.ChunkWindow
	if window <= 0 {
		window = defaultChunkWindow
	}
	chunks := splitChunks(out, window)
	var refusedErr error
	for idx, chunk := range chunks {
		err := q.W.Write(ctx, chunk)
		if err == nil {
			continue
		}
		var refused *RefusedError
		q.mu.Lock()
		q.errors++
		if errors.As(err, &refused) {
			q.mu.Unlock()
			refusedErr = err
			continue
		}
		q.requeue(chunks[idx:])
		q.mu.Unlock()
		return err
	}
	return refusedErr
}

// requeue puts unsent chunks back in front of anything added meanwhile.
// Caller holds q.mu.
func (q *Queue) requeue(chunks [][]Series) {
	newer, newerOrder := q.series, q.order
	q.series, q.order = map[string]*Series{}, nil
	appendSeries := func(s *Series) {
		k := key(s.Labels)
		if have, ok := q.series[k]; ok {
			have.Samples = append(have.Samples, s.Samples...)
		} else {
			cp := make([]Sample, len(s.Samples))
			copy(cp, s.Samples)
			q.series[k] = &Series{Labels: s.Labels, Samples: cp}
			q.order = append(q.order, k)
		}
	}
	for _, chunk := range chunks {
		for i := range chunk {
			appendSeries(&chunk[i])
		}
	}
	for _, k := range newerOrder {
		appendSeries(newer[k])
	}
}

func splitChunks(series []Series, window time.Duration) [][]Series {
	var minT, maxT time.Time
	for _, s := range series {
		for _, p := range s.Samples {
			if minT.IsZero() || p.T.Before(minT) {
				minT = p.T
			}
			if maxT.IsZero() || p.T.After(maxT) {
				maxT = p.T
			}
		}
	}
	if minT.IsZero() || maxT.Sub(minT) < window {
		return [][]Series{series}
	}
	byBucket := map[int][]Series{}
	for _, s := range series {
		perSeries := map[int][]Sample{}
		for _, p := range s.Samples {
			b := int(p.T.Sub(minT) / window)
			perSeries[b] = append(perSeries[b], p)
		}
		for b, samples := range perSeries {
			byBucket[b] = append(byBucket[b], Series{Labels: s.Labels, Samples: samples})
		}
	}
	buckets := make([]int, 0, len(byBucket))
	for b := range byBucket {
		buckets = append(buckets, b)
	}
	slices.Sort(buckets)
	chunks := make([][]Series, 0, len(buckets))
	for _, b := range buckets {
		chunks = append(chunks, byBucket[b])
	}
	return chunks
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
