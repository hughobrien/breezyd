# Simplification pass — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Trim ~245 LoC of mechanical duplication in production Go plus ~1000 lines of stale design/plan docs, in one bundled cleanup PR. No behaviour changes; all existing tests pass unchanged.

**Architecture:** Six tasks ordered deletion-first → highest-leverage code refactor last. Each task is independently verifiable and independently committable. Tasks 0-2 are doc-only (zero risk). Tasks 3-5 modify production Go but never change public types, public-method signatures, metric names, or HTTP-response shapes; existing tests are the regression gate.

**Tech Stack:** Go 1.22+, templ, Prometheus client_golang, datastar-go. No new dependencies.

Spec: `docs/superpowers/specs/2026-05-10-simplification-pass-design.md`.

Branch: `chore/simplification-pass` (already exists, off latest main; spec already committed).

---

### Task 0: Delete stale design docs

**Goal:** Remove 3 design docs that describe approaches superseded by later work.

**Files:**
- Delete: `docs/superpowers/specs/2026-05-06-htmx-migration-design.md`
- Delete: `docs/superpowers/specs/2026-05-07-preset-editor-restoration-design.md`
- Delete: `docs/superpowers/specs/2026-05-08-tests-ui-htmx-migration-archive.md`

**Acceptance Criteria:**
- [ ] Three files removed; git status confirms three deletions.
- [ ] `just check` still passes (docs aren't built; sanity check).
- [ ] No code or remaining doc references the deleted paths.

**Verify:**
```sh
grep -rn '2026-05-06-htmx-migration\|2026-05-07-preset-editor-restoration\|2026-05-08-tests-ui-htmx-migration-archive' docs/ cmd/ pkg/ internal/ README.md CLAUDE.md CHANGELOG.md 2>/dev/null
```
Expected: no matches.

**Steps:**

- [ ] **Step 1: Confirm no inbound references**

```sh
grep -rn '2026-05-06-htmx-migration\|2026-05-07-preset-editor-restoration\|2026-05-08-tests-ui-htmx-migration-archive' docs/ cmd/ pkg/ internal/ README.md CLAUDE.md CHANGELOG.md 2>/dev/null
```
Expected: no matches (or only matches inside the files being deleted themselves).

If any external file references them, stop and update the reference first.

- [ ] **Step 2: Delete the three files**

```sh
git rm docs/superpowers/specs/2026-05-06-htmx-migration-design.md
git rm docs/superpowers/specs/2026-05-07-preset-editor-restoration-design.md
git rm docs/superpowers/specs/2026-05-08-tests-ui-htmx-migration-archive.md
```

- [ ] **Step 3: Commit**

```sh
git commit -m "docs: delete superseded design docs (htmx migration, preset-editor cookies, htmx test archive)"
```

---

### Task 1: Delete plan docs for shipped, stable subsystems

**Goal:** Remove 5 implementation-plan docs whose corresponding subsystems shipped and are stable. The matching design doc stays as the evergreen reference.

**Files:**
- Delete: `docs/superpowers/plans/2026-05-04-homekit-bridge.md`
- Delete: `docs/superpowers/plans/2026-05-04-standalone-mode-phase-1-ops-extract.md`
- Delete: `docs/superpowers/plans/2026-05-04-standalone-mode-phase-2-cli-backend.md`
- Delete: `docs/superpowers/plans/2026-05-06-energy-tracking.md`
- Delete: `docs/superpowers/plans/2026-05-06-schedule-system.md`

**Acceptance Criteria:**
- [ ] Five plan files removed.
- [ ] Each matching design file in `specs/` still exists and is unmodified.
- [ ] `just check` passes.

**Verify:**
```sh
ls docs/superpowers/plans/2026-05-04-homekit-bridge.md docs/superpowers/plans/2026-05-04-standalone-mode-phase-1-ops-extract.md docs/superpowers/plans/2026-05-04-standalone-mode-phase-2-cli-backend.md docs/superpowers/plans/2026-05-06-energy-tracking.md docs/superpowers/plans/2026-05-06-schedule-system.md 2>&1
```
Expected: 5 "No such file or directory" errors.

```sh
ls docs/superpowers/specs/2026-05-04-homekit-bridge-design.md docs/superpowers/specs/2026-05-04-standalone-mode-design.md docs/superpowers/specs/2026-05-06-energy-tracking-design.md docs/superpowers/specs/2026-05-06-schedule-system-design.md
```
Expected: all 4 design files listed.

**Steps:**

- [ ] **Step 1: Confirm no inbound references**

```sh
grep -rn 'plans/2026-05-04-homekit-bridge\|plans/2026-05-04-standalone-mode-phase\|plans/2026-05-06-energy-tracking\|plans/2026-05-06-schedule-system' docs/ cmd/ pkg/ internal/ README.md CLAUDE.md CHANGELOG.md 2>/dev/null
```
Expected: no matches.

- [ ] **Step 2: Delete the five plan files**

```sh
git rm docs/superpowers/plans/2026-05-04-homekit-bridge.md
git rm docs/superpowers/plans/2026-05-04-standalone-mode-phase-1-ops-extract.md
git rm docs/superpowers/plans/2026-05-04-standalone-mode-phase-2-cli-backend.md
git rm docs/superpowers/plans/2026-05-06-energy-tracking.md
git rm docs/superpowers/plans/2026-05-06-schedule-system.md
```

- [ ] **Step 3: Commit**

```sh
git commit -m "docs: delete plan docs for shipped-stable subsystems (homekit, standalone-mode, energy, schedule)"
```

---

### Task 2: Tighten CLAUDE.md's "Spec & design docs" section

**Goal:** Replace the chronological bullet list at `CLAUDE.md:161-169` with a subsystem/version-grouped table, removing the now-dead htmx and preset-editor-restoration references.

**Files:**
- Modify: `CLAUDE.md` (the "Spec & design docs" section — currently around lines 161-169; the exact line numbers may shift, locate by the section heading)

**Acceptance Criteria:**
- [ ] The "Spec & design docs" section no longer references htmx-migration or preset-editor-restoration paths.
- [ ] Each remaining link points to a file that exists.
- [ ] `just check` passes.
- [ ] The "Out of scope" section, the "Release plumbing" section, and the rest of CLAUDE.md are unchanged.

**Verify:**
```sh
grep -n 'htmx-migration\|preset-editor-restoration' CLAUDE.md
```
Expected: no matches.

```sh
grep -oE 'docs/superpowers/specs/[0-9a-z-]+\.md' CLAUDE.md | sort -u | while read f; do test -f "$f" && echo "OK $f" || echo "MISSING $f"; done
```
Expected: every line starts with "OK".

**Steps:**

- [ ] **Step 1: Read the current section to find exact text**

```sh
grep -n '## Spec & design docs' CLAUDE.md
```
Note the line number, then read the section (the next ~10 lines after that heading).

- [ ] **Step 2: Replace the section body**

Use the Edit tool to replace the bullet list with the grouped version:

```markdown
## Spec & design docs

Grouped by subsystem / version. Reverse-chronological within each group.

**Protocol + CLI (v1.0):**
- `docs/superpowers/specs/2026-05-03-twinfresh-cli-design.md` — design doc covering protocol decisions, daemon architecture, error semantics, status-line format.
- `docs/superpowers/specs/2026-05-03-param-map.md` — every parameter ID with type, units, observed values.
- `docs/superpowers/specs/breezy-manual-vendor.pdf` — vendor protocol manual.

**Standalone CLI mode (v1.0):**
- `docs/superpowers/specs/2026-05-04-standalone-mode-design.md` — why daemon owns UDP, how the CLI opts into daemon-mode.

**Dashboard substrate (v1.1 → v1.4):**
- `docs/superpowers/specs/2026-05-04-basic-ui-design.md` — original v1.1 motivation: bind-address tradeoff, nginx integration.
- `docs/superpowers/specs/2026-05-08-datastar-migration-design.md` — current substrate (datastar + SSE + templ). Read this before touching the dashboard.

**Device backend interface (v1.2):**
- `docs/superpowers/specs/2026-05-08-device-backend-interface-design.md` — design for the in-process `MemClient` (the seam this CLAUDE.md describes under "Device backend").

Implementation plans live in `docs/superpowers/plans/`; shipped-and-stable plans have been pruned. Designs are the evergreen reference.
```

- [ ] **Step 3: Verify the change**

Run the verify commands above.

- [ ] **Step 4: Commit**

```sh
git add CLAUDE.md
git commit -m "docs(CLAUDE.md): regroup spec list by subsystem; drop dead htmx/preset-editor refs"
```

---

### Task 3: Convert metrics.go to table-driven construction

**Goal:** Replace 33 hand-stamped `prometheus.NewGaugeVec` / `NewCounterVec` calls plus a 22-element registration slice with a `[]metricDef` table + one construction loop + one registration loop. Public field shape, metric names, labels, and help text stay byte-identical so consumers and tests see no change.

**Files:**
- Modify: `cmd/breezyd/metrics.go` (lines ~97-313: `NewMetrics`)

**Acceptance Criteria:**
- [ ] All 34 metric names (33 gauges + 1 counter) emit unchanged.
- [ ] Every public field on `*Metrics` (e.g., `EnergyRecoveredWatts`) is populated after `NewMetrics` returns.
- [ ] Every help text string is preserved byte-identical (Prometheus exporters compare them).
- [ ] `go test ./cmd/breezyd -run TestMetrics` passes without edits to the test file.
- [ ] `metrics.go` shrinks by ~80-100 LoC.
- [ ] `just check` passes.

**Verify:**
```sh
go test ./cmd/breezyd -run TestMetrics -v
```
Expected: all pass.

```sh
wc -l cmd/breezyd/metrics.go
```
Expected: less than the current 525 — target ~420.

```sh
# Spot-check that names and helps survived
go test ./cmd/breezyd -run TestMetrics_NamesUnchanged -v 2>&1 | tail -10
```
Expected: pass (this test is part of `TestMetrics` — see metrics_test.go).

**Steps:**

- [ ] **Step 1: Read the current implementation**

```sh
# Read cmd/breezyd/metrics.go in full to confirm exactly which collectors exist
```
Internalize: per-device labels (`["device","id"]`), plus extras `"fan"`, `"sensor"`, `"position"`, `"kind"`, `["device","id","firmware","build_date"]` for `info`, and `["device"]` for the 11 energy gauges.

- [ ] **Step 2: Run the existing test baseline first**

```sh
go test ./cmd/breezyd -run TestMetrics -v
```
Expected: passes. Capture the output line count for comparison after refactor.

- [ ] **Step 3: Refactor `NewMetrics`**

Use the Edit tool to replace the body of `NewMetrics` (everything between `func NewMetrics(reg prometheus.Registerer) *Metrics {` and the closing `}` at end of function). The refactor adds three local types and three table-driven loops:

```go
// gaugeDef describes one Prometheus gauge collector. The `assign` closure
// stores the constructed *GaugeVec into the right field on *Metrics so the
// field shape that callers depend on is unchanged.
type gaugeDef struct {
	name, help string
	labels     []string
	assign     func(m *Metrics, g *prometheus.GaugeVec)
}

// counterDef mirrors gaugeDef for *CounterVec collectors. Today there's
// just one (poll_errors_total), so this is a one-element table; the shape
// matches gaugeDef for symmetry.
type counterDef struct {
	name, help string
	labels     []string
	assign     func(m *Metrics, c *prometheus.CounterVec)
}

// withExtra returns deviceLabels followed by extras. Built per-call so
// callers can't accidentally share the underlying slice.
func withExtra(extras ...string) []string {
	out := make([]string, 0, len(deviceLabels)+len(extras))
	out = append(out, deviceLabels...)
	out = append(out, extras...)
	return out
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{}

	gauges := []gaugeDef{
		// Configured / state.
		{"breezy_power", "Configured power state (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.power = g }},
		{"breezy_airflow_mode", "Configured airflow mode (0=ventilation, 1=regeneration, 2=supply, 3=extract).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.airflowMode = g }},
		{"breezy_speed_mode", "Configured speed mode (1-3 preset, 255=manual).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.speedMode = g }},
		{"breezy_speed_manual_pct", "Configured manual fan speed percentage (10-100).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.speedManualPct = g }},
		{"breezy_heater_enabled", "User toggle for the electric reheater (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.heaterEnabled = g }},
		{"breezy_humidity_threshold_pct", "Configured humidity threshold (RH%).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.humidityThreshold = g }},
		{"breezy_co2_threshold_ppm", "Configured CO2 threshold (ppm).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.co2Threshold = g }},
		{"breezy_voc_threshold_index", "Configured VOC index threshold.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.vocThreshold = g }},
		{"breezy_humidity_sensor_enabled", "Humidity sensor control (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.humiditySensorEnabled = g }},
		{"breezy_co2_sensor_enabled", "CO2 sensor control (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.co2SensorEnabled = g }},
		{"breezy_voc_sensor_enabled", "VOC sensor control (0=off, 1=on).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.vocSensorEnabled = g }},
		{"breezy_filter_timeout_days", "Filter replacement interval (days).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.filterTimeoutDays = g }},

		// Live state.
		{"breezy_fan_rpm", "Live fan RPM by position.", withExtra("fan"), func(m *Metrics, g *prometheus.GaugeVec) { m.fanRPM = g }},
		{"breezy_heater_running", "Heater is currently energized (0/1). Can be 1 even when heater_enabled=0 due to frost protection.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.heaterRunning = g }},
		{"breezy_in_user_control", "Device is doing what the user configured (1) or under sensor/timer override (0).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.inUserControl = g }},
		{"breezy_special_mode", "Active special mode (0=off, 1=night, 2=turbo).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.specialMode = g }},
		{"breezy_special_mode_remaining_seconds", "Seconds remaining in active special mode.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.specialModeRemaining = g }},
		{"breezy_sensor_alert", "Per-sensor over-threshold flag (0/1) decoded from 0x84.", withExtra("sensor"), func(m *Metrics, g *prometheus.GaugeVec) { m.sensorAlert = g }},
		{"breezy_recovery_efficiency_pct", "Heat-recovery efficiency (0-100%).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.recoveryEfficiency = g }},
		{"breezy_frost_protection_active", "Frost protection currently active (0/1).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.frostProtectionActive = g }},

		// Sensors.
		{"breezy_humidity_percent", "Current room humidity (RH%).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.humidityPct = g }},
		{"breezy_eco2_ppm", "Indoor eCO2 (CO2-equivalent computed from VOC sensor) in ppm.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.eco2PPM = g }},
		{"breezy_voc_index", "Live VOC index (0-500).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.vocIndex = g }},
		{"breezy_temperature_celsius", "Air temperature in degrees Celsius by position.", withExtra("position"), func(m *Metrics, g *prometheus.GaugeVec) { m.temperature = g }},

		// Service / health.
		{"breezy_filter_status", "Filter status (0=clean, 1=soiled).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.filterStatus = g }},
		{"breezy_filter_remaining_seconds", "Filter-change countdown remaining in seconds.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.filterRemainingSeconds = g }},
		{"breezy_motor_lifetime_seconds", "Lifetime motor operation odometer in seconds.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.motorLifetimeSeconds = g }},
		{"breezy_rtc_battery_volts", "RTC backup battery voltage in volts.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.rtcBatteryVolts = g }},
		{"breezy_fault_level", "Top-level fault severity (0=none, 1=alarm, 2=warning).", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.faultLevel = g }},

		// Daemon health.
		{"breezy_last_poll_timestamp", "Unix timestamp of the most recent poll attempt.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.lastPollTimestamp = g }},
		{"breezy_up", "1 if the most recent poll succeeded, else 0.", deviceLabels, func(m *Metrics, g *prometheus.GaugeVec) { m.up = g }},
		{"breezy_info", "Per-device build/firmware diagnostics (constant 1; data is in labels).", []string{"device", "id", "firmware", "build_date"}, func(m *Metrics, g *prometheus.GaugeVec) { m.info = g }},

		// Energy accounting (opt-in; "device"-only label).
		{"breezyd_energy_recovered_watts", "Instantaneous heat-transfer power across the HRV exchanger. Positive = heating recovered (winter), negative = cooling recovered (summer).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyRecoveredWatts = g }},
		{"breezyd_energy_consumed_watts", "Instantaneous electric draw of both fans combined (magnitude).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyConsumedWatts = g }},
		{"breezyd_energy_heating_today_kwh", "Heating energy recovered today (resets at local midnight).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyHeatingTodayKWh = g }},
		{"breezyd_energy_cooling_today_kwh", "Cooling energy recovered today (resets at local midnight).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyCoolingTodayKWh = g }},
		{"breezyd_energy_consumed_today_kwh", "Electric energy consumed by the fans today (resets at local midnight).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyConsumedTodayKWh = g }},
		{"breezyd_energy_heating_month_kwh", "Heating energy recovered this calendar month (resets on first-of-month, local TZ).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyHeatingMonthKWh = g }},
		{"breezyd_energy_cooling_month_kwh", "Cooling energy recovered this calendar month (resets on first-of-month, local TZ).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyCoolingMonthKWh = g }},
		{"breezyd_energy_consumed_month_kwh", "Electric energy consumed by the fans this calendar month (resets on first-of-month, local TZ).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyConsumedMonthKWh = g }},
		{"breezyd_energy_heating_lifetime_kwh", "Heating energy recovered cumulative (persists across daemon restart).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyHeatingLifetimeKWh = g }},
		{"breezyd_energy_cooling_lifetime_kwh", "Cooling energy recovered cumulative (persists across daemon restart).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyCoolingLifetimeKWh = g }},
		{"breezyd_energy_consumed_lifetime_kwh", "Electric energy consumed by the fans cumulative (persists across daemon restart).", []string{"device"}, func(m *Metrics, g *prometheus.GaugeVec) { m.EnergyConsumedLifetimeKWh = g }},
	}

	counters := []counterDef{
		{"breezy_poll_errors_total", "Total number of poll errors, by classification.", withExtra("kind"), func(m *Metrics, c *prometheus.CounterVec) { m.pollErrorsTotal = c }},
	}

	gaugeCollectors := make([]prometheus.Collector, 0, len(gauges))
	for _, d := range gauges {
		g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: d.name, Help: d.help}, d.labels)
		d.assign(m, g)
		gaugeCollectors = append(gaugeCollectors, g)
	}
	counterCollectors := make([]prometheus.Collector, 0, len(counters))
	for _, d := range counters {
		c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: d.name, Help: d.help}, d.labels)
		d.assign(m, c)
		counterCollectors = append(counterCollectors, c)
	}

	if reg != nil {
		for _, c := range gaugeCollectors {
			reg.MustRegister(c)
		}
		for _, c := range counterCollectors {
			reg.MustRegister(c)
		}
	}
	return m
}
```

The two top-level vars (`deviceLabels` line ~92) and the helpers (`Update`, `SetEnergy`, `RecordPollError`, `boolish`, `metricUint8`, `metricUint16`, `metricInt16`) stay unchanged below.

- [ ] **Step 4: Verify the refactor**

```sh
go build ./cmd/breezyd
go test ./cmd/breezyd -run TestMetrics -v
```
Expected: build succeeds, all metrics tests pass.

```sh
just check
```
Expected: passes.

```sh
wc -l cmd/breezyd/metrics.go
```
Expected: ~420 (down from 525).

- [ ] **Step 5: Commit**

```sh
git add cmd/breezyd/metrics.go
git commit -m "refactor(metrics): convert collector construction to table-driven NewMetrics"
```

---

### Task 4: Extract a `postUIWriteJSON` helper and collapse the 8 /ui write handlers

**Goal:** Introduce one generic helper that owns the device-resolve + JSON-decode + op-call + error-routing + notify shape that 8 `/ui/devices/{name}/...` handlers repeat verbatim. Each handler shrinks to ~6-10 lines. Also drop the per-handler value-range/enum validation: `pkg/breezy/ops.go` already returns `ErrInvalidArg` with well-worded messages; we add an `ErrInvalidArg → 422` branch to `uiWriteError` so those messages reach the SSE banner unchanged.

**Files:**
- Modify: `cmd/breezyd/handlers_ui_write.go` (the 8 `postUI*` action handlers at lines 296-540, plus `uiWriteError` at line 196-203)

**Acceptance Criteria:**
- [ ] One new helper function `postUIWriteJSON` exists with a clear signature documented in a one-line comment.
- [ ] Each of the 8 handlers (`postUIMode`, `postUIPreset`, `postUISpeed`, `postUIHeater`, `postUITimer`, `postUIPower`, `postUIResetFilter`, `postUIResetFaults`) uses the helper.
- [ ] `uiWriteError` routes `breezy.ErrInvalidArg` to HTTP 422 (not 502) and surfaces the error message verbatim.
- [ ] All existing `handlers_ui_write_test.go` cases pass without edits.
- [ ] `cmd/breezyd/handlers_ui_write.go` shrinks by ~80-120 LoC.
- [ ] `just test` and `just test-ui` pass.

**Verify:**
```sh
go test ./cmd/breezyd -run 'TestPostUI|TestPutUI|TestUIWriteError' -v
```
Expected: all pass.

```sh
just test-ui 2>&1 | tail -20
```
Expected: pass (the dashboard end-to-end exercise still works).

```sh
wc -l cmd/breezyd/handlers_ui_write.go
```
Expected: ~560 (down from 679).

**Steps:**

- [ ] **Step 1: Read the current handlers in full to baseline behaviour**

The 8 handlers are at:
- `postUIMode` (296-324)
- `postUIPreset` (329-364)
- `postUISpeed` (369-407)
- `postUIHeater` (412-437)
- `postUITimer` (446-474)
- `postUIResetFilter` (477-491) — no body
- `postUIResetFaults` (494-508) — no body
- `postUIPower` (513-538)

Each follows:
1. `name := r.PathValue("name")`
2. `h.Devices.Get(name)` 404 check
3. `var req struct{...}` + `h.decodeJSONBody` (skip for the two reset handlers)
4. Inline validation (mode enum, range checks, required-ptr checks)
5. `h.doDeviceOp(r, name, ...)` calling a `breezy.Set*` op
6. `h.uiWriteError` on err
7. `h.notifyAfterWrite(name)`; `w.WriteHeader(http.StatusOK)`

- [ ] **Step 2: Run existing tests as baseline**

```sh
go test ./cmd/breezyd -run 'TestPostUI|TestPutUI|TestUIWriteError' -v 2>&1 | tail -30
```
Expected: all pass. Note the test names so you can spot regressions after the refactor.

- [ ] **Step 3: Update `uiWriteError` to route `ErrInvalidArg` as 422**

Edit `cmd/breezyd/handlers_ui_write.go:196-203`. Replace the existing `uiWriteError` body:

```go
// uiWriteError emits a datastar-patch-elements event into
// #global-error-banner with the error message and the matching HTTP
// status. ErrInvalidArg from pkg/breezy/ops surfaces as 422 with the
// op's own message — that's the single source of truth for protocol
// validation (see ops.go). Auth failures are 401; other backend
// errors are 502. Caller should return after this.
func (h *Handler) uiWriteError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, breezy.ErrAuth):
		errorBannerSSE(w, r, http.StatusUnauthorized, "device authentication failed")
	case errors.Is(err, breezy.ErrInvalidArg):
		errorBannerSSE(w, r, http.StatusUnprocessableEntity, err.Error())
	default:
		errorBannerSSE(w, r, http.StatusBadGateway, uiBannerMsg(err))
	}
}
```

- [ ] **Step 4: Add the `postUIWriteJSON` helper**

Add this helper just below `decodeJSONBody` (around line 291). It uses Go generics:

```go
// postUIWriteJSON is the spine shared by every /ui/devices/{name}/...
// action handler. It resolves the device, decodes the JSON body into a
// fresh T, runs an optional shape-validator (for nil-pointer "field
// required" checks that precede the device round-trip), executes the
// op via doDeviceOp, surfaces errors through uiWriteError, and on
// success notifies subscribers and writes 200. The op closure may
// return breezy.ErrInvalidArg for value-range failures; uiWriteError
// translates that into a 422 banner with the op's own message.
//
// The `shapeOK` callback is for required-pointer checks where the
// caller wants the banner to say "missing 'on' field" instead of
// letting the op succeed silently or fail downstream. Return false
// after emitting your own error to abort; return true to continue.
// Pass nil if no shape check is needed.
func postUIWriteJSON[T any](
	h *Handler,
	w http.ResponseWriter,
	r *http.Request,
	shapeOK func(req *T) bool,
	op func(ctx context.Context, rc *recordingClient, req *T) error,
) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	var req T
	if !h.decodeJSONBody(w, r, name, &req) {
		return
	}
	if shapeOK != nil && !shapeOK(&req) {
		return
	}
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return op(ctx, rc, &req)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}

// postUIWriteNoBody is the body-less counterpart used by reset
// endpoints. Same flow without the decode + shape steps.
func postUIWriteNoBody(
	h *Handler,
	w http.ResponseWriter,
	r *http.Request,
	op func(ctx context.Context, rc *recordingClient) error,
) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.doDeviceOp(r, name, op); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}
```

- [ ] **Step 5: Rewrite the 8 handlers**

Replace the bodies of the 8 handlers in `cmd/breezyd/handlers_ui_write.go`. Below is the exact replacement set. The request struct types are now declared once inside each handler (still type-local — generics on closures don't see anonymous types, so we use named structs).

```go
// postUIMode sets the airflow mode.
//
// JSON: {"mode": "ventilation" | "regeneration" | "supply" | "extract"}
func (h *Handler) postUIMode(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Mode string `json:"mode"`
	}
	postUIWriteJSON(h, w, r, nil, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.SetMode(ctx, rc, q.Mode)
	})
}

// postUIPreset writes the per-preset supply/extract percentages.
//
// JSON: {"preset": 1|2|3, "supply": 10..100, "extract": 10..100}
func (h *Handler) postUIPreset(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Preset  int `json:"preset"`
		Supply  int `json:"supply"`
		Extract int `json:"extract"`
	}
	postUIWriteJSON(h, w, r, nil, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.SetPresetSpeed(ctx, rc, q.Preset, q.Supply, q.Extract)
	})
}

// postUISpeed sets the fan speed (manual percentage or preset).
//
// JSON: {"manual": N} (10..100) XOR {"preset": N} (1..3)
func (h *Handler) postUISpeed(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Manual *int `json:"manual,omitempty"`
		Preset *int `json:"preset,omitempty"`
	}
	shape := func(q *req) bool {
		hasManual := q.Manual != nil
		hasPreset := q.Preset != nil
		if hasManual == hasPreset {
			h.uiValidationError(w, r, "", "set exactly one of 'preset' (1-3) or 'manual' (10-100)")
			return false
		}
		return true
	}
	postUIWriteJSON(h, w, r, shape, func(ctx context.Context, rc *recordingClient, q *req) error {
		if q.Preset != nil {
			return breezy.SetSpeedPreset(ctx, rc, *q.Preset)
		}
		return breezy.SetSpeedManual(ctx, rc, *q.Manual)
	})
}

// postUIHeater toggles the heater.
//
// JSON: {"on": bool}
func (h *Handler) postUIHeater(w http.ResponseWriter, r *http.Request) {
	type req struct {
		On *bool `json:"on"`
	}
	shape := func(q *req) bool {
		if q.On == nil {
			h.uiValidationError(w, r, "", "missing 'on' field (true/false)")
			return false
		}
		return true
	}
	postUIWriteJSON(h, w, r, shape, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.SetHeater(ctx, rc, *q.On)
	})
}

// postUITimer toggles a special-mode timer.
//
// JSON: {"mode": "off" | "night" | "turbo"}
func (h *Handler) postUITimer(w http.ResponseWriter, r *http.Request) {
	type req struct {
		Mode string `json:"mode"`
	}
	postUIWriteJSON(h, w, r, nil, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.SetTimer(ctx, rc, q.Mode)
	})
}

// postUIResetFilter resets the filter-clogged counter. No form body needed.
func (h *Handler) postUIResetFilter(w http.ResponseWriter, r *http.Request) {
	postUIWriteNoBody(h, w, r, func(ctx context.Context, rc *recordingClient) error {
		return breezy.ResetFilter(ctx, rc)
	})
}

// postUIResetFaults clears the active fault list. No form body needed.
func (h *Handler) postUIResetFaults(w http.ResponseWriter, r *http.Request) {
	postUIWriteNoBody(h, w, r, func(ctx context.Context, rc *recordingClient) error {
		return breezy.ResetFaults(ctx, rc)
	})
}

// postUIPower toggles a device on/off.
//
// JSON: {"on": bool}
func (h *Handler) postUIPower(w http.ResponseWriter, r *http.Request) {
	type req struct {
		On *bool `json:"on"`
	}
	shape := func(q *req) bool {
		if q.On == nil {
			h.uiValidationError(w, r, "", "missing 'on' field (true/false)")
			return false
		}
		return true
	}
	postUIWriteJSON(h, w, r, shape, func(ctx context.Context, rc *recordingClient, q *req) error {
		return breezy.Power(ctx, rc, *q.On)
	})
}
```

Note: `uiValidationError`'s second positional arg (`name string`) is unused per the existing comment at line 227. We pass `""` since we have no easy access to name inside the closure. If a future refactor wants name, the helper can stash it on the closure.

- [ ] **Step 6: Build and run the targeted tests**

```sh
go build ./cmd/breezyd
go test ./cmd/breezyd -run 'TestPostUI|TestPutUI|TestUIWriteError|TestUIBannerMsg' -v
```
Expected: all pass. The tests assert HTTP status codes (422 for validation, 401 for auth, 502 for other), SSE event content, and the post-write notify behaviour — all of which the new helper preserves.

If any test asserts the previous 502 status for a range-failure case (e.g., out-of-range manual percent), it will now see 422 — that's the intended semantic improvement, so the test should be updated to expect 422. Inspect such failures: if the message changed but the status moved 502→422, that's a correct, expected behaviour change in line with the spec; update the test assertion. Document the changed expectation in the commit message.

- [ ] **Step 7: Run the full Go check + UI suite**

```sh
just check
just test-ui
```
Expected: pass.

```sh
wc -l cmd/breezyd/handlers_ui_write.go
```
Expected: ~560 (down from 679; ~120 LoC saved).

- [ ] **Step 8: Commit**

```sh
git add cmd/breezyd/handlers_ui_write.go cmd/breezyd/handlers_ui_write_test.go
git commit -m "$(cat <<'EOF'
refactor(handlers/ui): collapse 8 write handlers via postUIWriteJSON helper

Each /ui/devices/{name}/... action handler shared a verbatim spine:
resolve device, decode JSON, validate, doDeviceOp, route error, notify,
200. Extract that spine into postUIWriteJSON[T] (+ postUIWriteNoBody
for resets) and let each handler reduce to its op closure plus an
optional shape check.

Also route breezy.ErrInvalidArg through uiWriteError as HTTP 422 with
the op's own message. That removes the per-handler value-range and
enum pre-validation: pkg/breezy/ops.go is already the single source
of truth for those rules (it has well-worded ErrInvalidArg messages),
and dashboard users now see the same wording the daemon's JSON API
already returns. No protocol or wire-format change.

~120 LoC saved in handlers_ui_write.go.
EOF
)"
```

---

### Task 5: Final verification + spec status update

**Goal:** Run the full `ci` gate to confirm the bundle is regression-free, then mark the spec as shipped.

**Files:**
- Modify: `docs/superpowers/specs/2026-05-10-simplification-pass-design.md` (update the status line from "Drafted, awaiting plan." to "Shipped 2026-05-10.")

**Acceptance Criteria:**
- [ ] `just ci` passes end-to-end (vet + race + ui + asan + msan + staticcheck + templ-drift + test-test-admin).
- [ ] `git log --oneline main..HEAD` shows 5 distinct commits (one per task 0-4), plus the original spec commit.
- [ ] Spec doc status reflects "Shipped".

**Verify:**
```sh
just ci 2>&1 | tail -30
```
Expected: all jobs pass.

```sh
git log --oneline main..HEAD
```
Expected: 6 commits — spec + tasks 0-4.

**Steps:**

- [ ] **Step 1: Run the full CI gate**

```sh
just ci
```
Expected: pass. If it fails on a non-flaky job (race, msan, asan), investigate before claiming task done.

- [ ] **Step 2: Update the spec status line**

Edit `docs/superpowers/specs/2026-05-10-simplification-pass-design.md`:

```diff
- Status: Drafted, awaiting plan.
+ Status: Shipped 2026-05-10.
```

Add a short "Outcome" section at the bottom listing the actual LoC delta from `git diff main --stat`.

- [ ] **Step 3: Commit + open PR**

```sh
git add docs/superpowers/specs/2026-05-10-simplification-pass-design.md
git commit -m "docs: mark simplification pass spec as shipped"
git push -u origin chore/simplification-pass
gh pr create --title "chore: simplification pass — metrics, UI handlers, doc pruning" --body "$(cat <<'EOF'
## Summary
- Delete 3 stale design docs + 5 plan docs for shipped subsystems.
- Regroup CLAUDE.md's spec list by subsystem; drop dead refs.
- Convert metrics.go to table-driven collector construction (~95 LoC saved).
- Collapse 8 /ui write handlers via postUIWriteJSON helper + route ErrInvalidArg as 422 (~120 LoC saved).

Spec: \`docs/superpowers/specs/2026-05-10-simplification-pass-design.md\`.

## Test plan
- [x] just ci (vet + race + ui + asan + msan + staticcheck + templ-drift + test-test-admin) passes.
- [x] Metric names and help strings unchanged (existing TestMetrics_* covers).
- [x] /ui write handlers preserve status semantics, with one intended change: ErrInvalidArg is now 422 (was 502).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Auto-merge**

```sh
gh pr merge --squash --auto
```

This follows the user's standing preference (see memory).

---

## Out of scope

Items in the spec's "Out of scope" section are deliberately not pursued. Do not expand the PR by adding them.

## Self-review trail (during writing)

- Spec coverage: every numbered item in the spec maps to a task. Item 1 → Task 0, Item 2 → Task 1, Item 3 → Task 2, Item 4 → Task 3, Item 5 + revised Item 6 → Task 4, plus Task 5 closes the loop.
- Placeholder scan: none.
- Type consistency: `postUIWriteJSON[T any]`, `postUIWriteNoBody` named consistently; helper signature matches every call site; `*recordingClient` used consistently (matches existing `doDeviceOp` signature).
- Spec revision: item 6 corrected during planning after re-reading handlers_device.go and pkg/breezy/ops.go; spec was updated in-place to match the corrected approach before the plan was written.
