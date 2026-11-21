// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matryer/is"
)

func corruptFiles(t *testing.T, dir, prefix string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, prefix+".corrupt-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEnergyTracker_Load_CorruptFileQuarantined(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "d", StateDir: dir}
	is.NoErr(os.WriteFile(tr.statePath(), []byte("{not json"), 0o600))

	tr.Load()

	_, err := os.Stat(tr.statePath())
	is.True(os.IsNotExist(err)) // bad file moved out of the way
	aside := corruptFiles(t, dir, "energy_d.json")
	is.Equal(len(aside), 1) // evidence preserved
	b, err := os.ReadFile(aside[0])
	is.NoErr(err)
	is.Equal(string(b), "{not json")
	snap := tr.Snapshot()
	is.Equal(snap.HeatingLifetimeKWh, float64(0)) // starts empty
	is.True(snap.Supported)                       // counters still exported
	is.True(strings.Contains(snap.StateWarning, "energy_d.json.corrupt-"))
}

func TestEnergyTracker_Load_MissingFileNoWarning(t *testing.T) {
	is := is.New(t)
	tr := &EnergyTracker{Device: "d", StateDir: t.TempDir()}
	tr.Load()
	is.Equal(tr.Snapshot().StateWarning, "")
}

func TestEnergyTracker_Load_UnreadableFileQuarantined(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "d", StateDir: dir}
	// A directory where the file should be: exists, cannot be read.
	is.NoErr(os.Mkdir(tr.statePath(), 0o700))
	tr.Load()
	is.Equal(len(corruptFiles(t, dir, "energy_d.json")), 1)
	is.True(tr.Snapshot().StateWarning != "")
}

func TestScheduler_Load_CorruptFileQuarantined(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	s := &Scheduler{Device: "x", StateDir: dir}
	is.NoErr(os.WriteFile(s.statePath(), []byte("{not json"), 0o600))
	s.Load()

	_, err := os.Stat(s.statePath())
	is.True(os.IsNotExist(err)) // bad file moved aside, not left to be overwritten
	aside := corruptFiles(t, dir, "schedule_x.json")
	is.Equal(len(aside), 1)
	b, err := os.ReadFile(aside[0])
	is.NoErr(err)
	is.Equal(string(b), "{not json")

	snap := s.Snapshot()
	is.Equal(len(snap.Entries), 0)
	is.True(strings.Contains(snap.LoadWarning, "schedule_x.json.corrupt-"))
	v := scheduleViewFrom(snap)
	is.True(v.Alert) // dashboard block force-expands
	is.True(v.LoadWarning != "")

	// The user saving a fresh schedule clears the warning.
	is.NoErr(s.Replace(false, nil))
	is.Equal(s.Snapshot().LoadWarning, "")
}

func TestScheduler_Load_InvalidEntriesQuarantined(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	s := &Scheduler{Device: "x", StateDir: dir}
	is.NoErr(os.WriteFile(s.statePath(),
		[]byte(`{"version":1,"enabled":true,"entries":[{"at":"99:99","action":"bogus","pct":1}]}`), 0o600))
	s.Load()
	is.Equal(len(corruptFiles(t, dir, "schedule_x.json")), 1)
	is.True(s.Snapshot().LoadWarning != "")
}

func TestScheduler_Load_MissingFileNoWarning(t *testing.T) {
	is := is.New(t)
	s := &Scheduler{Device: "x", StateDir: t.TempDir()}
	s.Load()
	is.Equal(s.Snapshot().LoadWarning, "")
}

// TestDaemonStateDir_Failures pins that an undeterminable or uncreatable
// state dir is an error carrying no usable path (never "" -> cwd files).
func TestDaemonStateDir_Failures(t *testing.T) {
	t.Run("no home and no XDG", func(t *testing.T) {
		is := is.New(t)
		t.Setenv("STATE_DIRECTORY", "")
		t.Setenv("XDG_STATE_HOME", "")
		t.Setenv("HOME", "")
		_, err := daemonStateDir()
		is.True(err != nil)
	})
	t.Run("cannot create", func(t *testing.T) {
		is := is.New(t)
		blocker := filepath.Join(t.TempDir(), "file")
		is.NoErr(os.WriteFile(blocker, []byte("x"), 0o600))
		t.Setenv("STATE_DIRECTORY", filepath.Join(blocker, "sub"))
		_, err := daemonStateDir()
		is.True(err != nil)
	})
}

// TestRun_FailsWhenStateDirUnavailable pins that the daemon refuses to
// start rather than writing state files into the working directory.
func TestRun_FailsWhenStateDirUnavailable(t *testing.T) {
	is := is.New(t)
	cfgPath := writeTempConfig(t, freeListenAddr(t), "127.0.0.1:1", "0000000000000000", "1111")
	setRunFlags(t, cfgPath)
	blocker := filepath.Join(t.TempDir(), "file")
	is.NoErr(os.WriteFile(blocker, []byte("x"), 0o600))
	t.Setenv("STATE_DIRECTORY", filepath.Join(blocker, "sub"))

	err := run(context.Background())
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "state"))
}
