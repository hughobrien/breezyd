// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// writeJSONAtomic marshals v to JSON and writes it to path via a sibling
// temp file (path + ".tmp") + os.Rename, mode 0600. On rename failure the
// temp is removed (best-effort) before the error returns.
//
// Used by both EnergyTracker.save and Scheduler.save for their per-device
// state files. Keep this helper small and unopinionated — error messages
// here are short ("marshal:" / "write temp:" / "rename:") so callers can
// wrap them with a domain prefix ("energy:" / "schedule:") for the final
// log line.
func writeJSONAtomic(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
