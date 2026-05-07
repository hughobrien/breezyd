// SPDX-License-Identifier: GPL-3.0-or-later

// Plain-Go helpers callable from templ templates. Keep this file small and
// free of HTTP / state — anything that needs request context belongs in a
// handler, not here.
package ui

import (
	"fmt"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// HumanPct renders 0-100 as "0%" through "100%".
func HumanPct(p int) string {
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return fmt.Sprintf("%d%%", p)
}

// ModeName renders an airflow mode byte as the lowercase string used
// throughout the dashboard ("regeneration", "supply", "extract",
// "ventilation"). Delegates to breezy.AirflowModeName which is the
// canonical decoder for parameter 0xB7.
func ModeName(m uint8) string {
	return breezy.AirflowModeName(m)
}
