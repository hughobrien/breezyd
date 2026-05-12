// SPDX-License-Identifier: GPL-3.0-or-later

package templates

import (
	"fmt"
	"strings"
)

// cascadeFunc returns the JS string for implied signal updates that
// follow a write to its primary signal. The cascade fires AFTER the
// primary write, so it reads $signal.name directly to see the new
// value — no jsValue argument needed. This sidesteps operator-precedence
// bugs (e.g. `ternary !== 'off'` parsing wrongly) and stale-read bugs
// (re-evaluating a ternary against a now-mutated signal).
//
// nil means "no cascade for this signal" — registered explicitly so the
// AllWritableSignalsCovered test recognizes deliberate emptiness vs.
// omission.
type cascadeFunc func(name string) string

// cascades is the single source of truth for cross-signal invariants
// enforced by the firmware or the daemon's HTTP handlers. Every signal
// that a click handler writes optimistically MUST be registered here —
// clickAction panics on unknown signals to force the choice (real
// cascade or explicit nil).
//
// Firmware behavior was established by direct probing of the office
// Twinfresh Elite 160 on 2026-05-11 (see spec); handler behavior is
// encoded in cmd/breezyd/handlers_ui_write.go.
var cascades = map[string]cascadeFunc{
	// Firmware: any speed_mode write clears the timer.
	"speedMode": func(name string) string {
		return fmt.Sprintf(
			"$specialMode.%s = 'off'; $specialModeRemainingSeconds.%s = 0;",
			name, name)
	},

	// Handler: activating a timer (non-off) also writes power=on so the
	// $power flag stays coherent with "fans are running". Reads the
	// just-mutated $specialMode signal directly.
	"specialMode": func(name string) string {
		return fmt.Sprintf(
			"if ($specialMode.%s !== 'off') { $power.%s = true; }",
			name, name)
	},

	// Handler: power=off explicitly clears the timer (a still-running
	// timer on a powered-off unit would be an incoherent state for the
	// dashboard). Reads the just-mutated $power signal directly.
	"power": func(name string) string {
		return fmt.Sprintf(
			"if (!$power.%s) { $specialMode.%s = 'off'; $specialModeRemainingSeconds.%s = 0; }",
			name, name, name)
	},

	// No cascade. Probe confirmed firmware leaves the timer alone when
	// airflow_mode is written.
	"airflowMode": nil,

	// No cascade. Heater is independent of every other signal we track.
	"heater": nil,
}

// clickAction builds the data-on:click expression for a button whose
// primary intent is one optimistic signal write plus a POST. The
// cascade map contributes the implied signal updates between the
// primary write and the POST.
//
//	name    — device name (e.g. "alpha")
//	signal  — primary signal key; MUST appear in cascades or this panics
//	jsValue — JS expression for the new value (e.g. "'preset2'", "!$power.alpha")
//	url     — POST endpoint, full path
//	payload — JS object-literal expression for the POST body, or "" for no body.
//	          May reference the `__next` const which clickAction defines —
//	          equals the new signal value, computed once from jsValue.
//
// Order matters: jsValue is evaluated ONCE into __next (so toggle
// expressions like `!$power.alpha` don't re-evaluate against the
// just-mutated signal); primary signal write uses __next so the
// button's own aria-pressed binding lights up instantly; cascade reads
// the (now-updated) signal and applies its rule before the POST so any
// signals the firmware/handler will change are pre-flipped for sibling
// bindings (e.g. clicking a preset chip de-lights the night-mode chip
// without waiting for the SSE roundtrip).
func clickAction(name, signal, jsValue, url, payload string) string {
	cascade, ok := cascades[signal]
	if !ok {
		panic(fmt.Sprintf("clickAction: unknown signal %q — register it in the cascades map (use nil if there's no cascade)", signal))
	}
	parts := []string{
		fmt.Sprintf("const __next = %s;", jsValue),
		fmt.Sprintf("$%s.%s = __next;", signal, name),
	}
	if cascade != nil {
		parts = append(parts, cascade(name))
	}
	parts = append(parts, postActionExpr(url, payload))
	return strings.Join(parts, " ")
}
