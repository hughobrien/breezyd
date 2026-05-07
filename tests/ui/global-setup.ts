// SPDX-License-Identifier: GPL-3.0-or-later

// global-setup.ts — spawns fakedevice + breezyd for the real-daemon
// Playwright test suite. Runs once before all tests.

import { spawn, ChildProcess } from "node:child_process";
import { mkdtempSync, writeFileSync, mkdirSync, createWriteStream } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

export const REPO_ROOT = resolve(__dirname, "..", "..");

let fakedeviceProc: ChildProcess | undefined;
let breezydProc: ChildProcess | undefined;

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

export default async function globalSetup() {
  // Ensure the log dir exists.
  const logDir = join(__dirname, "test-results");
  mkdirSync(logDir, { recursive: true });

  // Snapshot shipped with the fakedevice package.
  const snapshotPath = join(REPO_ROOT, "pkg", "breezy", "fakedevice", "snapshot_148.json");

  // 1. Spawn the fakedevice.
  fakedeviceProc = spawn(
    "go",
    [
      "run",
      "-tags", "fakedevice_admin",
      "./cmd/fakedevice",
      "--snapshot", snapshotPath,
      "--id", "BREEZY00000000A0",
      "--password", "1111",
    ],
    { cwd: REPO_ROOT, stdio: ["ignore", "pipe", "pipe"] },
  );

  const fdLog = createWriteStream(join(logDir, "fakedevice.log"));
  fakedeviceProc.stderr?.pipe(fdLog);

  if (!fakedeviceProc.stdout) {
    throw new Error("fakedevice process has no stdout");
  }

  // Parse the single address line printed to stdout before piping the rest.
  const addrLine = await readFirstLine(fakedeviceProc.stdout, 60_000);
  fakedeviceProc.stdout.pipe(fdLog);

  const m = addrLine.match(/udp=(\S+)\s+admin=(\S+)/);
  if (!m) {
    throw new Error(`fakedevice did not print expected address line; got: ${JSON.stringify(addrLine)}`);
  }
  const [, udpAddr, adminAddr] = m;

  // 2. Write a temp breezyd config pointing at the fakedevice.
  const tmpDir = mkdtempSync(join(tmpdir(), "breezyd-test-"));
  const configPath = join(tmpDir, "config.toml");
  // Listen on a free port by binding to :0 — breezyd doesn't support :0,
  // so we pick a high port via a simple counter.  Use a fixed port range
  // well above system services; tests run serially so collision risk is low.
  // We use a fixed high port per test run to keep config simple.
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
    // ip must include the port since fakedevice uses an ephemeral port,
    // not 4000. buildDeviceMap adds :4000 only when there's no colon,
    // so we pass the full host:port from the fakedevice.
    `ip = "${udpAddr}"`,
  ].join("\n");

  writeFileSync(configPath, configToml, { mode: 0o600 });

  // 3. Spawn breezyd against that config.
  breezydProc = spawn(
    "go",
    [
      "run",
      "-tags", "fakedevice_admin",
      "./cmd/breezyd",
      "--config", configPath,
    ],
    { cwd: REPO_ROOT, stdio: ["ignore", "pipe", "pipe"] },
  );

  const bdLog = createWriteStream(join(logDir, "breezyd.log"));
  breezydProc.stdout?.pipe(bdLog);
  breezydProc.stderr?.pipe(bdLog);

  // 4. Wait for the HTTP server to be ready.
  const daemonURL = `http://127.0.0.1:${httpPort}`;
  try {
    await waitForHTTP(daemonURL + "/healthz", 60_000);
  } catch (e) {
    throw new Error(`breezyd did not become ready at ${daemonURL}: ${e}`);
  }

  // Expose addresses via env vars for tests and fixtures.
  process.env.BREEZYD_URL = daemonURL;
  process.env.BREEZYD_ADMIN_URL = `http://${adminAddr}`;
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
  return { fakedevice: fakedeviceProc, breezyd: breezydProc };
}
