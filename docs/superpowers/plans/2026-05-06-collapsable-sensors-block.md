# Collapsable Sensors block implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve issue #29 by wrapping the dashboard's per-card Sensors block in `<details>` so it's collapsable; expanded by default; force-expanded when any of `sensor_alerts.{humidity, co2, voc}` is true.

**Architecture:** Single-file UI change to `cmd/breezyd/ui/index.html`. Mirrors the existing `details.device-info` and `details.energy` patterns: a per-card state map (`sensorsCollapsed[name]`, with inverted polarity since default = open), an `expanded = alerting || !sensorsCollapsed[name]` computation in `renderCard`, and a new branch in the existing `toggle` capture-phase listener. CSS reuses the chevron treatment by extending the existing `details.energy > summary` rules to `details.energy, details.block.sensors`.

**Tech Stack:** Vanilla HTML/CSS/JS (single embedded `index.html`), Playwright (`@playwright/test`) for UI regression tests, `just` recipes for build/test/screenshot.

**Spec:** `docs/superpowers/specs/2026-05-06-collapsable-sensors-block-design.md`

---

## File Structure

- **Modify** `cmd/breezyd/ui/index.html`:
  - Extend the existing `details.energy > summary` CSS rule group (`index.html:82–97`) to also match `details.block.sensors > summary` — combine selectors instead of duplicating.
  - Declare `const sensorsCollapsed = {};` next to `deviceInfoOpen` / `energyOpen` (around line 339).
  - In `renderCard()` (around line 423–479), replace the `<div class="block">…<h3>Sensors</h3>…` wrapper around `${sensorsGrid(name, snap)}` with `<details class="block sensors"${expanded ? " open" : ""}>` + `<summary><h3>Sensors</h3></summary>`. Compute `expanded = sensorAlerting || !sensorsCollapsed[name]` from `snap.live?.sensor_alerts`.
  - Extend the `toggle` capture-phase listener (around line 1041–1056) with a `.sensors` branch.
- **Modify** `tests/ui/dashboard.spec.ts` — add three Playwright tests mirroring the existing `details.device-info` test shapes.
- **Regenerate** `tests/ui/screenshots/dashboard-1col.png` and `tests/ui/screenshots/dashboard-3col.png` via `just screenshot`. Visual difference is minimal because the screenshot fixture's `playroom` already fires `co2: true, voc: true` alerts (force-expanded) and the other two cards have no alerts (also expanded by default), so all three render expanded just as they do today.

No daemon-side, CLI-side, or protocol changes.

---

## Task 1: Wrap Sensors block in `<details>` with auto-expand-on-alert

**Goal:** The Sensors block is a `<details>` element, expanded by default, collapsable by the user, force-expanded when any of `humidity`/`co2`/`voc` is alerting. User collapse intent persists across the 5 s polling re-render via a per-card state map.

**Files:**
- Modify: `cmd/breezyd/ui/index.html` (CSS at ~`82-97`, state map at ~`336-339`, `renderCard()` at ~`471-474`, toggle listener at ~`1041-1056`)
- Modify: `tests/ui/dashboard.spec.ts` (new tests added near the existing `details.device-info` block, ~`866-907`)
- Regenerate: `tests/ui/screenshots/dashboard-1col.png`, `tests/ui/screenshots/dashboard-3col.png`

**Acceptance Criteria:**
- [ ] The Sensors block renders as `<details class="block sensors">` with `<summary><h3>Sensors</h3></summary>`.
- [ ] With no active alerts the `<details>` has the `open` attribute (default expanded).
- [ ] Clicking the summary closes the `<details>` (the `open` attribute is removed in the resulting DOM).
- [ ] When `snap.live.sensor_alerts.co2 === true` (or `humidity` or `voc`), the `<details>` has `open` regardless of `sensorsCollapsed[name]`.
- [ ] The toggle handler updates `sensorsCollapsed[name]`: sets it to `true` when the user collapses, deletes it when the user expands.
- [ ] The chevron renders to the left of "Sensors" with the same `▶`/`▼` styling the Energy block uses.
- [ ] All existing Playwright tests still pass — in particular the threshold tests (`tests/ui/dashboard.spec.ts:775,788,802,819,834,849`) which locate `.card .block` with `hasText: "Sensors"` and use `.sensor-cell` selectors. The `.block` class is preserved on the new `<details>`, and `<details>` body text is part of the element's text content even when collapsed, so `hasText` and `toContainText` still match.
- [ ] `just check` passes.
- [ ] `just test-ui` passes (52 existing + 3 new = 55).
- [ ] Screenshots regenerated and committed.

**Verify:** `just check && just test-ui` → both green; expect `55 passed`.

**Steps:**

- [ ] **Step 1: Write the three new failing tests**

In `tests/ui/dashboard.spec.ts`, add three tests immediately after the existing `details.device-info` block (i.e. after the test ending at ~ line 907, before the `ENERGY block: open state survives the 5 s grid re-render` test). Use the same shape as the device-info tests:

```typescript
test("sensors block: expanded by default with no alerts", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: { sensor_alerts: { humidity: false, co2: false, voc: false } },
    }),
  });
  const sensors = page.locator(".card details.sensors").first();
  await expect(sensors).toHaveCount(1);
  await expect(sensors).toHaveAttribute("open", "");
});

test("sensors block: clicking summary collapses the block", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: { sensor_alerts: { humidity: false, co2: false, voc: false } },
    }),
  });
  const sensors = page.locator(".card details.sensors").first();
  await expect(sensors).toHaveAttribute("open", "");
  await sensors.locator("summary").click();
  await expect(sensors).not.toHaveAttribute("open", "");
});

test("sensors block: auto-expanded when a sensor alert is active", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      sensors: { eco2_ppm: 3500 },
      configured: { co2_threshold_ppm: 1500 },
      live: { sensor_alerts: { humidity: false, co2: true, voc: false } },
    }),
  });
  const sensors = page.locator(".card details.sensors").first();
  await expect(sensors).toHaveAttribute("open", "");
});
```

- [ ] **Step 2: Run the new tests; expect all three to fail**

```sh
cd tests/ui && pnpm exec playwright test --grep "sensors block:"
```

Expected: **FAIL** for all three tests, with errors like `Expected: "open"; Received: <element not found>` because `details.sensors` doesn't exist yet — the wrapper today is `<div class="block">`.

- [ ] **Step 3: Add CSS — extend the existing chevron rule group to cover the Sensors block too**

In `cmd/breezyd/ui/index.html`, replace the existing `details.energy` chevron block (~ lines 82–97) with a combined-selector version that covers both. The behaviour is identical; the new selector adds `details.block.sensors` everywhere `details.energy` appeared:

```css
  /* ENERGY and Sensors <details>: same chevron-on-the-left treatment as
     Device Info, so the marker sits beside the heading instead of stacking
     under it. Without this, the browser-default marker overlaps the <h3>
     content. Sensors is expanded by default; alert-active force-expansion
     is computed in JS, see renderCard. */
  details.energy > summary,
  details.block.sensors > summary {
    display: flex;
    align-items: baseline;
    gap: 1ch;
    cursor: pointer;
    list-style: none;
  }
  details.energy > summary::-webkit-details-marker,
  details.block.sensors > summary::-webkit-details-marker { display: none; }
  details.energy > summary::before,
  details.block.sensors > summary::before {
    content: "▶";
    font-size: 0.65em;
    color: #888;
    align-self: center;
  }
  details.energy[open] > summary::before,
  details.block.sensors[open] > summary::before { content: "▼"; }
  details.energy > summary > h3,
  details.block.sensors > summary > h3 { margin: 0; }
```

- [ ] **Step 4: Add the `sensorsCollapsed` state map**

In `cmd/breezyd/ui/index.html`, immediately after the existing `energyOpen` declaration (~ line 339), add:

```javascript
// Per-card Sensors <details> collapsed state. Default is expanded, so we
// track only when the user has explicitly collapsed it (presence == true).
// Survives the 5 s grid re-render via the toggle listener at the bottom of
// this file. Auto-expand on active alert wins over user collapse — when
// the alert clears, the collapse intent is preserved and re-applies.
const sensorsCollapsed = {}; // name -> true (absent = default expanded)
```

- [ ] **Step 5: Wrap the Sensors block in `<details>` and compute `expanded`**

In `cmd/breezyd/ui/index.html`, replace the existing Sensors block in `renderCard()` (~ lines 471–474):

```javascript
    <div class="block">
      <h3>Sensors</h3>
      ${sensorsGrid(name, snap)}
    </div>
```

with:

```javascript
    ${(() => {
      const sa = snap.live?.sensor_alerts || {};
      const sensorAlerting = sa.humidity === true || sa.co2 === true || sa.voc === true;
      const sensorsExpanded = sensorAlerting || !sensorsCollapsed[name];
      return `<details class="block sensors"${sensorsExpanded ? " open" : ""}>
      <summary><h3>Sensors</h3></summary>
      ${sensorsGrid(name, snap)}
    </details>`;
    })()}
```

The IIFE keeps the `sensorAlerting` / `sensorsExpanded` locals from leaking into the surrounding template-literal scope, matching the local-helper pattern used elsewhere in this file.

- [ ] **Step 6: Extend the toggle listener with a `.sensors` branch**

In `cmd/breezyd/ui/index.html`, in the `document.addEventListener("toggle", …)` block (~ lines 1045–1056), add a final `else if` branch:

```javascript
document.addEventListener("toggle", (ev) => {
  const el = ev.target;
  const name = el.closest(".card")?.querySelector("h2")?.textContent;
  if (!name) return;
  if (el.classList?.contains("device-info")) {
    if (el.open) deviceInfoOpen[name] = true;
    else delete deviceInfoOpen[name];
  } else if (el.classList?.contains("energy")) {
    if (el.open) energyOpen[name] = true;
    else delete energyOpen[name];
  } else if (el.classList?.contains("sensors")) {
    // Inverted: state map tracks user-collapsed (since default is open).
    if (el.open) delete sensorsCollapsed[name];
    else sensorsCollapsed[name] = true;
  }
}, true);
```

- [ ] **Step 7: Run the three new tests; expect all to pass**

```sh
cd tests/ui && pnpm exec playwright test --grep "sensors block:"
```

Expected: **3 PASS.**

- [ ] **Step 8: Run the full UI suite to confirm no regression**

```sh
just test-ui
```

Expected: **55 passed** (52 prior + 3 new). The threshold tests at lines 775, 788, 802, 819, 834, 849 use `.card .block` with `hasText: "Sensors"` and `.sensor-cell` selectors — the `.block` class is preserved on the new `<details>`, the `<h3>Sensors</h3>` text is still inside, and the `.sensor-cell` grid is unchanged. They should pass unmodified. If any fails, do NOT silently update the test — diagnose: is it a regression in the wrapper change (fix the implementation) or a stale assertion the new design legitimately invalidates (update the test with a one-line comment explaining what changed)? Report both cases in your status.

- [ ] **Step 9: Run `just check`**

```sh
just check
```

Expected: lint and fast Go tests pass. (No Go code changed; pre-commit gate per project rule.)

- [ ] **Step 10: Manually verify in a browser**

Per the project's "Playwright for UI visual checks" memory:

```sh
just build
./breezyd &
# open http://localhost:8080/ in a browser
```

Confirm:
- A card with no active alerts shows the Sensors block expanded with a `▼` chevron next to "Sensors".
- Clicking the "Sensors" summary collapses the block; chevron flips to `▶`. The threshold-editor cells (eCO₂/VOC/RH) become hidden.
- The collapse persists across at least one 5 s poll cycle.
- A card whose `sensor_alerts.{humidity,co2,voc}` is true (e.g. the playroom in the user's setup) renders expanded; clicking the summary may visually toggle but the next render re-opens because alert force-expansion wins.

Stop the daemon: `kill %1`.

- [ ] **Step 11: Regenerate dashboard screenshots**

```sh
just screenshot
```

Expected: the two PNGs are rewritten. The fixture's `playroom` has `co2: true, voc: true` alerts (force-expanded), and `bedroom` / `office` have no alerts (default expanded). All three cards therefore render expanded, identical in content to today. Anti-aliasing pixel changes are acceptable.

- [ ] **Step 12: Commit**

```sh
git add cmd/breezyd/ui/index.html tests/ui/dashboard.spec.ts tests/ui/screenshots/dashboard-1col.png tests/ui/screenshots/dashboard-3col.png
git commit -m "$(cat <<'EOF'
ui: collapsable Sensors block, auto-expanded when alerting (#29)

Resolves #29. Wraps the per-card Sensors block in <details>, expanded
by default, force-expanded when any of sensor_alerts.{humidity, co2,
voc} is true. Collapse intent persists across the 5 s poll via a new
sensorsCollapsed[name] state map (mirrors deviceInfoOpen/energyOpen
with inverted polarity). Chevron CSS reuses the existing details.energy
selector group via combined selectors.
EOF
)"
```

---

## Self-Review

- **Spec coverage:**
  - Wrap Sensors in `<details class="block sensors">` with `<summary><h3>Sensors</h3></summary>` — Step 5. ✓
  - Default expanded — Step 5 (`sensorsExpanded = alerting || !collapsed`, both false initially → expanded=true). ✓
  - User can collapse, persists across 5 s re-render — Step 4 (state map declared at module scope, untouched by `render()`) + Step 6 (toggle listener writes to it) + Step 5 (each render reads it). ✓
  - Auto-expand on alert (force over user collapse) — Step 5 (`alerting || …` short-circuits to true when alerting). ✓
  - Chevron-on-the-left styling — Step 3 (combined-selector CSS). ✓
  - State polarity inverted from `deviceInfoOpen`/`energyOpen` — Step 4 (comment explains; absent == expanded). ✓
  - Toggle listener extended — Step 6. ✓
  - Tests for default expanded / click collapses / alert auto-expands — Step 1. ✓
  - Tests in spec for "user collapse persists across re-render" and "alert clears returns to user-collapsed state" are dropped from the test set deliberately. The existing test harness has no cross-render snapshot-mutation affordance (`window.render`/`refreshAll` are not exposed), and the existing `details.energy` re-render test relies on a no-op `(window as any).render?.()` call. Adding test infrastructure for these two cases is out of scope for this issue; the rendering logic is straightforward enough to verify by code inspection (each `render()` reads `sensorsCollapsed[name]`; `sensorAlerting` is computed fresh every call from `snap.live.sensor_alerts`). The three retained tests cover every distinct behaviour state an end-user can observe in one render.

- **Placeholder scan:** every code step shows the actual code; every command step shows the actual command and expected outcome. No "TBD"/"add validation"/"similar to Task N".

- **Type / selector consistency:**
  - State map name: `sensorsCollapsed` — used in Steps 4, 5, 6 consistently.
  - Variable name `sensorAlerting` — declared in Step 5 IIFE only, used only there.
  - CSS selectors `details.block.sensors` and `details.block.sensors > summary` — consistent across Steps 3, 5; the `.block` class on the `<details>` is required so the existing `.block` margin/padding/border (line 50 of `index.html`) applies.
  - Test selector `.card details.sensors` — consistent across the three new tests, matches the `<details class="block sensors">` element from Step 5.
  - Toggle-listener class check `el.classList?.contains("sensors")` — consistent with the `details.sensors` class in the markup.
