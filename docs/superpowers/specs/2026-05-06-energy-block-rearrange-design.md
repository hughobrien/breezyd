# Energy block — month tracking + 5×3 layout (issue #30)

## Problem

Issue #30 asks for two related changes to the per-card ENERGY block:

1. **Move it above the Sensors block.** Today the order inside `renderCard()` is `device-info → sensors → controls → energy`. Energy buries useful instantaneous and accumulated data below the controls.
2. **Rebuild the grid as 5×3.** Today the block is a single "now: {recovered} W · {consumed} W" line plus a 2×3 grid of `today` and `lifetime` totals (heating, cooling, consumed). The user wants:

   |              | col 1            | col 2           | col 3            |
   |--------------|------------------|-----------------|------------------|
   | Row 1 (now)  | regen_power      | regen_cost      | cop              |
   | Row 2        | cop_today        | cop_month       | cop_lifetime     |
   | Row 3        | heating_today    | heating_month   | heating_lifetime |
   | Row 4        | cooling_today    | cooling_month   | cooling_lifetime |
   | Row 5        | consumed_today   | consumed_month  | consumed_lifetime|

The new layout introduces:
- A "month" time window (currently only today + lifetime).
- A new derived metric, COP (Coefficient of Performance) = useful_work / consumed.
- Renames: "energy now" → `regen_power`, "energy consumed" → `regen_cost`.

## Goal

Calendar-month tracking on the daemon side; rebuild the dashboard grid; move the block above sensors. No state-file migration — the user has stated they will delete `<state_dir>/energy_<device>.json` between deployments.

## Decisions (locked in during brainstorming)

- **Month window** — calendar month, local TZ. Reset on the first Tick whose local `YYYY-MM` differs from the persisted `MonthStart`. Mirrors the existing today-rollover logic exactly.
- **COP formula**:
  - Time-windowed: `(heating + cooling) / consumed` for that window. Aggregating both directions of heat transfer is correct for an HRV/ERV that does heating in winter and cooling in summer (one or the other dominates per cycle).
  - Instantaneous: `|instant_w| / consumed_w`.
  - Display: bare 1-dp number (e.g. `8.3`); `—` when `consumed` is zero or NaN. No `×` suffix.
- **Position** — immediately above the sensors `<details>` block (`device-info → ENERGY → sensors → controls`). Not at the very top of the card.
- **No state-file migration** — Load on a state file without month fields treats them as zero-valued and the missing `MonthStart` as "" (which, being unequal to the current `YYYY-MM`, triggers a one-time "month rollover" that re-zeroes the already-zero counters). Persisted fields then accumulate from there. Behavior is correct without any explicit migration code; the user will simply delete the state file.

## Design

### Backend (Go)

#### `pkg/breezy/energy.go` — `EnergyValues`

Extend the JSON-tagged struct with three new fields:

```go
type EnergyValues struct {
    Supported           bool    `json:"supported"`
    InstantW            float64 `json:"instant_w"`
    ConsumedW           float64 `json:"consumed_w"`
    HeatingTodayKWh     float64 `json:"heating_today_kwh"`
    CoolingTodayKWh     float64 `json:"cooling_today_kwh"`
    ConsumedTodayKWh    float64 `json:"consumed_today_kwh"`
    HeatingMonthKWh     float64 `json:"heating_month_kwh"`     // new
    CoolingMonthKWh     float64 `json:"cooling_month_kwh"`     // new
    ConsumedMonthKWh    float64 `json:"consumed_month_kwh"`    // new
    HeatingLifetimeKWh  float64 `json:"heating_lifetime_kwh"`
    CoolingLifetimeKWh  float64 `json:"cooling_lifetime_kwh"`
    ConsumedLifetimeKWh float64 `json:"consumed_lifetime_kwh"`
    Error               string  `json:"error,omitempty"`
}
```

Field ordering inserts month between today and lifetime — preserves intuitive grouping in JSON output.

#### `cmd/breezyd/energy_tracker.go` — `EnergyTracker` and `persistedEnergy`

- Add `HeatingMonthKWh`, `CoolingMonthKWh`, `ConsumedMonthKWh` to both the in-memory struct and the on-disk JSON shape.
- Add `MonthStart string` (format `YYYY-MM` local TZ) to both, parallel to the existing `Today string`.
- In `Tick()`, after the existing date-rollover branch, add a month-rollover branch:

  ```go
  thisMonth := now.Local().Format("2006-01")
  if e.MonthStart != thisMonth {
      e.HeatingMonthKWh = 0
      e.CoolingMonthKWh = 0
      e.ConsumedMonthKWh = 0
      e.MonthStart = thisMonth
      if err := e.save(); err != nil {
          slog.Warn("energy: month rollover save failed", "device", e.Device, "err", err)
      }
  }
  ```

- In the accumulation block (the `if w > 0 {…} else if w < 0 {…}` block plus the unconditional `ConsumedTodayKWh +=`), also accumulate the month counters in lockstep.
- In `Load()`, after the existing today-restore branch, add a month-restore branch with identical shape.
- In `Snapshot()`, populate the three new fields.

The same `save()` call writes everything; no new file format, no new persistence path.

#### `cmd/breezyd/metrics.go` — three new Prometheus gauges

Mirror the today/lifetime trio:
- `breezyd_energy_heating_month_kwh`
- `breezyd_energy_cooling_month_kwh`
- `breezyd_energy_consumed_month_kwh`

Register them, add to the `all` slice in `SetEnergy()` (so `DeleteLabelValues` clears them too on Error), and `WithLabelValues(device).Set(ev.{Heating,Cooling,Consumed}MonthKWh)` after the today block.

#### Tests

- `cmd/breezyd/energy_tracker_test.go` — new tests `TestEnergyTracker_Tick_MonthRollover` and `TestEnergyTracker_Tick_MonthRolloverPersists` parallel to the existing `TestEnergyTracker_Tick_DateRollover` and `TestEnergyTracker_Tick_RolloverPersists`. Verifies that a Tick whose local-TZ month differs from `MonthStart` zeroes the month counters but does NOT touch today or lifetime.
- `cmd/breezyd/metrics_test.go` — extend `SetEnergy` test to assert the three new gauges are set, and to assert they're cleared on Error.
- `cmd/breezyd/server_test.go` — extend whatever fixture exposes `EnergyValues` over HTTP to include the new month fields and assert they round-trip.

### Frontend (HTML/JS in `cmd/breezyd/ui/index.html`)

#### Position

In `renderCard()`, move the line `${renderEnergy(name, snap)}` from after `${renderControls(name, snap, stale)}` (~ line 495) to **immediately before** the sensors `<details>` IIFE block (~ line 484). New order:

```
device-info
power toast
stale row
ENERGY                    ← moved up
sensors <details>
controls
```

#### `renderEnergy()` — unchanged

`renderEnergy()` already wraps the body in `<details class="block energy">` with the proper toggle hookup. No change needed there beyond what `energyBody()` returns.

#### `energyBody(ev)` — full rebuild

Replace the current body (a `<div class="row">now: …</div>` + 2×3 grid) with a single 5×3 grid using the existing `.sensor-grid.energy-grid` class:

```js
function energyBody(ev) {
  const fmtKwh = (v) => `${(v ?? 0).toFixed(2)} kWh`;
  const fmtW   = (v) => `${Math.round(v ?? 0)} W`;
  const fmtCop = (num, den) => {
    if (!den || den <= 0 || !isFinite(num / den)) return "—";
    return (Math.abs(num) / den).toFixed(1);
  };

  // Row 1 — instantaneous
  const regenPower = (() => {
    const w = ev.instant_w ?? 0;
    if (w > 0) return `${fmtW(w)} heating`;
    if (w < 0) return `${fmtW(-w)} cooling`;
    return "0 W";
  })();
  const regenCost = fmtW(ev.consumed_w ?? 0);
  const copNow   = fmtCop(ev.instant_w ?? 0, ev.consumed_w ?? 0);

  // Rows 2–5 — windowed
  const copToday    = fmtCop((ev.heating_today_kwh ?? 0) + (ev.cooling_today_kwh ?? 0), ev.consumed_today_kwh);
  const copMonth    = fmtCop((ev.heating_month_kwh ?? 0) + (ev.cooling_month_kwh ?? 0), ev.consumed_month_kwh);
  const copLifetime = fmtCop((ev.heating_lifetime_kwh ?? 0) + (ev.cooling_lifetime_kwh ?? 0), ev.consumed_lifetime_kwh);

  const cell = (label, value) =>
    `<div class="sensor-cell"><div class="sensor-label">${label}</div><div>${value}</div></div>`;

  return `<div class="sensor-grid energy-grid">
    ${cell("regen power",  esc(regenPower))}
    ${cell("regen cost",   esc(regenCost))}
    ${cell("COP",          esc(copNow))}
    ${cell("COP today",    esc(copToday))}
    ${cell("COP month",    esc(copMonth))}
    ${cell("COP lifetime", esc(copLifetime))}
    ${cell("heating today",    fmtKwh(ev.heating_today_kwh))}
    ${cell("heating month",    fmtKwh(ev.heating_month_kwh))}
    ${cell("heating lifetime", fmtKwh(ev.heating_lifetime_kwh))}
    ${cell("cooling today",    fmtKwh(ev.cooling_today_kwh))}
    ${cell("cooling month",    fmtKwh(ev.cooling_month_kwh))}
    ${cell("cooling lifetime", fmtKwh(ev.cooling_lifetime_kwh))}
    ${cell("consumed today",    fmtKwh(ev.consumed_today_kwh))}
    ${cell("consumed month",    fmtKwh(ev.consumed_month_kwh))}
    ${cell("consumed lifetime", fmtKwh(ev.consumed_lifetime_kwh))}
  </div>`;
}
```

Notes:
- Drops the standalone "now:" row — instantaneous values move into Row 1 cells.
- Existing `.sensor-grid.energy-grid { grid-template-columns: repeat(3, 1fr); }` already produces a 3-column grid; rows fall out from cell count.
- `cell()` is a local helper that mirrors the existing `plainCell()` pattern in `sensorsGrid()` but stays scoped to this function (the upstream callers don't need it).
- Labels are display strings ("regen power", "COP today" etc.). The cell-label CSS doesn't enforce casing.

#### Tests (`tests/ui/dashboard.spec.ts`)

The existing energy tests (~ lines 947–1057) assert on the old labels ("heating today", "cooling lifetime") and the standalone "now:" row. Replace with:

1. **ENERGY block: 5×3 grid renders all 15 cells** — count `.energy-grid .sensor-cell` is 15; row 1 contains `regen power`, `regen cost`, `COP`; the kWh rows are present.
2. **ENERGY block: instantaneous COP** — given `instant_w: 100`, `consumed_w: 25`, the COP cell shows `4.0`.
3. **ENERGY block: time-windowed COP** — given today heating + cooling = 1.5, consumed = 0.5, the COP-today cell shows `3.0`.
4. **ENERGY block: COP renders `—` when consumed is zero** — given `consumed_today_kwh: 0`, COP-today shows `—` (not `Infinity` or `NaN`).
5. **ENERGY block: position is above sensors** — assert that within a `.card`, `details.energy` precedes `details.block.sensors` in DOM order (use `.card > *:nth-of-type` or compare bounding-box order).
6. **ENERGY block: instantaneous regen-power formatting** — heating, cooling, and zero cases.

Drop tests that were checking the now-deleted "now:" row and the old 6-cell grid — but keep the existing open-state-survives-re-render test (its data path is unchanged).

The fixture in `loadDashboard`'s `service.energy: {…}` will need the new month fields. Helper update: extend `baseSnapshot`'s default `service.energy` (where present in tests) with `heating_month_kwh: …, cooling_month_kwh: …, consumed_month_kwh: …`.

### Screenshots

Regenerate `tests/ui/screenshots/dashboard-{1col,3col}.png`. Visually different — the block moves up (so cards reflow) and the grid grows from 2×3 to 5×3 (so each card is taller). Anti-aliasing pixel deltas are part of the noise; what matters is the layout is stable across runs.

The screenshot fixture (`tests/ui/screenshot.ts`) needs the new `*_month_kwh` fields populated for the playroom and bedroom devices (whose energy is `supported: true`). Office stays unsupported.

## Out of scope

- No state-file migration — user will delete `<state_dir>/energy_<device>.json`. The Load path coincidentally tolerates missing month fields (zeroes them via month-rollover), so deletion isn't strictly required, but it's the user's stated intent.
- No CHANGELOG / version bump — deferred to whenever the next release tag goes out.
- No tariff config (regen_cost is W of fan electricity, not currency).
- No per-month historical chart — just the running calendar-month total.
- No CLI changes; `breezy <name> status` doesn't surface energy.
- No HomeKit changes; the bridge doesn't expose energy.

## Rollout order

Two-task plan, executed in order:
1. **Backend month tracking** — `breezy.EnergyValues` + `EnergyTracker` + `metrics` + tests. JSON snapshots gain three fields. UI keeps the old layout (still works because the new fields are ignored by the current `energyBody`).
2. **Frontend energy rebuild** — move the block, rebuild the grid, COP, update tests + screenshots.

Each task produces a committable, testable outcome on its own.
