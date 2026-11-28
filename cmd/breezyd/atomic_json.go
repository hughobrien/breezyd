// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Seams for the durability steps so tests can observe their order.
var (
	fsyncFile = func(f *os.File) error { return f.Sync() }
	fsyncDir  = func(dir string) error {
		d, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer func() { _ = d.Close() }()
		return d.Sync()
	}
)

// writeJSONAtomic marshals v to JSON and writes it to path via a sibling
// temp file (path + ".tmp") + os.Rename, mode 0600. The temp file is
// fsynced before the rename and the parent directory after it, so a power
// cut leaves either the old or the new file, never an empty one. A
// directory-fsync error is ignored (unsupported on some platforms and
// filesystems). On any failure before the rename the temp is removed
// (best-effort) before the error returns.
//
// Used by Scheduler.save (rare user config edits where durability matters).
// High-frequency non-critical counters (EnergyTracker.save) use
// writeJSONAtomicNoSync instead. Keep these helpers small and unopinionated —
// error messages here are short ("marshal:" / "write temp:" / "sync temp:" /
// "rename:") so callers can wrap them with a domain prefix ("energy:" /
// "schedule:") for the final log line.
func writeJSONAtomic(path string, v any) error {
	return writeJSONAtomicOpts(path, v, true)
}

// writeJSONAtomicNoSync marshals v to JSON and writes it to path via a
// sibling temp file (path + ".tmp") + os.Rename, mode 0600, without calling
// fsync on the file or parent directory. Temp+rename still prevents a
// process crash from leaving a partial/empty target file, while avoiding
// synchronous write barriers for high-frequency, low-criticality state
// such as EnergyTracker counters.
func writeJSONAtomicNoSync(path string, v any) error {
	return writeJSONAtomicOpts(path, v, false)
}

func writeJSONAtomicOpts(path string, v any, sync bool) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp: %w", err)
	}
	if sync {
		if err := fsyncFile(f); err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("sync temp: %w", err)
		}
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	if sync {
		_ = fsyncDir(filepath.Dir(path))
	}
	return nil
}

// quarantineStateFile renames a state file that exists but cannot be
// read or parsed to "<path>.corrupt-<unix timestamp>" so the next save
// can never overwrite the evidence. Returns the new path. If the rename
// itself fails the error is returned and the original path is left alone.
func quarantineStateFile(path string) (string, error) {
	aside := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
	if err := os.Rename(path, aside); err != nil {
		return "", err
	}
	return aside, nil
}
