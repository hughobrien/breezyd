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
// Tests therefore wait for the card to *change* rather than for an
// htmx swap to complete.
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
    expect(html).toContain('data-on-load="@get(\'/ui/sse\')"');
    expect(html).not.toMatch(/htmx-\d/);
  });

  test("sensor block surfaces live values", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    const sensors = card.locator("details.sensors");
    if (!(await sensors.evaluate((el) => (el as HTMLDetailsElement).open))) {
      await sensors.locator("summary").click();
    }
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
    await card.locator('button:text-is("55/60")').click();
    await expect(editor2).toBeVisible({ timeout: 2_000 });
    await card.locator('button[aria-pressed="true"]:text-is("55/60")').click();
    await expect(editor2).toBeHidden({ timeout: 2_000 });
  });
});

test.describe("threshold editor", () => {
  test("click → edit → save → cell re-renders via SSE patch", async ({ page }) => {
    await reset(DEVICE);
    const card = await loadCard(page);
    const sensors = card.locator("details.sensors");
    if (!(await sensors.evaluate((el) => (el as HTMLDetailsElement).open))) {
      await sensors.locator("summary").click();
    }
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
      await expect(banner).toContainText(/err-banner|timeout|i\/o/i, {
        timeout: 4_000,
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
