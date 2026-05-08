# Schedule editor pct-sync fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the schedule editor's pct `<input>` reactively follow the action `<select>` so toggling action ↔ off immediately clears/locks/restores pct without a form round-trip; tighten the handler so off rows persist `Pct=0` instead of a misleading 10.

**Architecture:** Inline `data-on:change` on the action `<select>` (datastar attribute) plus a per-row `data-orig-pct` attribute as the restore target. Two new tiny helpers in the templ file (one for the JS expression, one for the orig-pct value). One-line handler tightening. One Playwright test exercising the round-trip.

**Tech Stack:** templ (server-rendered components), datastar v1.0 (reactive attributes over SSE), Go HTTP handlers, Playwright (real-daemon e2e).

**Spec:** `docs/superpowers/specs/2026-05-08-schedule-editor-pct-sync-design.md`

---

## File Structure

| File | Change |
|------|--------|
| `cmd/breezyd/ui/templates/schedule_block.templ` | Add 2 helpers; wire `data-on:change` on action select; add `data-orig-pct` on pct input. |
| `cmd/breezyd/ui/templates/schedule_block_templ.go` | Regenerated from `.templ` by `templ generate`. Do not hand-edit; run `just generate`. |
| `cmd/breezyd/handlers_ui_write.go` | One-line: change `pct = 10` to `pct = 0` for off-row pct fallback. |
| `tests/ui/dashboard.spec.ts` | Add new `test.describe("schedule editor")` block with one test. |

No new files.

---

### Task 1: Wire reactive pct sync in the schedule editor (templ + Playwright test)

**Goal:** Toggling the action `<select>` between `off` and any other value immediately clears or restores the row's pct `<input>` (value, `readonly` attribute, `pct-disabled` class). Verified by a new Playwright test.

**Files:**
- Modify: `cmd/breezyd/ui/templates/schedule_block.templ` (helpers + `ScheduleEditRow` lines 124–162)
- Regenerate: `cmd/breezyd/ui/templates/schedule_block_templ.go` (via `just generate`; do not hand-edit)
- Modify: `tests/ui/dashboard.spec.ts` (append a new `test.describe`)

**Acceptance Criteria:**
- [ ] Two new exported-by-package helpers in `schedule_block.templ`: `schedulePctOrigValue(e) string` and `scheduleActionChangeExpr() string`.
- [ ] `<input name="pct">` gains `data-orig-pct` whose value is `e.Pct` when in [10..100], else `"50"`.
- [ ] `<select name="action">` gains `data-on:change={ scheduleActionChangeExpr() }`.
- [ ] On change to `off`: pct value cleared, `readonly` attribute set, `pct-disabled` class added.
- [ ] On change away from `off`: pct value set to `dataset.origPct`, `readonly` removed, `pct-disabled` removed.
- [ ] `just generate` produces a clean `schedule_block_templ.go` (no drift after).
- [ ] A new Playwright test under `test.describe("schedule editor")` passes.

**Verify:**
- `just test-templ-drift` → no drift
- `just lint` → clean (gofmt + vet)
- `just test-ui` → all Playwright tests pass, including the new one

**Steps:**

- [ ] **Step 1: Write the failing Playwright test**

Append to `tests/ui/dashboard.spec.ts` (after the existing `reconnect` describe at line ~230):

```typescript
test.describe("schedule editor", () => {
  test("action select toggles pct readonly+value without round-trip", async ({
    page,
  }) => {
    await reset(DEVICE);
    // Seed schedule with one regen/60 entry, schedule enabled.
    const { withSchedule } = await import("./fixtures.js");
    await withSchedule(DEVICE, {
      enabled: true,
      entries: [{ at: "08:00", action: "regeneration", pct: 60 }],
    });

    const card = await loadCard(page);
    const schedule = card.locator("details.schedule");
    if (!(await schedule.evaluate((el) => (el as HTMLDetailsElement).open))) {
      await schedule.locator("summary").click();
    }
    await schedule.locator('button:has-text("edit schedule")').click();

    const row = schedule.locator("tbody.schedule-edit-tbody tr").first();
    const action = row.locator('select[name="action"]');
    const pct = row.locator('input[name="pct"]');

    // Pre-state: regen / 60, editable.
    await expect(action).toHaveValue("regeneration");
    await expect(pct).toHaveValue("60");
    await expect(pct).not.toHaveAttribute("readonly", /.*/);
    await expect(pct).not.toHaveClass(/pct-disabled/);

    // Toggle to off — pct clears, readonly, pct-disabled.
    await action.selectOption("off");
    await expect(pct).toHaveValue("");
    await expect(pct).toHaveAttribute("readonly", /.*/);
    await expect(pct).toHaveClass(/pct-disabled/);

    // Toggle to ventilation — pct restores 60, editable, no pct-disabled.
    await action.selectOption("ventilation");
    await expect(pct).toHaveValue("60");
    await expect(pct).not.toHaveAttribute("readonly", /.*/);
    await expect(pct).not.toHaveClass(/pct-disabled/);
  });
});
```

(`withSchedule` is dynamically imported because the existing top-level import at line 19 only pulls `reset, simulateAuthFailure, simulateUDPTimeout, presets`. Adding it there is fine too — pick one.)

- [ ] **Step 2: Run the test to verify it fails**

```bash
just test-ui -- --grep "schedule editor"
```

Expected: the assertion `await expect(pct).toHaveValue("")` after `selectOption("off")` fails because pct still shows `60` (no datastar binding on the select, the bug we're fixing).

- [ ] **Step 3: Add the two helpers to `cmd/breezyd/ui/templates/schedule_block.templ`**

Append next to the existing `schedulePctValue` and `scheduleActionLabel` helpers at the bottom of the file:

```go
// schedulePctOrigValue is the value to restore into the pct <input> when
// the user toggles action away from "off". Pct's valid range is [10..100],
// so 0 (or anything out of range) is treated as the "no value" sentinel
// and falls back to a sensible default of 50.
func schedulePctOrigValue(e ui.ScheduleEntryView) string {
	if e.Pct >= 10 && e.Pct <= 100 {
		return fmt.Sprintf("%d", e.Pct)
	}
	return "50"
}

// scheduleActionChangeExpr is the inline data-on:change expression for the
// action <select> in the schedule editor. It locates the row's pct <input>
// and synchronises value/readonly/class with the new action value:
// off ⇒ clear + lock; non-off ⇒ restore data-orig-pct + unlock.
func scheduleActionChangeExpr() string {
	return `const pct = evt.target.closest('tr').querySelector('input[name=pct]'); if (evt.target.value === 'off') { pct.value = ''; pct.setAttribute('readonly', ''); pct.classList.add('pct-disabled'); } else { pct.value = pct.dataset.origPct; pct.removeAttribute('readonly'); pct.classList.remove('pct-disabled'); }`
}
```

- [ ] **Step 4: Wire `data-on:change` into the action `<select>` in `ScheduleEditRow`**

In `cmd/breezyd/ui/templates/schedule_block.templ`, change lines 135–141:

```templ
				<select name="action">
					@scheduleActionOption("ventilation", "auto", e.Action)
					@scheduleActionOption("regeneration", "regen", e.Action)
					@scheduleActionOption("supply", "supply", e.Action)
					@scheduleActionOption("extract", "exhaust", e.Action)
					@scheduleActionOption("off", "off", e.Action)
				</select>
```

to:

```templ
				<select name="action" data-on:change={ scheduleActionChangeExpr() }>
					@scheduleActionOption("ventilation", "auto", e.Action)
					@scheduleActionOption("regeneration", "regen", e.Action)
					@scheduleActionOption("supply", "supply", e.Action)
					@scheduleActionOption("extract", "exhaust", e.Action)
					@scheduleActionOption("off", "off", e.Action)
				</select>
```

- [ ] **Step 5: Add `data-orig-pct` to the pct `<input>` in `ScheduleEditRow`**

In `cmd/breezyd/ui/templates/schedule_block.templ`, change lines 144–152:

```templ
			<input
				type="number"
				name="pct"
				min="10"
				max="100"
				value={ schedulePctValue(e) }
				class={ templ.KV("pct-disabled", e.Action == "off") }
				if e.Action == "off" { readonly }
			/>
```

to:

```templ
			<input
				type="number"
				name="pct"
				min="10"
				max="100"
				value={ schedulePctValue(e) }
				data-orig-pct={ schedulePctOrigValue(e) }
				class={ templ.KV("pct-disabled", e.Action == "off") }
				if e.Action == "off" { readonly }
			/>
```

- [ ] **Step 6: Regenerate templ output**

```bash
just generate
```

Expected: `cmd/breezyd/ui/templates/schedule_block_templ.go` updated; no other generated files change.

- [ ] **Step 7: Confirm no templ drift**

```bash
just test-templ-drift
```

Expected: no diff reported.

- [ ] **Step 8: Re-run the Playwright test, expect pass**

```bash
just test-ui -- --grep "schedule editor"
```

Expected: PASS.

- [ ] **Step 9: Run lint and full UI suite**

```bash
just lint && just test-ui
```

Expected: clean lint output; all Playwright tests green.

- [ ] **Step 10: Commit**

```bash
git add cmd/breezyd/ui/templates/schedule_block.templ \
  cmd/breezyd/ui/templates/schedule_block_templ.go \
  tests/ui/dashboard.spec.ts
git commit -m "$(cat <<'EOF'
fix(ui): sync schedule editor pct input on action toggle (#44)

Action <select> now drives the row's pct <input> reactively via an
inline data-on:change expression. Toggling to "off" clears the value,
locks the input, applies pct-disabled. Toggling to a non-off value
restores the row's data-orig-pct, unlocks, and removes pct-disabled.
data-orig-pct stashes the row's initial Pct (or 50 fallback for off
rows / out-of-range values).

Adds a Playwright test that exercises the round-trip in the editor
without needing a save+reload.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Tighten off-row pct sentinel in schedule write handler

**Goal:** When the schedule editor submits an off row with empty pct, the handler stores `Pct=0` (the in-band sentinel for "no value"), not `10`. Aligns persisted state with the UI's "off rows have no pct" semantics.

**Files:**
- Modify: `cmd/breezyd/handlers_ui_write.go` (line 173)

**Acceptance Criteria:**
- [ ] Line 173 stores `0` instead of `10` for off-row pct fallback.
- [ ] Existing Go tests still pass (`just test`).
- [ ] Existing Playwright tests still pass (`just test-ui`) — no UI-visible change since `schedulePctValue` already empties the field for off rows and the new `schedulePctOrigValue` already maps 0 → 50.

**Verify:**
- `just test` → all Go tests pass
- `just test-ui` → all Playwright tests pass
- `just check-all` → full pre-push gate passes

**Steps:**

- [ ] **Step 1: Apply the one-line change in `cmd/breezyd/handlers_ui_write.go`**

Change line 173 (inside the `pct` parsing block, in the `if action != "off"` else branch):

```go
			pct := 0
			if _, err := fmt.Sscanf(pcts[i], "%d", &pct); err != nil || pct < 10 || pct > 100 {
				if action != "off" {
					h.scheduleEditFrag(w, r, name, fmt.Sprintf("row %d: pct must be 10–100, got %q", i+1, pcts[i]))
					return
				}
				pct = 10 // off rows: pct is irrelevant, use default
			}
```

to:

```go
			pct := 0
			if _, err := fmt.Sscanf(pcts[i], "%d", &pct); err != nil || pct < 10 || pct > 100 {
				if action != "off" {
					h.scheduleEditFrag(w, r, name, fmt.Sprintf("row %d: pct must be 10–100, got %q", i+1, pcts[i]))
					return
				}
				pct = 0 // off rows: pct is the in-band "no value" sentinel
			}
```

- [ ] **Step 2: Run Go tests**

```bash
just test
```

Expected: all packages pass. (No existing test asserts the `10` placeholder; if any does, the assertion needs to flip to `0` and the spec calls that out as expected behavior.)

- [ ] **Step 3: Run the full pre-push gate**

```bash
just check-all
```

Expected: green across lint, tests, race, Playwright, templ-drift.

- [ ] **Step 4: Commit**

```bash
git add cmd/breezyd/handlers_ui_write.go
git commit -m "$(cat <<'EOF'
fix(ui): persist Pct=0 for off rows in schedule write handler (#44)

Pct's valid range is [10..100], so 0 is a free in-band sentinel for
"no value". Off rows submitted with empty pct now persist as 0
instead of a misleading 10, matching the UI's end-to-end "off rows
have no pct" semantics. The scheduler ignores pct on off rows
regardless of value, so downstream state consumers are unaffected.

Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>
EOF
)"
```

---

## Final Verification

After both tasks complete:

```bash
just check-all
```

Expected: clean. Smoke-test in a browser:
1. `just build && ./breezyd --config <test-cfg>`
2. Open `/`, edit a device's schedule.
3. Take a row with non-off action and a non-zero pct. Toggle to `off` — pct should clear, become readonly, get the disabled style. Toggle back to a non-off mode — pct should restore the prior value, become editable, lose the disabled style.
4. Save, reload, edit again — repeat. Off rows should now round-trip with no stale numeric stored.

---

## Self-Review

**Spec coverage:** Each spec section maps to a task —
- "Approach" / "Changes #1" (templ) → Task 1.
- "Changes #2" (handler) → Task 2.
- "Changes #3" (Playwright test) → Task 1.
- "Out of scope" — preserved (no tasks touch wire format, server validation logic, or the read variant).

**Placeholder scan:** No "TBD" / "TODO" / "appropriate handling" / "similar to". Each step shows the exact code, exact file paths with lines (135–141, 144–152, 173), and exact verify commands.

**Type consistency:** Helper names match across the plan (`schedulePctOrigValue`, `scheduleActionChangeExpr`). The data attribute is `data-orig-pct` everywhere (HTML attribute hyphen-form), accessed in JS as `dataset.origPct` (camelCase auto-conversion — both forms are correct). The pct input's selector `input[name=pct]` matches the existing `name="pct"` declaration. The `pct-disabled` class name matches the existing `templ.KV("pct-disabled", ...)` invocation.
