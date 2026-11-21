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

func flush(t *testing.T, ps promwrite.Pushers) {
	t.Helper()
	if err := ps.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPushEachPoll(t *testing.T) {
	reg, data := prometheus.NewRegistry(), prometheus.NewRegistry()
	targets := promwrite.Targets{{Name: "vm", URL: "http://vm"}}
	m := NewPushedMetrics(reg, data, targets.Labels())
	devices := NewDeviceRegistry(map[string]DeviceConfig{
		"office":  {ID: "0123456789abcdef"},
		"bedroom": {ID: "fedcba9876543210"},
	})
	w := &fakeWriter{}
	push := promwrite.NewPushers(targets, nil)
	push[0].Queue.W = w
	p := newMetricPusher(m, data, push, "breezyd", devices, nil)

	snap := representativeSnapshot()
	snap.LastPoll = time.Now().Truncate(time.Second) // the queue drops samples older than 50m
	p.push("bedroom", snap)                          // another device's gauges exist too
	flush(t, push)
	p.push("office", snap)
	flush(t, push)
	if len(w.got) != 2 {
		t.Fatalf("want a write per poll, got %d", len(w.got))
	}
	names := map[string]bool{}
	for _, s := range w.got[1] {
		if s.Labels["device"] != "office" || s.Labels["job"] != "breezyd" || s.Labels["pushed_vm"] != "" || len(s.Samples) != 1 || !s.Samples[0].T.Equal(snap.LastPoll) {
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

	p.push("office", snap) // same LastPoll: must still be newer
	flush(t, push)
	if got := w.got[2][0].Samples[0].T; !got.Equal(snap.LastPoll.Add(time.Millisecond)) {
		t.Errorf("repeat poll time pushed at %v, want 1ms after the last", got)
	}

	reg.MustRegister(p.Collectors()...)
	rec := httptest.NewRecorder()
	state := NewState()
	state.Set("office", snap)
	metricsHandler(prometheus.Gatherers{reg, data}, m, state, devices, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	// The device gauges stay on /metrics for any scraper, marked so vm's own
	// scrape can drop them; health is not marked.
	for _, want := range []string{
		`breezy_power{device="office",id="0123456789abcdef",pushed_vm="true"}`,
		`breezy_fan_rpm{device="office",fan=`,
		`breezy_up{device="office",id="0123456789abcdef"}`,
		`breezyd_push_errors_total{target="vm"} 0`,
		`breezyd_push_queued_samples{target="vm"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %s on /metrics:\n%s", want, body)
		}
	}
}

func TestNotifyNeverBlocks(t *testing.T) {
	p := newMetricPusher(NewPushedMetrics(nil, prometheus.NewRegistry(), nil), prometheus.NewRegistry(), nil, "breezyd",
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

func TestRedactURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://user:pw@host:8428/api?token=abc#f": "http://host:8428/api",
		"https://tok@host/":                        "https://host/",
		"http://127.0.0.1:8428":                    "http://127.0.0.1:8428",
		"http://%zz":                               "<invalid url>",
	} {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
	ts := promwrite.Targets{{Name: "vm", URL: "http://u:p@h:1/?k=v"}}
	if got := redactedTargets(ts); got != "vm=http://h:1/" {
		t.Errorf("redactedTargets() = %q", got)
	}
}
