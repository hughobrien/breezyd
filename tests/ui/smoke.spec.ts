// SPDX-License-Identifier: GPL-3.0-or-later

// smoke.spec.ts — Real-daemon smoke test.
//
// Verifies that breezyd (running against the in-process fakedevice spun up
// by global-setup) serves the dashboard and renders at least one device
// card. This test requires the real daemon — it does NOT use page.route()
// mocking.
//
// Tag: @smoke  — run in isolation with:
//   just test-ui -- --grep "@smoke"

import { test, expect } from "@playwright/test";

const DEVICE = "alpha";

test("@smoke daemon serves dashboard with device card", async ({ page }) => {
  // baseURL is set from process.env.BREEZYD_URL by global-setup.
  await page.goto("/");
  // The daemon polls the fakedevice on a 1s interval; allow up to 10s for
  // the first poll to complete and the card to appear.
  await expect(page.getByTestId(`card-${DEVICE}`)).toBeVisible({ timeout: 10_000 });
});
