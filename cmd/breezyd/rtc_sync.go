// SPDX-License-Identifier: GPL-3.0-or-later

// Per-device daily RTC sync. Writes the device's hardware clock
// (params 0x6F + 0x70 via breezy.SetRTC) once shortly after the
// goroutine starts, then daily at 04:00 local time. Closes the
// panel-display drift introduced by DST transitions, battery
// replacement (CR2032 at 0x24), and long-term RTC oscillator drift.
//
// No persisted state — daemon restart re-establishes the cycle via
// the initial-sync. See docs/superpowers/specs/2026-05-11-rtc-sync-design.md.
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// rtcInitialDelay is how long after Run starts before the initial sync
// fires. Tuned to let the poller's first reads land first so the sync
// write doesn't compete for the per-device UDP lock during startup.
// Package var so tests can shrink it.
var rtcInitialDelay = 30 * time.Second

// syncTimeout bounds a single SetRTC round-trip. Matches the
// scheduler's fireTimeout.
const syncTimeout = 5 * time.Second

// rtcSyncHour is the local-time hour-of-day for the daily sync (24h).
// Picked to land well past both DST transitions (02:00) and outside
// typical user-interaction hours.
const rtcSyncHour = 4

// RTCSyncer keeps the device's hardware clock aligned with the
// daemon's local time. One per configured device; runs as a single
// goroutine via Run.
type RTCSyncer struct {
	Device  string
	Dial    func(ctx context.Context) (rc breezy.DeviceClient, raw HandlerClient, err error)
	LockUDP func() func()
	Now     func() time.Time // test seam; nil → time.Now
}

// now is the test seam.
func (r *RTCSyncer) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// untilNext returns the duration from now until the next occurrence of
// hour:00:00 in now's location. If now is already at or past hour:00
// today, returns the duration until hour:00 tomorrow.
func untilNext(now time.Time, hour int) time.Duration {
	target := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, now.Location())
	d := target.Sub(now)
	if d <= 0 {
		d += 24 * time.Hour
	}
	return d
}

// Run blocks until ctx is done. Fires an initial sync after
// rtcInitialDelay, then daily at rtcSyncHour:00 local time.
func (r *RTCSyncer) Run(ctx context.Context) {
	// Initial sync after a short delay.
	select {
	case <-ctx.Done():
		return
	case <-time.After(rtcInitialDelay):
	}
	r.syncOnce(ctx)

	// Daily at rtcSyncHour:00 thereafter.
	for {
		wait := untilNext(r.now(), rtcSyncHour)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		r.syncOnce(ctx)
	}
}

// syncOnce performs a single SetRTC against the device. Failures log
// a warning and return — the next daily tick retries naturally.
func (r *RTCSyncer) syncOnce(ctx context.Context) {
	if r.LockUDP != nil {
		unlock := r.LockUDP()
		defer unlock()
	}
	cctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()

	if r.Dial == nil {
		slog.Warn("rtc sync: Dial not configured", "device", r.Device)
		return
	}
	client, raw, err := r.Dial(cctx)
	if err != nil {
		slog.Warn("rtc sync: dial failed", "device", r.Device, "err", err)
		return
	}
	defer func() { _ = raw.Close() }()

	now := r.now()
	if err := breezy.SetRTC(cctx, client, now); err != nil {
		slog.Warn("rtc sync: write failed", "device", r.Device, "err", err)
		return
	}
	slog.Info("rtc sync: wrote", "device", r.Device, "at", now.Format(time.RFC3339))
}
