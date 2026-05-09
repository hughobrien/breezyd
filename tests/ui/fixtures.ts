// SPDX-License-Identifier: GPL-3.0-or-later

// fixtures.ts — admin helpers for driving the memory backend from
// Playwright tests. Each function talks to the /test/... admin surface
// that breezyd exposes when built with the breezyd_test_admin tag.
//
// The `name` parameter identifies the device in every call and is used
// as the {name} path segment in the admin URL.

import { readFileSync } from "node:fs";
import { join } from "node:path";

// Read addresses from the env file written by globalSetup.
// We cannot rely on process.env mutations from globalSetup propagating
// to worker processes in Playwright; the file is the reliable channel.
const ENV_FILE = join(__dirname, "test-results", ".env.test");

let _envCache: Record<string, string> | undefined;

function readEnvFile(): Record<string, string> {
  if (_envCache) return _envCache;
  try {
    const lines = readFileSync(ENV_FILE, "utf8").trim().split("\n");
    const env: Record<string, string> = {};
    for (const line of lines) {
      const eq = line.indexOf("=");
      if (eq > 0) env[line.slice(0, eq)] = line.slice(eq + 1);
    }
    _envCache = env;
    return env;
  } catch {
    throw new Error(`Cannot read ${ENV_FILE} — did global-setup run?`);
  }
}

function daemonBase(): string {
  const env = readEnvFile();
  const url = env.BREEZYD_URL;
  if (!url) throw new Error("BREEZYD_URL not in env file — did global-setup run?");
  return url;
}

async function adminCall(method: string, path: string, body?: unknown): Promise<void> {
  const r = await fetch(`${daemonBase()}/test${path}`, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!r.ok) {
    const text = await r.text().catch(() => "");
    throw new Error(`admin ${method} /test${path}: HTTP ${r.status} ${text}`);
  }
}

async function daemonCall(method: string, path: string, body?: unknown): Promise<void> {
  const r = await fetch(`${daemonBase()}${path}`, {
    method,
    headers: body !== undefined ? { "Content-Type": "application/json" } : undefined,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  if (!r.ok) {
    const text = await r.text().catch(() => "");
    throw new Error(`daemon ${method} ${path}: HTTP ${r.status} ${text}`);
  }
}

/**
 * Overwrite one or more device parameters by hex param ID.
 *
 * @param name   Device name (used as the {name} segment in the admin URL).
 * @param params Map of 4-char hex param IDs to hex-encoded value bytes.
 *               e.g. { "0001": "01" } sets param 0x0001 to 0x01 (power on).
 */
export async function setDeviceState(
  name: string,
  params: Record<string, string>,
): Promise<void> {
  for (const [id, value] of Object.entries(params)) {
    await adminCall("POST", `/devices/${name}/params/${id}`, { value });
  }
}

/**
 * Simulate a fan-settle delay.
 *
 * NOT SUPPORTED by the memory backend. The fan-settle window is a
 * UDP-protocol fact; this scenario is ported to Go in T6.
 *
 * Throws always so that any test calling this produces a clear failure
 * rather than a silent no-op.
 */
export async function simulateFanSettle(_name: string, _ms: number): Promise<void> {
  throw new Error(
    "simulateFanSettle is not supported by the memory backend; " +
    "this scenario is ported to Go (poller_test.go) in T6",
  );
}

/**
 * Toggle auth-failure mode: when `on` (default), every request returns
 * FUNC=0x07 (auth failure) regardless of the password supplied.
 */
export async function simulateAuthFailure(name: string, on = true): Promise<void> {
  await adminCall("POST", `/devices/${name}/inject-error`, { kind: on ? "auth" : "none" });
}

/**
 * Toggle UDP timeout mode: when `on` (default), every request to the
 * device returns a timeout error instead of a real response.
 */
export async function simulateUDPTimeout(name: string, on = true): Promise<void> {
  await adminCall("POST", `/devices/${name}/inject-error`, { kind: on ? "timeout" : "none" });
}

/**
 * Reset the device to its original seed state, clearing all injected
 * faults (auth-failure, timeout).
 */
export async function reset(name: string): Promise<void> {
  await adminCall("POST", `/devices/${name}/reset`);
}

// ---------------------------------------------------------------------------
// Convenience state presets — encodes common device states as raw param bytes.
// Hex values must match the daemon's decode expectations (pkg/breezy/params.go).
//
// Fakedevice snapshot default state (snapshot_148.json):
//   power=on (0001=01), speed_mode=manual (0002=FF), manual_pct=100% (0044=64),
//   airflow_mode=extract (00B7=03), timer=turbo (0007=02),
//   humidity=54% (0025=36), co2=1175ppm (0027=9704),
//   fan_supply_rpm=0 (004A=0000), fan_extract_rpm=5400 (004B=1815),
//   heater=off (0068=00), fault=none (0083=00), filter=clean (0088=00)
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

  /**
   * Set preset speed mode (preset 1–3).
   * speed_mode (0x0002) = 0x01, 0x02, or 0x03 for presets 1, 2, 3.
   */
  asPresetSpeed: (name: string, n: 1 | 2 | 3) =>
    setDeviceState(name, { "0002": n.toString(16).padStart(2, "0") }),

  /**
   * Set airflow mode.
   * airflow_mode (0x00B7): 0=ventilation, 1=regeneration, 2=supply, 3=extract.
   */
  asMode: (name: string, mode: "ventilation" | "regeneration" | "supply" | "extract") => {
    const codes: Record<string, string> = {
      ventilation: "00",
      regeneration: "01",
      supply: "02",
      extract: "03",
    };
    return setDeviceState(name, { "00B7": codes[mode] });
  },

  /** Enable the heater (param 0x0068 = 0x01). */
  asHeaterOn: (name: string) => setDeviceState(name, { "0068": "01" }),

  /** Disable the heater (param 0x0068 = 0x00). */
  asHeaterOff: (name: string) => setDeviceState(name, { "0068": "00" }),

  /**
   * Set fault level.
   * fault_indicator (0x0083): 0=none, 1=alarm, 2=warning.
   */
  withFault: (name: string, level: "none" | "alarm" | "warning") => {
    const codes: Record<string, string> = { none: "00", alarm: "01", warning: "02" };
    return setDeviceState(name, { "0083": codes[level] });
  },

  /**
   * Set filter status to soiled.
   * filter_status (0x0088): 0=clean, 1=soiled.
   */
  withFilterSoiled: (name: string) => setDeviceState(name, { "0088": "01" }),

  /**
   * Set special timer mode.
   * timer (0x0007): 0=off, 1=night, 2=turbo.
   */
  withTimer: (name: string, mode: "off" | "night" | "turbo") => {
    const codes: Record<string, string> = { off: "00", night: "01", turbo: "02" };
    return setDeviceState(name, { "0007": codes[mode] });
  },

  /**
   * Set air quality alert flags via the alert bitmap (0x0084).
   * 5-byte bitmap: byte 0 = RH, byte 1 = CO2, bytes 2-3 reserved, byte 4 = VOC.
   */
  withSensorAlert: (name: string, opts: { rh?: boolean; co2?: boolean; voc?: boolean }) => {
    const b0 = opts.rh ? "01" : "00";
    const b1 = opts.co2 ? "01" : "00";
    const b4 = opts.voc ? "01" : "00";
    return setDeviceState(name, { "0084": `${b0}${b1}0000${b4}` });
  },

  /**
   * Set raw humidity and CO2 readings.
   * humidity (0x0025): uint8 %.
   * co2 (0x0027): uint16 LE ppm.
   */
  withSensors: (name: string, opts: { humidity?: number; co2?: number }) => {
    const params: Record<string, string> = {};
    if (opts.humidity !== undefined) {
      params["0025"] = opts.humidity.toString(16).padStart(2, "0");
    }
    if (opts.co2 !== undefined) {
      const lo = (opts.co2 & 0xff).toString(16).padStart(2, "0");
      const hi = ((opts.co2 >> 8) & 0xff).toString(16).padStart(2, "0");
      params["0027"] = lo + hi;
    }
    return setDeviceState(name, params);
  },

  /**
   * Set RPM readings directly.
   * fan_supply_rpm (0x004A): uint16 LE.
   * fan_extract_rpm (0x004B): uint16 LE.
   */
  withRPMs: (name: string, opts: { supply?: number; extract?: number }) => {
    const params: Record<string, string> = {};
    if (opts.supply !== undefined) {
      const lo = (opts.supply & 0xff).toString(16).padStart(2, "0");
      const hi = ((opts.supply >> 8) & 0xff).toString(16).padStart(2, "0");
      params["004A"] = lo + hi;
    }
    if (opts.extract !== undefined) {
      const lo = (opts.extract & 0xff).toString(16).padStart(2, "0");
      const hi = ((opts.extract >> 8) & 0xff).toString(16).padStart(2, "0");
      params["004B"] = lo + hi;
    }
    return setDeviceState(name, params);
  },

  /**
   * Set preset supply/extract percentages.
   * Preset N uses params: preset1=(003A,003B), preset2=(003C,003D), preset3=(003E,003F).
   */
  withPresetValues: (name: string, n: 1 | 2 | 3, supply: number, extract: number) => {
    const ids: Record<number, [string, string]> = {
      1: ["003A", "003B"],
      2: ["003C", "003D"],
      3: ["003E", "003F"],
    };
    const [sId, eId] = ids[n];
    return setDeviceState(name, {
      [sId]: supply.toString(16).padStart(2, "0"),
      [eId]: extract.toString(16).padStart(2, "0"),
    });
  },

  /**
   * Use the daemon's PUT /v1/devices/{name}/schedule API to write schedule
   * state (schedule is daemon-side state, not device params).
   */
  withSchedule: async (
    name: string,
    opts: {
      enabled: boolean;
      entries: Array<{ at: string; action: string; pct: number }>;
    },
  ) => {
    await daemonCall("PUT", `/v1/devices/${name}/schedule`, {
      enabled: opts.enabled,
      entries: opts.entries,
    });
  },

  /**
   * Set the old last_poll timestamp to simulate a stale device.
   * Uses the admin API to inject a stale last_poll marker.
   * Since the fakedevice can't set last_poll directly, we simulate by stopping
   * its UDP replies temporarily (simulateUDPTimeout) so the daemon flags it stale.
   * This is a different approach: just mark a stale timestamp via param injection.
   * In practice: the daemon marks stale when last_poll > STALE_THRESHOLD (90s).
   * We can't set last_poll from the fakedevice admin; use the simulateUDPTimeout
   * to prevent polls from updating last_poll, then wait.
   * For tests that only need the stale CSS class, use simulateUDPTimeout.
   */
} as const;
