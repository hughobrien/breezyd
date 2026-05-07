// SPDX-License-Identifier: GPL-3.0-or-later

// screenshot.ts — generates the README dashboard PNGs by spawning a real
// breezyd against three fakedevice instances and capturing the templ-
// rendered dashboard. Mirrors the spawn pattern in tests/ui/global-setup.ts.
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
// screenshot composition. All three speak to identical fakedevices; visual
// variation between cards is not the goal of this image, layout fidelity is.
const DEVICES = ["bedroom", "office", "playroom"] as const;

/** Read one complete line from a readable stream, with a timeout. */
function readFirstLine(stream: NodeJS.ReadableStream, timeoutMs: number): Promise<string> {
  return new Promise((res, rej) => {
    let buf = "";
    const onData = (chunk: Buffer) => {
      buf += chunk.toString();
      const nl = buf.indexOf("\n");
      if (nl >= 0) {
        stream.off("data", onData);
        res(buf.slice(0, nl).trim());
      }
    };
    stream.on("data", onData);
    setTimeout(() => {
      stream.off("data", onData);
      rej(new Error(`timeout waiting for fakedevice address line after ${timeoutMs}ms`));
    }, timeoutMs);
  });
}

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
  const fdLog = createWriteStream(join(logDir, "screenshot-fakedevice.log"));
  const bdLog = createWriteStream(join(logDir, "screenshot-breezyd.log"));

  const fdProcs: ChildProcess[] = [];
  const fdAddrs: string[] = [];

  for (let i = 0; i < DEVICES.length; i++) {
    const fd = spawn(
      "go",
      [
        "run",
        "-tags", "fakedevice_admin",
        "./cmd/fakedevice",
        "--snapshot", SNAPSHOT,
        "--id", `BREEZY00000000A${i}`,
        "--password", "1111",
      ],
      { cwd: REPO_ROOT, stdio: ["ignore", "pipe", "pipe"] },
    );
    fdProcs.push(fd);
    fd.stderr?.pipe(fdLog);
    if (!fd.stdout) throw new Error("fakedevice has no stdout");
    const addrLine = await readFirstLine(fd.stdout, 60_000);
    fd.stdout.pipe(fdLog);
    const m = addrLine.match(/udp=(\S+)/);
    if (!m) throw new Error(`fakedevice ${i} bad address line: ${addrLine}`);
    fdAddrs.push(m[1]);
  }

  const tmp = mkdtempSync(join(tmpdir(), "breezyd-screenshot-"));
  const cfgPath = join(tmp, "config.toml");
  const httpPort = await freePort();
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
      `ip = "${fdAddrs[i]}"`,
      "",
    ]),
  ].join("\n");
  writeFileSync(cfgPath, cfg, { mode: 0o600 });

  const bd = spawn(
    "go",
    [
      "run",
      "-tags", "fakedevice_admin",
      "./cmd/breezyd",
      "--config", cfgPath,
    ],
    { cwd: REPO_ROOT, stdio: ["ignore", "pipe", "pipe"] },
  );
  bd.stdout?.pipe(bdLog);
  bd.stderr?.pipe(bdLog);

  const daemonURL = `http://127.0.0.1:${httpPort}`;
  await waitForHTTP(daemonURL + "/healthz", 60_000);
  // One poll cycle so all cards have data.
  await new Promise((r) => setTimeout(r, 1500));

  try {
    await captureViewport(daemonURL, 1400, 900, resolve(OUT_DIR, "dashboard-3col.png"));
    await captureViewport(daemonURL, 480, 900, resolve(OUT_DIR, "dashboard-1col.png"));
  } finally {
    bd.kill();
    fdProcs.forEach((p) => p.kill());
  }
}

async function captureViewport(daemonURL: string, width: number, height: number, outFile: string) {
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
    await page.waitForSelector(".card", { timeout: 10_000 });
    await page.waitForFunction(
      (n) => document.querySelectorAll(".card").length >= n,
      DEVICES.length,
      { timeout: 10_000 },
    );
    // Let htmx finish any in-flight swap before snapping.
    await page.waitForTimeout(500);
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
