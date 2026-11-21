// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/internal/promwrite"
	"github.com/prometheus/client_golang/prometheus"
)

// A target that accepts the write and never answers must not delay or drop
// pushes to a healthy one; its own samples wait in its own queue.
func TestHangingTargetDoesNotBlockOthers(t *testing.T) {
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	var writes atomic.Int64
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(func() { close(release); hang.Close(); ok.Close() })

	targets := promwrite.Targets{{Name: "hang", URL: hang.URL}, {Name: "ok", URL: ok.URL}}
	reg, data := prometheus.NewRegistry(), prometheus.NewRegistry()
	m := NewPushedMetrics(reg, data, targets.Labels())
	devices := NewDeviceRegistry(map[string]DeviceConfig{"office": {ID: "0123456789abcdef"}})
	p := newMetricPusher(m, data, promwrite.NewPushers(targets, nil), "breezyd", devices, nil)
	hangQ, okQ := p.targets[0].Queue, p.targets[1].Queue

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	const polls = 100 // more than the polls channel holds
	snap := representativeSnapshot()
	base := time.Now().Truncate(time.Second)
	for i := range polls {
		snap.LastPoll = base.Add(time.Duration(i) * time.Millisecond)
		p.Notify("office", snap)
		time.Sleep(2 * time.Millisecond)
	}

	deadline := time.Now().Add(5 * time.Second)
	for len(p.polls) != 0 || okQ.Len() != 0 || writes.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("pending polls %d (want 0), healthy queue %d (want 0), healthy writes %d",
				len(p.polls), okQ.Len(), writes.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if okQ.Errors() != 0 {
		t.Errorf("healthy target errors = %d", okQ.Errors())
	}
	// The hanging target keeps what its stuck write has not taken, bounded
	// by the queue's MaxAge as before.
	if hangQ.Len() == 0 {
		t.Error("hanging target's samples should wait in its own queue")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}
