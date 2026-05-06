# Inline threshold editor implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve issue #28 by rendering the threshold editor inside the clicked sensor cell, replacing the value text with `[input] ✓ ✕` in place — instead of below the entire sensor grid.

**Architecture:** Single-file UI change. Refactor `thresholdCell()` in `cmd/breezyd/ui/index.html` to render either the value (default) or an inline editor (when `editingThreshold[name] === kind`). Shrink `thresholdEditor()` to a toast-only helper that still renders below the grid. All click/keyboard handlers, the `editingThreshold` state map, the `saveThreshold()` flow, and the `data-name`/`data-kind` attributes are preserved verbatim, so existing Playwright selectors keep working. One Playwright test asserts on the to-be-removed "set alert ≥" prefix label and must be rewritten; the rest pass unchanged.

**Tech Stack:** Vanilla HTML/CSS/JS (single embedded `index.html`), Playwright (`@playwright/test`) for UI regression tests, `just` recipes for build/test/screenshot.

**Spec:** `docs/superpowers/specs/2026-05-06-inline-threshold-editor-design.md`

---

## File Structure

- **Modify** `cmd/breezyd/ui/index.html` — CSS rule for `.thresh-edit-inline`, refactored `thresholdCell()`, shrunk `thresholdEditor()`. ~30 lines net.
- **Modify** `tests/ui/dashboard.spec.ts` — replace one test (line 788) that asserts on the dropped "set alert ≥" label.
- **Regenerate** `tests/ui/screenshots/dashboard-3col.png`, `tests/ui/screenshots/dashboard-1col.png` via `just screenshot`. The default (non-editing) state of the cards is visually identical — these may change only by anti-aliasing pixels, but the project rule is to refresh them on UI work.

No other files touched. No daemon-side, CLI-side, or protocol changes.

---

## Task 1: Inline threshold editor in cell

**Goal:** When the user clicks a threshold-bearing sensor value, the input + ✓ + ✕ controls render inside that cell, replacing the value text. The previous below-grid editor block is gone; the leftover-error toast still renders below the grid.

**Files:**
- Modify: `cmd/breezyd/ui/index.html` (CSS block, `thresholdCell` ~ lines 528–541, `thresholdEditor` ~ lines 545–576)
- Modify: `tests/ui/dashboard.spec.ts` (~ lines 788–800)
- Regenerate: `tests/ui/screenshots/dashboard-3col.png`, `tests/ui/screenshots/dashboard-1col.png`

**Acceptance Criteria:**
- [ ] Clicking `[data-action="edit-threshold"]` on RH/eCO₂/VOC replaces the cell's value text with an `<input class="thresh-input">` plus `[data-action="threshold-save"]` and `[data-action="threshold-cancel"]` buttons, all inside the same `.sensor-cell`.
- [ ] The cell's `.sensor-label` ("RH"/"eCO₂"/"VOC") remains visible above the inline controls.
- [ ] The "set alert ≥" prefix label no longer appears anywhere in the rendered DOM while editing.
- [ ] Per-card error toasts (`threshold-<kind>` key) still render in a slot below the sensor grid when present.
- [ ] Enter saves and Escape cancels (existing keyboard handler is reused).
- [ ] ✓ click saves; ✕ click cancels; auto-focus on the freshly rendered input.
- [ ] Polling re-render every 5 s preserves an open editor (existing `editingThreshold[name]` already covers this — verify nothing in the refactor regresses it).
- [ ] Stale snapshot or in-flight write disables the inline input and both buttons.
- [ ] `just check` passes.
- [ ] `just test-ui` passes (one test rewritten, others unchanged).
- [ ] Screenshots regenerated and committed.

**Verify:** `just check && just test-ui` → all green.

**Steps:**

- [ ] **Step 1: Rewrite the Playwright test that asserts on the dropped "set alert ≥" label**

In `tests/ui/dashboard.spec.ts`, replace the current test at line 788 with a test that asserts the new in-cell layout. The selectors `[data-action="edit-threshold"]`, `.thresh-input`, `[data-action="threshold-save"]`, `[data-action="threshold-cancel"]` all stay valid; only the placement assertion changes.

Replace this block:

```typescript
test("threshold: opening the editor reveals 'set alert ≥' label and threshold input", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { humidity_threshold_pct: 70 },
    }),
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const sensors = page.locator(".card .block", { hasText: "Sensors" });
  await expect(sensors).toContainText("set alert ≥");
  const input = page.locator('.thresh-input[data-name="playroom"][data-kind="humidity"]');
  await expect(input).toHaveValue("70");
});
```

with:

```typescript
test("threshold: opening the editor renders the input inside the clicked cell", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { humidity_threshold_pct: 70 },
    }),
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  // Input + save/cancel buttons must live inside the same .sensor-cell as the RH label.
  const rhCell = page.locator('.sensor-cell:has(.sensor-label:text-is("RH"))');
  await expect(rhCell.locator('.thresh-input')).toHaveValue("70");
  await expect(rhCell.locator('button[data-action="threshold-save"][data-kind="humidity"]')).toBeVisible();
  await expect(rhCell.locator('button[data-action="threshold-cancel"][data-kind="humidity"]')).toBeVisible();
  // The dropped "set alert ≥" prefix label must not appear anymore.
  const sensors = page.locator(".card .block", { hasText: "Sensors" });
  await expect(sensors).not.toContainText("set alert ≥");
});
```

- [ ] **Step 2: Run the test to confirm it fails against the current implementation**

Run:
```sh
cd tests/ui && pnpm exec playwright test --grep "renders the input inside the clicked cell"
```

Expected: **FAIL.** The current implementation does not place the input inside the `.sensor-cell` for RH (it renders below the grid), and the "set alert ≥" label still appears.

- [ ] **Step 3: Add the inline-editor CSS**

In `cmd/breezyd/ui/index.html`, in the CSS block near the existing `.thresh-edit` rule (~ line 233), add:

```css
.thresh-edit-inline {
  display: inline-flex;
  align-items: center;
  gap: 0.2rem;
}
.thresh-edit-inline .thresh-input {
  width: 3rem;
  padding: 0.05rem 0.2rem;
  font-family: inherit;
  font-size: 0.85rem;
  border: 1px solid #aaa;
  border-radius: 3px;
}
.thresh-edit-inline button {
  font-family: inherit;
  font-size: 0.85rem;
  padding: 0.05rem 0.3rem;
  border: 1px solid #aaa;
  background: #fafafa;
  border-radius: 3px;
  cursor: pointer;
}
.thresh-edit-inline button:hover:not(:disabled) {
  background: #eee;
  border-color: #888;
}
.thresh-edit-inline button:disabled,
.thresh-edit-inline .thresh-input:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
```

The existing `.thresh-edit`, `.thresh-input` (4.5rem), and `.thresh-edit button` rules can stay — they're now unused but harmless. (Leave them; a follow-up cleanup is out of scope.)

- [ ] **Step 4: Refactor `thresholdCell()` to render the inline editor in-place when this cell is being edited**

In `cmd/breezyd/ui/index.html`, replace the body of `thresholdCell` (~ lines 528–541) with:

```javascript
function thresholdCell(name, kind, snap) {
  const cfg = THRESHOLD_KINDS[kind];
  const live = snap.sensors?.[cfg.valueKey];
  const alerting = snap.live?.sensor_alerts?.[cfg.alertKey] === true;
  const liveStr = (live === null || live === undefined) ? "—" : `${live}${cfg.suffix}`;
  const liveCls = alerting ? "alert-fire" : "";
  const titleAttr = cfg.tooltip ? ` title="${esc(cfg.tooltip)}"` : "";
  const editing = editingThreshold[name] === kind;
  let body;
  if (editing) {
    const thresh = snap.configured?.[cfg.thresholdKey];
    const stale = (snap._fetchedAt
      ? (Date.now() - new Date(snap.last_poll).getTime()) > STALE_THRESHOLD_MS
      : false);
    const dis = (inFlight[name] || stale) ? "disabled" : "";
    const inputVal = (thresh === null || thresh === undefined) ? cfg.min : thresh;
    body = `<span class="thresh-edit-inline">
      <input type="number" min="${cfg.min}" max="${cfg.max}" step="${cfg.step}"
             value="${inputVal}"
             data-name="${esc(name)}" data-kind="${esc(kind)}"
             class="thresh-input" ${dis}>
      <button data-action="threshold-save" data-name="${esc(name)}" data-kind="${esc(kind)}" ${dis}>✓</button>
      <button data-action="threshold-cancel" data-name="${esc(name)}" data-kind="${esc(kind)}" ${dis}>✕</button>
    </span>`;
  } else {
    body = `<div class="value-clickable ${liveCls}"
         data-action="edit-threshold" data-name="${esc(name)}" data-kind="${esc(kind)}"
         >${esc(liveStr)}</div>`;
  }
  return `<div class="sensor-cell"${titleAttr}>
    <div class="sensor-label">${esc(cfg.label)}</div>
    ${body}
  </div>`;
}
```

- [ ] **Step 5: Shrink `thresholdEditor()` to render only the leftover error toast**

In `cmd/breezyd/ui/index.html`, replace the body of `thresholdEditor` (~ lines 545–576) with:

```javascript
// With the editor now rendered inline inside thresholdCell, this helper only
// emits any leftover threshold-<kind> error toasts below the grid.
function thresholdEditor(name, snap) {
  const t = toasts[name] || {};
  return ["humidity", "co2", "voc"].map(k =>
    t["threshold-" + k] ? `<div class="toast" role="alert">${esc(t["threshold-" + k])}</div>` : ""
  ).join("");
}
```

`sensorsGrid()` already concatenates `${thresholdEditor(name, snap)}` after the grid's closing `</div>` (line 515) — no change needed there.

- [ ] **Step 6: Run the rewritten Playwright test to confirm it now passes**

```sh
cd tests/ui && pnpm exec playwright test --grep "renders the input inside the clicked cell"
```

Expected: **PASS.**

- [ ] **Step 7: Run the full UI test suite to confirm no regression**

```sh
just test-ui
```

Expected: **all tests pass.** The other threshold tests (lines 775, 802, 819, 834, 849) use the preserved selectors (`[data-action="edit-threshold"]`, `.thresh-input[data-name=…]`, `[data-action="threshold-save"]`, `[data-action="threshold-cancel"]`) and should pass without modification. If any of them fails, do NOT paper over it — read the failure, decide if it's an unintended regression (fix the implementation) or a stale assertion that the spec deliberately invalidated (update the test, with a one-line comment explaining what changed).

- [ ] **Step 8: Run `just check`**

```sh
just check
```

Expected: lint and fast Go tests both pass. (No Go code changed, so this should be uneventful — but the project rule is to run the pre-commit gate.)

- [ ] **Step 9: Manually verify the editor in a browser**

Per the project's "Playwright for UI visual checks" memory:

```sh
just build
./breezyd &
# open http://localhost:8080/ in a browser
```

Click an RH / eCO₂ / VOC value on a card. Confirm:
- The input + ✓ + ✕ appear in-cell, with the cell's label still visible.
- Pressing Enter saves; Escape cancels.
- Clicking ✓ saves; ✕ cancels.
- A second click on a different threshold (e.g. RH while VOC is being edited) switches the open editor to the new cell.
- Five-second poll cycle does not close the editor mid-edit.

Stop the daemon: `kill %1`.

- [ ] **Step 10: Regenerate dashboard screenshots**

```sh
just screenshot
```

Expected: `tests/ui/screenshots/dashboard-1col.png` and `dashboard-3col.png` are rewritten. The default (non-editing) view is visually identical to before, but anti-aliasing may change a few pixels — that's fine; the project pattern is to refresh on UI changes regardless.

- [ ] **Step 11: Commit**

```sh
git add cmd/breezyd/ui/index.html tests/ui/dashboard.spec.ts tests/ui/screenshots/dashboard-1col.png tests/ui/screenshots/dashboard-3col.png
git commit -m "$(cat <<'EOF'
ui: inline threshold editor — replaces cell value with [input] ✓ ✕ (#28)

Resolves #28. The threshold editor now renders inside the clicked
sensor cell instead of below the whole sensor grid, so the control
sits next to the value being edited. Existing data-action selectors,
keyboard handlers, and saveThreshold flow are unchanged; one
Playwright test that asserted on the dropped "set alert ≥" prefix
label was rewritten to assert the new in-cell placement.
EOF
)"
```

---

## Self-Review

- **Spec coverage:**
  - Inline replacement inside the cell — Step 4 (`thresholdCell` refactor). ✓
  - Compact `[input] ✓ ✕` controls; "set alert ≥" prefix and unit suffix dropped — Step 3 (CSS) + Step 4 (markup). ✓
  - Error toast still renders below the grid — Step 5 (`thresholdEditor` shrunk to toast-only). ✓
  - Click / keyboard / `saveThreshold` / focus-on-render unchanged — preserved by Step 4 keeping the same `data-action`/`data-name`/`data-kind` attrs and `.thresh-input` class; verified manually in Step 9. ✓
  - Polling-driven re-render preserves the open editor — `editingThreshold[name]` state untouched; verified manually in Step 9. ✓
  - Existing Playwright spec passes verbatim except for the one test we rewrite — Step 7 explicitly checks the rest. ✓
  - Screenshots refreshed — Step 10. ✓

- **Placeholder scan:** every code step shows the actual code; every command step shows the actual command and expected outcome. No "TBD"/"add validation"/"similar to Task N".

- **Type / selector consistency:**
  - `data-action` values used in the new markup: `edit-threshold`, `threshold-save`, `threshold-cancel` — all match the existing handler `case` labels in lines 969–982.
  - `.thresh-input` class preserved — matches the keyboard handler (`el.classList.contains("thresh-input")`, line 990) and `saveThreshold`'s querySelector (line 1002).
  - `data-name` / `data-kind` preserved on input and buttons — matches `saveThreshold` lookup and the cross-cell focus query in line 973.
  - `editingThreshold[name] === kind` test mirrors the existing single-editor-per-card invariant in lines 970, 980, 996, 1011.
