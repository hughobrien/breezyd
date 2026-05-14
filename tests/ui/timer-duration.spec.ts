// SPDX-License-Identifier: GPL-3.0-or-later

// timer-duration.spec.ts — Playwright coverage for the duration editor.
//
// Re-click on an active night/turbo chip opens a centred two-input
// editor; commits write 0x0302/0x0303 and the firmware restarts the
// running countdown on its own (verified during design — see
// docs/superpowers/specs/2026-05-14-timer-duration-editor-design.md).
// Closure rides a single data-effect on the .ctrl-group-timer
// container, so deactivation, mode-switch, and power-off all close
// the editor without per-handler changes.

import { test, expect, Page, Locator } from "@playwright/test";
import { reset, presets, setDeviceState } from "./fixtures.js";

const DEVICE = "alpha";

// pollInterval matches the test daemon's --poll-interval=1s. Two
// intervals plus a buffer is plenty for the next push to land after a
// fakedevice mutation.
const POLL_PUSH_TIMEOUT = 4_000;

async function loadCard(page: Page, name = DEVICE): Promise<Locator> {
  await page.goto("/");
  const card = page.getByTestId(`card-${name}`);
  await expect(card).toBeVisible({ timeout: 10_000 });
  return card;
}

// Strict-mode-safe locator for one editor (filtered by which mode's
// data-bind it carries). The page has TWO `.timer-duration-editor`
// divs per card (night + turbo); we pick by the per-mode signal path.
function editorFor(card: Locator, mode: "night" | "turbo"): Locator {
  return card
    .locator(".timer-duration-editor")
    .filter({ has: card.page().locator(`input[data-bind*="${mode}.hours"]`) });
}

test.describe("timer duration editor", () => {
  test("night chip: first click activates, re-click opens centred editor", async ({ page }) => {
    await reset(DEVICE);
    await presets.withTimer(DEVICE, "off");
    await presets.withDuration(DEVICE, "night", 8, 0);
    const card = await loadCard(page);
    const nightChip = card.getByRole("button", { name: "night" });
    const editor = editorFor(card, "night");

    await expect(nightChip).toHaveAttribute("aria-pressed", "false");
    await expect(editor).toBeHidden();

    // First click: activate night timer. Editor stays closed.
    await nightChip.click();
    await expect(nightChip).toHaveAttribute("aria-pressed", "true", {
      timeout: POLL_PUSH_TIMEOUT,
    });
    await expect(editor).toBeHidden();

    // Second click: re-click opens the editor.
    await nightChip.click();
    await expect(editor).toBeVisible();
    await expect(editor.locator('input[data-bind$="night.hours"]')).toHaveValue("8");
    await expect(editor.locator('input[data-bind$="night.minutes"]')).toHaveValue("0");
  });

  test("editing inputs posts new duration and snaps countdown", async ({ page }) => {
    await reset(DEVICE);
    await presets.withTimer(DEVICE, "night");
    // Seed 0x000B (remaining seconds) to 1h 30m (5400s, 3-byte LE = 001e01)
    // so the SSE push also shows 1h 30m — ensuring the countdown assertion
    // doesn't race against the server overwriting the client-side snap.
    await setDeviceState(DEVICE, { "000B": "001e01" });
    const card = await loadCard(page);
    const nightChip = card.getByRole("button", { name: "night" });
    await expect(nightChip).toHaveAttribute("aria-pressed", "true");

    // Open the editor via re-click.
    await nightChip.click();
    const editor = editorFor(card, "night");
    await expect(editor).toBeVisible();

    // Capture POSTs before triggering them.
    const durationPosts: Array<{ mode: string; hours: number; minutes: number }> = [];
    page.on("request", (req) => {
      if (
        req.method() === "POST" &&
        req.url().endsWith(`/ui/devices/${DEVICE}/timer-duration`)
      ) {
        try {
          const body = JSON.parse(req.postData() || "{}");
          durationPosts.push(body);
        } catch {
          // non-JSON; ignore
        }
      }
    });

    // Fill both inputs via evaluate. The data-bind two-way sync runs
    // on "input" events; dispatch "input" first so the signal updates,
    // then "change" to trigger the debounced data-on:change handler.
    const hours = editor.locator('input[data-bind$="night.hours"]');
    const minutes = editor.locator('input[data-bind$="night.minutes"]');
    await hours.evaluate((el: HTMLInputElement) => {
      el.value = "1";
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await minutes.evaluate((el: HTMLInputElement) => {
      el.value = "30";
      el.dispatchEvent(new Event("input", { bubbles: true }));
      el.dispatchEvent(new Event("change", { bubbles: true }));
    });

    // Wait for at least one POST carrying the new duration.
    await expect
      .poll(() => durationPosts.length, { timeout: POLL_PUSH_TIMEOUT })
      .toBeGreaterThanOrEqual(1);

    // The last post should reflect the final values (1h 30m).
    const last = durationPosts[durationPosts.length - 1];
    expect(last.mode).toBe("night");
    expect(last.hours).toBe(1);
    expect(last.minutes).toBe(30);

    // Countdown text reflects the new total. The client-side snap sets
    // $specialModeRemainingSeconds = 5400 (1h 30m); the pre-seeded
    // 000B also ensures the server push agrees, avoiding an SSE race.
    // fmtRemaining formats as "Xh Ym remaining".
    const remaining = card.locator(".timer-remaining");
    await expect(remaining).toContainText(/1h\s*30m remaining/);
  });

  test("third click closes editor; chip stays active", async ({ page }) => {
    await reset(DEVICE);
    await presets.withTimer(DEVICE, "night");
    const card = await loadCard(page);
    const nightChip = card.getByRole("button", { name: "night" });
    await expect(nightChip).toHaveAttribute("aria-pressed", "true");

    // Open.
    await nightChip.click();
    const editor = editorFor(card, "night");
    await expect(editor).toBeVisible();

    // Close via third click.
    await nightChip.click();
    await expect(editor).toBeHidden();
    await expect(nightChip).toHaveAttribute("aria-pressed", "true");
  });

  test("preset chip click closes editor (cascade)", async ({ page }) => {
    await reset(DEVICE);
    await presets.asPresetSpeed(DEVICE, 1);
    await presets.withTimer(DEVICE, "night");
    const card = await loadCard(page);
    const nightChip = card.getByRole("button", { name: "night" });
    await expect(nightChip).toHaveAttribute("aria-pressed", "true");

    // Open editor.
    await nightChip.click();
    const editor = editorFor(card, "night");
    await expect(editor).toBeVisible();

    // Click preset 2 chip (labeled "48/49" from snapshot seed).
    // Preset chip aria-pressed is gated on $specialMode === 'off', so
    // look up by text rather than pressed state while timer is active.
    await card.getByRole("button", { name: "48/49" }).click();

    await expect(editor).toBeHidden();
    await expect(nightChip).toHaveAttribute("aria-pressed", "false", {
      timeout: POLL_PUSH_TIMEOUT,
    });
  });

  test("power-off click closes editor (cascade)", async ({ page }) => {
    await reset(DEVICE);
    await presets.withTimer(DEVICE, "night");
    const card = await loadCard(page);
    const nightChip = card.getByRole("button", { name: "night" });
    await expect(nightChip).toHaveAttribute("aria-pressed", "true");

    // Open editor.
    await nightChip.click();
    const editor = editorFor(card, "night");
    await expect(editor).toBeVisible();

    // Power off — cascades clear $specialMode → data-effect closes editor.
    const powerBtn = card.getByRole("button", { name: "power" });
    await powerBtn.click();
    await expect(editor).toBeHidden();
  });

  test("switching night→turbo while night editor open closes night editor", async ({ page }) => {
    await reset(DEVICE);
    await presets.withTimer(DEVICE, "night");
    const card = await loadCard(page);
    const nightChip = card.getByRole("button", { name: "night" });
    const turboChip = card.getByRole("button", { name: "turbo" });
    await expect(nightChip).toHaveAttribute("aria-pressed", "true");

    // Open night editor.
    await nightChip.click();
    const nightEditor = editorFor(card, "night");
    const turboEditor = editorFor(card, "turbo");
    await expect(nightEditor).toBeVisible();

    // Click turbo chip — switches timer mode; night editor closes via
    // data-effect (durationEditor !== specialMode → durationEditor = 'off').
    await turboChip.click();
    await expect(nightEditor).toBeHidden();
    // Turbo's editor stays closed on first activation click.
    await expect(turboEditor).toBeHidden();
    await expect(turboChip).toHaveAttribute("aria-pressed", "true", {
      timeout: POLL_PUSH_TIMEOUT,
    });
  });
});
