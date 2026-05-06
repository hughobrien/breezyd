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
    // Top-level overrides allowed for fields like last_poll/id/ip; nested
    // configured/live/sensors/service are already merged above, so excise
    // them here to prevent the outer spread from clobbering the merge.
    ...(() => {
      const { configured: _c, live: _l, sensors: _s, service: _sv, ...rest } = overrides as any;
      return rest;
    })(),
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
  await expect(card).toContainText("20.8°C");
  await expect(card).toContainText("85%");
});

test("preset mode: no fan slider rows render (preset row is the only control)", async ({ page }) => {
  // In preset mode the user reaches the editor by clicking the active
  // preset; there's no inline slider. Live rpms still surface in the
  // Sensors block.
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 30, extract: 30 } },
      live: {
        fan_supply_rpm: 5340,
        fan_extract_rpm: 5400,
        fan_supply_pct: 30,
        fan_extract_pct: 30,
      },
    }),
  });
  await expect(page.locator(".card .ctrl .fan-slider-row")).toHaveCount(0);
  const card = page.locator(".card").first();
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("supply rpm"))')).toContainText("5340 rpm");
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("exhaust rpm"))')).toContainText("5400 rpm");
});

test("preset editor open: no fan slider rows (editor is the control surface)", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 55, extract: 60 } },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await expect(page.locator(".preset-editor")).toBeVisible();
  await expect(page.locator(".card .ctrl .fan-slider-row")).toHaveCount(0);
});

test("fans: rpm=0 reads 'off' in the Sensors block", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { power: false, speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 30, extract: 30 } },
      live: {
        fan_supply_rpm: 0,
        fan_extract_rpm: 0,
        fan_supply_pct: 0,
        fan_extract_pct: 0,
      },
    }),
  });
  const card = page.locator(".card").first();
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("supply rpm"))')).toContainText("off");
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("exhaust rpm"))')).toContainText("off");
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

test("mode click in manual: carries the higher fan pct as new manual_pct", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "manual", manual_pct: 50, airflow_mode: "extract" },
      // Extract mode: extract fan running at 50%, supply forced off (0).
      live: { fan_supply_rpm: 0, fan_extract_rpm: 3120, fan_supply_pct: 0, fan_extract_pct: 50 },
    }),
  });
  await page.click('button[data-action="mode"][data-name="playroom"][data-value="supply"]');
  await page.waitForTimeout(200);
  const modePost = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
  const speedPost = requests.find(r => r.method === "POST" && r.url.endsWith("/speed"));
  expect(modePost?.body).toEqual({ mode: "supply" });
  expect(speedPost?.body).toEqual({ manual: 50 });
});

test("mode click in manual: optimistic overlay flips Sensors rpms immediately", async ({ page }) => {
  // The daemon's cache won't show the new fan_*_pct until the 12 s
  // fan-settle window passes; the dashboard should optimistically patch
  // the live values so the user doesn't see stale rpm/pct readings for
  // that long. Mode buttons are only visible in manual speed_mode now.
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "manual", manual_pct: 50, airflow_mode: "extract" },
      live: { fan_supply_rpm: 0, fan_extract_rpm: 3120, fan_supply_pct: 0, fan_extract_pct: 50 },
    }),
  });
  const card = page.locator(".card").first();
  // Pre-click sanity: supply rpm reads "off" (extract-only mode).
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("supply rpm"))')).toContainText("off");
  // Switch to supply mode. Daemon keeps returning the stale snapshot —
  // the optimistic overlay is what should flip supply rpm to "—" (the
  // overlay sets fan_supply_rpm to null until the next real poll) and
  // exhaust rpm to "off".
  await page.click('button[data-action="mode"][data-name="playroom"][data-value="supply"]');
  await page.waitForTimeout(250);
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("supply rpm"))')).toContainText("—");
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("exhaust rpm"))')).toContainText("off");
});

test("Mode block: visible only in manual speed_mode", async ({ page }) => {
  // In preset modes the airflow direction is encoded through the
  // preset-editor sliders (set a side to 0 = that fan off), so the
  // Mode buttons are redundant. They surface only in manual mode.
  await loadDashboard(page, {
    devices: [{ name: "preset" }, { name: "manual" }],
    snapshot: (n) => baseSnapshot(n, n === "preset"
      ? { configured: { speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 55, extract: 60 } } }
      : { configured: { speed_mode: "manual", airflow_mode: "regeneration" } }),
  });
  const presetCard = page.locator(".card", { hasText: "preset" }).first();
  const manualCard = page.locator(".card", { hasText: "manual" }).first();
  await expect(presetCard.locator(".ctrl", { hasText: "Mode" })).toHaveCount(0);
  await expect(manualCard.locator(".ctrl", { hasText: "Mode" })).toBeVisible();
});

test("preset buttons: labels are 'supply/extract' pcts from cached preset config", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: {
        speed_mode: "preset2",
        airflow_mode: "regeneration",
        preset1: { supply: 30, extract: 35 },
        preset2: { supply: 55, extract: 60 },
        preset3: { supply: 100, extract: 100 },
      },
    }),
  });
  const presetBtn = (n: number) =>
    page.locator(`button[data-action="preset"][data-name="playroom"][data-value="${n}"]`);
  await expect(presetBtn(1)).toHaveText("30/35");
  await expect(presetBtn(2)).toHaveText("55/60");
  await expect(presetBtn(3)).toHaveText("100/100");
});

test("preset editor: dragging supply to 0 implies extract mode", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 55, extract: 60 } },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  // Turn match off so dragging supply doesn't drag extract along.
  await page.locator('input[data-action="match-speeds-toggle"][data-name="playroom"]').uncheck();
  const supply = page.locator('input[data-action="preset-supply-slider"][data-name="playroom"]');
  await supply.evaluate((el: HTMLInputElement) => {
    el.value = "0";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(250);
  // No /preset write (supply=0 below protocol min); /mode write to extract.
  const presetPost = requests.find(r => r.method === "POST" && r.url.endsWith("/preset"));
  const modePost = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
  expect(presetPost).toBeFalsy();
  expect(modePost?.body).toEqual({ mode: "extract" });
});

test("preset editor: dragging extract to 0 implies supply mode", async ({ page }) => {
  // Symmetric counterpart to the supply→0 test: confirms the implied-
  // mode logic isn't accidentally biased toward the supply side.
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 55, extract: 60 } },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await page.locator('input[data-action="match-speeds-toggle"][data-name="playroom"]').uncheck();
  const extract = page.locator('input[data-action="preset-extract-slider"][data-name="playroom"]');
  await extract.evaluate((el: HTMLInputElement) => {
    el.value = "0";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(250);
  const presetPost = requests.find(r => r.method === "POST" && r.url.endsWith("/preset"));
  const modePost = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
  expect(presetPost).toBeFalsy();
  expect(modePost?.body).toEqual({ mode: "supply" });
});

test("preset editor: both > 0 in match-on mode implies regeneration", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "supply", preset2: { supply: 55, extract: 60 } },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  const supply = page.locator('input[data-action="preset-supply-slider"][data-name="playroom"]');
  await supply.evaluate((el: HTMLInputElement) => {
    el.value = "70";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(250);
  const presetPost = requests.find(r => r.method === "POST" && r.url.endsWith("/preset"));
  const modePost = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
  expect(presetPost?.body).toEqual({ preset: 2, supply: 70, extract: 70 });
  expect(modePost?.body).toEqual({ mode: "regeneration" });
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

test("speed preset: activating an inactive preset opens neither editor nor slider", async ({ page }) => {
  // First click on an inactive preset just activates it. No editor and
  // no fan slider — preset modes show only the preset row + Timer/
  // Heater. The user reaches the editor by clicking the preset again.
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset1", airflow_mode: "regeneration", preset1: { supply: 30, extract: 35 }, preset3: { supply: 100, extract: 100 } },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="3"]');
  await page.waitForTimeout(200);
  await expect(page.locator(".preset-editor")).toHaveCount(0);
  await expect(page.locator(".card .ctrl .fan-slider-row")).toHaveCount(0);
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

test("manual button: switches to manual speed_mode at the cached manual_pct", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", manual_pct: 70, airflow_mode: "regeneration" },
    }),
  });
  await page.click('button[data-action="manual-speed"][data-name="playroom"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/speed"));
  expect(post?.body).toEqual({ manual: 70 });
});

test("manual button: defaults to 50 when manual_pct is absent from the snapshot", async ({ page }) => {
  // Belt-and-braces fallback in the click handler — guards against a
  // device or daemon that skips emitting manual_pct (older firmware,
  // partial reads), so the manual button still produces a writable value.
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => {
      const s = baseSnapshot(n, {
        configured: { speed_mode: "preset2", airflow_mode: "regeneration" },
      });
      delete (s.configured as any).manual_pct;
      return s;
    },
  });
  await page.click('button[data-action="manual-speed"][data-name="playroom"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/speed"));
  expect(post?.body).toEqual({ manual: 50 });
});

test("manual mode: single combined slider row replaces the two fan rows", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "manual", manual_pct: 50, airflow_mode: "regeneration" },
      live: { fan_supply_rpm: 3120, fan_extract_rpm: 3180, fan_supply_pct: 50, fan_extract_pct: 50 },
    }),
  });
  // Exactly one fan-slider-row in manual mode.
  await expect(page.locator(".card .ctrl .fan-slider-row")).toHaveCount(1);
  // It carries data-side="manual" and shows the manual_pct setpoint.
  const row = page.locator(".card .ctrl .fan-slider-row");
  await expect(row.locator(".val")).toHaveText("50%");
  await expect(row.locator('input[type="range"][data-side="manual"]')).not.toBeDisabled();
  // rpms still surface in the Sensors block.
  const card = page.locator(".card").first();
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("supply rpm"))')).toContainText("3120 rpm");
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("exhaust rpm"))')).toContainText("3180 rpm");
});

test("speed manual slider: POSTs once on change, not on input", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, { configured: { speed_mode: "manual", manual_pct: 30 } }),
  });
  const slider = page.locator('input[type="range"][data-action="manual-slider"][data-name="playroom"][data-side="manual"]');
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
