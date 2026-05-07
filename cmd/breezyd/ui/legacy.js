// legacy.js — write-side JS extracted from the original SPA index.html.
// Handles all POST/PUT interactions: power, mode, speed (manual + presets),
// heater, timer, threshold inline-edit, schedule editor.
//
// The master poll, card rendering, and refreshAll machinery have been deleted
// from this file — htmx now handles polling via hx-get="/ui/devices" every 5s.
//
// PR2 will migrate each write interaction to htmx; this file is deleted in
// PR2 Task 21. Tests that depend on the inline-edit DOM state are marked
// .fixme in dashboard.spec.ts and restored in PR2.
"use strict";
(function() {

const STALE_THRESHOLD_MS = 90 * 1000;

// State for in-flight writes. Disables that card's controls while pending.
const inFlight = {}; // name -> bool
// Per-control toasts. Cleared on next successful poll.
const toasts = {}; // name -> { control: msg }
// Per-card threshold editor state. name -> "humidity" | "co2" | "voc".
// Only one threshold can be edited per card at a time.
const editingThreshold = {}; // name -> kind
// Per-card open preset editor: 1 | 2 | 3 when a preset's supply/extract
// editor is visible. Cleared on preset re-click, manual-slider use, or
// when a different card is interacted with elsewhere.
const editingPreset = {};   // name -> 1 | 2 | 3
// Per-device automode flag.
const automode = {};        // name -> bool
// Per-preset editor state.
const presetVals = {};      // name -> { 1: {supply,extract}, 2: ..., 3: ... }
// Per-device optimistic live overlay.
const optimisticLive = {}; // name -> { expiresAt, fan_supply_pct, fan_extract_pct, fan_supply_rpm, fan_extract_rpm }
// Per-card match-speeds toggle for the preset editor. Defaults to true.
const matchSpeeds = {};     // name -> bool
// scheduleEdits[name] holds the in-flight edit buffer.
const scheduleEdits = {};
const scheduleOpen = {};

// Per-kind config for the editable sensor rows.
const THRESHOLD_KINDS = {
  humidity: { label: "RH",   suffix: "%",   min: 40,  max: 80,   step: 1,
              valueKey: "humidity_pct",   thresholdKey: "humidity_threshold_pct",  alertKey: "humidity",
              enabledKey: "humidity_sensor_enabled" },
  co2:      { label: "eCO₂", suffix: " ppm", min: 400, max: 2000, step: 10,
              valueKey: "eco2_ppm",       thresholdKey: "co2_threshold_ppm",       alertKey: "co2",
              enabledKey: "co2_sensor_enabled" },
  voc:      { label: "VOC",  suffix: " idx", min: 50,  max: 250,  step: 1,
              valueKey: "voc_index",      thresholdKey: "voc_threshold_index",     alertKey: "voc",
              enabledKey: "voc_sensor_enabled",
              tooltip: "VOC Index — Sensirion 0-500 scale, ~100 = baseline indoor air" },
};

// lastSnapshots caches the most recent /v1/devices/{name} JSON response
// (keyed by name) so write handlers can read current state. Populated by
// postWrite's after-write refresh and the htmx poll indirectly (via the
// attribute data read below). This is legacy state that PR2 eliminates
// by reading state from templ-rendered DOM attributes instead.
const lastSnapshots = {};

// Seed lastSnapshots from the templ-rendered cards on load and after
// each htmx swap of #device-list. We read data-device and data-power
// from each .card element so the write handlers have a name→snapshot map.
// This is intentionally minimal — full snapshot seeding is not needed
// because postWrite always fetches /v1/devices/{name} after each write.
function seedFromDOM() {
  document.querySelectorAll(".card[data-device]").forEach(card => {
    const name = card.dataset.device;
    if (name && !lastSnapshots[name]) {
      // Minimal stub so write handlers don't crash on missing snapshot.
      lastSnapshots[name] = lastSnapshots[name] || {
        configured: {},
        live: {},
        sensors: {},
        service: {},
      };
    }
  });
}

// Re-seed on every htmx afterSwap so newly-rendered cards register.
document.body.addEventListener("htmx:afterSwap", seedFromDOM);
document.addEventListener("DOMContentLoaded", seedFromDOM);

function setOptimisticLive(name, fields, ttlMs = 15000) {
  optimisticLive[name] = { expiresAt: Date.now() + ttlMs, ...fields };
}

function applyOptimisticLive(name, snap) {
  const o = optimisticLive[name];
  if (!o) return snap;
  if (Date.now() > o.expiresAt) {
    delete optimisticLive[name];
    return snap;
  }
  const overlay = {};
  for (const k of Object.keys(o)) {
    if (k !== "expiresAt") overlay[k] = o[k];
  }
  return { ...snap, live: { ...(snap.live ?? {}), ...overlay } };
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, c => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
  }[c]));
}

// Single delegated click handler for all control buttons and clickable values.
document.addEventListener("click", async (ev) => {
  const el = ev.target.closest("[data-action]");
  if (!el) return;
  if (el.closest("summary")) {
    ev.preventDefault();
  }
  const name = el.dataset.name;
  const action = el.dataset.action;
  const value = el.dataset.value;
  const kind = el.dataset.kind;
  const snap = lastSnapshots[name] || { configured: {}, live: {}, sensors: {}, service: {} };

  switch (action) {
    case "power":
      await postWrite(name, "power", "/v1/devices/" + encodeURIComponent(name) + "/power",
                      { on: !snap.configured?.power });
      break;
    case "mode": {
      delete editingPreset[name];
      const prevSpeedMode = snap.configured?.speed_mode;
      const supplyPct = snap.live?.fan_supply_pct;
      const extractPct = snap.live?.fan_extract_pct;
      const a = typeof supplyPct === "number" ? supplyPct : -1;
      const b = typeof extractPct === "number" ? extractPct : -1;
      const preservePct = Math.max(a, b);
      await postWrite(name, "mode", "/v1/devices/" + encodeURIComponent(name) + "/mode",
                      { mode: value });
      if (prevSpeedMode === "manual" && preservePct >= 10) {
        await postWrite(name, "speed", "/v1/devices/" + encodeURIComponent(name) + "/speed",
                        { manual: preservePct });
      }
      const supplyRuns = value === "ventilation" || value === "regeneration" || value === "supply";
      const extractRuns = value === "ventilation" || value === "regeneration" || value === "extract";
      let supplyTargetPct, extractTargetPct;
      if (prevSpeedMode === "manual") {
        const m = preservePct >= 10 ? preservePct : (snap.configured?.manual_pct ?? 0);
        supplyTargetPct = m;
        extractTargetPct = m;
      } else if (typeof prevSpeedMode === "string" && prevSpeedMode.startsWith("preset")) {
        const cur = snap.configured?.[prevSpeedMode] ?? {};
        supplyTargetPct = typeof cur.supply === "number" ? cur.supply : 0;
        extractTargetPct = typeof cur.extract === "number" ? cur.extract : 0;
      } else {
        supplyTargetPct = 0;
        extractTargetPct = 0;
      }
      setOptimisticLive(name, {
        fan_supply_pct: supplyRuns ? supplyTargetPct : 0,
        fan_extract_pct: extractRuns ? extractTargetPct : 0,
        fan_supply_rpm: supplyRuns ? null : 0,
        fan_extract_rpm: extractRuns ? null : 0,
      });
      if (lastSnapshots[name]) {
        lastSnapshots[name] = applyOptimisticLive(name, lastSnapshots[name]);
      }
      break;
    }
    case "manual-speed": {
      delete editingPreset[name];
      const pct = typeof snap.configured?.manual_pct === "number" ? snap.configured.manual_pct : 50;
      await postWrite(name, "speed", "/v1/devices/" + encodeURIComponent(name) + "/speed",
                      { manual: pct });
      break;
    }
    case "preset": {
      const n = parseInt(value, 10);
      const speedMode = snap.configured?.speed_mode || "";
      const alreadyOnPreset = speedMode === "preset" + n;
      if (alreadyOnPreset && editingPreset[name] === n) {
        delete editingPreset[name];
      } else if (alreadyOnPreset) {
        editingPreset[name] = n;
      } else {
        await postWrite(name, "speed", "/v1/devices/" + encodeURIComponent(name) + "/speed",
                        { preset: n });
        const stored = (presetVals[name] || {})[n];
        const cur = snap.configured?.["preset" + n] || {};
        const supply = stored && typeof stored.supply === "number"
          ? stored.supply
          : (typeof cur.supply === "number" ? cur.supply : 50);
        const extract = stored && typeof stored.extract === "number"
          ? stored.extract
          : (typeof cur.extract === "number" ? cur.extract : 50);
        const desiredMode = computeAirflow(name, supply, extract);
        const currentMode = lastSnapshots[name]?.configured?.airflow_mode;
        if (desiredMode && desiredMode !== currentMode) {
          await applyAirflow(name, desiredMode, supply, extract);
        }
      }
      break;
    }
    case "heater":
      await postWrite(name, "heater", "/v1/devices/" + encodeURIComponent(name) + "/heater",
                      { on: !snap.configured?.heater_enabled });
      break;
    case "timer": {
      const current = snap.live?.special_mode || "off";
      const next = current === value ? "off" : value;
      await postWrite(name, "timer", "/v1/devices/" + encodeURIComponent(name) + "/timer",
                      { mode: next });
      break;
    }
    case "schedule-add":
      ensureScheduleEdit(name);
      scheduleEdits[name].entries.push({ at: "08:00", action: "regeneration", pct: 60 });
      break;
    case "schedule-del": {
      const idx = Number(el.dataset.row);
      ensureScheduleEdit(name);
      scheduleEdits[name].entries.splice(idx, 1);
      break;
    }
    case "schedule-save": {
      const buf = scheduleEdits[name];
      if (!buf) break;
      const cleaned = buf.entries.map(en => ({
        at: en.at,
        action: en.action,
        pct: Math.max(10, Math.min(100, Number(en.pct) || 10)),
      }));
      const ok = await postWrite(name, "schedule",
        "/v1/devices/" + encodeURIComponent(name) + "/schedule",
        { enabled: buf.enabled, entries: cleaned },
        "PUT");
      if (ok) delete scheduleEdits[name];
      break;
    }
    case "edit-threshold":
      editingThreshold[name] = kind;
      // Focus the freshly-rendered input so users can type immediately.
      const input = document.querySelector(
        `.thresh-input[data-name="${name}"][data-kind="${kind}"]`);
      if (input) { input.focus(); input.select(); }
      break;
    case "threshold-save":
      await saveThreshold(name, kind);
      break;
    case "threshold-cancel":
      delete editingThreshold[name];
      break;
  }
});

// Enter saves, Escape cancels while inside a threshold-edit input.
document.addEventListener("keydown", async (ev) => {
  const el = ev.target;
  if (!el.classList || !el.classList.contains("thresh-input")) return;
  if (ev.key === "Enter") {
    ev.preventDefault();
    await saveThreshold(el.dataset.name, el.dataset.kind);
  } else if (ev.key === "Escape") {
    ev.preventDefault();
    delete editingThreshold[el.dataset.name];
  }
});

async function saveThreshold(name, kind) {
  const input = document.querySelector(
    `.thresh-input[data-name="${name}"][data-kind="${kind}"]`);
  const cb = document.querySelector(
    `.thresh-auto-fan-input[data-name="${name}"][data-kind="${kind}"]`);
  if (!input || !cb) return;
  const value = parseInt(input.value, 10);
  if (isNaN(value)) return;
  const cfg = THRESHOLD_KINDS[kind];
  const snap = lastSnapshots[name] || {};
  const prevValue = snap.configured?.[cfg.thresholdKey];
  const prevEnabled = snap.configured?.[cfg.enabledKey] !== false;
  const enabledNow = cb.checked;
  const body = { kind };
  let dirty = false;
  if (value !== prevValue) { body.value = value; dirty = true; }
  if (enabledNow !== prevEnabled) { body.enabled = enabledNow; dirty = true; }
  if (!dirty) {
    delete editingThreshold[name];
    return;
  }
  const ok = await postWrite(name, "threshold-" + kind,
    "/v1/devices/" + encodeURIComponent(name) + "/threshold", body);
  if (ok) {
    delete editingThreshold[name];
  }
}

// Capture-phase listener for `toggle` events on Device <details> elements.
document.addEventListener("toggle", (ev) => {
  const el = ev.target;
  const name = el.closest(".card")?.querySelector("h2")?.textContent;
  if (!name) return;
  if (el.classList?.contains("schedule")) {
    if (el.open) scheduleOpen[name] = true;
    else delete scheduleOpen[name];
  }
}, true);

function ensureScheduleEdit(name) {
  if (scheduleEdits[name]) return;
  const sch = lastSnapshots[name]?.service?.schedule || { enabled: false, entries: [] };
  scheduleEdits[name] = {
    enabled: sch.enabled === true,
    entries: (sch.entries || []).map(e => ({ at: e.at, action: e.action, pct: e.pct })),
  };
}

// Schedule cell edits go through the input/change listener.
document.addEventListener("input", (ev) => {
  const el = ev.target;
  if (el instanceof HTMLInputElement || el instanceof HTMLSelectElement) {
    const action = el.dataset.action;
    const name = el.dataset.name;
    const rowIdx = Number(el.dataset.row);
    if (name) {
      if (action === "schedule-enabled") {
        ensureScheduleEdit(name);
        scheduleEdits[name].enabled = el.checked;
        return;
      }
      if (!Number.isNaN(rowIdx) && (action === "schedule-at" || action === "schedule-action" || action === "schedule-pct")) {
        ensureScheduleEdit(name);
        const entry = scheduleEdits[name].entries[rowIdx];
        if (entry) {
          if (action === "schedule-at") entry.at = el.value;
          else if (action === "schedule-action") entry.action = el.value;
          else entry.pct = Number(el.value);
        }
        return;
      }
    }
  }
  // Live-drag visual feedback for sliders.
  if (!el.matches('input[type="range"]')) return;
  const sliderRow = el.closest(".slider-row");
  const valSpan = sliderRow?.querySelector(".val");
  if (valSpan) valSpan.textContent = `${el.value}%`;
});

// Change handler for manual-slider and preset-editor sliders.
document.addEventListener("change", async (ev) => {
  const el = ev.target;
  const name = el.dataset.name;

  if (el.matches('input[type="range"][data-action="manual-slider"]')) {
    delete editingPreset[name];
    const pct = Math.max(10, parseInt(el.value, 10));
    await postWrite(name, "speed", "/v1/devices/" + encodeURIComponent(name) + "/speed",
                    { manual: pct });
    return;
  }

  if (el.matches('input[type="range"][data-action="preset-supply-slider"], input[type="range"][data-action="preset-extract-slider"]')) {
    const preset = parseInt(el.dataset.preset, 10);
    const cfg = lastSnapshots[name]?.configured || {};
    const cur = cfg["preset" + preset] || {};
    const rawValue = parseInt(el.value, 10);
    const newPct = (rawValue > 0 && rawValue < 10) ? 0 : rawValue;
    if (newPct !== rawValue) el.value = String(newPct);
    const isSupply = el.dataset.action === "preset-supply-slider";
    const matchOn = matchSpeeds[name] !== false;
    const siblingAction = isSupply ? "preset-extract-slider" : "preset-supply-slider";
    const sibling = document.querySelector(
      `input[type="range"][data-action="${siblingAction}"][data-name="${name}"][data-preset="${preset}"]`);
    const siblingPct = sibling ? parseInt(sibling.value, 10) : NaN;
    const fallbackOther = !isNaN(siblingPct)
      ? siblingPct
      : (isSupply ? (typeof cur.extract === "number" ? cur.extract : newPct)
                  : (typeof cur.supply === "number" ? cur.supply : newPct));
    let supply, extract;
    if (matchOn) {
      supply = newPct;
      extract = newPct;
    } else if (isSupply) {
      supply = newPct;
      extract = fallbackOther;
    } else {
      supply = fallbackOther;
      extract = newPct;
    }
    const canWritePreset = supply >= 10 && extract >= 10;
    if (canWritePreset) {
      await postWrite(name, "speed", "/v1/devices/" + encodeURIComponent(name) + "/preset",
                      { preset, supply, extract });
    }
    presetVals[name] = presetVals[name] || {};
    presetVals[name][preset] = { supply, extract };
    const speedMode = lastSnapshots[name]?.configured?.speed_mode;
    if (speedMode === "preset" + preset) {
      const impliedMode = computeAirflow(name, supply, extract);
      const currentMode = lastSnapshots[name]?.configured?.airflow_mode;
      if (impliedMode && impliedMode !== currentMode) {
        await applyAirflow(name, impliedMode, supply, extract);
      }
    }
    return;
  }

  if (el.matches('input[type="checkbox"][data-action="match-speeds-toggle"]')) {
    matchSpeeds[name] = el.checked;
    return;
  }

  if (el.matches('input[type="checkbox"][data-action="automode-toggle"]')) {
    automode[name] = el.checked;
    return;
  }
});

function computeAirflow(name, supply, extract) {
  if (automode[name] !== false) return "ventilation";
  if (supply > 0 && extract > 0) return "regeneration";
  if (supply === 0 && extract > 0) return "extract";
  if (extract === 0 && supply > 0) return "supply";
  return null;
}

async function applyAirflow(name, mode, supply, extract) {
  await postWrite(name, "mode", "/v1/devices/" + encodeURIComponent(name) + "/mode",
                  { mode });
  if (lastSnapshots[name]?.configured) {
    lastSnapshots[name].configured.airflow_mode = mode;
  }
  const supplyRuns = mode === "regeneration" || mode === "supply" || mode === "ventilation";
  const extractRuns = mode === "regeneration" || mode === "extract" || mode === "ventilation";
  setOptimisticLive(name, {
    fan_supply_pct: supplyRuns ? supply : 0,
    fan_extract_pct: extractRuns ? extract : 0,
    fan_supply_rpm: supplyRuns ? null : 0,
    fan_extract_rpm: extractRuns ? null : 0,
  });
  if (lastSnapshots[name]) {
    lastSnapshots[name] = applyOptimisticLive(name, lastSnapshots[name]);
  }
}

async function postWrite(name, controlKey, url, body, method = "POST") {
  inFlight[name] = true;
  let ok = false;
  try {
    const r = await fetch(url, {
      method,
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!r.ok) {
      let msg = `HTTP ${r.status}`;
      try {
        const env = await r.json();
        if (env.error) msg = env.error;
      } catch {}
      setToast(name, controlKey, msg);
    } else {
      ok = true;
      clearToast(name, controlKey);
      // Refresh this device immediately.
      try {
        const snapResp = await fetch("/v1/devices/" + encodeURIComponent(name));
        if (snapResp.ok) {
          lastSnapshots[name] = applyOptimisticLive(name, { ...(await snapResp.json()), _fetchedAt: Date.now() });
        }
      } catch {}
    }
  } catch (e) {
    setToast(name, controlKey, e.message);
  } finally {
    inFlight[name] = false;
  }
  return ok;
}

function setToast(name, controlKey, msg) {
  if (!toasts[name]) toasts[name] = {};
  toasts[name][controlKey] = msg;
  // Surface toast in the DOM if there's a slot for it.
  const slot = document.querySelector(`.toast[data-toast="${name}-${controlKey}"]`);
  if (slot) slot.textContent = msg;
}
function clearToast(name, controlKey) {
  if (toasts[name]) delete toasts[name][controlKey];
  const slot = document.querySelector(`.toast[data-toast="${name}-${controlKey}"]`);
  if (slot) slot.textContent = "";
}

})();
