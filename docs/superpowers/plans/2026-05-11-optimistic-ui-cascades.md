# Optimistic UI cascades — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every ad-hoc speculative-UI fix in the dashboard with one centralized helper (`clickAction`) and a single cascade table, plus one pure-derivation helper (`effPower`) for state the cascade pattern can't reach.

**Architecture:** Two layers, both in `cmd/breezyd/ui/templates/`. Layer B is the universal click-action helper plus a cascade table — every click handler becomes `clickAction(name, signal, value, url, payload)` (sometimes wrapped by `attentionIfOff`), and the table encodes every firmware/handler-driven implied write in one place. Layer A is a tiny JS helper (`effPower`) for the only state transition that originates outside the dashboard (panel button / IR remote starting a timer with power=0). A coverage test panics on unknown signals, so a new writable signal can't be added without a cascade entry.

**Tech Stack:** Go + templ (server-rendered HTML), datastar v1.0 (client signals + SSE patches), Playwright (UI tests).

**Spec:** `docs/superpowers/specs/2026-05-11-optimistic-ui-cascades-design.md`

---

## File structure

| File | Responsibility | Status |
| --- | --- | --- |
| `cmd/breezyd/ui/templates/cascades.go` | Cascade table, `cascadeFunc` type, `clickAction()` helper. Pure Go, no templ. | **NEW** |
| `cmd/breezyd/ui/templates/controls_block.templ` | Six click handlers (preset chip, manual chip, mode buttons, timer buttons, heater button) rewritten to call `clickAction()`. Removes `presetChipExpr`, `heaterClickExpr`, `closeEditorThen`, `timerClickExpr`. Keeps `attentionIfOff`, `postActionExpr`, `withDatastarOpts`, `manualBtnPct`, `presetLabel`, `presetChipDataText`, `presetEditor` etc. | MODIFY |
| `cmd/breezyd/ui/templates/device_card.templ` | Power button rewritten to use `clickAction()`. Removes `powerButtonExpr`. Switches power button `data-attr:aria-pressed` to call `effPower(...)`. | MODIFY |
| `cmd/breezyd/ui/templates/layout.templ` | Add JS helper `effPower(power, special)` next to existing `fmtRemaining`. | MODIFY |
| `cmd/breezyd/ui/templates/render_test.go` | Add `TestCascades_*` (one per entry), `TestCascadeTable_AllWritableSignalsCovered`, `TestClickAction_*`. Replace `TestPresetChipExpr` with `TestPresetClickExpr` (or fold into a table-driven `TestClickHandlers_*`). | MODIFY |
| `cmd/breezyd/ui/templates/testdata/golden_healthy.html` | Regenerate after each click-handler migration (`-update` flag). | REGEN |
| `cmd/breezyd/ui/templates/testdata/golden_stale.html` | Regenerate. | REGEN |
| `tests/ui/dashboard.spec.ts` | Add one regression test: "click preset chip while night-mode active → night chip aria-pressed flips to false within 100ms (optimistic)". Add a second test for `effPower`: with timer active and `$power=false` (forced via test admin), the power button shows aria-pressed=true. | MODIFY |

---

## Task 0: Foundation — cascades.go + clickAction helper

**Goal:** Build the cascade table, `cascadeFunc` type, and `clickAction()` helper with full unit-test coverage. No callers yet; this task only adds infrastructure.

**Files:**
- Create: `cmd/breezyd/ui/templates/cascades.go`
- Modify: `cmd/breezyd/ui/templates/render_test.go` (append tests)

**Acceptance Criteria:**
- [ ] `cascades.go` defines `cascadeFunc` type and `cascades` map with entries for `speedMode`, `specialMode`, `power`, `airflowMode`, `heater`.
- [ ] `clickAction(name, signal, jsValue, url, payload)` returns a JS expression that concatenates: primary signal write, cascade (if non-nil), POST.
- [ ] `clickAction` panics if `signal` is not registered in the map (forces new writable signals to be considered).
- [ ] Unit tests pin every cascade's output for at least one (name, value) pair.
- [ ] `TestCascadeTable_AllWritableSignalsCovered` enumerates a hardcoded whitelist `["speedMode", "specialMode", "power", "heater", "airflowMode"]` and asserts each is registered.
- [ ] `go test ./cmd/breezyd/ui/templates/` passes.

**Verify:** `go test ./cmd/breezyd/ui/templates/ -run "TestCascades|TestClickAction|TestCascadeTable" -v` → all pass.

**Steps:**

- [ ] **Step 1: Write cascades.go**

Create `cmd/breezyd/ui/templates/cascades.go`:

```go
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
```

- [ ] **Step 2: Append tests to render_test.go**

Append to `cmd/breezyd/ui/templates/render_test.go` (after the existing tests, before EOF):

```go
// TestCascades_SpeedModeClearsTimer locks the firmware invariant probed
// on 2026-05-11: writing any speed_mode value clears the timer (param
// 0x0007). The cascade mirrors that on the client so sibling bindings
// (the night/turbo chips) de-light optimistically.
func TestCascades_SpeedModeClearsTimer(t *testing.T) {
	got := cascades["speedMode"]("alpha")
	want := "$specialMode.alpha = 'off'; $specialModeRemainingSeconds.alpha = 0;"
	if got != want {
		t.Errorf("speedMode cascade:\n  got: %s\n want: %s", got, want)
	}
}

// TestCascades_SpecialModeActivatePowersOn locks the handler invariant:
// activating a timer also writes power=on. The cascade reads the
// just-mutated $specialMode signal directly — at runtime, the primary
// write has set $specialMode to the new value, and this guard fires
// only when that value is non-'off'.
func TestCascades_SpecialModeActivatePowersOn(t *testing.T) {
	got := cascades["specialMode"]("alpha")
	want := "if ($specialMode.alpha !== 'off') { $power.alpha = true; }"
	if got != want {
		t.Errorf("specialMode cascade:\n  got: %s\n want: %s", got, want)
	}
}

// TestCascades_PowerOffClearsTimer locks the handler invariant: writing
// power=off also clears the timer. The cascade reads the just-mutated
// $power signal directly.
func TestCascades_PowerOffClearsTimer(t *testing.T) {
	got := cascades["power"]("alpha")
	want := "if (!$power.alpha) { $specialMode.alpha = 'off'; $specialModeRemainingSeconds.alpha = 0; }"
	if got != want {
		t.Errorf("power cascade:\n  got: %s\n want: %s", got, want)
	}
}

// TestCascades_AirflowModeNil pins the explicit "no cascade" decision —
// removing this entry would make the AllWritableSignalsCovered test
// fail, but we want a separate assertion documenting that this nil-ness
// is deliberate (firmware probe confirmed timer survives mode writes).
func TestCascades_AirflowModeNil(t *testing.T) {
	if cascades["airflowMode"] != nil {
		t.Errorf("airflowMode cascade should be nil (firmware leaves timer alone); got non-nil")
	}
}

// TestCascades_HeaterNil similarly pins the heater "no cascade" decision.
func TestCascades_HeaterNil(t *testing.T) {
	if cascades["heater"] != nil {
		t.Errorf("heater cascade should be nil (no cross-signal effects); got non-nil")
	}
}

// TestCascadeTable_AllWritableSignalsCovered is the keystone: every
// signal that a click handler writes optimistically MUST appear in the
// cascades map (with nil if there's deliberately no cascade). If you
// add a new writable signal to a click handler, add it here and to the
// map in the same PR.
func TestCascadeTable_AllWritableSignalsCovered(t *testing.T) {
	writable := []string{"speedMode", "specialMode", "power", "heater", "airflowMode"}
	for _, sig := range writable {
		if _, ok := cascades[sig]; !ok {
			t.Errorf("signal %q is written by a click handler but not in cascades — register it (use nil for no cascade)", sig)
		}
	}
}

// TestClickAction_NoCascade pins the structure of a click-action
// expression for a signal whose cascade is nil (heater). Expected
// shape: __next const, primary write, POST. No cascade segment.
// Callers reference __next in payload to send the new value to the
// server without re-reading the just-mutated signal.
func TestClickAction_NoCascade(t *testing.T) {
	got := clickAction("alpha", "heater", "!$heater.alpha",
		"/ui/devices/alpha/heater", "{on: __next}")
	want := "const __next = !$heater.alpha; $heater.alpha = __next; @post('/ui/devices/alpha/heater', {payload: {on: __next}})"
	if got != want {
		t.Errorf("clickAction (no cascade):\n  got: %s\n want: %s", got, want)
	}
}

// TestClickAction_WithCascade pins the structure for a signal with a
// cascade (specialMode → power-on guard). Expected shape: __next const,
// primary write, cascade (reads the just-mutated signal), POST — joined
// by single spaces.
func TestClickAction_WithCascade(t *testing.T) {
	got := clickAction("alpha", "specialMode", "'night'",
		"/ui/devices/alpha/timer", "{mode: __next}")
	want := "const __next = 'night'; $specialMode.alpha = __next; if ($specialMode.alpha !== 'off') { $power.alpha = true; } @post('/ui/devices/alpha/timer', {payload: {mode: __next}})"
	if got != want {
		t.Errorf("clickAction (with cascade):\n  got: %s\n want: %s", got, want)
	}
}

// TestClickAction_NoPayload pins the empty-payload form: postActionExpr
// emits @post('url') without a {payload: ...} when payload is "". This
// path is used by reset-faults / reset-filter style buttons that send
// no body (covered by other helpers, not clickAction in this plan, but
// the no-payload code path stays exercised for completeness).
func TestClickAction_NoPayload(t *testing.T) {
	got := clickAction("alpha", "heater", "!$heater.alpha",
		"/ui/devices/alpha/heater", "")
	want := "const __next = !$heater.alpha; $heater.alpha = __next; @post('/ui/devices/alpha/heater')"
	if got != want {
		t.Errorf("clickAction (no payload):\n  got: %s\n want: %s", got, want)
	}
}

// TestClickAction_UnknownSignalPanics is the canary: adding a click
// handler that writes an unregistered signal must crash the build
// (template render) immediately, not silently emit a half-formed
// expression.
func TestClickAction_UnknownSignalPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("clickAction should panic for unknown signal, but didn't")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "unknown signal") {
			t.Errorf("panic message should mention 'unknown signal', got: %v", r)
		}
	}()
	_ = clickAction("alpha", "notARealSignal", "true", "/x", "")
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./cmd/breezyd/ui/templates/ -run "TestCascades|TestClickAction|TestCascadeTable" -v
```

Expected: all ten tests pass.

- [ ] **Step 4: Run the full templates package test to confirm no regression**

```bash
go test ./cmd/breezyd/ui/templates/
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/breezyd/ui/templates/cascades.go cmd/breezyd/ui/templates/render_test.go
git commit -m "feat(ui): cascade table + clickAction helper

Foundation for centralized optimistic UI updates. No callers yet —
that lands per-handler in subsequent commits. Spec:
docs/superpowers/specs/2026-05-11-optimistic-ui-cascades-design.md"
```

---

## Task 1: Add Playwright regression test for the headline scenario

**Goal:** Lock in the user-facing failure mode before any migration — with night-mode active and `$specialMode='night'`, clicking a preset chip MUST flip the night chip's aria-pressed to false within 100ms (optimistic), not wait for the SSE roundtrip. This test fails today (presetChipExpr doesn't cascade) and passes after Task 2.

**Files:**
- Modify: `tests/ui/dashboard.spec.ts`

**Acceptance Criteria:**
- [ ] New Playwright test inside `test.describe("controls", ...)` named `"preset chip click optimistically de-lights active timer chip"`.
- [ ] Test sets `timer=night` via `presets.withTimer(DEVICE, "night")` and `speed=preset1` via `presets.asPresetSpeed(DEVICE, 1)`, waits for the night chip to be aria-pressed=true, then clicks preset 2.
- [ ] Within 100ms of the click (using `expect(...).toHaveAttribute(..., { timeout: 100 })`), the night chip must show aria-pressed=false. The 100ms bound is well below the daemon's poll interval (1s) and below typical UDP roundtrip (200-800ms) — it can ONLY pass if the cascade fires optimistically.
- [ ] Test currently FAILS (before Task 2 migration). Run the test and capture the failure to confirm it's exercising the right scenario.

**Verify:** `cd tests/ui && pnpm exec playwright test -g "optimistically de-lights" --reporter=line` → FAIL (timeout waiting for aria-pressed=false). After Task 2 it will PASS.

**Steps:**

- [ ] **Step 1: Add the test**

Append inside the existing `test.describe("controls", ...)` block in `tests/ui/dashboard.spec.ts`, right after the existing "preset chip: click opens editor only when already active" test (around line 230):

```typescript
  // Pins the optimistic-cascade contract: a click that the firmware will
  // honor by clearing the timer must de-light the timer chip on the
  // CLIENT immediately — well before the SSE roundtrip reports the new
  // state. Without the cascade, the night chip stays visually pressed
  // for 200-800ms (UDP roundtrip + daemon poll + push). 100ms is well
  // below the daemon's 1s poll interval, so the only way this assertion
  // passes is if $specialMode flipped client-side, not server-pushed.
  test("preset chip click optimistically de-lights active timer chip", async ({ page }) => {
    await reset(DEVICE);
    await presets.asPresetSpeed(DEVICE, 1);
    await presets.withTimer(DEVICE, "night");
    const card = await loadCard(page);

    // Wait for the night chip to show as pressed (poll has caught up to
    // the seeded timer state).
    const nightChip = card.getByRole("button", { name: "night" });
    await expect(nightChip).toHaveAttribute("aria-pressed", "true", {
      timeout: POLL_PUSH_TIMEOUT,
    });

    // Click preset 2 — the firmware will clear the timer, but our test
    // asserts the CLIENT reflects that clearing within 100ms, not after
    // the roundtrip. Preset-2 chip label is "48/49" (snapshot seed).
    await card.getByRole("button", { name: "48/49" }).click();
    await expect(nightChip).toHaveAttribute("aria-pressed", "false", {
      timeout: 100,
    });
  });
```

- [ ] **Step 2: Run the test and confirm it fails**

```bash
cd tests/ui && pnpm exec playwright test -g "optimistically de-lights" --reporter=line
```

Expected: 1 failed (timeout: night chip stays aria-pressed=true for the full 100ms). Capture the failure output — that's the bug we're about to fix.

- [ ] **Step 3: Commit the failing test**

```bash
git add tests/ui/dashboard.spec.ts
git commit -m "test(ui): pin optimistic timer-chip de-light on preset click

Currently failing (night chip stays pressed for the SSE roundtrip).
Task 2 of the cascades plan makes this pass."
```

This test stays red across Task 1's commit so reviewers can see exactly what behavior the plan delivers. It goes green in Task 2.

---

## Task 2: Migrate preset chip handler to clickAction

**Goal:** Rewrite `presetBtn`'s click handler to use `clickAction("speedMode", ...)`, delete `presetChipExpr` and `closeEditorThen`, update the unit test, regenerate goldens. The headline Playwright test from Task 1 must turn green.

**Files:**
- Modify: `cmd/breezyd/ui/templates/controls_block.templ` (`presetBtn` template, `presetChipExpr` and `closeEditorThen` helpers around lines 78-86 and 449-464)
- Modify: `cmd/breezyd/ui/templates/render_test.go` (`TestPresetChipExpr` around lines 864-889)
- Regenerate: `cmd/breezyd/ui/templates/controls_block_templ.go`
- Regenerate: `cmd/breezyd/ui/templates/testdata/golden_healthy.html`
- Regenerate: `cmd/breezyd/ui/templates/testdata/golden_stale.html`

**Acceptance Criteria:**
- [ ] `presetChipExpr` is deleted from `controls_block.templ`.
- [ ] `presetBtn` template inlines: editor-toggle (using `wasActive` flag captured before clickAction) + `clickAction(v.Name, "speedMode", "'preset<n>'", ...)`.
- [ ] `TestPresetChipExpr` is renamed `TestPresetClickExpr` (or merged into the click-handler table test) and pins the new structure.
- [ ] `closeEditorThen` is NOT deleted in this task — it's still used by manualBtn and modeBtn (deleted in Task 3 after their migration).
- [ ] `just check` passes (vet + tests + templ-drift).
- [ ] `just test-ui` passes — including the Task 1 regression test, now green.

**Verify:** `just check && cd tests/ui && pnpm exec playwright test -g "optimistically de-lights|preset chip" --reporter=line` → all green.

**Steps:**

- [ ] **Step 1: Edit presetBtn template**

In `cmd/breezyd/ui/templates/controls_block.templ`, replace the `presetBtn` template (lines 66-86) with:

```templ
// presetBtn renders one of the three SPEED preset chips. Click handler
// uses clickAction() so $speedMode flips optimistically and the
// speedMode cascade clears $specialMode (firmware clears timer on any
// speed_mode write). Editor-toggle logic runs first, using a wasActive
// flag captured BEFORE clickAction's primary write — otherwise the
// $speedMode optimistic update would make every preset click look
// "active" by the time the editor check runs.
//
// aria-pressed reads $speedMode directly: after clickAction sets it
// optimistically, the chip lights up instantly. The (formerly-needed)
// $specialMode === 'off' && ... gate is no longer required because the
// cascade keeps $specialMode coherent.
templ presetBtn(v ui.DeviceView, n int) {
	<button
		type="button"
		data-on:click={ attentionIfOff(v.Name, presetClickExpr(v.Name, n)) }
		if v.Stale { disabled }
		data-attr:aria-pressed={ fmt.Sprintf("$specialMode.%s === 'off' && $speedMode.%s === 'preset%d' ? 'true' : 'false'", v.Name, v.Name, n) }
		data-text={ presetChipDataText(v.Name, n) }
	>{ presetLabel(v, n) }</button>
}

// presetClickExpr is the data-on:click body for preset chips. Computes
// wasActive (so the editor only opens when re-clicking the already-
// active preset) using the pre-click signal values, sets $editor, then
// delegates the primary signal write + cascade + POST to clickAction.
func presetClickExpr(name string, n int) string {
	return fmt.Sprintf(
		"const wasActive = ($specialMode.%s === 'off' && $speedMode.%s === 'preset%d'); $editor.%s = wasActive ? ($editor.%s === %d ? 0 : %d) : 0; %s",
		name, name, n, name, name, n, n,
		clickAction(name,
			"speedMode",
			fmt.Sprintf("'preset%d'", n),
			fmt.Sprintf("/ui/devices/%s/speed", name),
			fmt.Sprintf("{preset: %d}", n)),
	)
}
```

Then delete the old `presetChipExpr` function (lines ~449-465 in the unmodified file — find by `func presetChipExpr`).

- [ ] **Step 2: Update render_test.go**

Replace `TestPresetChipExpr` (around line 864) with:

```go
// TestPresetClickExpr pins the active-only-expand behavior of the preset
// chip click handler, now built on top of clickAction. The editor toggles
// only when the clicked chip is the currently-active preset; clicking a
// non-active chip selects it (clickAction writes $speedMode optimistically
// + the speedMode cascade clears $specialMode) without expanding. The
// wasActive snapshot is captured BEFORE clickAction's primary write, so
// the editor-toggle check uses pre-click signal state.
//
// Strict equality (===) matters in the wasActive check because the JS
// engine handles stringified-vs-numeric edge cases — `==` would coerce
// and match unexpectedly when the seed type drifted (see G-web-8).
func TestPresetClickExpr(t *testing.T) {
	got := presetClickExpr("alpha", 2)
	want := "const wasActive = ($specialMode.alpha === 'off' && $speedMode.alpha === 'preset2'); $editor.alpha = wasActive ? ($editor.alpha === 2 ? 0 : 2) : 0; const __next = 'preset2'; $speedMode.alpha = __next; $specialMode.alpha = 'off'; $specialModeRemainingSeconds.alpha = 0; @post('/ui/devices/alpha/speed', {payload: {preset: 2}})"
	if got != want {
		t.Errorf("presetClickExpr(alpha, 2):\n  got: %s\n want: %s", got, want)
	}
	// Negative: must use strict equality, not loose.
	if strings.Contains(got, "$editor.alpha == 2") {
		t.Errorf("presetClickExpr must use strict equality (===), got: %s", got)
	}
	// Negative: must scope $editor per-device, not unscoped.
	if strings.Contains(got, "$editor =") || strings.Contains(got, "$editor ===") {
		t.Errorf("presetClickExpr must scope $editor per-device; got: %s", got)
	}
}
```

- [ ] **Step 3: Regenerate templ + goldens**

```bash
just generate
go test ./cmd/breezyd/ui/templates/ -run TestDeviceCardGolden -update
```

- [ ] **Step 4: Inspect the golden diff to confirm it's only the preset chip's click handler that changed**

```bash
git diff cmd/breezyd/ui/templates/testdata/golden_healthy.html | grep -oE 'data-on:click="[^"]*preset[^"]*"' | head -6
```

Expected: three matches (one per preset chip), each containing `$speedMode.living-room = 'preset<n>'; $specialMode.living-room = 'off'; $specialModeRemainingSeconds.living-room = 0; @post(...)`.

- [ ] **Step 5: Run the full check + Playwright suite**

```bash
just check
just test-ui
```

Expected: green. The Task 1 regression test now passes.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/ui/templates/controls_block.templ cmd/breezyd/ui/templates/controls_block_templ.go cmd/breezyd/ui/templates/render_test.go cmd/breezyd/ui/templates/testdata/
git commit -m "feat(ui): migrate preset chip to clickAction cascade

Preset clicks now optimistically clear \$specialMode via the speedMode
cascade. Closes the SSE-roundtrip window where the night/turbo chip
stayed visually pressed after a preset selection."
```

---

## Task 3: Migrate manual + mode buttons to clickAction

**Goal:** Rewrite `manualBtn` and `modeBtn` to use `clickAction()`. Delete `closeEditorThen` (now only the editor `$editor.X = 0` reset is needed, which is one small line). Regenerate.

**Files:**
- Modify: `cmd/breezyd/ui/templates/controls_block.templ` (`manualBtn` lines 97-104, `modeBtn` lines 109-116, `closeEditorThen` lines 461-464)
- Regenerate: `controls_block_templ.go`, goldens

**Acceptance Criteria:**
- [ ] `manualBtn` click handler closes the preset editor (`$editor.X = 0`) then calls `clickAction(name, "speedMode", "'manual'", ...)`. `closeEditorThen` deleted.
- [ ] `modeBtn` click handler closes the preset editor then calls `clickAction(name, "airflowMode", "'<value>'", ...)`.
- [ ] No new unit tests required (clickAction itself is tested in Task 0; per-handler tests would just re-pin the same strings).
- [ ] `just check` passes; Playwright tests (`manual slider drag posts dragged value`, etc.) stay green.

**Verify:** `just check && just test-ui` → green.

**Steps:**

- [ ] **Step 1: Edit manualBtn**

Replace `manualBtn` (lines 88-104) in `controls_block.templ`:

```templ
// manualBtn renders the SPEED-row "manual" chip. Click handler closes
// the preset editor and uses clickAction to optimistically flip
// $speedMode + cascade-clear $specialMode. aria-pressed reads the
// signals directly (the speedMode cascade keeps $specialMode coherent).
templ manualBtn(v ui.DeviceView) {
	<button
		type="button"
		data-on:click={ attentionIfOff(v.Name, manualClickExpr(v)) }
		if v.Stale { disabled }
		data-attr:aria-pressed={ fmt.Sprintf("$specialMode.%s === 'off' && $speedMode.%s === 'manual' ? 'true' : 'false'", v.Name, v.Name) }
	>manual</button>
}

func manualClickExpr(v ui.DeviceView) string {
	return fmt.Sprintf(
		"$editor.%s = 0; %s",
		v.Name,
		clickAction(v.Name,
			"speedMode",
			"'manual'",
			fmt.Sprintf("/ui/devices/%s/speed", v.Name),
			fmt.Sprintf("{manual: %d}", manualBtnPct(v))),
	)
}
```

- [ ] **Step 2: Edit modeBtn**

Replace `modeBtn` (lines 106-116):

```templ
// modeBtn renders one of the four MODE chips. Click handler closes the
// preset editor and uses clickAction to optimistically flip
// $airflowMode (no cascade — firmware leaves the timer alone on
// airflow_mode writes per the 2026-05-11 probe).
templ modeBtn(v ui.DeviceView, label, value string) {
	<button
		type="button"
		data-on:click={ attentionIfOff(v.Name, modeClickExpr(v.Name, value)) }
		if v.Stale { disabled }
		data-attr:aria-pressed={ fmt.Sprintf("$airflowMode.%s === '%s' ? 'true' : 'false'", v.Name, value) }
	>{ label }</button>
}

func modeClickExpr(name, value string) string {
	return fmt.Sprintf(
		"$editor.%s = 0; %s",
		name,
		clickAction(name,
			"airflowMode",
			fmt.Sprintf("'%s'", value),
			fmt.Sprintf("/ui/devices/%s/mode", name),
			fmt.Sprintf("{mode: '%s'}", value)),
	)
}
```

- [ ] **Step 3: Delete closeEditorThen**

Find `func closeEditorThen` (lines ~457-464) in `controls_block.templ` and delete it entirely, along with its preceding doc comment.

- [ ] **Step 4: Regenerate + verify**

```bash
just generate
go test ./cmd/breezyd/ui/templates/ -run TestDeviceCardGolden -update
just check
just test-ui
```

Expected: all green.

- [ ] **Step 5: Inspect golden diff for correctness**

```bash
git diff cmd/breezyd/ui/templates/testdata/golden_healthy.html | grep -oE 'data-on:click="[^"]*(speedMode\.[a-z-]+ = .manual.|airflowMode\.[a-z-]+ = )' | sort -u
```

Expected: one match for manual chip, four matches for mode chips (one per mode).

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/ui/templates/controls_block.templ cmd/breezyd/ui/templates/controls_block_templ.go cmd/breezyd/ui/templates/testdata/
git commit -m "feat(ui): migrate manual + mode buttons to clickAction"
```

---

## Task 4: Migrate timer button to clickAction

**Goal:** Rewrite `timerBtn` / `timerClickExpr` to use `clickAction("specialMode", ...)`. The cascade handles `$power=true` on activation. The remaining-seconds optimistic seed stays inline (it's not a cascade — different responsibility, lives in the click handler).

**Files:**
- Modify: `cmd/breezyd/ui/templates/controls_block.templ` (`timerBtn` lines 124-131, `timerClickExpr` lines 147-162)
- Regenerate: `controls_block_templ.go`, goldens

**Acceptance Criteria:**
- [ ] `timerClickExpr` no longer hand-writes `$specialMode` / `$power` / `$specialModeRemainingSeconds` assignments; it composes the remaining-seconds seed + `clickAction()`.
- [ ] Toggle behavior (click active chip → 'off') preserved: jsValue is a ternary read of `$specialMode`.
- [ ] `$specialModeRemainingSeconds` seeding still happens inline before `clickAction` (cascade only writes `$power=true`; the countdown seed is separate).
- [ ] `just check` + `just test-ui` green.
- [ ] If `TestTimerClickExpr` exists (grep first), update the want string. If not, add a fresh `TestTimerClickExpr` pinning the new structure.

**Verify:** `just check && just test-ui` → green.

**Steps:**

- [ ] **Step 1: Check for an existing test**

```bash
grep -n "TestTimerClickExpr\|timerClickExpr" cmd/breezyd/ui/templates/render_test.go
```

If a test exists, update its want string (Step 4). If not, add one in Step 4.

- [ ] **Step 2: Edit timerBtn + timerClickExpr**

Replace `timerBtn` and `timerClickExpr` (lines 118-162) in `controls_block.templ`:

```templ
// timerBtn renders one of the two SPECIAL-mode chips (night / turbo).
// Click handler seeds the local $specialModeRemainingSeconds countdown
// then delegates to clickAction, which optimistically flips
// $specialMode + (via the specialMode cascade) writes $power=true
// when activating. aria-pressed reads the signal directly so toggle-off
// works correctly even with controls-block patches filtered.
templ timerBtn(v ui.DeviceView, label, value string) {
	<button
		type="button"
		data-on:click={ attentionIfOff(v.Name, timerClickExpr(v.Name, value)) }
		if v.Stale { disabled }
		data-attr:aria-pressed={ fmt.Sprintf("$specialMode.%s === '%s' ? 'true' : 'false'", v.Name, value) }
	>{ label }</button>
}

// timerClickExpr is the data-on:click body for night/turbo chips.
// Computes the toggle: if the chip is already active, the new
// $specialMode is 'off' (which deactivates); otherwise it's the chip's
// own value. Before clickAction runs, seeds $specialModeRemainingSeconds
// to the per-mode duration signal so the countdown line appears
// instantly — this is NOT a cascade (cascades fire on the primary
// signal generically; the duration seed depends on which mode is being
// activated). When deactivating (new value 'off'), zero the countdown
// in the same seed expression.
//
// POST payload references __next (defined by clickAction from the
// toggle ternary in jsValue) so the wire value matches what was
// written locally — without this the payload's re-evaluation of the
// ternary would read the just-mutated $specialMode and send the
// inverse of what the user intended.
func timerClickExpr(name, value string) string {
	newSpecial := fmt.Sprintf("$specialMode.%s === '%s' ? 'off' : '%s'", name, value, value)
	seedRemaining := fmt.Sprintf(
		"$specialModeRemainingSeconds.%s = ($specialMode.%s === '%s') ? 0 : $%sDurationSeconds.%s;",
		name, name, value, value, name)
	return fmt.Sprintf(
		"%s %s",
		seedRemaining,
		clickAction(name,
			"specialMode",
			newSpecial,
			fmt.Sprintf("/ui/devices/%s/timer", name),
			"{mode: __next}"),
	)
}
```

Note: the new expression preserves three behaviors of the old `timerClickExpr`:
- Toggle: clicking active chip posts 'off', else posts the chip's value (`$specialMode === '<value>' ? 'off' : '<value>'`).
- Countdown seed: sets `$specialModeRemainingSeconds` to the duration when activating, 0 when toggling off.
- Power=on on activation: now delegated to the `specialMode` cascade (`if (newValue !== 'off') $power=true`).

The duration signal name is `$<value>DurationSeconds.<name>` (e.g. `$nightDurationSeconds.alpha`), which is seeded by `initialCardSignals` in `device_card.templ` (verify by grepping `DurationSeconds` there).

- [ ] **Step 3: Verify the duration signal naming**

```bash
grep -n "DurationSeconds" cmd/breezyd/ui/templates/device_card.templ
```

Expected: lines seeding `nightDurationSeconds` and `turboDurationSeconds`. If the naming differs from `<value>DurationSeconds`, adjust `seedRemaining` accordingly.

- [ ] **Step 4: Add or update TestTimerClickExpr**

Append to or update `render_test.go`:

```go
// TestTimerClickExpr pins the structure of the timer-chip click
// handler after the clickAction migration: countdown seed, then
// clickAction (__next = toggle ternary, primary $specialMode write,
// power-on cascade reading $specialMode, POST with __next). The
// cascade reads the just-mutated $specialMode signal — so the
// power-on guard fires only when the click is activating, not
// toggling off. POST uses __next so wire value matches the locally-
// written value (the toggle ternary evaluates once).
func TestTimerClickExpr(t *testing.T) {
	got := timerClickExpr("alpha", "night")
	want := "$specialModeRemainingSeconds.alpha = ($specialMode.alpha === 'night') ? 0 : $nightDurationSeconds.alpha; const __next = $specialMode.alpha === 'night' ? 'off' : 'night'; $specialMode.alpha = __next; if ($specialMode.alpha !== 'off') { $power.alpha = true; } @post('/ui/devices/alpha/timer', {payload: {mode: __next}})"
	if got != want {
		t.Errorf("timerClickExpr(alpha, night):\n  got: %s\n want: %s", got, want)
	}
}
```

- [ ] **Step 5: Regenerate + verify**

```bash
just generate
go test ./cmd/breezyd/ui/templates/ -run TestDeviceCardGolden -update
just check
just test-ui
```

Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/ui/templates/controls_block.templ cmd/breezyd/ui/templates/controls_block_templ.go cmd/breezyd/ui/templates/render_test.go cmd/breezyd/ui/templates/testdata/
git commit -m "feat(ui): migrate timer button to clickAction"
```

---

## Task 5: Migrate heater button to clickAction

**Goal:** Rewrite `heaterClickExpr` to use `clickAction("heater", ...)`. Simplest migration — no cascade, no toggle ternary on a different signal.

**Files:**
- Modify: `cmd/breezyd/ui/templates/controls_block.templ` (`heaterClickExpr` lines ~186-195; the button itself is inline in `ControlsBlock` around line 56)
- Regenerate: `controls_block_templ.go`, goldens

**Acceptance Criteria:**
- [ ] `heaterClickExpr` body is just `return clickAction(name, "heater", ...)`.
- [ ] No new test needed (clickAction's `TestClickAction_NoCascade` already covers the structure with heater).
- [ ] `just check` + `just test-ui` green.

**Verify:** `just check && just test-ui` → green.

**Steps:**

- [ ] **Step 1: Edit heaterClickExpr**

Replace `heaterClickExpr` (lines ~186-195) in `controls_block.templ`:

```templ
// heaterClickExpr toggles $heater optimistically and POSTs the new
// value. No cascade — heater is independent of every other signal.
// Reads $heater (not v.Heater) so a rapid double-click can't fire two
// same-direction toggles before the SSE patch lands. Payload uses
// __next (defined by clickAction) so the wire value matches what was
// just written locally.
func heaterClickExpr(name string) string {
	return clickAction(name,
		"heater",
		fmt.Sprintf("!$heater.%s", name),
		fmt.Sprintf("/ui/devices/%s/heater", name),
		"{on: __next}")
}
```

- [ ] **Step 2: Regenerate + verify**

```bash
just generate
go test ./cmd/breezyd/ui/templates/ -run TestDeviceCardGolden -update
just check
just test-ui
```

Expected: green.

- [ ] **Step 3: Commit**

```bash
git add cmd/breezyd/ui/templates/controls_block.templ cmd/breezyd/ui/templates/controls_block_templ.go cmd/breezyd/ui/templates/testdata/
git commit -m "feat(ui): migrate heater button to clickAction"
```

---

## Task 6: Migrate power button to clickAction

**Goal:** Rewrite `powerButtonExpr` to use `clickAction("power", ...)`. The power cascade replaces the existing hand-rolled `if ($power) { $specialMode='off'; ... }` block.

**Files:**
- Modify: `cmd/breezyd/ui/templates/device_card.templ` (`powerButtonExpr` lines 151-173)
- Regenerate: `device_card_templ.go`, goldens

**Acceptance Criteria:**
- [ ] `powerButtonExpr` body collapses to one `clickAction(...)` call. The hand-rolled cascade block (current lines 169-172) is gone.
- [ ] `just check` + `just test-ui` green.
- [ ] If a `TestPowerButtonExpr` exists, update it (grep first); otherwise add one.

**Verify:** `just check && just test-ui` → green.

**Steps:**

- [ ] **Step 1: Check for existing test**

```bash
grep -n "TestPowerButtonExpr\|powerButtonExpr" cmd/breezyd/ui/templates/render_test.go
```

- [ ] **Step 2: Edit powerButtonExpr**

Replace `powerButtonExpr` (lines 151-173) in `device_card.templ`:

```templ
// powerButtonExpr produces the data-on:click expression for the power
// toggle. Uses clickAction so the primary $power write fires optimistically
// AND the power cascade clears $specialMode + $specialModeRemainingSeconds
// when transitioning to off (the daemon's /power handler does the same
// timer-clear server-side; the cascade mirrors it on the client).
//
// Reads the live $power signal (not v.Power at render time) so a rapid
// double-click can't fire two same-direction toggles before the SSE
// patch lands. Payload uses __next so the wire value matches the
// just-written local value.
func powerButtonExpr(v ui.DeviceView) string {
	return clickAction(v.Name,
		"power",
		fmt.Sprintf("!$power.%s", v.Name),
		fmt.Sprintf("/ui/devices/%s/power", v.Name),
		"{on: __next}")
}
```

- [ ] **Step 3: Add or update TestPowerButtonExpr**

If no existing test, append to `render_test.go`:

```go
// TestPowerButtonExpr pins the structure after the clickAction
// migration: __next const, primary $power toggle, power cascade (reads
// $power, clears timer on transition to off), POST with __next.
func TestPowerButtonExpr(t *testing.T) {
	v := ui.DeviceView{Name: "alpha"}
	got := powerButtonExpr(v)
	want := "const __next = !$power.alpha; $power.alpha = __next; if (!$power.alpha) { $specialMode.alpha = 'off'; $specialModeRemainingSeconds.alpha = 0; } @post('/ui/devices/alpha/power', {payload: {on: __next}})"
	if got != want {
		t.Errorf("powerButtonExpr:\n  got: %s\n want: %s", got, want)
	}
}
```

- [ ] **Step 4: Regenerate + verify**

```bash
just generate
go test ./cmd/breezyd/ui/templates/ -run TestDeviceCardGolden -update
just check
just test-ui
```

Expected: green.

- [ ] **Step 5: Commit**

```bash
git add cmd/breezyd/ui/templates/device_card.templ cmd/breezyd/ui/templates/device_card_templ.go cmd/breezyd/ui/templates/render_test.go cmd/breezyd/ui/templates/testdata/
git commit -m "feat(ui): migrate power button to clickAction"
```

---

## Task 7: Add effPower + switch power button binding + Playwright assertion

**Goal:** Add the Layer-A `effPower` JS helper to `layout.templ`. Switch the power button's `data-attr:aria-pressed` from `$power` to `effPower(...)`. Lock in the behavior with a Playwright test that forces the external-actor scenario (timer active + `$power=false`).

**Files:**
- Modify: `cmd/breezyd/ui/templates/layout.templ` (inside the existing inline `<script>` block, near `fmtRemaining`)
- Modify: `cmd/breezyd/ui/templates/device_card.templ` (power button `data-attr:aria-pressed` binding, around line 79)
- Modify: `tests/ui/dashboard.spec.ts` (new test)
- Regenerate: `device_card_templ.go`, `layout_templ.go`, goldens

**Acceptance Criteria:**
- [ ] `effPower(power, special)` defined in `layout.templ`'s inline script. Returns truthy when special is non-off OR power is true.
- [ ] Power button's `data-attr:aria-pressed` reads `effPower($power.X, $specialMode.X) ? 'true' : 'false'`.
- [ ] New Playwright test sets `power=off` then `timer=night` directly via the test admin API (`setDeviceState` with both params), then asserts the power button shows aria-pressed=true within `POLL_PUSH_TIMEOUT`. This is the externally-induced-state scenario the helper exists for.
- [ ] `just check` + `just test-ui` green; `TestLayout` golden checks for `effPower` string presence.

**Verify:** `just check && just test-ui` → green.

**Steps:**

- [ ] **Step 1: Add effPower to layout.templ**

Find the existing inline `<script>` block in `cmd/breezyd/ui/templates/layout.templ` (look for `fmtRemaining`) and add the helper next to it:

```javascript
// effPower bridges externally-induced state where the firmware
// runs the fans during timer mode without setting power=1. The
// /timer HTTP handler in breezyd writes power=on to keep the
// flag coherent for user-driven clicks, but an external actor
// (panel button, IR remote) can start a timer with power=0. The
// poller honestly reflects that; effPower makes the dashboard
// show "on" anyway.
function effPower(power, special) {
    return special !== 'off' || power;
}
```

- [ ] **Step 2: Switch power button binding**

In `cmd/breezyd/ui/templates/device_card.templ`, line 79 currently reads:

```templ
data-attr:aria-pressed={ fmt.Sprintf("$power.%s ? 'true' : 'false'", v.Name) }
```

Replace with:

```templ
data-attr:aria-pressed={ fmt.Sprintf("effPower($power.%s, $specialMode.%s) ? 'true' : 'false'", v.Name, v.Name) }
```

- [ ] **Step 3: Update TestLayout to assert effPower is present**

In `cmd/breezyd/ui/templates/render_test.go`, find `TestLayout` (line ~32) and add `effPower` to the `wantContains` slice:

```go
wantContains := []string{
    // ... existing entries ...
    `function effPower`, // bridges timer-active state where $power stays 0
}
```

- [ ] **Step 4: Add the Playwright assertion**

Append to `tests/ui/dashboard.spec.ts` inside `test.describe("controls", ...)`:

```typescript
  // Pins effPower: when a timer is active but $power reads false
  // (the firmware doesn't auto-power-on when timer activates; only
  // breezyd's /timer handler does, so an external actor can leave
  // power=0 + timer=night), the dashboard must still show the power
  // button as pressed. setDeviceState writes both params directly,
  // bypassing the daemon's handler-side power=on coupling.
  test("effPower: power button reflects timer state when power flag is desynced", async ({ page }) => {
    await reset(DEVICE);
    await presets.asPowerOff(DEVICE);
    await presets.withTimer(DEVICE, "night");
    const card = await loadCard(page);

    const powerBtn = card.locator(".power-toggle");
    await expect(powerBtn).toHaveAttribute("aria-pressed", "true", {
      timeout: POLL_PUSH_TIMEOUT,
    });
  });
```

- [ ] **Step 5: Regenerate + verify**

```bash
just generate
go test ./cmd/breezyd/ui/templates/ -run TestDeviceCardGolden -update
just check
just test-ui
```

Expected: green. The new `effPower` assertion exercises Layer A; all prior cascade tests stay green.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/ui/templates/layout.templ cmd/breezyd/ui/templates/layout_templ.go cmd/breezyd/ui/templates/device_card.templ cmd/breezyd/ui/templates/device_card_templ.go cmd/breezyd/ui/templates/render_test.go cmd/breezyd/ui/templates/testdata/ tests/ui/dashboard.spec.ts
git commit -m "feat(ui): add effPower derivation for desynced power flag

External actors (panel button, IR remote) can leave the device in
timer-active + power-off state. Dashboard's power button now reads
effPower(\$power, \$specialMode) so it shows pressed regardless."
```

---

## Final verification (run after Task 7)

```bash
just ci
```

Expected: all green. Specifically:
- All `TestCascades_*`, `TestClickAction_*`, `TestCascadeTable_AllWritableSignalsCovered`, plus updated `TestPresetClickExpr`, `TestTimerClickExpr`, `TestPowerButtonExpr` pass.
- Golden files match.
- Playwright: the two new tests pass (`optimistically de-lights active timer chip`, `effPower: ... when power flag is desynced`); all existing tests stay green.
- `just test-templ-drift` clean.

Then push the branch and open a PR per the repo's standard flow.
