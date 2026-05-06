import { chromium } from "@playwright/test";
import { readFileSync, mkdirSync } from "node:fs";
import { resolve } from "node:path";

const INDEX_HTML = readFileSync(
  resolve(__dirname, "..", "..", "cmd", "breezyd", "ui", "index.html"),
  "utf8",
);
const OUT_DIR = resolve(__dirname, "screenshots");
mkdirSync(OUT_DIR, { recursive: true });

function snapshot(name: string) {
  return {
    name,
    id: `BREEZY00000000${name === "playroom" ? "A0" : name === "bedroom" ? "A1" : "A2"}`,
    ip: name === "playroom" ? "192.168.1.148" : name === "bedroom" ? "192.168.1.152" : "192.168.1.160",
    last_poll: new Date().toISOString(),
    configured: {
      power: name !== "office",
      speed_mode: name === "playroom" ? "manual" : name === "bedroom" ? "preset2" : "manual",
      manual_pct: name === "playroom" ? 30 : 50,
      airflow_mode: name === "playroom" ? "regeneration" : "ventilation",
      heater_enabled: name === "bedroom",
      humidity_threshold_pct: 60,
      co2_threshold_ppm: 1500,
      voc_threshold_index: 250,
    },
    live: {
      fan_supply_rpm: name === "office" ? 0 : (name === "playroom" ? 5340 : 3120),
      fan_extract_rpm: name === "office" ? 0 : (name === "playroom" ? 5400 : 3180),
      fan_supply_pct: name === "office" ? 0 : (name === "playroom" ? 30 : 55),
      fan_extract_pct: name === "office" ? 0 : (name === "playroom" ? 30 : 60),
      heater_running: false,
      in_user_control: name !== "playroom" && name !== "bedroom",
      sensor_alerts: name === "playroom"
        ? { humidity: false, co2: true, voc: true }
        : { humidity: false, co2: false, voc: false },
      special_mode: name === "bedroom" ? "night" : "off",
      special_mode_remaining_seconds: name === "bedroom" ? 21600 : 0, // 6h
    },
    sensors: {
      humidity_pct: name === "playroom" ? 52 : name === "bedroom" ? 47 : 41,
      eco2_ppm: name === "playroom" ? 3500 : name === "bedroom" ? 600 : null,
      voc_index: name === "playroom" ? 350 : name === "bedroom" ? 120 : null,
      temp_outdoor_c: 20.8,
      temp_supply_c: name === "office" ? null : 21.9,
      temp_exhaust_inlet_c: 21.6,
      temp_exhaust_outlet_c: 20.9,
      recovery_efficiency_pct: name === "office" ? null : 85,
    },
    service: {
      filter_status: "clean",
      filter_remaining_seconds: name === "playroom" ? 7732560 : name === "bedroom" ? 3542400 : 8812800,
      motor_lifetime_seconds: name === "playroom" ? 52320 : name === "bedroom" ? 65100 : 33060,
      rtc_battery_volts: 3.34,
      fault_level: "none",
      frost_protection_active: false,
      energy: name === "office"
        ? { supported: false, error: "device powered off — no airflow" }
        : name === "playroom"
        ? {
            supported: true,
            instant_w: 245,
            consumed_w: 18,
            heating_today_kwh: 1.23,
            cooling_today_kwh: 0.46,
            consumed_today_kwh: 0.12,
            heating_lifetime_kwh: 234.5,
            cooling_lifetime_kwh: 123.4,
            consumed_lifetime_kwh: 12.3,
          }
        : {
            supported: true,
            instant_w: 95,
            consumed_w: 12,
            heating_today_kwh: 0.62,
            cooling_today_kwh: 0.21,
            consumed_today_kwh: 0.08,
            heating_lifetime_kwh: 142.8,
            cooling_lifetime_kwh: 71.2,
            consumed_lifetime_kwh: 8.1,
          },
    },
    firmware: { version: "0.11", build_date: "2025-03-21" },
  };
}

async function captureViewport(width: number, height: number, outFile: string) {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width, height } });
  const page = await ctx.newPage();

  const pageErrors: string[] = [];
  page.on("pageerror", (e) => pageErrors.push(String(e)));
  page.on("console", (msg) => {
    if (msg.type() === "error") pageErrors.push(`console.error: ${msg.text()}`);
  });

  // Mock the API. The HTML is served at a fake origin so relative
  // /v1/... fetches are interceptable.
  await page.route("**/index.html", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "text/html; charset=utf-8",
      body: INDEX_HTML,
    });
  });
  await page.route("**/v1/devices", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        devices: [{ name: "playroom" }, { name: "bedroom" }, { name: "office" }],
      }),
    });
  });
  await page.route("**/v1/devices/*", async (route) => {
    const url = route.request().url();
    const name = decodeURIComponent(url.split("/").pop() ?? "");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(snapshot(name)),
    });
  });

  await page.goto("http://breezy.test/index.html");
  // Wait for cards to render (bootstrap fetches /v1/devices then per-device).
  await page.waitForSelector(".card", { timeout: 5000 });
  // Give one extra render tick so all three cards are populated.
  await page.waitForTimeout(500);

  await page.screenshot({ path: outFile, fullPage: true });

  if (pageErrors.length > 0) {
    await browser.close();
    throw new Error(`pageerror events on ${width}x${height}:\n${pageErrors.join("\n")}`);
  }

  await browser.close();
  console.log(`wrote ${outFile}`);
}

async function main() {
  await captureViewport(1400, 900, resolve(OUT_DIR, "dashboard-3col.png"));
  await captureViewport(480, 900, resolve(OUT_DIR, "dashboard-1col.png"));
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
