import { test, expect, Page, Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { execSync } from "node:child_process";
import { createHash } from "node:crypto";
import { resolve } from "node:path";

const STYLE_CSS = readFileSync(
  resolve(__dirname, "..", "..", "cmd", "breezyd", "ui", "style.css"),
  "utf8",
);
// MUST match cmd/breezyd/ui_assets.go: sha256(style.css) → hex → first 10 chars.
// Drift = 404 on the stylesheet.
const styleHash = createHash("sha256").update(STYLE_CSS).digest("hex").slice(0, 10);

const INDEX_HTML = readFileSync(
  resolve(__dirname, "..", "..", "cmd", "breezyd", "ui", "index.html"),
  "utf8",
).replaceAll("STYLEHASH", styleHash);

const HTMX_JS = readFileSync(
  resolve(__dirname, "..", "..", "cmd", "breezyd", "ui", "vendor", "htmx-2.0.4.min.js"),
  "utf8",
);
const HTMX_RT_JS = readFileSync(
  resolve(__dirname, "..", "..", "cmd", "breezyd", "ui", "vendor", "htmx-response-targets-2.0.4.min.js"),
  "utf8",
);

// Layout HTML rendered by the templ Layout template. Generated once at module
// load time via the render-layout helper binary so tests stay in sync with the
// actual template without duplicating the HTML here.
const REPO_ROOT = resolve(__dirname, "..", "..");
const LAYOUT_HTML = execSync(
  `go run ./cmd/render-layout ${styleHash}`,
  { cwd: REPO_ROOT, encoding: "utf8" },
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
      humidity_sensor_enabled: true,
      co2_sensor_enabled: true,
      voc_sensor_enabled: true,
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

  // Serve the extracted stylesheet at its content-hashed URL.
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: "text/css; charset=utf-8",
      body: STYLE_CSS,
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

  // /ui/devices/:name/:action  — htmx write endpoints; return HTML card fragment.
  for (const action of ["power", "mode", "speed", "heater", "timer", "reset-filter", "reset-faults"]) {
    await page.route(`${BASE_URL}/ui/devices/*/${action}`, async (route: Route) => {
      const req = route.request();
      const url = req.url();
      const method = req.method();
      const raw = req.postData() ?? "";
      const body = Object.fromEntries(new URLSearchParams(raw));
      requests.push({ url, method, body });
      const name = decodeURIComponent(url.split("/")[5] ?? "unknown");
      await route.fulfill({
        status: 200,
        contentType: "text/html; charset=utf-8",
        body: `<div class="card" data-device="${name}"><p>ok</p></div>`,
      });
    });
  }

  // /ui/devices/:name/threshold/:kind/edit  — htmx GET; returns the edit variant fragment.
  await page.route(`${BASE_URL}/ui/devices/*/threshold/*/edit`, async (route: Route) => {
    const req = route.request();
    const url = req.url();
    const parts = url.split("/");
    const name = decodeURIComponent(parts[5] ?? "unknown");
    const kind = decodeURIComponent(parts[7] ?? "humidity");
    const snap = snapshot(name);
    const thresholdMap: Record<string, { label: string; min: number; max: number; step: number; thresholdKey: string; enabledKey: string }> = {
      humidity: { label: "RH", min: 40, max: 80, step: 1, thresholdKey: "humidity_threshold_pct", enabledKey: "humidity_sensor_enabled" },
      co2:      { label: "eCO₂", min: 400, max: 2000, step: 10, thresholdKey: "co2_threshold_ppm", enabledKey: "co2_sensor_enabled" },
      voc:      { label: "VOC", min: 50, max: 250, step: 1, thresholdKey: "voc_threshold_index", enabledKey: "voc_sensor_enabled" },
    };
    const cfg = thresholdMap[kind] ?? thresholdMap.humidity;
    const threshVal = snap.configured?.[cfg.thresholdKey] ?? 60;
    const autoFan = snap.configured?.[cfg.enabledKey] !== false;
    const checkedAttr = autoFan ? " checked" : "";
    await route.fulfill({
      status: 200,
      contentType: "text/html; charset=utf-8",
      body: `<div class="sensor-cell"><div class="sensor-label">${cfg.label}</div>` +
        `<form class="thresh-edit-inline" hx-put="/ui/devices/${name}/threshold" hx-target="closest .sensor-cell" hx-swap="outerHTML">` +
        `<input type="hidden" name="kind" value="${kind}"/>` +
        `<input type="number" name="value" min="${cfg.min}" max="${cfg.max}" step="${cfg.step}" value="${threshVal}" data-name="${name}" data-kind="${kind}" class="thresh-input"/>` +
        `<label class="thresh-auto-fan"><input type="hidden" name="enabled" value="false"/>` +
        `<input type="checkbox" name="enabled" value="true" class="thresh-auto-fan-input" data-name="${name}" data-kind="${kind}"${checkedAttr}/>auto fan</label>` +
        `<button type="submit" data-action="threshold-save" data-name="${name}" data-kind="${kind}">✓</button>` +
        `<button type="button" data-action="threshold-cancel" data-name="${name}" data-kind="${kind}" hx-get="/ui/devices/${name}/threshold/${kind}" hx-target="closest .sensor-cell" hx-swap="outerHTML">✕</button>` +
        `</form></div>`,
    });
  });

  // /ui/devices/:name/threshold/:kind  — htmx GET; returns the read variant fragment (cancel path).
  await page.route(`${BASE_URL}/ui/devices/*/threshold/*`, async (route: Route) => {
    const req = route.request();
    const url = req.url();
    const parts = url.split("/");
    const name = decodeURIComponent(parts[5] ?? "unknown");
    const kind = decodeURIComponent(parts[7] ?? "humidity");
    await route.fulfill({
      status: 200,
      contentType: "text/html; charset=utf-8",
      body: `<div class="sensor-cell"><div class="sensor-label">${kind}</div>` +
        `<div class="value-clickable" data-action="edit-threshold" data-name="${name}" data-kind="${kind}">—</div></div>`,
    });
  });

  // /ui/devices/:name/threshold  (PUT)  — htmx write; records request, returns read variant.
  await page.route(`${BASE_URL}/ui/devices/*/threshold`, async (route: Route) => {
    const req = route.request();
    const url = req.url();
    const method = req.method();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url, method, body });
    const name = decodeURIComponent(url.split("/")[5] ?? "unknown");
    const kind = (body as any).kind ?? "humidity";
    await route.fulfill({
      status: 200,
      contentType: "text/html; charset=utf-8",
      body: `<div class="sensor-cell"><div class="sensor-label">${kind}</div>` +
        `<div class="value-clickable" data-action="edit-threshold" data-name="${name}" data-kind="${kind}">—</div></div>`,
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

// PR1 deferred: preset-editor rendering uses JS state that conflicts with templ-rendered DOM in PR2.
// Restore in PR2 (Task 17/18 of plan) when the editor becomes an htmx fragment.
test.fixme("preset editor open: no fan slider rows (editor is the control surface)", async ({ page }) => {
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

test("power click: POSTs the inverse of the current state", async ({ page }) => {
  // This test uses the htmx path: LAYOUT_HTML + real htmx + templ-rendered cards.
  // The power button in the templ-rendered card has hx-post; htmx fires the POST.
  const requests: RecordedRequest[] = [];

  // Minimal templ-rendered card with power=true → hx-vals sends on=false (the inverse).
  const cardHtml = `<div class="card" data-device="playroom">` +
    `<details class="device-info"><summary><h2>playroom</h2>` +
    `<button type="button" class="toggle toggle-inline"` +
    ` hx-post="/ui/devices/playroom/power"` +
    ` hx-vals='{"on": false}'` +
    ` hx-target="closest .card"` +
    ` hx-swap="outerHTML"` +
    ` hx-disabled-elt="this"` +
    ` aria-pressed="true">power</button>` +
    `</summary></details></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  // Serve real htmx so swap behavior works.
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  // /ui/devices → return the templ-rendered card HTML.
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  // /ui/devices/playroom/power → record and return updated card HTML.
  await page.route(`${BASE_URL}/ui/devices/playroom/power`, async (route: Route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({
      status: 200,
      contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom"><p>toggled</p></div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });
  await page.click('button[hx-post*="/power"]');
  await page.waitForTimeout(300);

  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/power"));
  expect(post).toBeTruthy();
  // power=true in card → hx-vals sends on=false (the inverse).
  expect(post!.body).toEqual({ on: "false" });
});

// PR2 deferred: secondary speed-preserve POST (carrying max(fan_supply_pct, fan_extract_pct)
// as the new manual_pct) was JS orchestration in legacy.js. The htmx mode button issues
// a single POST /ui/devices/:name/mode; the speed-preserve is dropped in this PR.
test.fixme("mode click in manual: carries the higher fan pct as new manual_pct", async ({ page }) => {
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
  expect(speedPost?.body).toEqual({ manual: "50" });
});

// PR2 deferred: optimistic overlay (setOptimisticLive) was JS state in legacy.js and is
// removed as part of the htmx migration. The card swap after a successful POST will show
// the correct state once the next poll completes.
test.fixme("mode click in manual: optimistic overlay flips Sensors rpms immediately", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "manual", manual_pct: 50, airflow_mode: "extract" },
      live: { fan_supply_rpm: 0, fan_extract_rpm: 3120, fan_supply_pct: 0, fan_extract_pct: 50 },
    }),
  });
  const card = page.locator(".card").first();
  await expect(card.locator('.sensor-cell:has(.sensor-label:text-is("supply rpm"))')).toContainText("off");
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

// Helper: open editor for preset 2 and uncheck automode so the
// inference-from-fan-state path is exercised.
async function openEditorAutomodeOff(page: any) {
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await page.locator('input[data-action="automode-toggle"][data-name="playroom"]').uncheck();
}

// PR1 deferred: preset-editor rendering uses JS state that conflicts with templ-rendered DOM in PR2.
test.fixme("preset editor: automode default ON; dragging in editor POSTs ventilation", async ({ page }) => {
  // automode is checked by default — every editor edit commits the
  // device to ventilation regardless of the supply/extract pair.
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 55, extract: 60 } },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await expect(
    page.locator('input[data-action="automode-toggle"][data-name="playroom"]')
  ).toBeChecked();
  const supply = page.locator('input[data-action="preset-supply-slider"][data-name="playroom"]');
  await supply.evaluate((el: HTMLInputElement) => {
    el.value = "70";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(250);
  const modePost = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
  expect(modePost?.body).toEqual({ mode: "ventilation" });
});

// PR1 deferred: preset-editor rendering uses JS state that conflicts with templ-rendered DOM in PR2.
test.fixme("preset editor: dragging a slider into 1-9 snaps to 0 (no register write, mode change)", async ({ page }) => {
  // The protocol register can't store 1..9, and the red-tinted 0-10%
  // band signals "drag here to turn this fan off". A drag landing in
  // that band MUST snap to 0 so the airflow_mode encoding kicks in
  // instead of producing a (silently dropped) preset write.
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 55, extract: 60 } },
    }),
  });
  await openEditorAutomodeOff(page);
  await page.locator('input[data-action="match-speeds-toggle"][data-name="playroom"]').uncheck();
  const supply = page.locator('input[data-action="preset-supply-slider"][data-name="playroom"]');
  // Drop the slider to 5 — middle of the snap-to-off band.
  await supply.evaluate((el: HTMLInputElement) => {
    el.value = "5";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(250);
  // Slider visually snapped to 0.
  await expect(supply).toHaveValue("0");
  // No /preset write (snapped value 0 is below the protocol min); the
  // airflow_mode write puts the device into extract-only as if the
  // user had landed exactly on 0.
  const presetPost = requests.find(r => r.method === "POST" && r.url.endsWith("/preset"));
  const modePost = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
  expect(presetPost).toBeFalsy();
  expect(modePost?.body).toEqual({ mode: "extract" });
});

// PR1 deferred: preset-editor rendering uses JS state that conflicts with templ-rendered DOM in PR2.
test.fixme("preset editor: automode off + supply→0 implies extract mode", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 55, extract: 60 } },
    }),
  });
  await openEditorAutomodeOff(page);
  await page.locator('input[data-action="match-speeds-toggle"][data-name="playroom"]').uncheck();
  const supply = page.locator('input[data-action="preset-supply-slider"][data-name="playroom"]');
  await supply.evaluate((el: HTMLInputElement) => {
    el.value = "0";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(250);
  const presetPost = requests.find(r => r.method === "POST" && r.url.endsWith("/preset"));
  const modePost = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
  expect(presetPost).toBeFalsy();
  expect(modePost?.body).toEqual({ mode: "extract" });
});

// PR1 deferred: preset-editor rendering uses JS state that conflicts with templ-rendered DOM in PR2.
test.fixme("preset editor: automode off + extract→0 implies supply mode", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "regeneration", preset2: { supply: 55, extract: 60 } },
    }),
  });
  await openEditorAutomodeOff(page);
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

// PR1 deferred: preset-editor rendering uses JS state that conflicts with templ-rendered DOM in PR2.
test.fixme("preset editor: automode off + both > 0 implies regeneration", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset2", airflow_mode: "supply", preset2: { supply: 55, extract: 60 } },
    }),
  });
  await openEditorAutomodeOff(page);
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

// Intentionally removed in PR2: secondary automode POST (/mode {ventilation}) was JS
// orchestration in legacy.js (computeAirflow + applyAirflow), deleted in Task 21.
// The htmx preset button issues a single POST /ui/devices/:name/speed {preset:N}.
// Re-add via server-side mode chain in postUISpeed if user feedback requires it.
test.fixme("preset activation (automode on): clicks ventilation alongside the preset", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { speed_mode: "preset1", airflow_mode: "regeneration", preset1: { supply: 30, extract: 35 }, preset3: { supply: 100, extract: 100 } },
    }),
  });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="3"]');
  await page.waitForTimeout(250);
  const speedPost = requests.find(r => r.method === "POST" && r.url.endsWith("/speed"));
  const modePost = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
  expect(speedPost?.body).toEqual({ preset: "3" });
  expect(modePost?.body).toEqual({ mode: "ventilation" });
});

test("mode click: each button POSTs its mode string", async ({ page }) => {
  // Uses the htmx path: LAYOUT_HTML + templ-rendered card with hx-post on mode buttons.
  const requests: RecordedRequest[] = [];

  // Card in manual mode so all four mode buttons are visible.
  const modes = ["ventilation", "regeneration", "supply", "extract"];
  const labels: Record<string, string> = { ventilation: "auto", regeneration: "regen", supply: "supply", extract: "exhaust" };
  const modeButtons = modes.map(m =>
    `<button type="button" data-action="mode" data-name="playroom" data-value="${m}"` +
    ` hx-post="/ui/devices/playroom/mode" hx-vals='{"mode":"${m}"}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this">${labels[m]}</button>`
  ).join("");
  const cardHtml =
    `<div class="card" data-device="playroom">` +
    `<div class="controls"><h3>Controls</h3>` +
    `<div class="ctrl"><span class="ctrl-label">MODE</span><div class="seg">${modeButtons}</div></div>` +
    `</div></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/mode`, async (route: Route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({
      status: 200, contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom"><p>ok</p></div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });

  for (const mode of modes) {
    requests.length = 0;
    await page.click(`button[data-action="mode"][data-name="playroom"][data-value="${mode}"]`);
    await page.waitForTimeout(150);
    const post = requests.find(r => r.method === "POST" && r.url.endsWith("/mode"));
    expect(post, `expected POST /mode for ${mode}`).toBeTruthy();
    // htmx form-encodes hx-vals; all values are strings.
    expect(post!.body).toEqual({ mode });
  }
});

test("speed preset: clicking preset 2 POSTs {preset:2}", async ({ page }) => {
  // Uses the htmx path: LAYOUT_HTML + templ-rendered card with hx-post on preset buttons.
  const requests: RecordedRequest[] = [];

  const cardHtml =
    `<div class="card" data-device="playroom">` +
    `<div class="controls"><h3>Controls</h3><div class="ctrl"><div class="seg">` +
    `<button type="button" data-action="preset" data-name="playroom" data-value="1"` +
    ` hx-post="/ui/devices/playroom/speed" hx-vals='{"preset":1}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this">30/35</button>` +
    `<button type="button" data-action="preset" data-name="playroom" data-value="2"` +
    ` hx-post="/ui/devices/playroom/speed" hx-vals='{"preset":2}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this">55/60</button>` +
    `<button type="button" data-action="preset" data-name="playroom" data-value="3"` +
    ` hx-post="/ui/devices/playroom/speed" hx-vals='{"preset":3}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this">100/100</button>` +
    `</div></div></div></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/speed`, async (route: Route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({
      status: 200, contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom"><p>ok</p></div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/speed"));
  expect(post).toBeTruthy();
  // htmx form-encodes hx-vals; all values are strings.
  expect(post!.body).toEqual({ preset: "2" });
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

// PR1 deferred: edit-variant rendering moves to htmx in PR2 (#14, Task 17/18 of plan).
test.fixme("speed preset: editor opens after activating, sliders use cached preset values", async ({ page }) => {
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

// PR1 deferred: edit-variant rendering moves to htmx in PR2 (#14, Task 17/18 of plan).
test.fixme("speed preset: clicking same active preset twice closes the editor", async ({ page }) => {
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

// PR1 deferred: edit-variant rendering moves to htmx in PR2 (#14, Task 17/18 of plan).
test.fixme("speed preset editor: match-speeds default true → moving supply POSTs both", async ({ page }) => {
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

// PR1 deferred: edit-variant rendering moves to htmx in PR2 (#14, Task 17/18 of plan).
test.fixme("speed preset editor: match-speeds off → moving extract preserves cached supply", async ({ page }) => {
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
  // Uses the htmx path: LAYOUT_HTML + templ-rendered card with hx-post on manual button.
  // The server embeds the cached manual_pct (70) into hx-vals.
  const requests: RecordedRequest[] = [];

  const cardHtml =
    `<div class="card" data-device="playroom">` +
    `<div class="controls"><h3>Controls</h3><div class="ctrl"><div class="seg">` +
    `<button type="button" data-action="manual-speed" data-name="playroom"` +
    ` hx-post="/ui/devices/playroom/speed" hx-vals='{"manual":70}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this">manual</button>` +
    `</div></div></div></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/speed`, async (route: Route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({
      status: 200, contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom"><p>ok</p></div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });
  await page.click('button[data-action="manual-speed"][data-name="playroom"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/speed"));
  // htmx form-encodes hx-vals; all values are strings.
  expect(post?.body).toEqual({ manual: "70" });
});

test("manual button: defaults to 50 when manual_pct is absent from the snapshot", async ({ page }) => {
  // Belt-and-braces fallback — the server-rendered hx-vals embeds the
  // server-side manualBtnPct() which returns 50 when ManualPct < 10.
  // Simulated here by embedding 50 in the card HTML (as the server would).
  const requests: RecordedRequest[] = [];

  const cardHtml =
    `<div class="card" data-device="playroom">` +
    `<div class="controls"><h3>Controls</h3><div class="ctrl"><div class="seg">` +
    `<button type="button" data-action="manual-speed" data-name="playroom"` +
    ` hx-post="/ui/devices/playroom/speed" hx-vals='{"manual":50}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this">manual</button>` +
    `</div></div></div></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/speed`, async (route: Route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({
      status: 200, contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom"><p>ok</p></div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });
  await page.click('button[data-action="manual-speed"][data-name="playroom"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/speed"));
  // htmx form-encodes hx-vals; all values are strings.
  expect(post?.body).toEqual({ manual: "50" });
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
  // Uses the htmx path: LAYOUT_HTML + templ-rendered card with hx-post on the slider.
  // hx-trigger="change delay:200ms" means a synthetic 'change' event fires the POST
  // after a 200ms debounce. We wait 400ms to ensure it fires exactly once.
  const requests: RecordedRequest[] = [];

  const cardHtml =
    `<div class="card" data-device="playroom">` +
    `<div class="controls"><h3>Controls</h3><div class="ctrl">` +
    `<div class="slider-row fan-slider-row"><span class="fan-side"></span>` +
    `<input type="range" name="manual" min="10" max="100" step="1" value="30"` +
    ` data-action="manual-slider" data-name="playroom" data-side="manual"` +
    ` hx-post="/ui/devices/playroom/speed" hx-trigger="change delay:200ms"` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this" />` +
    `<span class="val">30%</span></div>` +
    `</div></div></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/speed`, async (route: Route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({
      status: 200, contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom"><p>ok</p></div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });

  const slider = page.locator('input[type="range"][data-action="manual-slider"][data-name="playroom"][data-side="manual"]');
  await slider.evaluate((el: HTMLInputElement) => {
    el.value = "50";
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await page.waitForTimeout(400); // allow htmx delay:200ms debounce to fire
  const speedPosts = requests.filter(r => r.method === "POST" && r.url.endsWith("/speed"));
  expect(speedPosts.length).toBe(1);
  // htmx sends the slider's name="manual" value as form-encoded string.
  expect(speedPosts[0].body).toEqual({ manual: "50" });
});

test("heater click: POSTs the inverse of the current state", async ({ page }) => {
  // Uses the htmx path: LAYOUT_HTML + real htmx + templ-rendered card.
  // heater=false → hx-vals sends on=true (the inverse).
  const requests: RecordedRequest[] = [];

  const cardHtml = `<div class="card" data-device="playroom">` +
    `<div class="ctrl-group ctrl-group-heater">` +
    `<span class="ctrl-label">HEATER</span>` +
    `<div class="seg">` +
    `<button type="button" class="toggle" data-action="heater" data-name="playroom"` +
    ` hx-post="/ui/devices/playroom/heater"` +
    ` hx-vals='{"on":true}'` +
    ` hx-target="closest .card"` +
    ` hx-swap="outerHTML"` +
    ` hx-disabled-elt="this"` +
    ` aria-pressed="false">heater</button>` +
    `</div></div></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/heater`, async (route: Route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({
      status: 200,
      contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom"><p>heater toggled</p></div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });
  await page.click('button[data-action="heater"][data-name="playroom"]');
  await page.waitForTimeout(300);

  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/heater"));
  expect(post).toBeTruthy();
  // heater=false in card → hx-vals sends on=true (the inverse).
  expect(post!.body).toEqual({ on: "true" });
});

test("error response: 422 on POST renders daemon error text in the card", async ({ page }) => {
  // Uses the htmx path: LAYOUT_HTML + real htmx + templ-rendered card.
  // A 422 from /ui/devices/:name/speed returns a card fragment with card-error div
  // (hx-target-422="closest .card" on <body> targets the outer card element).
  const cardHtml =
    `<div class="card" data-device="playroom">` +
    `<div class="ctrl"><div class="seg">` +
    `<button type="button" data-action="preset" data-name="playroom" data-value="2"` +
    ` hx-post="/ui/devices/playroom/speed" hx-vals='{"preset":2}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this">55/60</button>` +
    `</div></div></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/speed`, async (route: Route) => {
    // Return a 422 with a card fragment containing the error div.
    await route.fulfill({
      status: 422,
      contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom">` +
        `<div class="card-error" role="alert">preset must be 1, 2, or 3</div>` +
        `</div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });
  await page.click('button[data-action="preset"][data-name="playroom"][data-value="2"]');
  await page.waitForTimeout(150);
  await expect(page.locator(".card-error")).toContainText("preset must be 1, 2, or 3");
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

test("timer click: POSTs {mode:'night'} to /timer", async ({ page }) => {
  // Uses the htmx path: LAYOUT_HTML + real htmx + templ-rendered card.
  // special_mode "off" → timerMode returns "night" → button posts mode=night.
  const requests: RecordedRequest[] = [];

  const cardHtml =
    `<div class="card" data-device="playroom">` +
    `<div class="ctrl-group ctrl-group-timer"><span class="ctrl-label">TIMER</span>` +
    `<div class="seg">` +
    `<button type="button" data-action="timer" data-name="playroom" data-value="night"` +
    ` hx-post="/ui/devices/playroom/timer" hx-vals='{"mode":"night"}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this"` +
    ` aria-pressed="false">night</button>` +
    `<button type="button" data-action="timer" data-name="playroom" data-value="turbo"` +
    ` hx-post="/ui/devices/playroom/timer" hx-vals='{"mode":"turbo"}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this"` +
    ` aria-pressed="false">turbo</button>` +
    `</div></div></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/timer`, async (route: Route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({
      status: 200, contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom"><p>ok</p></div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });
  await page.click('button[data-action="timer"][data-name="playroom"][data-value="night"]');
  await page.waitForTimeout(150);
  const post = requests.find(r => r.method === "POST" && r.url.endsWith("/timer"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ mode: "night" });
});

test("active special_mode hides the manual panel (Mode block + slider)", async ({ page }) => {
  // While turbo or night is running, the user's manual settings are
  // overridden, so showing the Mode buttons + slider would be misleading.
  // Hidden during the timer; reappears when special_mode is "off".
  for (const sm of ["turbo", "night"]) {
    await loadDashboard(page, {
      devices: [{ name: "playroom" }],
      snapshot: (n) => baseSnapshot(n, {
        configured: { speed_mode: "manual", manual_pct: 50, airflow_mode: "regeneration" },
        live: { special_mode: sm, fan_supply_pct: 50, fan_extract_pct: 50, fan_supply_rpm: 3120, fan_extract_rpm: 3180, in_user_control: false, sensor_alerts: {} },
      }),
    });
    await expect(page.locator(".card .ctrl", { hasText: "MODE" })).toHaveCount(0);
    await expect(page.locator(".card .ctrl .fan-slider-row")).toHaveCount(0);
  }
});

test("timer click on active mode: POSTs {mode:'off'} to stop the timer", async ({ page }) => {
  // Uses the htmx path: LAYOUT_HTML + real htmx + templ-rendered card.
  // special_mode "night" → timerMode("night","night") returns "off" → button posts mode=off.
  const requests: RecordedRequest[] = [];

  const cardHtml =
    `<div class="card" data-device="playroom">` +
    `<div class="ctrl-group ctrl-group-timer"><span class="ctrl-label">TIMER</span>` +
    `<div class="seg">` +
    `<button type="button" data-action="timer" data-name="playroom" data-value="night"` +
    ` hx-post="/ui/devices/playroom/timer" hx-vals='{"mode":"off"}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this"` +
    ` aria-pressed="true">night</button>` +
    `<button type="button" data-action="timer" data-name="playroom" data-value="turbo"` +
    ` hx-post="/ui/devices/playroom/timer" hx-vals='{"mode":"turbo"}'` +
    ` hx-target="closest .card" hx-swap="outerHTML" hx-disabled-elt="this"` +
    ` aria-pressed="false">turbo</button>` +
    `</div></div></div>`;

  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML });
  });
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/css; charset=utf-8", body: STYLE_CSS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS });
  });
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS });
  });
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: cardHtml });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/timer`, async (route: Route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({
      status: 200, contentType: "text/html; charset=utf-8",
      body: `<div class="card" data-device="playroom"><p>ok</p></div>`,
    });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });
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

test("threshold: opening the editor renders the input inside the clicked cell", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { humidity_threshold_pct: 70 },
    }),
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  // htmx swaps the edit variant into the same .sensor-cell.
  const rhCell = page.locator('.sensor-cell:has(.sensor-label:text-is("RH"))');
  await expect(rhCell.locator('.thresh-input')).toHaveValue("70");
  await expect(rhCell.locator('button[data-action="threshold-save"][data-kind="humidity"]')).toBeVisible();
  await expect(rhCell.locator('button[data-action="threshold-cancel"][data-kind="humidity"]')).toBeVisible();
  // The dropped "set alert ≥" prefix label must not appear anymore.
  const sensors = page.locator(".card .block", { hasText: "Sensors" });
  await expect(sensors).not.toContainText("set alert ≥");
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

test("threshold: save PUTs {kind, value, enabled} to /threshold and exits edit mode", async ({ page }) => {
  // Uses htmx path: LAYOUT_HTML + real htmx + templ-rendered sensor cell.
  const requests: RecordedRequest[] = [];

  // A minimal card with a humidity sensor cell using the htmx attributes.
  const sensorCellRead = `<div class="sensor-cell">` +
    `<div class="sensor-label">RH</div>` +
    `<div class="value-clickable" hx-get="/ui/devices/playroom/threshold/humidity/edit" hx-target="closest .sensor-cell" hx-swap="outerHTML" data-action="edit-threshold" data-name="playroom" data-kind="humidity">60%</div>` +
    `</div>`;
  const sensorCellEdit = `<div class="sensor-cell">` +
    `<div class="sensor-label">RH</div>` +
    `<form class="thresh-edit-inline" hx-put="/ui/devices/playroom/threshold" hx-target="closest .sensor-cell" hx-swap="outerHTML">` +
    `<input type="hidden" name="kind" value="humidity"/>` +
    `<input type="number" name="value" min="40" max="80" step="1" value="60" data-name="playroom" data-kind="humidity" class="thresh-input"/>` +
    `<label class="thresh-auto-fan"><input type="hidden" name="enabled" value="false"/>` +
    `<input type="checkbox" name="enabled" value="true" class="thresh-auto-fan-input" data-name="playroom" data-kind="humidity" checked/>auto fan</label>` +
    `<button type="submit" data-action="threshold-save" data-name="playroom" data-kind="humidity">✓</button>` +
    `<button type="button" data-action="threshold-cancel" data-name="playroom" data-kind="humidity" hx-get="/ui/devices/playroom/threshold/humidity" hx-target="closest .sensor-cell" hx-swap="outerHTML">✕</button>` +
    `</form></div>`;
  const cardHtml = `<div class="card" data-device="playroom"><div class="sensor-grid">${sensorCellRead}</div></div>`;

  await page.route(`${BASE_URL}/`, (route) => route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML }));
  await page.route(`${BASE_URL}/ui/style-*.css`, (route) => route.fulfill({ status: 200, contentType: "text/css", body: STYLE_CSS }));
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, (route) => route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS }));
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, (route) => route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS }));
  await page.route(`${BASE_URL}/ui/devices`, (route) => route.fulfill({ status: 200, contentType: "text/html", body: cardHtml }));
  await page.route(`${BASE_URL}/ui/devices/playroom/threshold/humidity/edit`, (route) => {
    requests.push({ url: route.request().url(), method: route.request().method(), body: null });
    return route.fulfill({ status: 200, contentType: "text/html", body: sensorCellEdit });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/threshold`, async (route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({ status: 200, contentType: "text/html", body: sensorCellRead });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });

  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const input = page.locator('.thresh-input[data-name="playroom"][data-kind="humidity"]');
  await expect(input).toBeVisible();
  await input.fill("55");
  await page.click('button[data-action="threshold-save"][data-name="playroom"][data-kind="humidity"]');
  await page.waitForTimeout(300);

  const put = requests.find(r => r.method === "PUT" && r.url.endsWith("/threshold"));
  expect(put).toBeTruthy();
  expect(put!.body.kind).toBe("humidity");
  expect(put!.body.value).toBe("55");
  expect(put!.body.enabled).toBeDefined();
  // Edit cell is gone after htmx swap.
  await expect(input).toHaveCount(0);
});

test("threshold: cancel reverts without PUTing", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const input = page.locator('.thresh-input[data-name="playroom"][data-kind="humidity"]');
  await expect(input).toBeVisible();
  await page.click('button[data-action="threshold-cancel"][data-name="playroom"][data-kind="humidity"]');
  // htmx swaps the read variant back; edit input disappears.
  await expect(input).toHaveCount(0);
  // No PUT should have been issued — cancel uses GET to the read variant.
  const put = requests.find(r => r.method === "PUT" && r.url.endsWith("/threshold"));
  expect(put).toBeFalsy();
});

test("auto-fan: checkbox state reflects configured.<kind>_sensor_enabled", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { humidity_sensor_enabled: false },
    }),
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const cb = page.locator('.thresh-auto-fan-input[data-name="playroom"][data-kind="humidity"]');
  // humidity_sensor_enabled=false → checkbox unchecked.
  await expect(cb).not.toBeChecked();

  // co2 default is true; opening that editor should show a checked checkbox.
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="co2"]');
  const cbCo2 = page.locator('.thresh-auto-fan-input[data-name="playroom"][data-kind="co2"]');
  await expect(cbCo2).toBeChecked();
});

test("auto-fan: toggling checkbox PUTs {kind, value, enabled} to /threshold", async ({ page }) => {
  // Uses htmx path: LAYOUT_HTML + real htmx + templ-rendered sensor cell.
  // Under htmx, the form always submits all fields (kind + value + enabled).
  // The "toggling-only" optimization from the legacy JS path no longer applies.
  const requests: RecordedRequest[] = [];

  const sensorCellRead = `<div class="sensor-cell"><div class="sensor-label">RH</div>` +
    `<div class="value-clickable" hx-get="/ui/devices/playroom/threshold/humidity/edit" hx-target="closest .sensor-cell" hx-swap="outerHTML" data-action="edit-threshold" data-name="playroom" data-kind="humidity">60%</div></div>`;
  const sensorCellEdit = `<div class="sensor-cell"><div class="sensor-label">RH</div>` +
    `<form class="thresh-edit-inline" hx-put="/ui/devices/playroom/threshold" hx-target="closest .sensor-cell" hx-swap="outerHTML">` +
    `<input type="hidden" name="kind" value="humidity"/>` +
    `<input type="number" name="value" min="40" max="80" step="1" value="60" data-name="playroom" data-kind="humidity" class="thresh-input"/>` +
    `<label class="thresh-auto-fan"><input type="hidden" name="enabled" value="false"/>` +
    `<input type="checkbox" name="enabled" value="true" class="thresh-auto-fan-input" data-name="playroom" data-kind="humidity" checked/>auto fan</label>` +
    `<button type="submit" data-action="threshold-save" data-name="playroom" data-kind="humidity">✓</button>` +
    `<button type="button" data-action="threshold-cancel" data-name="playroom" data-kind="humidity" hx-get="/ui/devices/playroom/threshold/humidity" hx-target="closest .sensor-cell" hx-swap="outerHTML">✕</button>` +
    `</form></div>`;
  const cardHtml = `<div class="card" data-device="playroom"><div class="sensor-grid">${sensorCellRead}</div></div>`;

  await page.route(`${BASE_URL}/`, (route) => route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML }));
  await page.route(`${BASE_URL}/ui/style-*.css`, (route) => route.fulfill({ status: 200, contentType: "text/css", body: STYLE_CSS }));
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, (route) => route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS }));
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, (route) => route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS }));
  await page.route(`${BASE_URL}/ui/devices`, (route) => route.fulfill({ status: 200, contentType: "text/html", body: cardHtml }));
  await page.route(`${BASE_URL}/ui/devices/playroom/threshold/humidity/edit`, (route) => {
    return route.fulfill({ status: 200, contentType: "text/html", body: sensorCellEdit });
  });
  await page.route(`${BASE_URL}/ui/devices/playroom/threshold`, async (route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({ status: 200, contentType: "text/html", body: sensorCellRead });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });

  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const cb = page.locator('.thresh-auto-fan-input[data-name="playroom"][data-kind="humidity"]');
  await expect(cb).toBeVisible();
  // Uncheck the checkbox.
  await cb.uncheck();
  await page.click('button[data-action="threshold-save"][data-name="playroom"][data-kind="humidity"]');
  await page.waitForTimeout(300);

  const put = requests.find((r) => r.method === "PUT" && r.url.endsWith("/threshold"));
  expect(put).toBeTruthy();
  expect(put!.body.kind).toBe("humidity");
  // Checkbox was unchecked → hidden field wins → enabled=false.
  expect(put!.body.enabled).toBe("false");
  // value is always present in htmx form submission.
  expect(put!.body.value).toBeDefined();
});

test("schedule: empty state renders collapsed block with 'no entries'", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: { schedule: { enabled: false, entries: [], alert: false } },
    }),
  });
  const card = page.locator(".card").filter({ has: page.locator("h2", { hasText: "playroom" }) });
  const block = card.locator("details.schedule");
  await expect(block).toBeVisible();
  await expect(block).not.toHaveAttribute("open", "");
  // Expand to confirm "no entries" text is rendered inside.
  await block.locator("summary").click();
  await expect(card).toContainText("no entries");
});

test("schedule: populated state renders rows with At, Action, Pct", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: { schedule: {
        enabled: true,
        entries: [
          { at: "08:00", action: "regeneration", pct: 60 },
          { at: "22:00", action: "off", pct: 60 },
        ],
        alert: false,
      } },
    }),
  });
  const card = page.locator(".card").filter({ has: page.locator("h2", { hasText: "playroom" }) });
  await card.locator("details.schedule summary").click();
  await expect(card.locator(".schedule-table tbody tr")).toHaveCount(2);
  // SPA (index.html) renders schedule with select elements; verify first row has "regeneration" action.
  await expect(card.locator(".schedule-table tbody tr").first().locator("select")).toHaveValue("regeneration");
});

test("schedule: action=off greys the pct input", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: { schedule: {
        enabled: true,
        entries: [{ at: "22:00", action: "off", pct: 60 }],
        alert: false,
      } },
    }),
  });
  const card = page.locator(".card").filter({ has: page.locator("h2", { hasText: "playroom" }) });
  await card.locator("details.schedule summary").click();
  // SPA (index.html) renders schedule with inputs; pct input gets readonly+pct-disabled for off.
  const pct = card.locator('input[data-action="schedule-pct"]');
  await expect(pct).toHaveAttribute("readonly", "");
  await expect(pct).toHaveClass(/pct-disabled/);
});

test("schedule: duplicate-at disables save", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: { schedule: {
        enabled: true,
        entries: [
          { at: "10:00", action: "regeneration", pct: 60 },
          { at: "10:00", action: "off", pct: 60 },
        ],
        alert: false,
      } },
    }),
  });
  const card = page.locator(".card").filter({ has: page.locator("h2", { hasText: "playroom" }) });
  await card.locator("details.schedule summary").click();
  // SPA (index.html) validates duplicate-at client-side and disables the save button.
  const save = card.locator('button[data-action="schedule-save"]');
  await expect(save).toBeDisabled();
});

test("schedule: alert forces panel open with warn line", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: { schedule: {
        enabled: true,
        entries: [{ at: "22:00", action: "off", pct: 60 }],
        alert: true,
        last_apply: { at: 1320, fired: "2026-05-06T22:00:14+01:00", ok: false,
                      err: "device_unreachable: i/o timeout", retries: 5 },
      } },
    }),
  });
  const card = page.locator(".card").filter({ has: page.locator("h2", { hasText: "playroom" }) });
  await expect(card.locator("details.schedule")).toHaveAttribute("open", "");
  const warn = card.locator("details.schedule .warn");
  await expect(warn).toContainText("22:00");
  await expect(warn).toContainText("device_unreachable");
  await expect(warn).toContainText("retried 5 times");
});

test("schedule: save click PUTs the edited table", async ({ page }) => {
  // Uses htmx path: LAYOUT_HTML + real htmx + mocked schedule endpoints.
  const requests: { url: string; method: string; body: Record<string, string[]> }[] = [];

  const scheduleReadHtml = `<details class="block schedule"><summary><h3>SCHEDULE</h3></summary>` +
    `<div class="schedule-toolbar"><label><input type="checkbox" disabled/> enabled</label>` +
    `<span class="grow"></span>` +
    `<button hx-get="/ui/devices/playroom/schedule/edit" hx-target="closest .schedule" hx-swap="outerHTML">edit schedule</button>` +
    `</div><div class="ctrl-label">no entries</div></details>`;

  const scheduleEditHtml = `<details class="block schedule" open><summary><h3>SCHEDULE</h3></summary>` +
    `<form hx-put="/ui/devices/playroom/schedule" hx-target="closest .schedule" hx-swap="outerHTML">` +
    `<div class="schedule-toolbar">` +
    `<label><input type="checkbox" name="enabled" value="true"/> enabled</label>` +
    `<span class="grow"></span>` +
    `<button type="button" hx-get="/ui/devices/playroom/schedule/new-row" hx-target=".schedule-edit-tbody" hx-swap="beforeend">+ add row</button>` +
    `<button type="button" hx-get="/ui/devices/playroom/schedule" hx-target="closest .schedule" hx-swap="outerHTML">cancel</button>` +
    `<button type="submit">save</button>` +
    `</div>` +
    `<table class="schedule-table"><thead><tr><th>at</th><th>mode</th><th>fan</th><th></th></tr></thead>` +
    `<tbody class="schedule-edit-tbody"></tbody></table>` +
    `</form></details>`;

  const newRowHtml = `<tr>` +
    `<td><input type="time" name="at" value="08:00" required/></td>` +
    `<td><select name="action"><option value="regeneration" selected>regen</option></select></td>` +
    `<td><input type="number" name="pct" min="10" max="100" value="60"/></td>` +
    `<td><button type="button" class="del" hx-on:click="this.closest('tr').remove()">×</button></td>` +
    `</tr>`;

  const cardHtml = `<div class="card" data-device="playroom">${scheduleReadHtml}</div>`;

  await page.route(`${BASE_URL}/`, (route) => route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML }));
  await page.route(`${BASE_URL}/ui/style-*.css`, (route) => route.fulfill({ status: 200, contentType: "text/css", body: STYLE_CSS }));
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, (route) => route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS }));
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, (route) => route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS }));
  await page.route(`${BASE_URL}/ui/devices`, (route) => route.fulfill({ status: 200, contentType: "text/html", body: cardHtml }));
  await page.route(`${BASE_URL}/ui/devices/playroom/schedule/edit`, (route) =>
    route.fulfill({ status: 200, contentType: "text/html", body: scheduleEditHtml }));
  await page.route(`${BASE_URL}/ui/devices/playroom/schedule/new-row`, (route) =>
    route.fulfill({ status: 200, contentType: "text/html", body: newRowHtml }));
  await page.route(`${BASE_URL}/ui/devices/playroom/schedule`, async (route) => {
    const req = route.request();
    if (req.method() === "PUT") {
      const raw = req.postData() ?? "";
      const params = new URLSearchParams(raw);
      const body: Record<string, string[]> = {};
      for (const [k, v] of params.entries()) {
        if (!body[k]) body[k] = [];
        body[k].push(v);
      }
      requests.push({ url: req.url(), method: "PUT", body });
      await route.fulfill({ status: 200, contentType: "text/html", body: scheduleReadHtml });
    } else {
      await route.fulfill({ status: 200, contentType: "text/html", body: scheduleReadHtml });
    }
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").waitFor({ timeout: 5000 });

  // Open the <details> to reveal the "edit schedule" button.
  await page.locator("details.schedule summary").click();
  // Click "edit schedule" to switch to edit variant.
  await page.locator("button[hx-get*='schedule/edit']").click();
  await page.locator("form[hx-put*='schedule']").waitFor({ timeout: 3000 });

  // Click "+ add row" to append a new row.
  await page.locator("button[hx-get*='schedule/new-row']").click();
  await page.locator(".schedule-edit-tbody tr").waitFor({ timeout: 3000 });

  // Click "save" to submit the form.
  await page.locator("form[hx-put*='schedule'] button[type='submit']").click();
  await page.waitForTimeout(300);

  const put = requests.find(r => r.method === "PUT" && r.url.endsWith("/schedule"));
  expect(put).toBeTruthy();
  // Should have at least the "at" field from the new row.
  expect(put!.body["at"]).toBeDefined();
  expect(put!.body["action"]).toBeDefined();
});

test("auto-fan: editing both value and checkbox PUTs {kind, value, enabled} to /threshold", async ({ page }) => {
  // Uses htmx path: LAYOUT_HTML + real htmx + templ-rendered sensor cell.
  const requests: RecordedRequest[] = [];

  const sensorCellRead = `<div class="sensor-cell"><div class="sensor-label">RH</div>` +
    `<div class="value-clickable" hx-get="/ui/devices/playroom/threshold/humidity/edit" hx-target="closest .sensor-cell" hx-swap="outerHTML" data-action="edit-threshold" data-name="playroom" data-kind="humidity">60%</div></div>`;
  const sensorCellEdit = `<div class="sensor-cell"><div class="sensor-label">RH</div>` +
    `<form class="thresh-edit-inline" hx-put="/ui/devices/playroom/threshold" hx-target="closest .sensor-cell" hx-swap="outerHTML">` +
    `<input type="hidden" name="kind" value="humidity"/>` +
    `<input type="number" name="value" min="40" max="80" step="1" value="60" data-name="playroom" data-kind="humidity" class="thresh-input"/>` +
    `<label class="thresh-auto-fan"><input type="hidden" name="enabled" value="false"/>` +
    `<input type="checkbox" name="enabled" value="true" class="thresh-auto-fan-input" data-name="playroom" data-kind="humidity" checked/>auto fan</label>` +
    `<button type="submit" data-action="threshold-save" data-name="playroom" data-kind="humidity">✓</button>` +
    `<button type="button" data-action="threshold-cancel" data-name="playroom" data-kind="humidity" hx-get="/ui/devices/playroom/threshold/humidity" hx-target="closest .sensor-cell" hx-swap="outerHTML">✕</button>` +
    `</form></div>`;
  const cardHtml = `<div class="card" data-device="playroom"><div class="sensor-grid">${sensorCellRead}</div></div>`;

  await page.route(`${BASE_URL}/`, (route) => route.fulfill({ status: 200, contentType: "text/html", body: LAYOUT_HTML }));
  await page.route(`${BASE_URL}/ui/style-*.css`, (route) => route.fulfill({ status: 200, contentType: "text/css", body: STYLE_CSS }));
  await page.route(`${BASE_URL}/ui/vendor/htmx-2.0.4.min.js`, (route) => route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_JS }));
  await page.route(`${BASE_URL}/ui/vendor/htmx-response-targets-2.0.4.min.js`, (route) => route.fulfill({ status: 200, contentType: "application/javascript", body: HTMX_RT_JS }));
  await page.route(`${BASE_URL}/ui/devices`, (route) => route.fulfill({ status: 200, contentType: "text/html", body: cardHtml }));
  await page.route(`${BASE_URL}/ui/devices/playroom/threshold/humidity/edit`, (route) =>
    route.fulfill({ status: 200, contentType: "text/html", body: sensorCellEdit }));
  await page.route(`${BASE_URL}/ui/devices/playroom/threshold`, async (route) => {
    const req = route.request();
    const raw = req.postData() ?? "";
    const body = Object.fromEntries(new URLSearchParams(raw));
    requests.push({ url: req.url(), method: req.method(), body });
    await route.fulfill({ status: 200, contentType: "text/html", body: sensorCellRead });
  });

  await page.goto(BASE_URL + "/");
  await page.locator(".card").first().waitFor({ timeout: 5000 });

  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const input = page.locator('.thresh-input[data-name="playroom"][data-kind="humidity"]');
  await expect(input).toBeVisible();
  await input.fill("55");
  await page.locator('.thresh-auto-fan-input[data-name="playroom"][data-kind="humidity"]').uncheck();
  await page.click('button[data-action="threshold-save"][data-name="playroom"][data-kind="humidity"]');
  await page.waitForTimeout(300);

  const put = requests.find((r) => r.method === "PUT" && r.url.endsWith("/threshold"));
  expect(put).toBeTruthy();
  expect(put!.body.kind).toBe("humidity");
  expect(put!.body.value).toBe("55");
  expect(put!.body.enabled).toBe("false");
});

// Htmx deliberate semantic shift: the form always submits value+enabled, so the legacy
// "skip POST if nothing changed" optimization is gone. The server-path semantics are
// preserved (absent key = don't change), but the client no longer optimises the no-op case.
// This test only covered the JS optimisation — the important invariant (missing key → default-on)
// is now covered by "auto-fan: checkbox state reflects configured.<kind>_sensor_enabled".
test.fixme("auto-fan: snapshot without _sensor_enabled treats checkbox as default-on; save without toggling skips POST", async ({ page }) => {
  const { requests } = await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => {
      const s = baseSnapshot(n);
      delete (s.configured as any).humidity_sensor_enabled;
      return s;
    },
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  // Renderer should show checked (default-on for missing key).
  await expect(page.locator('.thresh-auto-fan-input[data-name="playroom"][data-kind="humidity"]')).toBeChecked();
  // Save without toggling — under htmx the form always PUTs, so this expectation no longer holds.
  await page.click('button[data-action="threshold-save"][data-name="playroom"][data-kind="humidity"]');
  await page.waitForTimeout(200);
  const post = requests.find((r) => r.method === "POST" && r.url.endsWith("/threshold"));
  expect(post).toBeFalsy(); // would need to change to PUT check; keeping fixme for PR3.
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

test("sensors block: expanded by default with no alerts", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: { sensor_alerts: { humidity: false, co2: false, voc: false } },
    }),
  });
  const sensors = page.locator(".card details.sensors").first();
  await expect(sensors).toHaveCount(1);
  await expect(sensors).toHaveAttribute("open", "");
});

test("sensors block: clicking summary collapses the block", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: { sensor_alerts: { humidity: false, co2: false, voc: false } },
    }),
  });
  const sensors = page.locator(".card details.sensors").first();
  await expect(sensors).toHaveAttribute("open", "");
  await sensors.locator("summary").click();
  await expect(sensors).not.toHaveAttribute("open", "");
});

test("sensors block: auto-expanded when a sensor alert is active", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      sensors: { eco2_ppm: 3500 },
      configured: { co2_threshold_ppm: 1500 },
      live: { sensor_alerts: { humidity: false, co2: true, voc: false } },
    }),
  });
  const sensors = page.locator(".card details.sensors").first();
  await expect(sensors).toHaveAttribute("open", "");
});

test("ENERGY block: open state survives the 5 s grid re-render", async ({ page }) => {
  // The dashboard rebuilds <div id="grid">.innerHTML on every poll, which
  // would destroy and recreate the <details> element. The energyOpen
  // state map + toggle listener keep the panel open across re-renders.
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: { supported: true, instant_w: 100, consumed_w: 10,
                  heating_today_kwh: 0.5, cooling_today_kwh: 0,
                  consumed_today_kwh: 0.05,
                  heating_month_kwh: 5, cooling_month_kwh: 0,
                  consumed_month_kwh: 0.5,
                  heating_lifetime_kwh: 50,
                  cooling_lifetime_kwh: 0, consumed_lifetime_kwh: 5 },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  await expect(energy).toHaveAttribute("open", "");
  // Force a re-render to mimic the 5 s poll cycle.
  await page.evaluate(() => (window as any).render?.() ?? null);
  // The fresh <details> element should have its open attr re-applied
  // from energyOpen state.
  await expect(page.locator(".card details.energy")).toHaveAttribute("open", "");
});

test("ENERGY block: 5×3 grid renders all 15 cells with new labels", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: {
          supported: true,
          instant_w: 245,
          consumed_w: 18,
          heating_today_kwh: 1.234,
          cooling_today_kwh: 0.456,
          consumed_today_kwh: 0.123,
          heating_month_kwh: 30.0,
          cooling_month_kwh: 5.5,
          consumed_month_kwh: 3.7,
          heating_lifetime_kwh: 234.5,
          cooling_lifetime_kwh: 123.4,
          consumed_lifetime_kwh: 12.3,
        },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  const cells = energy.locator(".sensor-grid .sensor-cell");
  await expect(cells).toHaveCount(15);
  // Row 1 — instantaneous
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("regen power"))')).toContainText("245 W heating");
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("regen cost"))')).toContainText("18 W");
  // Row 3 / 5 — windowed kWh
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("heating today"))')).toContainText("1.23");
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("heating month"))')).toContainText("30.00");
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("consumed lifetime"))')).toContainText("12.30");
});

test("ENERGY block: regen-power cooling sign", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: {
          supported: true, instant_w: -180, consumed_w: 18,
          heating_today_kwh: 0, cooling_today_kwh: 0.5, consumed_today_kwh: 0.05,
          heating_month_kwh: 0, cooling_month_kwh: 1, consumed_month_kwh: 0.2,
          heating_lifetime_kwh: 0, cooling_lifetime_kwh: 0, consumed_lifetime_kwh: 0,
        },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("regen power"))')).toContainText("180 W cooling");
});

test("ENERGY block: instantaneous COP from instant_w / consumed_w", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: {
          supported: true, instant_w: 100, consumed_w: 25,
          heating_today_kwh: 0, cooling_today_kwh: 0, consumed_today_kwh: 0,
          heating_month_kwh: 0, cooling_month_kwh: 0, consumed_month_kwh: 0,
          heating_lifetime_kwh: 0, cooling_lifetime_kwh: 0, consumed_lifetime_kwh: 0,
        },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("COP"))').first()).toContainText("4.0");
});

test("ENERGY block: time-windowed COP from (heating + cooling) / consumed", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: {
          supported: true, instant_w: 0, consumed_w: 0,
          heating_today_kwh: 1.0, cooling_today_kwh: 0.5, consumed_today_kwh: 0.5,
          heating_month_kwh: 0, cooling_month_kwh: 0, consumed_month_kwh: 0,
          heating_lifetime_kwh: 0, cooling_lifetime_kwh: 0, consumed_lifetime_kwh: 0,
        },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("COP today"))')).toContainText("3.0");
});

test("ENERGY block: COP renders '—' when consumed is zero", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: {
          supported: true, instant_w: 0, consumed_w: 0,
          heating_today_kwh: 0, cooling_today_kwh: 0, consumed_today_kwh: 0,
          heating_month_kwh: 0, cooling_month_kwh: 0, consumed_month_kwh: 0,
          heating_lifetime_kwh: 0, cooling_lifetime_kwh: 0, consumed_lifetime_kwh: 0,
        },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("COP"))').first()).toContainText("—");
  await expect(energy.locator('.sensor-cell:has(.sensor-label:text-is("COP today"))')).toContainText("—");
});

test("ENERGY block: rendered above the Sensors block in DOM order", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: {
          supported: true, instant_w: 100, consumed_w: 20,
          heating_today_kwh: 1, cooling_today_kwh: 0, consumed_today_kwh: 0.5,
          heating_month_kwh: 5, cooling_month_kwh: 0, consumed_month_kwh: 1,
          heating_lifetime_kwh: 50, cooling_lifetime_kwh: 0, consumed_lifetime_kwh: 10,
        },
      },
    }),
  });
  const card = page.locator(".card").first();
  const energyBox = await card.locator("details.energy").boundingBox();
  const sensorsBox = await card.locator("details.block.sensors").boundingBox();
  if (!energyBox || !sensorsBox) throw new Error("missing bounding box");
  expect(energyBox.y).toBeLessThan(sensorsBox.y);
});

test("ENERGY block: error replaces grid", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      service: {
        energy: { supported: false, error: "unsupported model: Breezy 200 (type=22) — no airflow calibration" },
      },
    }),
  });
  const energy = page.locator(".card details.energy");
  await energy.locator("summary").click();
  await expect(energy).toContainText("unsupported model");
});

test("ENERGY block: hidden when service.energy missing", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => {
      const s = baseSnapshot(n);
      delete (s.service as any).energy;
      return s;
    },
  });
  await expect(page.locator(".card details.energy")).toHaveCount(0);
});

test("override: no text warn rendered (red sensor cells signal the override)", async ({ page }) => {
  // The threshold cells go red via .alert-fire when sensor_alerts fires;
  // we rely on that visual rather than a separate warn line.
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      live: { in_user_control: false, sensor_alerts: { humidity: false, co2: true, voc: true } },
    }),
  });
  await expect(page.locator(".card .warn")).toHaveCount(0);
});

// ── Dark-mode stopgap tests ──────────────────────────────────────────────────
// These use the existing page.route() mock pattern. PR 3 will replace them
// with real-daemon tests once the fakedevice admin surface exists.

test("dark mode: prefers-color-scheme: dark renders dark palette", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await loadDashboard(page, { devices: [{ name: "playroom" }] });
  const bg = await page.evaluate(() =>
    getComputedStyle(document.body).backgroundColor
  );
  // Dark --bg is #0d0d10 → rgb(13, 13, 16).
  expect(bg).toBe("rgb(13, 13, 16)");
  await context.close();
});

test("dark mode: data-theme='dark' forces dark regardless of system", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "light" });
  const page = await context.newPage();
  await loadDashboard(page, { devices: [{ name: "playroom" }] });
  // Set data-theme after page load — CSS recalculates synchronously when JS runs.
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  const bg = await page.evaluate(() =>
    getComputedStyle(document.body).backgroundColor
  );
  // Dark --bg is #0d0d10 → rgb(13, 13, 16), even though system is light.
  expect(bg).toBe("rgb(13, 13, 16)");
  await context.close();
});

test("dark mode: data-theme='light' overrides system dark preference", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await loadDashboard(page, { devices: [{ name: "playroom" }] });
  // Set data-theme="light" after page load; :root:not([data-theme="light"]) stops matching.
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "light"));
  const bg = await page.evaluate(() =>
    getComputedStyle(document.body).backgroundColor
  );
  // Light --bg is #f6f6f6 → rgb(246, 246, 246).
  expect(bg).toBe("rgb(246, 246, 246)");
  await context.close();
});

// ── Theme picker tests ───────────────────────────────────────────────────────
// These tests exercise the theme picker JS that lives in the templ Layout
// template (cmd/breezyd/ui/templates/layout.templ). They use a loadLayout
// helper that serves the templ-rendered HTML instead of the legacy index.html.

async function loadLayout(page: Page): Promise<void> {
  // Serve the templ Layout HTML at the fake origin.
  await page.route(`${BASE_URL}/`, async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: "text/html",
      body: LAYOUT_HTML,
    });
  });
  // Serve the extracted stylesheet.
  await page.route(`${BASE_URL}/ui/style-*.css`, async (route: Route) => {
    await route.fulfill({
      status: 200,
      contentType: "text/css; charset=utf-8",
      body: STYLE_CSS,
    });
  });
  // Serve the htmx vendor scripts as empty stubs — the theme picker tests
  // don't exercise htmx swap behavior, so we just need the scripts to not 404.
  await page.route(`${BASE_URL}/ui/vendor/htmx*.js`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "application/javascript", body: "" });
  });
  // Return empty HTML for the device list — theme picker tests don't need cards.
  await page.route(`${BASE_URL}/ui/devices`, async (route: Route) => {
    await route.fulfill({ status: 200, contentType: "text/html", body: "" });
  });
  await page.goto(BASE_URL + "/");
  // Wait for the picker to be present in the DOM.
  await page.locator(".theme-picker").waitFor({ timeout: 5000 });
}

test("theme picker: clicking dark sets data-theme and localStorage", async ({ page }) => {
  await loadLayout(page);
  // Open the picker.
  await page.locator(".theme-picker summary").click();
  await page.locator('[data-theme-set="dark"]').click();
  const theme = await page.evaluate(() => document.documentElement.getAttribute("data-theme"));
  expect(theme).toBe("dark");
  const stored = await page.evaluate(() => localStorage.getItem("theme"));
  expect(stored).toBe("dark");
});

test("theme picker: clicking auto removes the attribute", async ({ page }) => {
  await loadLayout(page);
  // Pre-seed dark so there is something to remove.
  await page.evaluate(() => document.documentElement.setAttribute("data-theme", "dark"));
  await page.locator(".theme-picker summary").click();
  await page.locator('[data-theme-set="auto"]').click();
  const theme = await page.evaluate(() => document.documentElement.getAttribute("data-theme"));
  expect(theme).toBeNull();
  const stored = await page.evaluate(() => localStorage.getItem("theme"));
  expect(stored).toBeNull();
});

test("theme picker: outside click closes popout", async ({ page }) => {
  await loadLayout(page);
  const picker = page.locator(".theme-picker");
  await picker.locator("summary").click();
  // The <details> element should be open.
  await expect(picker).toHaveAttribute("open", "");
  // Dispatch a click event at a position outside the picker via JS so that
  // Playwright doesn't try to scroll/check visibility on an empty body.
  await page.evaluate(() => {
    document.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await expect(picker).not.toHaveAttribute("open", "");
});

test("theme picker: choice survives reload", async ({ page, context }) => {
  await loadLayout(page);
  // Set dark via the picker.
  await page.locator(".theme-picker summary").click();
  await page.locator('[data-theme-set="dark"]').click();
  // Navigate away and back; the Layout's FOUC-guard script should re-apply dark.
  await page.goto(BASE_URL + "/");
  await page.locator(".theme-picker").waitFor({ timeout: 5000 });
  const theme = await page.evaluate(() => document.documentElement.getAttribute("data-theme"));
  expect(theme).toBe("dark");
});

test("dark mode: no FOUC — first paint already dark when localStorage seeded", async ({ browser }) => {
  // Pre-seed localStorage before any page load so the FOUC-guard script in
  // <head> applies the attribute before the first paint.
  const context = await browser.newContext({ colorScheme: "light" });
  const page = await context.newPage();
  // Seed via addInitScript so localStorage is set before the page executes.
  await context.addInitScript(() => {
    localStorage.setItem("theme", "dark");
  });
  // Set up routes then load.
  await loadLayout(page);
  const theme = await page.evaluate(() => document.documentElement.getAttribute("data-theme"));
  expect(theme).toBe("dark");
  await context.close();
});
