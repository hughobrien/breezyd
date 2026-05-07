// SPDX-License-Identifier: GPL-3.0-or-later

package templates

import (
	"context"
	"strings"
	"testing"
)

func TestSpeedLabel(t *testing.T) {
	var sb strings.Builder
	if err := SpeedLabel(75).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	if !strings.Contains(got, "75%") {
		t.Errorf("output missing 75%%: %q", got)
	}
	if !strings.Contains(got, `class="speed-label"`) {
		t.Errorf("output missing class: %q", got)
	}
}
