// SPDX-License-Identifier: GPL-3.0-or-later

package promwrite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type fakeWriter struct {
	got [][]Series
	err error
}

func (w *fakeWriter) Write(_ context.Context, s []Series) error {
	if w.err != nil {
		return w.err
	}
	w.got = append(w.got, s)
	return nil
}

func at(sec int) time.Time { return time.Unix(1700000000+int64(sec), 0) }

func one(name string, sec int, v float64) Series {
	return Series{Labels: map[string]string{"__name__": name, "unit": "office"}, Samples: []Sample{{at(sec), v}}}
}

func TestQueueMergesAndRetries(t *testing.T) {
	w := &fakeWriter{}
	now := at(100)
	q := &Queue{W: w, Now: func() time.Time { return now }}
	ctx := context.Background()

	q.Add(one("a", 1, 1), one("b", 1, 10))
	q.Add(one("a", 2, 2))
	w.err = errors.New("connection refused")
	if err := q.Flush(ctx); err == nil || q.Len() != 3 || q.Errors() != 1 {
		t.Fatalf("a failed flush should keep 3 samples: err %v, len %d", err, q.Len())
	}
	q.Add(one("a", 3, 3))
	w.err = nil
	if err := q.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.got) != 1 || len(w.got[0]) != 2 {
		t.Fatalf("want one write of two series, got %v", w.got)
	}
	a := w.got[0][0]
	if a.Labels["__name__"] != "a" || len(a.Samples) != 3 || a.Samples[0].V != 1 || a.Samples[2].V != 3 {
		t.Errorf("series a should hold the kept samples, then the new one, in order: %v", a)
	}
	if q.Len() != 0 {
		t.Error("queue not empty after a good flush")
	}

	w.err = &RefusedError{Status: 400}
	q.Add(one("a", 4, 4))
	_ = q.Flush(ctx)
	if q.Len() != 0 {
		t.Error("a refused write should be dropped")
	}

	w.err = nil
	q.Add(one("a", 5, 5))
	now = at(5 + 7*3600)
	_ = q.Flush(ctx)
	if len(w.got) != 1 {
		t.Error("samples older than MaxAge should be dropped, not sent")
	}
}

func TestFromFamilies(t *testing.T) {
	reg := prometheus.NewRegistry()
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "room_celsius", ConstLabels: Targets{{"vm", "http://vm"}}.Labels()}, []string{"unit"})
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Name: "latency_seconds"})
	reg.MustRegister(g, h)
	g.WithLabelValues("office").Set(21.5)
	g.WithLabelValues("house").Set(19)
	h.Observe(1)
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	s := FromFamilies(fams, at(7), func(l map[string]string) bool { return l["unit"] == "office" }, map[string]string{"job": "comfortd"})
	if len(s) != 1 {
		t.Fatalf("want only office's gauge (no histogram), got %v", s)
	}
	l := s[0].Labels
	if l["__name__"] != "room_celsius" || l["unit"] != "office" || l["job"] != "comfortd" || len(l) != 3 || s[0].Samples[0] != (Sample{at(7), 21.5}) {
		t.Errorf("got %v", s[0])
	}
}

type cutoffWriter struct {
	cutoff   time.Time
	err      error
	accepted []Sample
	writes   int
}

func (w *cutoffWriter) Write(_ context.Context, series []Series) error {
	w.writes++
	if w.err != nil {
		return w.err
	}
	for _, s := range series {
		for _, p := range s.Samples {
			if p.T.Before(w.cutoff) {
				return &RefusedError{Status: 400, Body: "out of bounds"}
			}
		}
	}
	for _, s := range series {
		w.accepted = append(w.accepted, s.Samples...)
	}
	return nil
}

func TestQueueFlushChunksRefusedOldKeepsNew(t *testing.T) {
	now := at(90 * 60)
	w := &cutoffWriter{cutoff: now.Add(-60 * time.Minute)}
	q := &Queue{W: w, Now: func() time.Time { return now }}
	for m := 0; m <= 90; m += 10 {
		q.Add(one("a", m*60, float64(m)))
	}
	err := q.Flush(context.Background())
	var re *RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *RefusedError", err)
	}
	if q.Errors() == 0 {
		t.Fatal("expected refused chunks to increment Errors()")
	}
	if q.Len() != 0 {
		t.Fatalf("Len = %d, want 0", q.Len())
	}
	if len(w.accepted) < 5 {
		t.Fatalf("expected newer chunks to be accepted, got %v", w.accepted)
	}
	for i := 1; i < len(w.accepted); i++ {
		if !w.accepted[i-1].T.Before(w.accepted[i].T) {
			t.Fatalf("accepted samples out of order: %v", w.accepted)
		}
	}
	if w.accepted[len(w.accepted)-1].V != 90 {
		t.Fatalf("newest sample lost: %v", w.accepted)
	}
}

type failNthWriter struct {
	failOn int
	err    error
	calls  int
	got    []Sample
}

func (w *failNthWriter) Write(_ context.Context, series []Series) error {
	w.calls++
	if w.failOn > 0 && w.calls == w.failOn {
		return w.err
	}
	for _, s := range series {
		w.got = append(w.got, s.Samples...)
	}
	return nil
}

func TestQueueFlushMidChunkTransientErrorRequeuesOnlyUnsent(t *testing.T) {
	now := at(60 * 60)
	w := &failNthWriter{failOn: 2, err: errors.New("503 unavailable")}
	q := &Queue{W: w, Now: func() time.Time { return now }}
	q.Add(one("a", 0, 0), one("a", 20*60, 20), one("a", 40*60, 40))

	if err := q.Flush(context.Background()); err == nil {
		t.Fatal("expected transient error on second chunk")
	}
	if q.Errors() != 1 || len(w.got) != 1 || w.got[0].V != 0 || q.Len() != 2 {
		t.Fatalf("after partial flush: errors=%d got=%v len=%d", q.Errors(), w.got, q.Len())
	}

	q.Add(one("a", 50*60, 50))
	w.failOn = 0
	if err := q.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if q.Len() != 0 || len(w.got) != 4 {
		t.Fatalf("after catch-up flush: len=%d got=%v", q.Len(), w.got)
	}
	want := []float64{0, 20, 40, 50}
	for i, v := range want {
		if w.got[i].V != v {
			t.Fatalf("got[%d] = %v, want %v (all=%v)", i, w.got[i].V, v, w.got)
		}
	}
}
