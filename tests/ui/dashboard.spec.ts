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
  await expect(card).toContainText("3500 ppm");
  await expect(card).toContainText("20.8 °C");
  await expect(card).toContainText("85%");
});

test("fans: pct and rpm both rendered on each fan row", async ({ page }) => {
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
  const fans = page.locator(".card .block", { hasText: "Fans" });
  await expect(fans).toContainText("30% / 5340 rpm");
  await expect(fans).toContainText("30% / 5400 rpm");
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
  const fans = page.locator(".card .block", { hasText: "Fans" });
  await expect(fans).toContainText("0% / 0 rpm");
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
    card.locator('button[data-action="timer"][data-value="off"]'),
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

test("threshold: sensor row shows current and alert threshold", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      sensors: { humidity_pct: 52 },
      configured: { humidity_threshold_pct: 70 },
    }),
  });
  const sensors = page.locator(".card .block", { hasText: "Sensors" });
  await expect(sensors).toContainText("52%");
  await expect(sensors).toContainText("alert 70%");
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
