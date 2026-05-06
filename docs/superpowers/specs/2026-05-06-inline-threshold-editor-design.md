# Inline threshold editor (issue #28)

## Problem

In `cmd/breezyd/ui/index.html`, clicking a sensor-alert threshold value (eCO₂, VOC, or RH) opens an editor block that renders **below the entire sensor grid** — i.e. after four rows of temperatures and rpms that have nothing to do with the cell the user just clicked. The control feels disconnected from the value being edited (issue #28: "can it be closer to the current sensor value?").

## Goal

When the user clicks a threshold-bearing value, the input control appears **in that exact cell**, replacing the value text in place. No restructuring of the surrounding grid, no other layout changes.

## Current shape (for reference)

The sensor block is a 2-column × 6-row grid:

| col 1 | col 2 |
|---|---|
| eCO₂ ★ | VOC ★ |
| RH ★ | recovery |
| supply | exhaust |
| supply_regen | exhaust_regen |
| Δ | Δ |
| supply rpm | exhaust rpm |

★ = threshold-editable. The other nine cells are read-only.

`sensorsGrid()` emits the 12 cells, then concatenates `thresholdEditor()` after the closing `</div>` of the grid. `thresholdEditor()` is what currently renders the editor block (or a leftover error toast) at the bottom.

## Design

### Inline replacement inside the cell

`thresholdCell(name, kind, snap)` gains a branch:

- **default branch** — render `<div class="value-clickable" data-action="edit-threshold" …>VALUE</div>` (current behavior).
- **editing branch** (`editingThreshold[name] === kind`) — render `<div class="thresh-edit-inline">[input] [✓] [✕]</div>` instead, with the same `data-name` / `data-kind` attributes the existing handlers expect.

The cell's `.sensor-label` ("RH"/"eCO₂"/"VOC") stays in place above the controls. The grid layout is untouched; the cell may grow slightly taller while editing, which is acceptable.

### Compact inline controls

The current bottom-rendered editor reads `RH set alert ≥ [input] % [✓] [✕]`. Inline, this collapses to `[input] [✓] [✕]`:

- The `set alert ≥` prefix label is dropped — the cell label disambiguates.
- The unit suffix (`%`, ` ppm`, ` idx`) is dropped — the cell label is enough context, and the cell is too narrow to spare the characters.
- Input width drops from `4.5rem` to roughly `3rem` so the row fits comfortably inside one grid column.
- `min` / `max` / `step` validation, the `disabled` state during in-flight or stale snapshots, and the auto-focus + select on open all stay identical.

### Error toast placement

The current `thresholdEditor()` also renders any leftover `threshold-<kind>` error toast. With the editor gone, the toast still needs a home — it renders in a small slot **below the sensor grid**, so a failed save (validation or HTTP) is still visible without crowding the cell.

`thresholdEditor()` collapses to a ~5-line helper that emits only the toast row (or empty string when no toast is set). The toast key (`threshold-<kind>`), the clear-on-success behavior, and the `postWrite` flow are unchanged.

### Handlers stay the same

The behavior the user sees is unchanged outside the visual location:

- Click handler — `case "edit-threshold"`, `case "threshold-save"`, `case "threshold-cancel"` — unchanged.
- Keyboard handler — Enter saves, Escape cancels while focused inside `.thresh-input` — unchanged.
- `saveThreshold(name, kind)` — unchanged.
- Focus-on-render — the existing `document.querySelector(".thresh-input[data-name=…][data-kind=…]")` lookup still works because the input keeps the same class and data attributes; it just lives in a different DOM ancestor.
- Polling-driven re-render — `editingThreshold[name]` already survives the 5 s `render()` cycle, so the open editor is preserved across polls.

### CSS

A new rule for `.thresh-edit-inline` mirroring `.thresh-edit` (inline-flex, gap, button styles), with input width tightened. The existing `.thresh-edit*` rules can be retained or merged — implementation choice during the edit. No new icons or assets.

## Out of scope

- No grid restructuring (still 2 × 6, same rows in the same order).
- No inline edit affordance for non-threshold cells.
- No change to validation ranges, save semantics, or HTTP envelope.
- No change to the `breezy` CLI or daemon side.

## Verification

- **Playwright** (`tests/ui/`): existing specs that exercise threshold edit click `[data-action=edit-threshold]`, type into `.thresh-input`, then click `[data-action=threshold-save]`. Those selectors are preserved verbatim, so existing tests should pass without changes. Confirm by running `just test-ui`.
- **Manual visual** (per the project's "render with Playwright before claiming done" rule): re-run `just screenshot` so the committed PNGs reflect the new editor location.
- **Lint / fast tests**: `just check` for the pre-commit gate.
- **Cross-cell switching**: clicking RH while eCO₂ is being edited should switch the editor to RH (existing single-editor-per-card invariant via `editingThreshold[name]` — verify still holds).
- **Stale snapshot**: with a stale card, the inline input + buttons must still be `disabled` (existing `dis` flag passed through unchanged).

## Files touched

- `cmd/breezyd/ui/index.html` — only file with substantive changes (CSS rule + two function refactors).
- `tests/ui/screenshots/*.png` — regenerated.
- Possibly `tests/ui/*.spec.ts` if a test happens to assert on the editor's old position. Audit during implementation; update only if a real assertion breaks.
