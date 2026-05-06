// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"testing"
	"time"
)

func TestEnergyTracker_Load_MissingFile(t *testing.T) {
	tr := &EnergyTracker{
		Device:   "test-device",
		StateDir: t.TempDir(),
	}

	if err := tr.Load(); err != nil {
		t.Fatalf("Load on missing file: got error %v, want nil", err)
	}

	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 || snap.CoolingTodayKWh != 0 || snap.ConsumedTodayKWh != 0 {
		t.Errorf("today counters not zero: heating=%v cooling=%v consumed=%v",
			snap.HeatingTodayKWh, snap.CoolingTodayKWh, snap.ConsumedTodayKWh)
	}
	if snap.HeatingLifetimeKWh != 0 || snap.CoolingLifetimeKWh != 0 || snap.ConsumedLifetimeKWh != 0 {
		t.Errorf("lifetime counters not zero: heating=%v cooling=%v consumed=%v",
			snap.HeatingLifetimeKWh, snap.CoolingLifetimeKWh, snap.ConsumedLifetimeKWh)
	}

	wantDate := time.Now().Local().Format("2006-01-02")
	if tr.Today != wantDate {
		t.Errorf("Today = %q, want %q", tr.Today, wantDate)
	}
}

func TestEnergyTracker_Load_MalformedFile(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:   "test-device",
		StateDir: dir,
	}

	// Write malformed JSON to the state path.
	if err := os.WriteFile(tr.statePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := tr.Load(); err != nil {
		t.Fatalf("Load on malformed file: got error %v, want nil", err)
	}

	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 || snap.CoolingTodayKWh != 0 || snap.ConsumedTodayKWh != 0 {
		t.Errorf("today counters not zero after malformed load: heating=%v cooling=%v consumed=%v",
			snap.HeatingTodayKWh, snap.CoolingTodayKWh, snap.ConsumedTodayKWh)
	}
	if snap.HeatingLifetimeKWh != 0 || snap.CoolingLifetimeKWh != 0 || snap.ConsumedLifetimeKWh != 0 {
		t.Errorf("lifetime counters not zero after malformed load: heating=%v cooling=%v consumed=%v",
			snap.HeatingLifetimeKWh, snap.CoolingLifetimeKWh, snap.ConsumedLifetimeKWh)
	}
}

func TestEnergyTracker_Load_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "playroom", StateDir: dir}
	// A zero-byte file at the state path simulates an interrupted write
	// that left a truncated artefact behind. Load should treat it as
	// malformed (warn + fresh state) rather than panic on the unmarshal.
	if err := os.WriteFile(tr.statePath(), nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tr.Load(); err != nil {
		t.Fatalf("Load on empty file should succeed (start fresh), got %v", err)
	}
	if tr.HeatingLifetimeKWh != 0 {
		t.Errorf("expected fresh state on empty file, got HeatingLifetimeKWh=%v", tr.HeatingLifetimeKWh)
	}
	if tr.Today != time.Now().Local().Format("2006-01-02") {
		t.Errorf("Today not set on empty-file load")
	}
}

func TestEnergyTracker_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Local().Format("2006-01-02")

	tr := &EnergyTracker{
		Device:              "living-room",
		StateDir:            dir,
		HeatingTodayKWh:     1.1,
		CoolingTodayKWh:     2.2,
		ConsumedTodayKWh:    3.3,
		HeatingLifetimeKWh:  100.1,
		CoolingLifetimeKWh:  200.2,
		ConsumedLifetimeKWh: 300.3,
		Today:               today,
	}

	if err := tr.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	tr2 := &EnergyTracker{
		Device:   "living-room",
		StateDir: dir,
	}
	if err := tr2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if tr2.HeatingTodayKWh != tr.HeatingTodayKWh {
		t.Errorf("HeatingTodayKWh: got %v, want %v", tr2.HeatingTodayKWh, tr.HeatingTodayKWh)
	}
	if tr2.CoolingTodayKWh != tr.CoolingTodayKWh {
		t.Errorf("CoolingTodayKWh: got %v, want %v", tr2.CoolingTodayKWh, tr.CoolingTodayKWh)
	}
	if tr2.ConsumedTodayKWh != tr.ConsumedTodayKWh {
		t.Errorf("ConsumedTodayKWh: got %v, want %v", tr2.ConsumedTodayKWh, tr.ConsumedTodayKWh)
	}
	if tr2.HeatingLifetimeKWh != tr.HeatingLifetimeKWh {
		t.Errorf("HeatingLifetimeKWh: got %v, want %v", tr2.HeatingLifetimeKWh, tr.HeatingLifetimeKWh)
	}
	if tr2.CoolingLifetimeKWh != tr.CoolingLifetimeKWh {
		t.Errorf("CoolingLifetimeKWh: got %v, want %v", tr2.CoolingLifetimeKWh, tr.CoolingLifetimeKWh)
	}
	if tr2.ConsumedLifetimeKWh != tr.ConsumedLifetimeKWh {
		t.Errorf("ConsumedLifetimeKWh: got %v, want %v", tr2.ConsumedLifetimeKWh, tr.ConsumedLifetimeKWh)
	}
	if tr2.Today != tr.Today {
		t.Errorf("Today: got %q, want %q", tr2.Today, tr.Today)
	}
}

func TestEnergyTracker_Snapshot_IsCopy(t *testing.T) {
	tr := &EnergyTracker{
		Device:          "office",
		StateDir:        t.TempDir(),
		HeatingTodayKWh: 5.5,
		CoolingTodayKWh: 6.6,
		Today:           "2026-05-06",
	}

	snap := tr.Snapshot()

	// Mutate the tracker after taking the snapshot.
	tr.HeatingTodayKWh = 999.9
	tr.CoolingTodayKWh = 888.8
	tr.Today = "1970-01-01"

	if snap.HeatingTodayKWh != 5.5 {
		t.Errorf("snapshot HeatingTodayKWh mutated: got %v, want 5.5", snap.HeatingTodayKWh)
	}
	if snap.CoolingTodayKWh != 6.6 {
		t.Errorf("snapshot CoolingTodayKWh mutated: got %v, want 6.6", snap.CoolingTodayKWh)
	}
}
