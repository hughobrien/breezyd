// SPDX-License-Identifier: GPL-3.0-or-later

package ui

import "fmt"

// FmtTempC formats a *float64 in degrees Celsius. nil → "—".
func FmtTempC(v *float64) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%.1f°C", *v)
}

// FmtOptPct formats an optional percentage. nil → "—".
func FmtOptPct(v *int) string {
	if v == nil {
		return "—"
	}
	return fmt.Sprintf("%d%%", *v)
}

// RPMStr formats an optional fan RPM reading. nil → "—", 0 → "off".
func RPMStr(v *int) string {
	if v == nil {
		return "—"
	}
	if *v == 0 {
		return "off"
	}
	return fmt.Sprintf("%d rpm", *v)
}

// TempDeltaStr formats a signed Celsius delta between two optional
// temperatures. Either nil → "—".
func TempDeltaStr(a, b *float64) string {
	if a == nil || b == nil {
		return "—"
	}
	d := *a - *b
	if d >= 0 {
		return fmt.Sprintf("+%.1f°C", d)
	}
	return fmt.Sprintf("%.1f°C", d)
}
