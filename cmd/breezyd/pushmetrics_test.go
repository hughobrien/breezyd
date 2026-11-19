// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/internal/promwrite"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeWriter struct{ got [][]promwrite.Series }

func (w *fakeWriter) Write(_ context.Context, s []promwrite.Series) error {
	w.got = append(w.got, s)
	return nil
}

func TestPushEachPoll(t *testing.T) {
	reg, data := prometheus.NewRegistry(), prometheus.NewRegistry()
	m := NewMetrics(reg, data)
	devices := NewDeviceRegistry(map[string]DeviceConfig{
		"office":  {ID: "0123456789abcdef"},
		"bedroom": {ID: "fedcba9876543210"},
	})
	w := &fakeWriter{}
	p := newMetricPusher(m, data, w, "breezyd", devices, nil)
	ctx := context.Background()

	snap := representativeSnapshot()
	snap.LastPoll = time.Now().Truncate(time.Second) // the queue drops samples older than 50m
	p.push(ctx, "bedroom", snap)                     // another device's gauges exist too
	p.push(ctx, "office", snap)
	if len(w.got) != 2 {
		t.Fatalf("want a write per poll, got %d", len(w.got))
	}
	names := map[string]bool{}
	for _, s := range w.got[1] {
		if s.Labels["device"] != "office" || s.Labels["job"] != "breezyd" || len(s.Samples) != 1 || !s.Samples[0].T.Equal(snap.LastPoll) {
			t.Fatalf("series %v: want office only, job breezyd, one sample at the poll time", s)
		}
		names[s.Labels["__name__"]] = true
	}
	if !names["breezy_power"] || !names["breezy_fan_rpm"] {
		t.Errorf("device gauges not pushed: %v", names)
	}
	if names["breezy_up"] || names["breezy_last_poll_timestamp"] {
		t.Error("health stays on /metrics, not pushed")
	}

	p.push(ctx, "office", snap) // same LastPoll: must still be newer
	if got := w.got[2][0].Samples[0].T; !got.Equal(snap.LastPoll.Add(time.Millisecond)) {
		t.Errorf("repeat poll time pushed at %v, want 1ms after the last", got)
	}

	reg.MustRegister(p.Collectors()...)
	rec := httptest.NewRecorder()
	metricsHandler(reg, m, NewState(), devices, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	if strings.Contains(body, "breezy_power{") || strings.Contains(body, "breezy_fan_rpm{") {
		t.Errorf("pushed gauges should not be on /metrics:\n%s", body)
	}
	for _, want := range []string{`breezy_up{device="office"`, "breezyd_push_errors_total 0", "breezyd_push_queued_samples 0"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s on /metrics:\n%s", want, body)
		}
	}
}

func TestNotifyNeverBlocks(t *testing.T) {
	p := newMetricPusher(NewMetrics(nil, prometheus.NewRegistry()), prometheus.NewRegistry(), &fakeWriter{}, "breezyd",
		NewDeviceRegistry(nil), nil)
	done := make(chan struct{})
	go func() {
		for range cap(p.polls) + 10 { // nobody is running the pusher
			p.Notify("office", Snapshot{})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked the poller")
	}
}
