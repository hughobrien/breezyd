// SPDX-License-Identifier: GPL-3.0-or-later

// screenshot.ts — generates the README dashboard PNGs by spawning a single
// breezyd in memory-backend mode and capturing the templ-rendered dashboard.
// Mirrors the spawn pattern in tests/ui/global-setup.ts.
//
// Run via: just screenshot

import { chromium } from "@playwright/test";
import { spawn, ChildProcess } from "node:child_process";
import { mkdirSync, mkdtempSync, writeFileSync, createWriteStream } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createServer } from "node:net";

const REPO_ROOT = resolve(__dirname, "..", "..");
const SNAPSHOT = join(REPO_ROOT, "pkg", "breezy", "fakedevice", "snapshot_148.json");
const OUT_DIR = resolve(__dirname, "screenshots");
mkdirSync(OUT_DIR, { recursive: true });

// Cards in 3-col order: bedroom, office, playroom — matching the original
// screenshot composition. All three are seeded from the same snapshot;
// visual variation between cards is not the goal of this image, layout
// fidelity is.
const DEVICES = ["bedroom", "office", "playroom"] as const;

/** Poll an HTTP URL until it responds non-5xx, or timeout. */
async function waitForHTTP(url: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  let lastErr: unknown = "not started";
  while (Date.now() < deadline) {
    try {
      const r = await fetch(url);
      if (r.status < 500) return;
      lastErr = `HTTP ${r.status}`;
    } catch (e) {
      lastErr = e;
    }
    await new Promise((r) => setTimeout(r, 150));
  }
  throw new Error(`timeout waiting for ${url}: ${lastErr}`);
}

/**
 * SIGTERM the process and wait for it to exit. Without the await, Node
 * exits before `go run` forwards SIGTERM to its child binary, leaving an
 * orphan breezyd running on the loopback port until manual cleanup.
 * Mirrors tests/ui/global-teardown.ts::killAndWait.
 */
function killAndWait(p: ChildProcess): Promise<void> {
  if (p.exitCode !== null || p.signalCode !== null) return Promise.resolve();
  p.kill("SIGTERM");
  return new Promise((res) => p.once("exit", () => res()));
}

/** Bind to :0 briefly to discover a free TCP port. */
function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const srv = createServer().listen(0, "127.0.0.1", () => {
      const addr = srv.address();
      if (!addr || typeof addr === "string") {
        srv.close(() => rej(new Error("unexpected address shape")));
        return;
      }
      const port = addr.port;
      srv.close(() => res(port));
    });
    srv.on("error", rej);
  });
}

async function main() {
  const logDir = join(__dirname, "test-results");
  mkdirSync(logDir, { recursive: true });
  const bdLog = createWriteStream(join(logDir, "screenshot-breezyd.log"));

  const tmp = mkdtempSync(join(tmpdir(), "breezyd-screenshot-"));
  const cfgPath = join(tmp, "config.toml");
  const httpPort = await freePort();
  // ip is required by config validation but unused in memory mode.
  const cfg = [
    "[daemon]",
    `listen = "127.0.0.1:${httpPort}"`,
    `poll_interval = "1s"`,
    `discovery = "off"`,
    "",
    ...DEVICES.flatMap((name, i) => [
      `[devices.${name}]`,
      `id = "BREEZY00000000A${i}"`,
      `password = "1111"`,
      `ip = "127.0.0.1:0"`,
      "",
    ]),
  ].join("\n");
  writeFileSync(cfgPath, cfg, { mode: 0o600 });

  // Use -tags breezyd_test_admin for build-cache locality with the Playwright
  // suite (same tag, same binary). The /test/... admin surface is not used
  // by screenshot.ts but reusing the cached build avoids a full recompile.
  const bd = spawn(
    "go",
    [
      "run",
      "-tags", "breezyd_test_admin",
      "./cmd/breezyd",
      "--config", cfgPath,
      "--backend=memory",
      "--seed", SNAPSHOT,
    ],
    { cwd: REPO_ROOT, stdio: ["ignore", "pipe", "pipe"] },
  );
  bd.stdout?.pipe(bdLog);
  bd.stderr?.pipe(bdLog);

  const daemonURL = `http://127.0.0.1:${httpPort}`;
  await waitForHTTP(daemonURL + "/healthz", 60_000);
  // Two poll cycles so all cards have data before screenshot.
  await new Promise((r) => setTimeout(r, 2 * 1500));

  try {
    await captureViewport(daemonURL, 1400, 900, resolve(OUT_DIR, "dashboard-3col.png"), async (page) => {
      // Open preset 2's editor on the bedroom card so the README
      // screenshot shows the editor in its open state. Datastar's
      // data-show toggles based on the card-level $editor signal —
      // clicking the chip flips it to 2.
      const chip = page
        .locator('.card:first-of-type [data-preset-editor="2"]')
        .locator("xpath=preceding::div[contains(@class,'seg')][1]")
        .locator('button:nth-of-type(2)');
      await chip.scrollIntoViewIfNeeded();
      await chip.click();
      // data-show is reactive; the editor becomes visible without a
      // server round-trip.
      await page.waitForSelector(
        '.card:first-of-type [data-preset-editor="2"]:visible',
        { timeout: 5_000 },
      );
      // Scroll the now-visible editor into view for the screenshot.
      await page
        .locator('.card:first-of-type .preset-editor[data-preset-editor="2"]')
        .scrollIntoViewIfNeeded();
    });
    await captureViewport(daemonURL, 480, 900, resolve(OUT_DIR, "dashboard-1col.png"));
  } finally {
    await killAndWait(bd);
  }
}

async function captureViewport(
  daemonURL: string,
  width: number,
  height: number,
  outFile: string,
  beforeShot?: (page: import("@playwright/test").Page) => Promise<void>,
) {
  const browser = await chromium.launch();
  try {
    const ctx = await browser.newContext({ viewport: { width, height } });
    const page = await ctx.newPage();

    const pageErrors: string[] = [];
    page.on("pageerror", (e) => pageErrors.push(String(e)));
    page.on("console", (msg) => {
      if (msg.type() === "error") pageErrors.push(`console.error: ${msg.text()}`);
    });

    await page.goto(daemonURL + "/");
    // Wait for the SSE-driven initial-state pass to populate every card.
    await page.waitForFunction(
      (n) => document.querySelectorAll(".card").length >= n,
      DEVICES.length,
      { timeout: 10_000 },
    );
    // Brief pause so any follow-up patch (next poll tick) has a chance
    // to land before the screenshot.
    await page.waitForTimeout(500);
    if (beforeShot) await beforeShot(page);
    await page.screenshot({ path: outFile, fullPage: true });

    if (pageErrors.length > 0) {
      throw new Error(`pageerror events on ${width}x${height}:\n${pageErrors.join("\n")}`);
    }
    console.log(`wrote ${outFile}`);
  } finally {
    await browser.close();
  }
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
