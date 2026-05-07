// SPDX-License-Identifier: GPL-3.0-or-later

// global-teardown.ts — SIGTERMs the fakedevice and breezyd processes
// that globalSetup spawned, and asserts clean exits.

import type { ChildProcess } from "node:child_process";
import { __processes } from "./global-setup";

/**
 * Send SIGTERM and wait for exit. Returns the numeric exit code, or
 * 143 when the process was killed by SIGTERM (Node reports code=null
 * and signal="SIGTERM" in that case).
 */
function killAndWait(p: ChildProcess): Promise<number> {
  p.kill("SIGTERM");
  return new Promise((res) =>
    p.once("exit", (code, signal) => {
      if (code !== null) {
        res(code);
      } else if (signal === "SIGTERM") {
        // Process was terminated by the signal — treat as 143 (128+15).
        res(143);
      } else {
        // Killed by some other signal.
        res(-1);
      }
    }),
  );
}

export default async function globalTeardown() {
  const { fakedevice, breezyd } = __processes();

  // Kill breezyd first so it stops trying to talk UDP to the fakedevice.
  if (breezyd) {
    const code = await killAndWait(breezyd);
    // 0 = clean exit, 143 = SIGTERM (128+15), both are acceptable.
    if (code !== 0 && code !== 143) {
      throw new Error(`breezyd exited with unexpected code ${code}`);
    }
  }

  if (fakedevice) {
    const code = await killAndWait(fakedevice);
    if (code !== 0 && code !== 143) {
      throw new Error(`fakedevice exited with unexpected code ${code}`);
    }
  }
}
