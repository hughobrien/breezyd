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
  const card = page.locator(`.card[data-device="${name}"]`);
  await expect(card).toBeVisible({ timeout: 10_000 });
  return card;
}

// pollInterval matches the test daemon's --poll-interval=1s. Two
// intervals plus a buffer is plenty for the next push to land after a
// fakedevice mutation.
const POLL_PUSH_TIMEOUT = 4_000;

test.describe("rendering", () => {
  test("@smoke card renders for the configured device", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    await expect(card.locator("h2")).toContainText(DEVICE);
  });

  test("layout loads datastar, not htmx", async ({ page }) => {
    const resp = await page.goto("/");
    expect(resp?.status()).toBe(200);
    const html = await page.content();
    expect(html).toContain("datastar-1.0.1.min.js");
    expect(html).toContain('data-init="@get(\'/ui/sse\')"');
    expect(html).not.toMatch(/htmx-\d/);
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
    await expect(page.locator(".card")).toBeVisible({ timeout: 5_000 });
    expect(sseRequests.length).toBeGreaterThan(0);
  });

  test("sensor block surfaces live values", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    // Sensors defaults open via $detailsOpen.sensors=true signal seed.
    // Assert rather than defensively toggle — if the default ever flips
    // to false, this fails loudly here instead of silently masking a
    // closed-block test elsewhere.
    const sensors = card.locator("details.sensors");
    await expect(sensors).toHaveAttribute("open", "");
    await expect(card).toContainText("54%");
    await expect(card).toContainText("1175 ppm");
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
    const cardA = pageA.locator(`.card[data-device="${DEVICE}"]`);
    const cardB = pageB.locator(`.card[data-device="${DEVICE}"]`);
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

test.describe("controls", () => {
  test("power toggle: button click switches state and pushes new card", async ({ page }) => {
    await reset(DEVICE);
    await presets.asPowerOn(DEVICE);
    const card = await loadCard(page);
    const power = card.locator('button.toggle-inline[aria-pressed="true"]');
    await expect(power).toBeVisible({ timeout: POLL_PUSH_TIMEOUT });
    await power.click();
    await expect(
      card.locator('button.toggle-inline[aria-pressed="false"]'),
    ).toBeVisible({ timeout: POLL_PUSH_TIMEOUT });
  });

  test("mode chip: click triggers mode change", async ({ page }) => {
    await reset(DEVICE);
    await presets.asPresetSpeed(DEVICE, 1);
    await presets.asMode(DEVICE, "regeneration");
    await presets.withTimer(DEVICE, "off");
    const card = await loadCard(page);
    // Switch to manual + click "supply" mode chip.
    await card.locator('button[aria-pressed="false"]:text-is("manual")').click();
    await expect(card).toHaveAttribute("data-speed-mode", "manual", {
      timeout: POLL_PUSH_TIMEOUT,
    });
    await card.locator('button:text-is("supply")').click();
    await expect(card).toHaveAttribute("data-airflow-mode", "supply", {
      timeout: POLL_PUSH_TIMEOUT,
    });
  });

  test("preset chip: click opens editor, second click closes it", async ({ page }) => {
    await reset(DEVICE);
    await presets.asPresetSpeed(DEVICE, 1);
    const card = await loadCard(page);
    const editor2 = card.locator('[data-preset-editor="2"]');
    await expect(editor2).toBeHidden();
    await card.locator('button:text-is("48/49")').click();
    await expect(editor2).toBeVisible({ timeout: 2_000 });
    await card.locator('button[aria-pressed="true"]:text-is("48/49")').click();
    await expect(editor2).toBeHidden({ timeout: 2_000 });
  });
});

test.describe("threshold editor", () => {
  test("click → edit → save → cell re-renders via SSE patch", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    // Sensors defaults open via $detailsOpen.sensors=true signal seed; assert
    // rather than defensively toggle (see #82).
    const sensors = card.locator("details.sensors");
    await expect(sensors).toHaveAttribute("open", "");
    const cell = card.locator('[data-threshold-cell="humidity"]');
    await cell.locator(".value-clickable").click();
    const input = cell.locator(".thresh-input");
    await expect(input).toBeVisible({ timeout: 2_000 });
    await input.fill("65");
    await cell.locator('button[type="submit"]').click();
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
      await card.locator('button.toggle-inline').click();
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
      await card.locator('button.toggle-inline').click();
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
});

test.describe("editor preservation across polls (#65)", () => {
  test("schedule editor survives multiple polls with typed pct intact", async ({ page }) => {
    await reset(DEVICE);
    await presets.withSchedule(DEVICE, {
      enabled: true,
      entries: [{ at: "08:00", action: "regeneration", pct: 60 }],
    });
    const card = await loadCard(page);

    // Open the schedule <details> by updating the datastar signal directly.
    // A summary click alone would toggle `open`, but the MutationObserver
    // bound by data-attr:open="$detailsOpen.schedule" immediately re-evaluates
    // the (still-false) signal and removes the open attribute again.
    // Importing the module and calling mergePaths updates the reactive store,
    // so the binding re-evaluates to true and keeps the details open.
    await page.evaluate(async () => {
      const { mergePaths } = await import("/ui/vendor/datastar-1.0.1.min.js");
      mergePaths([["detailsOpen.schedule", true]]);
    });

    // Click "edit schedule" to enter edit mode.
    const editBtn = card.locator('button:text-is("edit schedule")');
    await expect(editBtn).toBeVisible({ timeout: 2_000 });
    await editBtn.click();

    // The edit variant replaces the block with data-edit="true".
    const editDetails = card.locator('details.schedule[data-edit="true"]');
    await expect(editDetails).toBeVisible({ timeout: 2_000 });

    // The test pre-loaded exactly one schedule entry — assert the locator
    // resolves to one row instead of using .first() (which would hide a
    // row-count regression). See #83.
    const pctInput = editDetails.locator('input[name="pct"]');
    await expect(pctInput).toHaveCount(1);
    await expect(pctInput).toBeVisible();
    await pctInput.fill("77");

    // Wait through 3 poll intervals (3s). The per-block patch uses
    // :not([data-edit]) so an open editor must not be replaced.
    await page.waitForTimeout(3_000);

    // Form and typed value must still be present.
    await expect(editDetails).toBeVisible();
    await expect(pctInput).toHaveValue("77");
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
    // Wait for the edit form to appear (the patched cell now has .thresh-input).
    const input = card.locator('[data-threshold-cell="co2"] .thresh-input');
    await expect(input).toBeVisible({ timeout: 4_000 });
    await input.fill("1234");

    // Confirm the edit marker is on the cell.
    const editCell = card.locator('[data-threshold-cell="co2"][data-edit="true"]');
    await expect(editCell).toBeVisible();

    // Wait through 3 poll intervals (3s). The sensor-cell patch uses
    // :not([data-edit]) so an open threshold editor must not be replaced.
    await page.waitForTimeout(3_000);

    // The edit cell and typed value must still be present.
    await expect(editCell).toBeVisible();
    await expect(input).toHaveValue("1234");
  });

  test("preset editor slider value survives multiple polls", async ({ page }) => {
    await reset(DEVICE);
    await presets.asPresetSpeed(DEVICE, 1);
    const card = await loadCard(page);

    // Open the preset-2 editor by clicking its chip. Preset-2 chip text is
    // "48/49" (snapshot_148.json: 0x003C=0x30=48, 0x003D=0x31=49).
    // Use the same text selector as the existing preset-chip test for reliability.
    await card.locator('button:text-is("48/49")').click();

    const editor2 = card.locator('[data-preset-editor="2"]');
    await expect(editor2).toBeVisible({ timeout: 2_000 });

    // The supply slider is identifiable by data-side rather than DOM
    // position — using .first() would silently flip to the extract slider
    // if the order ever changed. See #83.
    const supplySlider = editor2.locator('[data-side="supply"]');
    await expect(supplySlider).toBeVisible();

    // Set slider value via JS (avoid dispatching a change event that would
    // trigger a server write and reset the value from the server response).
    await supplySlider.evaluate((el: HTMLInputElement) => {
      el.value = "85";
    });

    // Confirm the JS set took effect before the wait.
    await expect(supplySlider).toHaveValue("85");

    // Wait through 3 poll intervals (3s). The controls block is guarded by
    // data-attr:data-edit="$editor !== 0 ? 'true' : null"; the per-block SSE
    // selector excludes [data-edit] from replacement.
    await page.waitForTimeout(3_000);

    // Editor still visible; slider value preserved.
    await expect(editor2).toBeVisible();
    await expect(supplySlider).toHaveValue("85");
  });

  // Skipped: requires stale threshold <90s. The daemon hardcodes 90s
  // (cmd/breezyd/ui_view.go) and the test config does not override it.
  // The signal-driven stale patch is covered by the Go push_hub test.
  test.skip("stale class applied via signal patch preserves card identity", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);

    // Tag the card so we can confirm it was NOT re-rendered.
    await card.evaluate((el) => { (el as HTMLElement).dataset.testTag = "marker-1"; });

    await simulateUDPTimeout(DEVICE, true);
    try {
      // The daemon marks stale after 90s without a successful poll.
      // 100s timeout; this test is skip'd because it's impractically slow.
      await expect(card).toHaveClass(/stale/, { timeout: 100_000 });
      const stillTagged = await card.evaluate((el) => (el as HTMLElement).dataset.testTag);
      expect(stillTagged).toBe("marker-1");
    } finally {
      await simulateUDPTimeout(DEVICE, false);
    }
  });
});
