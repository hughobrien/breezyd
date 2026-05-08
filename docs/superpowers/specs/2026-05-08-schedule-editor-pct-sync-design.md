# Schedule editor: sync pct input when action toggles to/from off

Closes #44.

## Problem

In the schedule editor (`ScheduleBlockEdit` / `ScheduleEditRow` in
`cmd/breezyd/ui/templates/schedule_block.templ`), the action `<select>` has no
datastar binding. Toggling it between `off` and a non-off value does not update
the sibling pct `<input>` until the form round-trips. Two visible glitches:

- Action set to `off` while pct shows a non-zero value: pct stays editable and
  keeps the stale number until save+reload.
- Action moved away from `off`: pct stays empty and `readonly` until
  save+reload.

The static render is already correct: `schedulePctValue` returns `""` when
`Action == "off"`, and the input gets `readonly` plus the `pct-disabled` class.
The bug is purely interactive.

## Approach

Inline `data-on:change` on the action `<select>`, plus a per-row stashed
"original pct" so the restore-on-toggle path has a sensible value. No new
signals, no shared state. One template, one helper, one handler one-liner.

Approach B from the issue (per-row datastar signal) is rejected — promoting
this to a signal is more machinery than the interaction warrants and there is
no other reactive binding accumulating in this editor.

## Changes

### 1. `cmd/breezyd/ui/templates/schedule_block.templ`

`ScheduleEditRow` (lines 124–162):

- Add `data-orig-pct={ schedulePctOrigValue(e) }` on the pct input. This is the
  value the input should restore to when the user toggles action away from
  `off`.
- Add `data-on:change="..."` on the action `<select>`. The expression locates
  the row's pct input via `el.closest('tr').querySelector('input[name=pct]')`
  and:
  - if `el.value === 'off'`: clear `value`, set the `readonly` attribute, add
    the `pct-disabled` class.
  - else: set `value` to `pct.dataset.origPct`, remove `readonly`, remove the
    `pct-disabled` class.

New helper at the bottom of the file:

```go
// schedulePctOrigValue returns the value to restore into the pct input when
// the user toggles action away from "off". For rows whose persisted Pct is
// in range, that's Pct verbatim; for off rows (Pct == 0) and any
// out-of-range stored values, fall back to 50.
func schedulePctOrigValue(e ui.ScheduleEntryView) string {
    if e.Pct >= 10 && e.Pct <= 100 {
        return fmt.Sprintf("%d", e.Pct)
    }
    return "50"
}
```

Both call sites are covered by a single template edit: the in-place edit
variant (`ScheduleBlockEdit` → `ScheduleEditRow` loop) and the "+ add row"
endpoint (`getUIScheduleNewRow` → `ScheduleEditRow`) both render through the
exported `ScheduleEditRow`.

### 2. `cmd/breezyd/handlers_ui_write.go`

Line 173 currently stores `pct = 10` as a placeholder for off rows when the
form's pct field is empty/invalid. Change to `pct = 0`. Pct's valid range is
[10..100], so 0 is a free in-band sentinel for "no value" and matches the
"off ⇒ no pct" semantics the UI now expresses end-to-end.

This is the only handler change. The scheduler ignores pct on off rows
regardless of value, so persisted-state consumers downstream are unaffected.

### 3. `tests/ui/dashboard.spec.ts`

Add a new `test.describe("schedule editor")` block with one test:

1. Seed the device's schedule via the existing `withSchedule` fixture
   (`tests/ui/fixtures.ts:275`) with one entry: `at=08:00`,
   `action=regeneration`, `pct=60`, schedule enabled.
2. Open the device card, click "edit schedule".
3. Locate the row's `<select name="action">` and the row's
   `<input name="pct">`.
4. `selectOption(action, "off")`. Assert pct: `value === ""`, has `readonly`
   attribute, has `pct-disabled` class.
5. `selectOption(action, "ventilation")`. Assert pct: `value === "60"`, no
   `readonly`, no `pct-disabled` class.

The test exercises both transitions in a single flow because they share row
state; splitting them would force two seed-and-open setups for no extra
coverage.

## Out of scope

Echoes the issue:

- Server-side validation of pct on off rows. The handler already accepts an
  empty/invalid pct field when action is off.
- Wire format and schedule data model. The Pct=0 sentinel was already legal
  (valid range is [10..100]); the handler change only stops writing a
  misleading 10.
- The read-only display variant. `scheduleReadRow` already renders `—` and
  applies `pct-disabled` for off rows.

## Verification

- `just test-templ-drift` — confirms generated `*_templ.go` files match the
  edited template.
- `just test` — fast Go suite.
- `just test-ui` — Playwright suite, including the new test.
- Manual: open `/`, edit schedule on a device with a non-off row, toggle the
  action select to `off` → confirm pct clears and is readonly; toggle to a
  non-off mode → confirm pct restores its prior value and becomes editable.
  Repeat from a row that was saved as off → confirm pct restores to 50 on
  first toggle to a non-off mode.
