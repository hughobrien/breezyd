// SPDX-License-Identifier: GPL-3.0-or-later

// dashboard.spec.ts — Real-daemon Playwright tests for the breezyd
// dashboard.
//
// All tests run against the real breezyd daemon (backed by the
// in-process fakedevice admin surface) spawned by global-setup.ts.
//
// The dashboard is fully SSE-driven: opening the page makes a single
// long-lived GET /ui/sse, the daemon emits one datastar-patch-elements
// event per device on connect, then streams updates from the poller.
// Tests therefore wait for the card to *change* rather than for a
// specific request/response cycle to complete.
//
// Device name: "alpha" (sole device in the test config).
// baseURL: process.env.BREEZYD_URL (set by global-setup).

import { test, expect, Page, Locator } from "@playwright/test";
import {
  reset,
  simulateAuthFailure,
  simulateUDPTimeout,
  presets,
} from "./fixtures.js";

const DEVICE = "alpha";

// loadCard navigates to "/", waits for the SSE-driven initial-state
// patch to land, and returns a Locator scoped to the card.
async function loadCard(page: Page, name = DEVICE): Promise<Locator> {
  await page.goto("/");
  const card = page.getByTestId(`card-${name}`);
  await expect(card).toBeVisible({ timeout: 10_000 });
  return card;
}

// pollInterval matches the test daemon's --poll-interval=1s. Two
// intervals plus a buffer is plenty for the next push to land after a
// fakedevice mutation.
const POLL_PUSH_TIMEOUT = 4_000;

// assertStableAcrossPolls samples `invariants` across a 3+ second window
// (covers ≥3 poll cycles at the test daemon's 1s interval). Each iteration
// asserts the invariants with a short per-call timeout, so a regression that
// breaks any of them fails inside ~200ms rather than going unnoticed for the
// whole window. Used by the editor-preservation tests; see #81 for why a
// per-poll deterministic signal isn't tractable in Playwright (datastar's
// long-lived SSE delivers events the test framework can't observe at event
// granularity).
async function assertStableAcrossPolls(
  page: Page,
  invariants: () => Promise<void>,
): Promise<void> {
  const samples = 6;
  const intervalMs = 500;
  for (let i = 0; i < samples; i++) {
    await invariants();
    if (i < samples - 1) {
      // Bounded inter-sample sleep — the assertions above and below this
      // line catch a regression within one sample, so this is not a
      // "wait for state to settle" timeout (which the playwright skill
      // rightly forbids); it's a "verify state stays settled" sample step.
      await page.waitForTimeout(intervalMs);
    }
  }
}

test.describe("rendering", () => {
  test("@smoke card renders for the configured device", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    await expect(card.getByRole("heading", { name: DEVICE })).toBeVisible();
  });

  // Pins the failure mode below the "card not visible" symptom: if datastar
  // ever stops firing the page-load handler that opens /ui/sse (e.g. an
  // attribute rename, a bundle that doesn't ship the matching plugin), we
  // want this test to fail before any cosmetic test does.
  test("datastar opens /ui/sse on page load", async ({ page }) => {
    // datastar's @get appends a `?datastar=...` query string, so match
    // by substring rather than endsWith.
    const sseRequests: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/ui/sse")) sseRequests.push(r.url());
    });
    await page.goto("/");
    await expect(page.getByTestId(`card-${DEVICE}`)).toBeVisible({ timeout: 5_000 });
    expect(sseRequests.length).toBeGreaterThan(0);
  });

  // Pins #118: clicking a <details> summary must toggle the open state and
  // keep the toggled state across datastar's reactive cycle. Before the fix,
  // data-attr:open enforced the (still-false) signal one-way, so the open
  // attribute got reverted by the next pass and clicks no-op'd. The fix is
  // a data-on:toggle handler that writes evt.target.open back into the
  // signal so signal and DOM stay reconciled.
  test("details summary click toggles open state (round-trip)", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    // data-block="info" sits on the wrap div (not the <details>) since #32.
    const info = card.locator('.device-info-wrap > details.device-info');
    // Defaults closed (initialCardSignals seeds detailsOpen.info=false).
    await expect(info).not.toHaveAttribute("open", "");

    await info.locator("summary").click();
    await expect(info).toHaveAttribute("open", "");

    await info.locator("summary").click();
    await expect(info).not.toHaveAttribute("open", "");
  });

  // Pins #191: opening the theme picker reveals an SSE debug block whose
  // values come from page-level $debug signals bumped by document-level
  // listeners on datastar-patch-elements / datastar-fetch.
  test("theme-picker popout shows live SSE debug rows (closes #191)", async ({ page }) => {
    await reset(DEVICE);
    await loadCard(page);
    const debug = page.locator(".sse-debug");
    // Hidden while picker is closed (display:none on collapsed <details>).
    await expect(debug).toBeHidden();

    await page.locator(".theme-picker > summary").click();
    await expect(debug).toBeVisible();

    // datastar-fetch:started on /ui/sse drives streamOpen=true; the
    // initial-state datastar-patch-elements (one per device) bumps events.
    await expect(debug.locator('dd').filter({ hasText: 'open' })).toBeVisible();
    await expect(debug.locator('dd').filter({ hasText: /^\d+$/ })).toBeVisible();
    await expect(debug.locator('dd').filter({ hasText: /^\d+s ago$/ })).toBeVisible();
  });
});

test.describe("SSE push", () => {
  test("device change in fakedevice → card updates without reload", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    // Start in regen mode.
    await presets.asMode(DEVICE, "regeneration");
    await expect(card).toHaveAttribute("data-airflow-mode", "regeneration", {
      timeout: POLL_PUSH_TIMEOUT,
    });

    // Mutate the underlying device. The poller picks it up; the SSE
    // stream patches the card.
    await presets.asMode(DEVICE, "extract");
    await expect(card).toHaveAttribute("data-airflow-mode", "extract", {
      timeout: POLL_PUSH_TIMEOUT,
    });
  });

  test("cross-tab: action in tab A reflects in tab B", async ({ browser }) => {
    await reset(DEVICE);
    const ctxA = await browser.newContext();
    const ctxB = await browser.newContext();
    const pageA = await ctxA.newPage();
    const pageB = await ctxB.newPage();

    await pageA.goto("/");
    await pageB.goto("/");
    const cardA = pageA.getByTestId(`card-${DEVICE}`);
    const cardB = pageB.getByTestId(`card-${DEVICE}`);
    await expect(cardA).toBeVisible({ timeout: 10_000 });
    await expect(cardB).toBeVisible({ timeout: 10_000 });

    // Drive a state change from outside and assert tab B sees it.
    await presets.asMode(DEVICE, "supply");
    await expect(cardA).toHaveAttribute("data-airflow-mode", "supply", {
      timeout: POLL_PUSH_TIMEOUT,
    });
    await expect(cardB).toHaveAttribute("data-airflow-mode", "supply", {
      timeout: POLL_PUSH_TIMEOUT,
    });

    await ctxA.close();
    await ctxB.close();
  });
});

// Scope of this `controls` block: behaviors that genuinely require a real
// browser. Click→@post→SSE-push round-trips for *every* control button
// (power, mode, heater, timer, reset-filter, reset-faults) used to live
// here as one test per endpoint. They were demoted per #185 / #36 Phase 4
// because:
//
// - Endpoint contracts are pinned by Go tests in handlers_ui_write_test.go
//   (TestUIWritePower_Happy, TestUIWriteMode_Happy, TestUIWriteHeater_Happy,
//   TestUIWriteTimer_Happy, TestUIWriteResetFilter_Happy,
//   TestUIWriteResetFaults_Happy, TestUIWriteAction_NotFound).
// - Render contracts (button presence, aria-pressed, disabled-when-stale)
//   are pinned by render_test.go (TestRenderControlsBlock_StaleDisablesEveryControl,
//   TestRenderControls_NoColonFormDataBind, golden_healthy/golden_stale).
// - The remaining E2E-unique question — "do click handlers fire in the
//   browser at all" — is covered by the preset chip / slider drag /
//   manual slider tests below, all of which would fail catastrophically
//   if data-on:click → @post wiring broke globally.
//
// What stays here: behaviors that pin browser-side state or library
// quirks (the $editor signal flip on chip toggle; the debounce timing
// on slider drag; the datastar lowercase-bind regression that drove
// #116). These are not unit-testable.
test.describe("controls", () => {
  test("preset chip: click opens editor, second click closes it", async ({ page }) => {
    await reset(DEVICE);
    // The seed has timer=turbo (0x0007=02). Speed-chip aria-pressed is
    // gated on $specialMode === 'off' so that an active timer
    // de-highlights the previously-selected speed chip. Clear the timer
    // here so the pressed:true assertion below can match the chip
    // after the click.
    await presets.withTimer(DEVICE, "off");
    // Under the clickAction migration (2026-05-11), the preset editor
    // only opens when re-clicking the already-active preset. Seed
    // preset2 as the active speed so the first click toggles the editor
    // open rather than just selecting the preset.
    await presets.asPresetSpeed(DEVICE, 2);
    const card = await loadCard(page);
    const editor2 = card.locator('[data-preset-editor="2"]');
    await expect(editor2).toBeHidden();
    await card.getByRole("button", { name: "48/49", pressed: true }).click();
    await expect(editor2).toBeVisible({ timeout: 2_000 });
    await card.getByRole("button", { name: "48/49", pressed: true }).click();
    await expect(editor2).toBeHidden({ timeout: 2_000 });
  });

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

  // Catalog B-17: a rapid drag of a slider with `data-on:change__debounce.200ms`
  // should produce exactly one POST, not one per intermediate value. Pins
  // the debounce attribute against accidental removal/relaxation.
  //
  // Targets the preset-2 supply slider; the manual slider's drag-value
  // round-trip is pinned by the dedicated #116 test below.
  test("preset slider drag debounces — one POST per drag", async ({ page }) => {
    await reset(DEVICE);
    // Under the clickAction migration the editor opens only when the
    // clicked preset is already active — seed preset2 active.
    await presets.asPresetSpeed(DEVICE, 2);
    await presets.withTimer(DEVICE, "off");
    const card = await loadCard(page);

    // Open the preset-2 editor (clicking the already-active chip toggles
    // it open).
    await card.getByRole("button", { name: "48/49", pressed: true }).click();
    const editor2 = card.locator('[data-preset-editor="2"]');
    await expect(editor2).toBeVisible({ timeout: 2_000 });
    const supplySlider = editor2.locator('input[name="supply"]');
    await expect(supplySlider).toBeVisible();

    // matchSpeeds defaults true: a supply drag will mirror to extract,
    // and the @post payload carries both sides on each fire. That's
    // fine for this test — we still expect exactly ONE POST after the
    // debounce, and we read .supply from it.

    // Count POSTs to /ui/devices/{name}/preset.
    const presetPosts: Array<{ supply: number; extract: number }> = [];
    page.on("request", (req) => {
      if (
        req.method() === "POST" &&
        req.url().endsWith(`/ui/devices/${DEVICE}/preset`)
      ) {
        try {
          const body = JSON.parse(req.postData() || "{}");
          if (body.preset === 2) {
            presetPosts.push({ supply: body.supply, extract: body.extract });
          }
        } catch {
          // Non-JSON bodies don't pass the filter; ignore.
        }
      }
    });

    // Synthesize a drag: five intermediate values dispatched synchronously
    // within the 200ms debounce window. The preset expression reads
    // evt.target.value directly, so a single `change` event per step is
    // enough to drive the debounced @post.
    await supplySlider.evaluate((el: HTMLInputElement) => {
      for (const v of [20, 35, 50, 65, 80]) {
        el.value = String(v);
        el.dispatchEvent(new Event("change", { bubbles: true }));
      }
    });

    // Wait for the daemon to record the @post on fakedevice. The
    // /test/devices/{name}/params/0x003C endpoint reads param 0x003C
    // (preset-2 supply pct) from fakedevice. We poll it until it
    // reflects the dragged value (80=0x50). That confirms the @post
    // fired AND the daemon wrote it through. Any racing extra POSTs
    // would have fired before this one completes.
    await expect
      .poll(() => presetPosts.length, { timeout: POLL_PUSH_TIMEOUT })
      .toBeGreaterThanOrEqual(1);

    // Give any racing extras a chance to land. The debounce window is
    // 200ms; once the first POST has landed, the listener has already
    // received any earlier-fired requests (Playwright preserves
    // request/response event order). A short positive-signal wait —
    // for the SSE-patched extract value to mirror — ensures we're past
    // the debounce window of any plausibly-racing extras.
    const extractSlider = editor2.locator('input[name="extract"]');
    await expect(extractSlider).toHaveValue("80", { timeout: POLL_PUSH_TIMEOUT });

    // Exactly one POST, carrying the final dragged value.
    expect(presetPosts).toHaveLength(1);
    expect(presetPosts[0].supply).toBe(80);
  });

  // Pins #116: the manual slider's @post must carry the value the user
  // dragged to, not the value frozen in a stale $manualPct signal at
  // initial render. Pre-fix, `data-bind:manualPct` was lowercased by the
  // HTML parser to `data-bind:manualpct`, autocreating a separate
  // `$manualpct` signal. The user's input updated the lowercased signal;
  // the @post expression read camelCase `$manualPct`, frozen at the
  // initial-render seed. The fix drops the bind entirely and reads
  // `evt.target.valueAsNumber` in the @post — the input's own value is
  // the source of truth.
  // Pins #1 (regression): clicking a preset chip while in manual mode
  // must close the MODE / manual-slider pane. The pane visibility used
  // to be server-rendered, so when the preset click opened the editor
  // (setting data-edit="true") the controls-block HTML patch was
  // filtered and the manual pane stayed in the DOM. Fix is a client-
  // side data-show keyed on the live $speedMode / $specialMode signals.
  test("preset chip click hides manual pane via data-show", async ({ page }) => {
    await reset(DEVICE);
    await presets.asManualSpeed(DEVICE, 50);
    await presets.withTimer(DEVICE, "off");
    const card = await loadCard(page);

    const manualPane = card.locator(".manual-pane");
    await expect(manualPane).toBeVisible({ timeout: POLL_PUSH_TIMEOUT });

    // Click any preset chip — it opens the editor (data-edit="true")
    // which filters the controls-block patch. The data-show should
    // hide the pane based on the speedMode signal flipping to preset.
    await card.getByRole("button", { name: "48/49" }).click();
    await expect(manualPane).toBeHidden({ timeout: POLL_PUSH_TIMEOUT });
  });

  // Pins #3 (regression): with match-speeds enabled, dragging the
  // supply slider must mirror to the extract slider live (on the
  // `input` event), not only on release via the debounced `change`
  // handler. Pre-fix the mirror ran inside presetSliderExpr (the
  // change handler), so the extract slider visually lagged a full
  // release behind the supply slider during the drag.
  test("match-speeds mirrors live during slider input (not just on change)", async ({ page }) => {
    await reset(DEVICE);
    // Editor opens only on re-click of the already-active preset.
    await presets.asPresetSpeed(DEVICE, 2);
    await presets.withTimer(DEVICE, "off");
    const card = await loadCard(page);

    await card.getByRole("button", { name: "48/49", pressed: true }).click();
    const editor2 = card.locator('[data-preset-editor="2"]');
    await expect(editor2).toBeVisible({ timeout: 2_000 });
    const supplySlider = editor2.locator('input[name="supply"]');
    const extractSlider = editor2.locator('input[name="extract"]');

    // matchSpeeds defaults true. Dispatch ONLY an input event (no
    // change) and assert the mirror happened. If the implementation
    // mirrored only on change, this assertion fails.
    await supplySlider.evaluate((el: HTMLInputElement) => {
      el.value = "70";
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await expect(extractSlider).toHaveValue("70", { timeout: 1_000 });
  });

  // Pins the slider's clamp pathway. Range inputs natively constrain
  // values to [min, max] on user drag, but JS-set values bypass that
  // (browser engines differ on whether `el.value = "5"` with min=10 is
  // auto-clamped or accepted verbatim). manualChangeExpr's clamp
  // catches the JS-set case so a programmatic write — or a future
  // regression that loosens the slider's min — can't reach the wire.
  test("manual slider clamps below-min values to 10", async ({ page }) => {
    await reset(DEVICE);
    await presets.asManualSpeed(DEVICE, 50);
    await presets.withTimer(DEVICE, "off");
    const card = await loadCard(page);

    const manualSlider = card.locator('input[name="manual"]');
    await expect(manualSlider).toBeVisible({ timeout: POLL_PUSH_TIMEOUT });

    const manualPosts: number[] = [];
    page.on("request", (req) => {
      if (req.method() === "POST" && req.url().endsWith(`/ui/devices/${DEVICE}/speed`)) {
        try {
          const body = JSON.parse(req.postData() || "{}");
          if (typeof body.manual === "number") manualPosts.push(body.manual);
        } catch {}
      }
    });

    await manualSlider.evaluate((el: HTMLInputElement) => {
      el.value = "5";
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });

    await expect
      .poll(() => manualPosts.length, { timeout: POLL_PUSH_TIMEOUT })
      .toBeGreaterThanOrEqual(1);
    expect(manualPosts[0]).toBe(10);
  });

  // Same clamp pathway for the preset editor's range sliders. Supply
  // bound is [0, 100]; presetSliderExpr's NaN/clamp/snap-to-zero
  // preamble guards JS-set out-of-range values.
  test("preset slider clamps above-max values to 100", async ({ page }) => {
    await reset(DEVICE);
    // Editor opens only on re-click of the already-active preset.
    await presets.asPresetSpeed(DEVICE, 2);
    await presets.withTimer(DEVICE, "off");
    const card = await loadCard(page);

    await card.getByRole("button", { name: "48/49", pressed: true }).click();
    const editor2 = card.locator('[data-preset-editor="2"]');
    await expect(editor2).toBeVisible({ timeout: 2_000 });
    const supplySlider = editor2.locator('input[type="range"][name="supply"]');

    const presetPosts: Array<{ supply: number; extract: number }> = [];
    page.on("request", (req) => {
      if (req.method() === "POST" && req.url().endsWith(`/ui/devices/${DEVICE}/preset`)) {
        try {
          const body = JSON.parse(req.postData() || "{}");
          if (body.preset === 2) presetPosts.push({ supply: body.supply, extract: body.extract });
        } catch {}
      }
    });

    await supplySlider.evaluate((el: HTMLInputElement) => {
      el.value = "150";
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });

    await expect
      .poll(() => presetPosts.length, { timeout: POLL_PUSH_TIMEOUT })
      .toBeGreaterThanOrEqual(1);
    expect(presetPosts[0].supply).toBe(100);
  });

  // Pins the fan-pct clamp: HTML5 number inputs don't enforce min/max
  // on free-typed values (the attrs only fire on form-submit, and the
  // pct input isn't in a form). Without the manualChangeExpr clamp
  // a value of "5" reached the wire, the server 422'd, and the input
  // displayed "5" until the next poll. The clamp now snaps the input
  // and the signal up to min before posting.
  test("manual input clamps below-min values to 10", async ({ page }) => {
    await reset(DEVICE);
    await presets.asManualSpeed(DEVICE, 50);
    await presets.withTimer(DEVICE, "off");
    const card = await loadCard(page);

    const manualInput = card.locator('.fan-slider-row input[type="number"]');
    await expect(manualInput).toBeVisible({ timeout: POLL_PUSH_TIMEOUT });

    const manualPosts: number[] = [];
    page.on("request", (req) => {
      if (req.method() === "POST" && req.url().endsWith(`/ui/devices/${DEVICE}/speed`)) {
        try {
          const body = JSON.parse(req.postData() || "{}");
          if (typeof body.manual === "number") manualPosts.push(body.manual);
        } catch {}
      }
    });

    await manualInput.evaluate((el: HTMLInputElement) => {
      el.value = "5";
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });

    await expect
      .poll(() => manualPosts.length, { timeout: POLL_PUSH_TIMEOUT })
      .toBeGreaterThanOrEqual(1);
    expect(manualPosts[0]).toBe(10);
    await expect(manualInput).toHaveValue("10");
  });

  test("manual slider drag posts dragged value (closes #116)", async ({ page }) => {
    await reset(DEVICE);
    // Force the slider to render: speed_mode=manual + special-mode=off
    // (the snapshot's default timer=turbo suppresses the manual slider
    // via ControlsBlock's `SpecialMode == "off"` guard).
    await presets.asManualSpeed(DEVICE, 50);
    await presets.withTimer(DEVICE, "off");
    const card = await loadCard(page);

    const manualSlider = card.locator('input[name="manual"]');
    await expect(manualSlider).toBeVisible({ timeout: POLL_PUSH_TIMEOUT });

    const manualPosts: number[] = [];
    page.on("request", (req) => {
      if (
        req.method() === "POST" &&
        req.url().endsWith(`/ui/devices/${DEVICE}/speed`)
      ) {
        try {
          const body = JSON.parse(req.postData() || "{}");
          if (typeof body.manual === "number") {
            manualPosts.push(body.manual);
          }
        } catch {
          // Non-JSON / non-manual bodies don't pass the filter; ignore.
        }
      }
    });

    // Drag to 75: set value, dispatch change. The @post reads
    // evt.target.valueAsNumber directly.
    await manualSlider.evaluate((el: HTMLInputElement) => {
      el.value = "75";
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });

    await expect
      .poll(() => manualPosts.length, { timeout: POLL_PUSH_TIMEOUT })
      .toBeGreaterThanOrEqual(1);
    expect(manualPosts[0]).toBe(75);
  });
});

test.describe("schedule editor", () => {
  // Pins the inline-edit autosave path. Focus a pct cell, type a new
  // value, blur — exactly one PUT goes out and the daemon's schedule
  // snapshot reflects the new value.
  test("edit pct field → autosave PUT persists the value", async ({ page }) => {
    await reset(DEVICE);
    await presets.withSchedule(DEVICE, {
      enabled: true,
      entries: [{ at: "08:00", action: "regeneration", pct: 60 }],
    });
    const card = await loadCard(page);
    await card.locator('details[data-block="schedule"] > summary').click();
    const details = card.locator('details[data-block="schedule"]');
    await expect(details).toHaveAttribute("data-edit", "true", { timeout: 2_000 });

    const pctInput = details.locator('input[name="pct"]');
    await expect(pctInput).toHaveCount(1);

    const putRequests: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "PUT" && req.url().endsWith(`/ui/devices/${DEVICE}/schedule`)) {
        putRequests.push(req.postData() || "");
      }
    });

    await pctInput.fill("77");
    await pctInput.blur();

    await expect.poll(() => putRequests.length, { timeout: POLL_PUSH_TIMEOUT })
      .toBeGreaterThanOrEqual(1);
    expect(putRequests[0]).toContain("pct=77");
  });

  // Pins #6 (the previous auto-seed) replacement: clicking + adds a
  // client-side row, but no PUT fires until the user edits a field.
  // Avoids creating phantom 08:00 entries on misclicks.
  test("+ add row adds a tr but no PUT until field commit", async ({ page }) => {
    await reset(DEVICE);
    await presets.withSchedule(DEVICE, { enabled: false, entries: [] });
    const card = await loadCard(page);
    await card.locator('details[data-block="schedule"] > summary').click();
    const details = card.locator('details[data-block="schedule"]');

    const putRequests: string[] = [];
    page.on("request", (req) => {
      if (req.method() === "PUT" && req.url().endsWith(`/ui/devices/${DEVICE}/schedule`)) {
        putRequests.push(req.url());
      }
    });

    await card.getByRole("button", { name: "+ add row" }).click();
    // After the SSE patch lands, exactly one row exists in the table body.
    await expect(details.locator('tbody.schedule-edit-tbody tr')).toHaveCount(1, {
      timeout: POLL_PUSH_TIMEOUT,
    });

    // Give a generous window for an unwanted autosave to fire. None should.
    await page.waitForTimeout(500);
    expect(putRequests).toHaveLength(0);
  });
});

test.describe("threshold editor", () => {
  test("click → edit → save → cell re-renders via SSE patch", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    // Sensors defaults open via $detailsOpen.sensors=true signal seed; assert
    // rather than defensively toggle (see #82).
    const sensors = card.locator('details[data-block="sensors"]');
    await expect(sensors).toHaveAttribute("open", "");
    const cell = card.locator('[data-threshold-cell="humidity"]');
    await cell.locator(".value-clickable").click();
    const input = cell.getByRole("spinbutton");
    await expect(input).toBeVisible({ timeout: 2_000 });
    await input.fill("65");
    await cell.getByRole("button", { name: "✓" }).click();
    // Wait for the read-variant to come back via SSE patch.
    await expect(cell.locator(".value-clickable")).toBeVisible({
      timeout: POLL_PUSH_TIMEOUT,
    });
  });
});

test.describe("error paths", () => {
  test("auth failure surfaces in the global error banner", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    await simulateAuthFailure(DEVICE, true);
    try {
      // Click any action; the SSE error envelope should land in the banner.
      await card.getByRole("button", { name: "power" }).click();
      const banner = page.locator("#global-error-banner");
      await expect(banner).toContainText(/auth/i, { timeout: 4_000 });
    } finally {
      await simulateAuthFailure(DEVICE, false);
    }
  });

  test("UDP timeout surfaces in the global error banner", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    await simulateUDPTimeout(DEVICE, true);
    try {
      await card.getByRole("button", { name: "power" }).click();
      const banner = page.locator("#global-error-banner");
      // handlerOpTimeout is 5s in the daemon; the request blocks for the
      // full timeout before the SSE banner event lands. 12s gives enough
      // headroom that ordering with prior tests doesn't tip us into flake.
      await expect(banner).toContainText(/err-banner|timeout|i\/o/i, {
        timeout: 12_000,
      });
    } finally {
      await simulateUDPTimeout(DEVICE, false);
    }
  });
});

test.describe("reconnect", () => {
  test("EventSource reconnects after a forced close", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    // Force-close any open EventSources via the page's window.
    // datastar uses fetch+streams under the hood; an explicit close is
    // not exposed, so we instead trigger a navigation reload and assert
    // recovery.
    await page.reload();
    await expect(card).toBeVisible({ timeout: 10_000 });
    // Drive a fakedevice change; SSE on the reloaded page must deliver
    // it to confirm the channel is alive.
    await presets.asMode(DEVICE, "ventilation");
    await expect(card).toHaveAttribute("data-airflow-mode", "ventilation", {
      timeout: POLL_PUSH_TIMEOUT,
    });
  });

  // Catalog B-32 (#36): on reconnect the dashboard must not show
  // duplicate cards. The handler differentiates cold-load vs reconnect
  // via Last-Event-ID (see handlers_ui_sse.go::emitInitialCard) — cold
  // load uses mode=append against #device-list; reconnect uses
  // mode=outer against .card[data-device=...] to replace in-place.
  //
  // This test asserts the cold-load path stays at exactly one card per
  // device across page reloads. The reconnect-with-Last-Event-ID path
  // is harder to drive end-to-end (datastar's AbortController for the
  // SSE stream is not exposed; we'd need to intercept the fetch
  // response body to force a mid-stream disconnect), and is already
  // pinned server-side by Go tests:
  //   - TestGetUISSE_ReconnectUsesOuterMode (with Last-Event-ID set)
  //   - TestGetUISSE_ColdLoadUsesAppendMode (without)
  // so the wire contract is verified at unit-test speed.
  test("page reload does not duplicate the device card", async ({ page }) => {
    await reset(DEVICE);
    await loadCard(page);
    await expect(page.getByTestId(`card-${DEVICE}`)).toHaveCount(1);

    // Reload — second cold load against the same DOM does NOT happen
    // (each navigation is a fresh document) but this is the closest
    // user-observable behavior to the "stale tab kept open" pattern
    // that started prompting the catalog gap.
    await page.reload();
    await expect(page.getByTestId(`card-${DEVICE}`)).toHaveCount(1);
  });
});

test.describe("editor preservation across polls (#65)", () => {
  test("schedule editor survives multiple polls with typed pct intact", async ({ page }) => {
    await reset(DEVICE);
    await presets.withSchedule(DEVICE, {
      enabled: true,
      entries: [{ at: "08:00", action: "regeneration", pct: 60 }],
    });
    const card = await loadCard(page);

    await card.locator('details[data-block="schedule"] > summary').click();
    const details = card.locator('details[data-block="schedule"]');
    await expect(details).toHaveAttribute("data-edit", "true", { timeout: 2_000 });

    const pctInput = details.locator('input[name="pct"]');
    await expect(pctInput).toHaveCount(1);
    await pctInput.fill("77");
    // Don't blur yet — we want to assert the typed value survives polls
    // while the input still has focus (the autosave hasn't fired yet,
    // and data-edit="true" filters incoming patches).

    await assertStableAcrossPolls(page, async () => {
      await expect(details).toBeVisible({ timeout: 200 });
      await expect(pctInput).toHaveValue("77", { timeout: 200 });
    });
  });

  test("threshold editor (co2) survives multiple polls with typed value intact", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);

    // Sensors defaults open via $detailsOpen.sensors=true signal seed; no
    // summary click needed.

    // Click the eCO2 value to enter edit mode (@get fetches the edit variant,
    // which patches [data-sensor-cell="co2"] with the edit version that has
    // data-edit="true" on the outer div).
    const co2Cell = card.locator('[data-threshold-cell="co2"]');
    await co2Cell.locator(".value-clickable").click();

    // After the SSE patch, the cell itself carries data-edit="true".
    // Wait for the edit form to appear (the patched cell now has the spinbutton).
    const input = card.locator('[data-threshold-cell="co2"]').getByRole("spinbutton");
    await expect(input).toBeVisible({ timeout: 4_000 });
    await input.fill("1234");

    // Confirm the edit marker is on the cell.
    const editCell = card.locator('[data-threshold-cell="co2"][data-edit="true"]');
    await expect(editCell).toBeVisible();

    // Same continuously-asserted-invariant pattern as the schedule case;
    // see the comment there and #81 for the rationale.
    await assertStableAcrossPolls(page, async () => {
      await expect(editCell).toBeVisible({ timeout: 200 });
      await expect(input).toHaveValue("1234", { timeout: 200 });
    });
  });

  test("preset editor slider value survives multiple polls", async ({ page }) => {
    await reset(DEVICE);
    // Editor opens only on re-click of the already-active preset.
    await presets.asPresetSpeed(DEVICE, 2);
    await presets.withTimer(DEVICE, "off");
    const card = await loadCard(page);

    // Open the preset-2 editor by clicking its chip. Preset-2 chip text is
    // "48/49" (snapshot_148.json: 0x003C=0x30=48, 0x003D=0x31=49).
    // Use the same role-by-name selector as the existing preset-chip test.
    await card.getByRole("button", { name: "48/49", pressed: true }).click();

    const editor2 = card.locator('[data-preset-editor="2"]');
    await expect(editor2).toBeVisible({ timeout: 2_000 });

    // The supply slider is identifiable by name="supply" — a stable form
    // field attribute, no .first() (#83).
    const supplySlider = editor2.locator('input[name="supply"]');
    await expect(supplySlider).toBeVisible();

    // Set slider value AND dispatch change so the @post-driven server
    // round-trip persists the value (post-#72 the editor is signal-driven;
    // skipping the change event would let the next poll's data-signals
    // patch reseed $preset.2.supply to the server's stored value).
    await supplySlider.evaluate((el: HTMLInputElement) => {
      el.value = "85";
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });

    // Confirm the slider holds the new value (after the debounced @post +
    // server poll round-trip).
    await expect(supplySlider).toHaveValue("85", { timeout: POLL_PUSH_TIMEOUT });

    // Same continuously-asserted-invariant pattern as the schedule case;
    // see the comment there and #81 for the rationale.
    await assertStableAcrossPolls(page, async () => {
      await expect(editor2).toBeVisible({ timeout: 200 });
      await expect(supplySlider).toHaveValue("85", { timeout: 200 });
    });
  });

  // Verifies SPECIFICATION-web.md "Card states": after the stale window
  // (3×poll_interval = 3s with the test daemon's poll_interval=1s) of
  // failed polls, the card gets the stale class via signal patch
  // without DOM replacement (data-testTag survives).
  test("stale class applied via signal patch preserves card identity", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    await card.evaluate((el) => { (el as HTMLElement).dataset.testTag = "marker-1"; });
    await simulateUDPTimeout(DEVICE, true);
    try {
      await expect(card).toHaveClass(/stale/, { timeout: 8_000 });
      const stillTagged = await card.evaluate((el) => (el as HTMLElement).dataset.testTag);
      expect(stillTagged).toBe("marker-1");
    } finally {
      await simulateUDPTimeout(DEVICE, false);
    }
  });
});
