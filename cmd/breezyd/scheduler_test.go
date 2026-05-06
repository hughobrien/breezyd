// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

func TestScheduleTime_ParseAndString(t *testing.T) {
	cases := []struct {
		in   string
		want ScheduleTime
	}{
		{"00:00", 0},
		{"08:00", 480},
		{"22:30", 22*60 + 30},
		{"23:59", 1439},
	}
	for _, c := range cases {
		got, err := ParseScheduleTime(c.in)
		if err != nil {
			t.Errorf("ParseScheduleTime(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseScheduleTime(%q) = %d, want %d", c.in, got, c.want)
		}
		if back := got.String(); back != c.in {
			t.Errorf("%d.String() = %q, want %q", got, back, c.in)
		}
	}
	bad := []string{"", "8:00", "08:0", "24:00", "08:60", "08-00", "abc", "08:00:00"}
	for _, in := range bad {
		if _, err := ParseScheduleTime(in); err == nil {
			t.Errorf("ParseScheduleTime(%q): want error, got nil", in)
		}
	}
}

func TestScheduler_Validation(t *testing.T) {
	s := &Scheduler{}
	good := []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
		{At: 1320, Action: "off", Pct: 60},
	}
	if err := s.validate(good); err != nil {
		t.Errorf("good schedule rejected: %v", err)
	}
	if err := s.validate(nil); err != nil {
		t.Errorf("empty disabled schedule rejected: %v", err)
	}
	if err := s.validate(nil); err != nil {
		t.Errorf("empty enabled schedule rejected: %v", err)
	}

	type badCase struct {
		name    string
		entries []ScheduleEntry
	}
	bads := []badCase{
		{"unknown action", []ScheduleEntry{{At: 480, Action: "boost", Pct: 60}}},
		{"pct too low", []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 5}}},
		{"pct too high", []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 101}}},
		{"pct zero", []ScheduleEntry{{At: 480, Action: "regeneration", Pct: 0}}},
		{"duplicate at", []ScheduleEntry{
			{At: 600, Action: "regeneration", Pct: 60},
			{At: 600, Action: "off", Pct: 60},
		}},
	}
	for _, b := range bads {
		err := s.validate(b.entries)
		if err == nil {
			t.Errorf("%s: want error, got nil", b.name)
			continue
		}
		if !errors.Is(err, breezy.ErrInvalidArg) {
			t.Errorf("%s: want ErrInvalidArg, got %v", b.name, err)
		}
	}

	too := make([]ScheduleEntry, 25)
	for i := range too {
		too[i] = ScheduleEntry{At: ScheduleTime(i), Action: "regeneration", Pct: 60}
	}
	if err := s.validate(too); !errors.Is(err, breezy.ErrInvalidArg) {
		t.Errorf("25 entries: want ErrInvalidArg, got %v", err)
	}
}

func TestScheduleEntry_JSON(t *testing.T) {
	in := ScheduleEntry{At: 480, Action: "regeneration", Pct: 60}
	data, err := in.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"at":"08:00","action":"regeneration","pct":60}`
	if string(data) != want {
		t.Errorf("marshal: got %s, want %s", data, want)
	}
	var out ScheduleEntry
	if err := out.UnmarshalJSON([]byte(want)); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("roundtrip: %+v != %+v", out, in)
	}
}

func TestScheduler_PersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &Scheduler{Device: "playroom", StateDir: dir}

	s.mu.Lock()
	s.enabled = true
	s.entries = []ScheduleEntry{
		{At: 480, Action: "regeneration", Pct: 60},
		{At: 1320, Action: "off", Pct: 60},
	}
	s.lastApply = &LastApply{
		At:      1320,
		Fired:   time.Date(2026, 5, 6, 22, 0, 14, 0, time.UTC),
		OK:      false,
		Err:     "device_unreachable: i/o timeout",
		Retries: 5,
	}
	if err := s.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	s.mu.Unlock()

	s2 := &Scheduler{Device: "playroom", StateDir: dir}
	if err := s2.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	snap := s2.Snapshot()
	if !snap.Enabled || len(snap.Entries) != 2 || snap.Entries[0].At != 480 || snap.Entries[1].Action != "off" {
		t.Errorf("entries did not survive roundtrip: %+v", snap)
	}
	if snap.LastApply == nil || snap.LastApply.Retries != 5 || snap.LastApply.OK {
		t.Errorf("lastApply did not survive roundtrip: %+v", snap.LastApply)
	}
}

func TestScheduler_LoadMissingFileStartsEmpty(t *testing.T) {
	s := &Scheduler{Device: "x", StateDir: t.TempDir()}
	if err := s.Load(); err != nil {
		t.Fatalf("load missing: %v", err)
	}
	snap := s.Snapshot()
	if snap.Enabled || len(snap.Entries) != 0 || snap.LastApply != nil {
		t.Errorf("expected empty state, got %+v", snap)
	}
}

func TestScheduler_LoadMalformedFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := &Scheduler{Device: "x", StateDir: dir}
	if err := os.WriteFile(s.statePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed bad file: %v", err)
	}
	if err := s.Load(); err != nil {
		t.Fatalf("load malformed: %v", err)
	}
	snap := s.Snapshot()
	if snap.Enabled || len(snap.Entries) != 0 {
		t.Errorf("malformed file should start empty, got %+v", snap)
	}
}

func TestScheduler_SaveAtomic(t *testing.T) {
	dir := t.TempDir()
	s := &Scheduler{Device: "x", StateDir: dir}
	s.mu.Lock()
	s.enabled = false
	if err := s.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	s.mu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file leaked: %s", e.Name())
		}
	}
}
