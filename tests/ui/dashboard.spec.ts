import { test, expect, Page, Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const INDEX_HTML = readFileSync(
  resolve(__dirname, "..", "..", "cmd", "breezyd", "ui", "index.html"),
  "utf8",
);

// A fake origin used so that relative /v1/... fetches resolve correctly.
const BASE_URL = "http://breezy.test";

function baseSnapshot(name: string, overrides: Record<string, unknown> = {}) {
  const now = new Date().toISOString();
  return {
    name,
    id: `BREEZY00000000${name === "playroom" ? "A0" : "A1"}`,
    ip: name === "playroom" ? "192.168.1.148" : "192.168.1.152",
    last_poll: now,
    configured: {
      power: true,
      speed_mode: "manual",
      manual_pct: 30,
      airflow_mode: "regeneration",
      heater_enabled: false,
      humidity_threshold_pct: 60,
      co2_threshold_ppm: 1500,
      voc_threshold_index: 250,
      ...((overrides as any).configured ?? {}),
    },
    live: {
      fan_supply_rpm: 5340,
      fan_extract_rpm: 5400,
      fan_supply_pct: 30,
      fan_extract_pct: 30,
      heater_running: false,
      in_user_control: true,
      sensor_alerts: { humidity: false, co2: false, voc: false },
      ...((overrides as any).live ?? {}),
    },
    sensors: {
      humidity_pct: 52,
      eco2_ppm: 3500,
      voc_index: 350,
      temp_outdoor_c: 20.8,
      temp_supply_c: 21.9,
      temp_exhaust_inlet_c: 21.6,
      temp_exhaust_outlet_c: 20.9,
      recovery_efficiency_pct: 85,
      ...((overrides as any).sensors ?? {}),
    },
    service: {
      filter_status: "clean",
      filter_remaining_seconds: 7732560,
      motor_lifetime_seconds: 52320,
      rtc_battery_volts: 3.34,
      fault_level: "none",
      frost_protection_active: false,
      ...((overrides as any).service ?? {}),
    },
    firmware: { version: "0.11", build_date: "2025-03-21" },
    ...overrides,
  };
}

type RecordedRequest = { url: string; method: string; body: any };

async function loadDashboard(
  page: Page,
  opts: {
    devices?: { name: string }[];
    snapshot?: (name: string) => any;
    postResponse?: (req: { url: string; method: string; body: any }) => {
      status: number;
      body: any;
    };
    failBootstrap?: boolean;
  } = {},
): Promise<{ requests: RecordedRequest[] }> {
  const devList = opts.devices ?? [{ name: "playroom" }, { name: "bedroom" }];
  const snapshot = opts.snapshot ?? ((n) => baseSnapshot(n));
  const requests: RecordedRequest[] = [];

  // Serve the HTML at our fake origin so relative /v1/... fetches resolve.
  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: "text/html",
      body: INDEX_HTML,
    });
  });

  // /v1/devices  — bootstrap list
  await page.route(`${BASE_URL}/v1/devices`, async (route: Route) => {
    if (opts.failBootstrap) {
      await route.fulfill({ status: 502, body: "bad gateway" });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ devices: devList }),
    });
  });

  // /v1/devices/:name/:action  — POST endpoints (must come before the two-segment route)
  await page.route(`${BASE_URL}/v1/devices/*/*`, async (route: Route) => {
    const req = route.request();
    const url = req.url();
    const method = req.method();
    let body: any = null;
    try { body = JSON.parse(req.postData() ?? ""); } catch {}
    requests.push({ url, method, body });

    const resp = opts.postResponse?.({ url, method, body });
    await route.fulfill({
      status: resp?.status ?? 200,
      contentType: "application/json",
      body: JSON.stringify(resp?.body ?? { ok: true }),
    });
  });

  // /v1/devices/:name  — GET snapshots and per-device POSTs
  await page.route(`${BASE_URL}/v1/devices/*`, async (route: Route) => {
    const req = route.request();
    const url = req.url();
    const method = req.method();
    let body: any = null;
    try { body = JSON.parse(req.postData() ?? ""); } catch {}
    requests.push({ url, method, body });

    if (method === "GET") {
      const name = decodeURIComponent(url.split("/").pop() ?? "");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(snapshot(name)),
      });
      return;
    }

    if (method === "POST") {
      const resp = opts.postResponse?.({ url, method, body });
      await route.fulfill({
        status: resp?.status ?? 200,
        contentType: "application/json",
        body: JSON.stringify(resp?.body ?? { ok: true }),
      });
      return;
    }

    await route.continue();
  });

  await page.goto(BASE_URL + "/");

  // Wait for the JS bootstrap to finish populating the grid.
  // For the failBootstrap case, wait for the error banner instead.
  if (opts.failBootstrap) {
    await page.locator(".err-banner").waitFor({ timeout: 5000 });
  } else {
    await page.locator(".card").first().waitFor({ timeout: 5000 });
  }

  return { requests };
}

test("bootstrap: cards render for each configured device", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }, { name: "bedroom" }],
  });
  await expect(page.locator(".card")).toHaveCount(2);
  await expect(page.locator(".card h2", { hasText: "playroom" })).toBeVisible();
  await expect(page.locator(".card h2", { hasText: "bedroom" })).toBeVisible();
});

test("sensors: mocked values appear in the card", async ({ page }) => {
  await loadDashboard(page, { devices: [{ name: "playroom" }] });
  const card = page.locator(".card").first();
  await expect(card).toContainText("52%");
  await expect(card).toContainText("3500ppm");
  await expect(card).toContainText("20.8°C");
  await expect(card).toContainText("85%");
});

test("fans: rpm-left / slider / pct-right per fan in the Speed control", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: {
        fan_supply_rpm: 5340,
        fan_extract_rpm: 5400,
        fan_supply_pct: 30,
        fan_extract_pct: 30,
      },
    }),
  });
  const rows = page.locator(".card .ctrl .fan-slider-row");
  await expect(rows).toHaveCount(2);
  // Supply row: 5340rpm on the left, 30% on the right, slider interactive.
  await expect(rows.nth(0).locator(".val-label")).toHaveText("5340rpm");
  await expect(rows.nth(0).locator(".val")).toHaveText("30%");
  await expect(rows.nth(0).locator('input[type="range"]')).not.toBeDisabled();
  // Extract row: 5400rpm on the left, 30% on the right, slider disabled.
  await expect(rows.nth(1).locator(".val-label")).toHaveText("5400rpm");
  await expect(rows.nth(1).locator(".val")).toHaveText("30%");
  await expect(rows.nth(1).locator('input[type="range"]')).toBeDisabled();
});

test("fans: pct=0 / rpm=0 when fans are off", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { power: false },
      live: {
        fan_supply_rpm: 0,
        fan_extract_rpm: 0,
        fan_supply_pct: 0,
        fan_extract_pct: 0,
      },
    }),
  });
  const rows = page.locator(".card .ctrl .fan-slider-row");
  await expect(rows.nth(0).locator(".val-label")).toHaveText("0rpm");
  await expect(rows.nth(0).locator(".val")).toHaveText("0%");
  await expect(rows.nth(1).locator(".val-label")).toHaveText("0rpm");
  await expect(rows.nth(1).locator(".val")).toHaveText("0%");
});

test("stale indicator: old last_poll desaturates the card", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      last_poll: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
    }),
  });
  await expect(page.locator(".card.stale")).toHaveCount(1);
  await expect(page.locator(".ts.red")).toBeVisible();
});

test("sensor override: warning line appears when in_user_control is false", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: { in_user_control: false, sensor_alerts: { humidity: false, co2: true, voc: true } },
    }),
  });
  await expect(page.locator(".warn")).toContainText("sensor override");
  await expect(page.locator(".warn")).toContainText("co2");
  await expect(page.locator(".warn")).toContainText("voc");
});

test("power click: POSTs the inverse of the current state", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, { configured: { power: true } }),
  });
  await page.click('button[data-action="power"][data-name="playroom"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/power"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ on: false });
});

test("mode click: each button POSTs its mode string", async ({ page }) => {
  const { requests } = await loadDashboard(page, { devices: [{ name: "playroom" }] });
  for (const mode of ["ventilation", "regeneration", "supply", "extract"]) {
    requests.length = 0;
    await page.click(`button[data-action="mode"][data-name="playroom"][data-value="${mode}"]`);
    await page.waitForTimeout(150);
    const post = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
    expect(post, `expected POST /mode for ${mode}`).toBeTruthy();
    expect(post!.body).toEqual({ mode });
  }
});

test("speed preset: clicking preset 2 POSTs {preset:2}", async ({ page }) => {
  const { requests } = await loadDashboard(page, { devices: [{ name: "playroom" }] });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/speed"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ preset: 2 });
});

test("speed preset: editor opens after activating, sliders use cached preset values", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: {
        speed_mode: "preset2",
        preset1: { supply: 30, extract: 35 },
        preset2: { supply: 55, extract: 60 },
        preset3: { supply: 100, extract: 100 },
      },
    }),
  });
  // Preset 2 is already active. First click on 2 with editor closed → editor opens.
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  const editor = page.locator(".preset-editor");
  await expect(editor).toBeVisible();
  await expect(editor.locator('input[data-action="preset-supply-slider"]')).toHaveValue("55");
  await expect(editor.locator('input[data-action="preset-extract-slider"]')).toHaveValue("60");
});

test("speed preset: clicking same active preset twice closes the editor", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: {
        speed_mode: "preset2",
        preset2: { supply: 55, extract: 60 },
      },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await expect(page.locator(".preset-editor")).toBeVisible();
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await expect(page.locator(".preset-editor")).toHaveCount(0);
});

test("speed preset editor: match-speeds default true → moving supply POSTs both", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: {
        speed_mode: "preset2",
        preset2: { supply: 55, extract: 60 },
      },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  // Default state contract: the checkbox is :checked on first open.
  await expect(
    page.locator('input[data-action="match-speeds-toggle"][data-name="playroom"]')
  ).toBeChecked();
  const supply = page.locator('input[data-action="preset-supply-slider"][data-name="playroom"]');
  await supply.evaluate((el: HTMLInputElement) => {
    el.value = "70";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/preset"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ preset: 2, supply: 70, extract: 70 });
});

test("speed preset editor: manual slider hidden while editor is open", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: {
        speed_mode: "preset2",
        preset2: { supply: 55, extract: 60 },
      },
    }),
  });
  // Open the editor; the supply (manual) slider stays visible for live
  // ramp feedback but is disabled so the user reaches for the editor.
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await expect(page.locator(".preset-editor")).toBeVisible();
  await expect(
    page.locator('input[type="range"][data-action="manual-slider"][data-name="playroom"]')
  ).toBeDisabled();
  // Click again → editor closes, manual slider becomes interactive.
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await expect(page.locator(".preset-editor")).toHaveCount(0);
  await expect(
    page.locator('input[type="range"][data-action="manual-slider"][data-name="playroom"]')
  ).not.toBeDisabled();
});

test("speed preset editor: match-speeds off → moving extract preserves cached supply", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: {
        speed_mode: "preset2",
        preset2: { supply: 55, extract: 60 },
      },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  const matchBox = page.locator('input[data-action="match-speeds-toggle"][data-name="playroom"]');
  await matchBox.uncheck();
  const extract = page.locator('input[data-action="preset-extract-slider"][data-name="playroom"]');
  await extract.evaluate((el: HTMLInputElement) => {
    el.value = "80";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/preset"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ preset: 2, supply: 55, extract: 80 });
});

test("speed preset editor: manual slider interactive when editor closed", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: {
        speed_mode: "preset1",
        preset1: { supply: 30, extract: 35 },
        preset2: { supply: 55, extract: 60 },
      },
    }),
  });
  // Initial render: preset1 active, editor closed → manual slider enabled.
  await expect(
    page.locator('input[type="range"][data-action="manual-slider"][data-name="playroom"]')
  ).not.toBeDisabled();
  await expect(page.locator(".preset-editor")).toHaveCount(0);
});

test("speed manual slider: POSTs once on change, not on input", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, { configured: { speed_mode: "manual", manual_pct: 30 } }),
  });
  const slider = page.locator('input[type="range"][data-action="manual-slider"][data-name="playroom"]');
  await slider.evaluate((el: HTMLInputElement) => {
    el.value = "50";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(150);
  const speedPosts = requests.filter(r => r.method === "POST" && r.url.endsWith("/speed"));
  expect(speedPosts.length).toBe(1);
  expect(speedPosts[0].body).toEqual({ manual: 50 });
});

test("heater click: POSTs the inverse of the current state", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, { configured: { heater_enabled: false } }),
  });
  await page.click('button[data-action="heater"][data-name="playroom"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/heater"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ on: true });
});

test("error toast: 4xx on POST shows the daemon's error text", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    postResponse: () => ({ status: 400, body: { error: "preset must be 1, 2, or 3", code: "bad_request" } }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await expect(page.locator(".toast")).toContainText("preset must be 1, 2, or 3");
});

test("daemon-unreachable: bootstrap failure shows the top error banner", async ({ page }) => {
  await loadDashboard(page, { failBootstrap: true });
  await expect(page.locator(".err-banner")).toContainText("cannot reach daemon");
});

test("timer turbo: button pressed and countdown line rendered", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: {
        special_mode: "turbo",
        special_mode_remaining_seconds: 5400, // 1h 30m
        in_user_control: false,
        sensor_alerts: { humidity: false, co2: false, voc: false },
      },
    }),
  });
  const card = page.locator(".card").first();
  await expect(
    card.locator('button[data-action="timer"][data-value="turbo"]'),
  ).toHaveAttribute("aria-pressed", "true");
  await expect(
    card.locator('button[data-action="timer"][data-value="night"]'),
  ).toHaveAttribute("aria-pressed", "false");
  await expect(card).toContainText("1h 30m remaining");
});

test("timer override: warn line attributes the override to the timer, not sensors", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: {
        special_mode: "turbo",
        special_mode_remaining_seconds: 600,
        in_user_control: false,
        sensor_alerts: { humidity: false, co2: false, voc: false },
      },
    }),
  });
  const warn = page.locator(".card .warn");
  await expect(warn).toContainText("timer active (turbo)");
  await expect(warn).not.toContainText("sensor override");
});

test("timer click: POSTs {mode:'night'} to /timer", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
  });
  await page.click('button[data-action="timer"][data-name="playroom"][data-value="night"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/timer"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ mode: "night" });
});

test("timer click on active mode: POSTs {mode:'off'} to stop the timer", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: {
        special_mode: "night",
        special_mode_remaining_seconds: 3600,
        in_user_control: false,
        sensor_alerts: { humidity: false, co2: false, voc: false },
      },
    }),
  });
  await page.click('button[data-action="timer"][data-name="playroom"][data-value="night"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/timer"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ mode: "off" });
});

test("threshold: sensor row shows current value only (threshold hidden until edit)", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      sensors: { humidity_pct: 52 },
      configured: { humidity_threshold_pct: 70 },
    }),
  });
  const sensors = page.locator(".card .block", { hasText: "Sensors" });
  await expect(sensors).toContainText("52%");
  await expect(sensors).not.toContainText("alert 70%");
});

test("threshold: opening the editor reveals 'set alert ≥' label and threshold input", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { humidity_threshold_pct: 70 },
    }),
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const sensors = page.locator(".card .block", { hasText: "Sensors" });
  await expect(sensors).toContainText("set alert ≥");
  const input = page.locator('.thresh-input[data-name="playroom"][data-kind="humidity"]');
  await expect(input).toHaveValue("70");
});

test("threshold: alert-fire class on the value when sensor_alerts is true", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      sensors: { eco2_ppm: 3500 },
      configured: { co2_threshold_ppm: 1500 },
      live: {
        in_user_control: false,
        sensor_alerts: { humidity: false, co2: true, voc: false },
      },
    }),
  });
  const eco2 = page.locator('[data-action="edit-threshold"][data-kind="co2"]').first();
  await expect(eco2).toContainText("3500");
  await expect(eco2).toHaveClass(/alert-fire/);
});

test("threshold: clicking the value reveals an editor with current threshold", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { humidity_threshold_pct: 65 },
    }),
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const input = page.locator('.thresh-input[data-name="playroom"][data-kind="humidity"]');
  await expect(input).toBeVisible();
  await expect(input).toHaveValue("65");
  await expect(input).toHaveAttribute("min", "40");
  await expect(input).toHaveAttribute("max", "80");
});

test("threshold: save POSTs {kind, value} to /threshold and exits edit mode", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const input = page.locator('.thresh-input[data-name="playroom"][data-kind="humidity"]');
  await input.fill("55");
  await page.click('button[data-action="threshold-save"][data-name="playroom"][data-kind="humidity"]');
  await page.waitForTimeout(200);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/threshold"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ kind: "humidity", value: 55 });
  await expect(input).toHaveCount(0);
});

test("threshold: cancel reverts without POSTing", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const input = page.locator('.thresh-input[data-name="playroom"][data-kind="humidity"]');
  await expect(input).toBeVisible();
  await page.click('button[data-action="threshold-cancel"][data-name="playroom"][data-kind="humidity"]');
  await expect(input).toHaveCount(0);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/threshold"));
  expect(post).toBeFalsy();
});

test("device info: collapsed by default", async ({ page }) => {
  await loadDashboard(page, { devices: [{ name: "playroom" }] });
  const card = page.locator(".card").first();
  const info = card.locator("details.device-info");
  await expect(info).toHaveCount(1);
  await expect(info).not.toHaveAttribute("open", "");
  // Body rows (serial, ip, fw, filter, …) aren't visible while collapsed.
  await expect(info.locator("text=BREEZY")).toBeHidden();
});

test("device info: auto-expanded when fault is active", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: { fault_level: "alarm" },
    }),
  });
  const info = page.locator("details.device-info").first();
  await expect(info).toHaveAttribute("open", "");
});

test("device info: auto-expanded when filter is soiled", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: { filter_status: "soiled" },
    }),
  });
  const info = page.locator("details.device-info").first();
  await expect(info).toHaveAttribute("open", "");
});

test("device info: clicking summary toggles open and reveals serial/ip/fw", async ({ page }) => {
  await loadDashboard(page, { devices: [{ name: "playroom" }] });
  const info = page.locator("details.device-info").first();
  await expect(info).not.toHaveAttribute("open", "");
  await info.locator("summary").click();
  await expect(info).toHaveAttribute("open", "");
  await expect(info).toContainText("BREEZY00000000A0");
  await expect(info).toContainText("192.168.1.148");
  await expect(info).toContainText("0.11");
});
