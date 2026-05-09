// SPDX-License-Identifier: GPL-3.0-or-later

// global-setup.ts — spawns breezyd (memory backend) for the real-daemon
// Playwright test suite. Runs once before all tests.
//
// No fakedevice process is spawned. breezyd is built with the
// breezyd_test_admin tag and run with --backend=memory --seed so the
// in-process MemClient serves as the device. The /test/devices/{name}/...
// admin surface is mounted on the same HTTP server.

import { spawn, ChildProcess } from "node:child_process";
import { mkdtempSync, writeFileSync, mkdirSync, createWriteStream } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

/** Path to the env file that workers read for the daemon URL. */
export const ENV_FILE = join(__dirname, "test-results", ".env.test");

export const REPO_ROOT = resolve(__dirname, "..", "..");

let breezydProc: ChildProcess | undefined;

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

export default async function globalSetup() {
  // Ensure the log dir exists.
  const logDir = join(__dirname, "test-results");
  mkdirSync(logDir, { recursive: true });

  // Snapshot shipped with the fakedevice package — used as the memory seed.
  const snapshotPath = join(REPO_ROOT, "pkg", "breezy", "fakedevice", "snapshot_148.json");

  // 1. Write a temp breezyd config.
  const tmpDir = mkdtempSync(join(tmpdir(), "breezyd-test-"));
  const configPath = join(tmpDir, "config.toml");
  const httpPort = await freePort();

  const configToml = [
    "[daemon]",
    `listen = "127.0.0.1:${httpPort}"`,
    `poll_interval = "1s"`,
    `discovery = "off"`,
    "",
    "[devices.alpha]",
    `id = "BREEZY00000000A0"`,
    `password = "1111"`,
    // ip is required by config validation but unused in memory mode.
    `ip = "127.0.0.1:0"`,
  ].join("\n");

  writeFileSync(configPath, configToml, { mode: 0o600 });

  // 2. Spawn breezyd with the memory backend and test-admin surface.
  breezydProc = spawn(
    "go",
    [
      "run",
      "-tags", "breezyd_test_admin",
      "./cmd/breezyd",
      "--config", configPath,
      "--backend=memory",
      "--seed", snapshotPath,
    ],
    { cwd: REPO_ROOT, stdio: ["ignore", "pipe", "pipe"] },
  );

  const bdLog = createWriteStream(join(logDir, "breezyd.log"));
  breezydProc.stdout?.pipe(bdLog);
  breezydProc.stderr?.pipe(bdLog);

  // 3. Wait for the HTTP server to be ready.
  const daemonURL = `http://127.0.0.1:${httpPort}`;
  try {
    await waitForHTTP(daemonURL + "/healthz", 60_000);
  } catch (e) {
    throw new Error(`breezyd did not become ready at ${daemonURL}: ${e}`);
  }

  // Write addresses to a file so workers can read them reliably.
  // (process.env mutations in globalSetup are not guaranteed to propagate
  // to worker processes in all Playwright versions.)
  writeFileSync(
    ENV_FILE,
    [
      `BREEZYD_URL=${daemonURL}`,
      `BREEZYD_DEVICE_NAME=alpha`,
    ].join("\n") + "\n",
  );

  // Also set on the main process for playwright.config.ts baseURL resolution.
  process.env.BREEZYD_URL = daemonURL;
  process.env.BREEZYD_DEVICE_NAME = "alpha";
}

/** Return a free TCP port by briefly binding to :0. */
async function freePort(): Promise<number> {
  const { createServer } = await import("node:net");
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

/** Exposed for global-teardown. */
export function __processes() {
  return { breezyd: breezydProc };
}
