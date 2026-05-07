// SPDX-License-Identifier: GPL-3.0-or-later

// fixtures.ts — admin-port helpers for driving the fakedevice from
// Playwright tests. Each function talks to the HTTP admin plane that
// the fakedevice_admin-tagged fakedevice exposes.
//
// The `name` parameter is kept in every signature for forward compatibility
// (when tests eventually target multiple devices). The current admin server
// only knows about one device, so the parameter is unused internally.

function adminBase(): string {
  const url = process.env.BREEZYD_ADMIN_URL;
  if (!url) throw new Error("BREEZYD_ADMIN_URL is not set — did global-setup run?");
  return url;
}

async function adminCall(method: string, path: string, body?: unknown): Promise<void> {
  const r = await fetch(`${adminBase()}${path}`, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!r.ok) {
    const text = await r.text().catch(() => "");
    throw new Error(`admin ${method} ${path}: HTTP ${r.status} ${text}`);
  }
}

/**
 * Overwrite one or more device parameters by hex param ID.
 *
 * @param _name  Device name (unused; kept for API symmetry with multi-device future).
 * @param params Map of 4-char hex param IDs to hex-encoded value bytes.
 *               e.g. { "0001": "01" } sets param 0x0001 to 0x01 (power on).
 */
export async function setDeviceState(
  _name: string,
  params: Record<string, string>,
): Promise<void> {
  await adminCall("PUT", "/state", { params });
}

/**
 * Simulate a fan-settle delay: the fakedevice adds `ms` milliseconds of
 * latency before each reply, mimicking the 10–15 s window after a
 * speed/mode write during which the unit lies about RPMs.
 *
 * Pass ms=0 to clear the delay.
 */
export async function simulateFanSettle(_name: string, ms: number): Promise<void> {
  await adminCall("POST", `/simulate/fan-settle?ms=${ms}`);
}

/**
 * Toggle auth-failure mode: when `on` (default), every request returns
 * FUNC=0x07 (auth failure) regardless of the password supplied.
 */
export async function simulateAuthFailure(_name: string, on = true): Promise<void> {
  await adminCall("POST", `/simulate/auth-failure?on=${on}`);
}

/**
 * Toggle UDP timeout mode: when `on` (default), the fakedevice silently
 * drops every incoming UDP request instead of replying — the caller's
 * read deadline will expire and return a timeout error.
 */
export async function simulateUDPTimeout(_name: string, on = true): Promise<void> {
  await adminCall("POST", `/simulate/udp-timeout?on=${on}`);
}

/**
 * Reset the fakedevice to its original snapshot state, clearing all
 * simulation flags (auth-failure, silent mode, reply delay) and
 * reloading the snapshot file.
 */
export async function reset(_name: string): Promise<void> {
  await adminCall("POST", "/reset");
}

// ---------------------------------------------------------------------------
// Convenience state presets — encodes common device states as raw param bytes.
// Hex values must match the daemon's decode expectations (pkg/breezy/params.go).
// ---------------------------------------------------------------------------
export const presets = {
  /** Power the unit on (param 0x0001 = 0x01). */
  asPowerOn: (name: string) => setDeviceState(name, { "0001": "01" }),

  /** Power the unit off (param 0x0001 = 0x00). */
  asPowerOff: (name: string) => setDeviceState(name, { "0001": "00" }),

  /**
   * Set manual speed mode at the given percentage.
   * speed_mode (0x0002) = 0xFF (manual), manual_pct (0x0044) = pct as hex.
   */
  asManualSpeed: (name: string, pct: number) =>
    setDeviceState(name, {
      "0002": "ff",
      "0044": pct.toString(16).padStart(2, "0"),
    }),
} as const;
