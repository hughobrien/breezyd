// SPDX-License-Identifier: GPL-3.0-or-later

package templates

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui"
)

var update = flag.Bool("update", false, "update golden files")

func loadView(t *testing.T, name string) ui.DeviceView {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var v ui.DeviceView
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDeviceCardGolden(t *testing.T) {
	cases := []string{
		"snapshot_regen", "snapshot_manual", "snapshot_settling",
		"snapshot_sensor_alert", "snapshot_schedule_alert",
		"snapshot_energy_error", "snapshot_no_energy",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			view := loadView(t, name)
			var sb strings.Builder
			if err := DeviceCard(view).Render(context.Background(), &sb); err != nil {
				t.Fatal(err)
			}
			got := sb.String()
			goldenPath := filepath.Join("testdata", "golden_"+strings.TrimPrefix(name, "snapshot_")+".html")
			if *update {
				if err := os.WriteFile(goldenPath, []byte(got), 0644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != got {
				t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, string(want))
			}
		})
	}
}
