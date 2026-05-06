# Energy block — month tracking + 5×3 layout implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve issue #30 by adding calendar-month energy counters on the daemon side, rebuilding the dashboard ENERGY block as a 5×3 grid (instantaneous row + COP / heating / cooling / consumed across today / month / lifetime), and moving the block above the Sensors block.

**Architecture:** Two-phase change with a clean dependency: Task 1 extends `breezy.EnergyValues`, `EnergyTracker`, and Prometheus metrics with month counters and rollover; Task 2 rebuilds the dashboard's `energyBody()` and moves the call site, consuming Task 1's new JSON fields. Each task lands as one commit with its own tests and is independently green.

**Tech Stack:** Go 1.x for the daemon (`pkg/breezy`, `cmd/breezyd`), Prometheus client_golang for metrics, vanilla HTML/CSS/JS for the dashboard, Playwright (`@playwright/test`) for UI regression tests, `just` recipes for build/test/screenshot.

**Spec:** `docs/superpowers/specs/2026-05-06-energy-block-rearrange-design.md`

---

## File Structure

### Task 1 — backend month tracking

- **Modify** `pkg/breezy/energy.go` — add three `*MonthKWh` fields to `EnergyValues` (JSON `*_month_kwh`).
- **Modify** `cmd/breezyd/energy_tracker.go` — add three counters + `MonthStart` to both the in-memory struct and `persistedEnergy`; add a month-rollover branch to `Tick()` mirroring the existing date branch; add a month-restore branch to `Load()`; populate the new fields in `Snapshot()`; accumulate alongside today/lifetime in the math block.
- **Modify** `cmd/breezyd/metrics.go` — three new gauges (`breezyd_energy_{heating,cooling,consumed}_month_kwh`); register them; set them in `SetEnergy()`; include in the `all` slice for the unsupported-clear path.
- **Modify** `cmd/breezyd/energy_tracker_test.go` — two new tests `TestEnergyTracker_Tick_MonthRollover` and `TestEnergyTracker_Tick_MonthRolloverPersists` mirroring the existing `..._DateRollover` and `..._RolloverPersists`.
- **Modify** `cmd/breezyd/metrics_test.go` — extend `TestMetrics_SetEnergy_Supported` and `TestMetrics_SetEnergy_UnsupportedDropsLabels` to cover the three new gauges.
- **Modify** `cmd/breezyd/server_test.go` — extend `TestHandler_GetDevice_IncludesEnergy` to seed and assert the month fields round-trip through `/v1/devices/<name>`.

### Task 2 — frontend rebuild + reposition

- **Modify** `cmd/breezyd/ui/index.html` — move the `${renderEnergy(name, snap)}` call site in `renderCard()` from after `renderControls` (~ line 495) to immediately before the sensors `<details>` IIFE (~ line 484); rebuild `energyBody(ev)` (~ lines 514–534) into a single 5×3 grid with the COP-formatting helpers.
- **Modify** `tests/ui/dashboard.spec.ts` — keep the open-state-survives-re-render test; replace the four other ENERGY tests; add four new tests (instantaneous COP, time-windowed COP, COP `—` divide-by-zero, position-above-sensors).
- **Modify** `tests/ui/screenshot.ts` — extend the fixture's `service.energy` block for `playroom` and `bedroom` with `heating_month_kwh`, `cooling_month_kwh`, `consumed_month_kwh`.
- **Regenerate** `tests/ui/screenshots/dashboard-1col.png` and `tests/ui/screenshots/dashboard-3col.png`.

No CLI / HomeKit / module changes.

---

## Task 1: Backend month tracking

**Goal:** `EnergyTracker` accumulates `Heating/Cooling/Consumed-MonthKWh` alongside today and lifetime, with calendar-month rollover (local TZ) parallel to the existing date rollover. JSON snapshots and Prometheus gauges expose all three. UI is untouched and existing UI tests continue to pass.

**Files:**
- Modify: `pkg/breezy/energy.go` (struct definition ~ lines 96–106)
- Modify: `cmd/breezyd/energy_tracker.go` (struct ~ 38–73, `persistedEnergy` ~ 25–34, `Load` ~ 86–132, `save` ~ 136–162, `Snapshot` ~ 166–181, `Tick` ~ 203–300)
- Modify: `cmd/breezyd/metrics.go` (struct fields ~ 80–85, gauge registration ~ 250–273, collectors slice ~ 288–290, `SetEnergy` ~ 452–472)
- Modify: `cmd/breezyd/energy_tracker_test.go` (add two tests after the existing rollover tests at line 413)
- Modify: `cmd/breezyd/metrics_test.go` (extend tests at lines 359 and 399)
- Modify: `cmd/breezyd/server_test.go` (extend `TestHandler_GetDevice_IncludesEnergy` at line 381)

**Acceptance Criteria:**
- [ ] `breezy.EnergyValues` has new fields `HeatingMonthKWh`, `CoolingMonthKWh`, `ConsumedMonthKWh` with JSON tags `*_month_kwh`.
- [ ] `persistedEnergy` JSON shape gains the same three fields plus `MonthStart` (`json:"month_start"`).
- [ ] `EnergyTracker` struct gains the same four fields (three counters + `MonthStart`).
- [ ] `Load()` zeroes the three month counters when persisted `MonthStart != currentMonth`; restores them when equal.
- [ ] `Tick()` zeroes the three month counters and persists when `e.MonthStart != now.Local().Format("2006-01")`, parallel to the existing today rollover.
- [ ] `Tick()`'s accumulation block adds the same delta to `*MonthKWh` that it adds to `*LifetimeKWh` (heating goes to heating-month, cooling to cooling-month, consumed always).
- [ ] `Snapshot()` populates the three new fields.
- [ ] Three new Prometheus gauges (`breezyd_energy_heating_month_kwh`, `..._cooling_month_kwh`, `..._consumed_month_kwh`) are registered and emitted.
- [ ] `SetEnergy()` sets the three new gauges on the supported path, and `DeleteLabelValues` clears them on the Error path (i.e. they're in the `all` slice).
- [ ] `TestHandler_GetDevice_IncludesEnergy` asserts `heating_month_kwh`, `cooling_month_kwh`, `consumed_month_kwh` round-trip through the JSON response.
- [ ] New `TestEnergyTracker_Tick_MonthRollover` verifies a Tick whose local-TZ month differs from `MonthStart` zeroes the three month counters but does NOT touch today or lifetime.
- [ ] New `TestEnergyTracker_Tick_MonthRolloverPersists` verifies the rollover save survives a `Load()`.
- [ ] `just check` passes.
- [ ] Existing UI tests still pass (frontend unchanged in this task).

**Verify:** `just check && just test-ui` → both green.

**Steps:**

- [ ] **Step 1: Write the failing tests (TDD red)**

In `cmd/breezyd/energy_tracker_test.go`, add two new tests immediately after `TestEnergyTracker_Tick_RolloverPersists` (the existing test ends at line 413):

```go
func TestEnergyTracker_Tick_MonthRollover(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:             "month-rollover",
		StateDir:           dir,
		HeatingTodayKWh:    1.5,
		HeatingMonthKWh:    20.0,
		HeatingLifetimeKWh: 100.0,
		Today:              "2026-04-30",
		MonthStart:         "2026-04",
	}
	// Tick at just after midnight on 2026-05-01: BOTH day and month roll over.
	t1 := time.Date(2026, 5, 1, 0, 0, 5, 0, time.Local)
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("HeatingTodayKWh = %v after rollover, want 0", snap.HeatingTodayKWh)
	}
	if snap.HeatingMonthKWh != 0 {
		t.Errorf("HeatingMonthKWh = %v after rollover, want 0", snap.HeatingMonthKWh)
	}
	if snap.HeatingLifetimeKWh != 100.0 {
		t.Errorf("HeatingLifetimeKWh = %v, want 100 (lifetime must survive rollover)", snap.HeatingLifetimeKWh)
	}
	if tr.MonthStart != "2026-05" {
		t.Errorf("MonthStart = %q, want 2026-05", tr.MonthStart)
	}
}

func TestEnergyTracker_Tick_MonthRolloverPersists(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:          "month-rollover-persist",
		StateDir:        dir,
		HeatingMonthKWh: 5.0,
		// Same calendar day so the date branch is a no-op; only the month
		// branch fires. Use mid-day on the 1st.
		Today:      "2026-05-01",
		MonthStart: "2026-04",
	}
	t1 := time.Date(2026, 5, 1, 12, 0, 0, 0, time.Local)
	notRegen := makeRegenSnap(50, 0, 20)
	notRegen[0x00B7] = []byte{0} // ventilation; early-return after rollovers
	tr.Tick(notRegen, t1)

	tr2 := &EnergyTracker{Device: "month-rollover-persist", StateDir: dir}
	if err := tr2.Load(); err != nil {
		t.Fatalf("Load after rollover: %v", err)
	}
	if tr2.MonthStart != "2026-05" {
		t.Errorf("persisted MonthStart = %q, want 2026-05", tr2.MonthStart)
	}
	if tr2.HeatingMonthKWh != 0 {
		t.Errorf("persisted HeatingMonthKWh = %v, want 0 after rollover", tr2.HeatingMonthKWh)
	}
}
```

In `cmd/breezyd/metrics_test.go`, extend `TestMetrics_SetEnergy_Supported` (~ line 359):

```go
func TestMetrics_SetEnergy_Supported(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	m.SetEnergy("playroom", breezy.EnergyValues{
		Supported:           true,
		InstantW:            245,
		ConsumedW:           18,
		HeatingTodayKWh:     1.234,
		ConsumedTodayKWh:    0.123,
		HeatingMonthKWh:     30.0,
		CoolingMonthKWh:     5.5,
		ConsumedMonthKWh:    3.7,
		HeatingLifetimeKWh:  234.5,
		ConsumedLifetimeKWh: 12.3,
	})
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"breezyd_energy_recovered_watts":       245,
		"breezyd_energy_consumed_watts":        18,
		"breezyd_energy_heating_today_kwh":     1.234,
		"breezyd_energy_consumed_today_kwh":    0.123,
		"breezyd_energy_heating_month_kwh":     30.0,
		"breezyd_energy_cooling_month_kwh":     5.5,
		"breezyd_energy_consumed_month_kwh":    3.7,
		"breezyd_energy_heating_lifetime_kwh":  234.5,
		"breezyd_energy_consumed_lifetime_kwh": 12.3,
	}
	for _, fam := range families {
		w, ok := want[fam.GetName()]
		if !ok {
			continue
		}
		got := fam.GetMetric()[0].GetGauge().GetValue()
		if got != w {
			t.Errorf("%s = %v, want %v", fam.GetName(), got, w)
		}
		delete(want, fam.GetName())
	}
	for name := range want {
		t.Errorf("expected metric %s not emitted", name)
	}
}
```

And extend the `energyGauges` map in `TestMetrics_SetEnergy_UnsupportedDropsLabels` (~ line 417):

```go
energyGauges := map[string]bool{
	"breezyd_energy_recovered_watts":       true,
	"breezyd_energy_consumed_watts":        true,
	"breezyd_energy_heating_today_kwh":     true,
	"breezyd_energy_cooling_today_kwh":     true,
	"breezyd_energy_consumed_today_kwh":    true,
	"breezyd_energy_heating_month_kwh":     true,
	"breezyd_energy_cooling_month_kwh":     true,
	"breezyd_energy_consumed_month_kwh":    true,
	"breezyd_energy_heating_lifetime_kwh":  true,
	"breezyd_energy_cooling_lifetime_kwh":  true,
	"breezyd_energy_consumed_lifetime_kwh": true,
}
```

In `cmd/breezyd/server_test.go`, extend `TestHandler_GetDevice_IncludesEnergy` (~ line 381) to seed and assert the new fields:

```go
func TestHandler_GetDevice_IncludesEnergy(t *testing.T) {
	h, _, _ := newServerHandler(t)
	dir := t.TempDir()
	today := time.Now().Local().Format("2006-01-02")
	thisMonth := time.Now().Local().Format("2006-01")
	tr := &EnergyTracker{
		Device:             "playroom",
		StateDir:           dir,
		HeatingTodayKWh:    1.234,
		HeatingMonthKWh:    30.0,
		HeatingLifetimeKWh: 234.5,
		Today:              today,
		MonthStart:         thisMonth,
	}
	if h.Pollers == nil {
		h.Pollers = map[string]*Poller{}
	}
	h.Pollers["playroom"] = &Poller{Energy: tr}
	seedSnapshot(t, h, "playroom", snapshotAllParams(t))

	rec := doRequest(t, h, http.MethodGet, "/v1/devices/playroom", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	service, ok := resp["service"].(map[string]any)
	if !ok {
		t.Fatalf("service block missing or wrong type: %v", resp)
	}
	energy, ok := service["energy"].(map[string]any)
	if !ok {
		t.Fatalf("service.energy missing or wrong type: %v", service)
	}
	if energy["heating_today_kwh"] != 1.234 {
		t.Errorf("heating_today_kwh = %v, want 1.234", energy["heating_today_kwh"])
	}
	if energy["heating_month_kwh"] != 30.0 {
		t.Errorf("heating_month_kwh = %v, want 30.0", energy["heating_month_kwh"])
	}
	if energy["heating_lifetime_kwh"] != 234.5 {
		t.Errorf("heating_lifetime_kwh = %v, want 234.5", energy["heating_lifetime_kwh"])
	}
}
```

- [ ] **Step 2: Run the new + extended tests; expect them to fail**

```sh
go test ./cmd/breezyd -run 'TestEnergyTracker_Tick_MonthRollover|TestEnergyTracker_Tick_MonthRolloverPersists|TestMetrics_SetEnergy_Supported|TestMetrics_SetEnergy_UnsupportedDropsLabels|TestHandler_GetDevice_IncludesEnergy' -v
go test ./pkg/breezy -v   # confirm baseline still green
```

Expected: the four month-related tests **FAIL** (compile error or assertion failure), because `EnergyValues` doesn't yet have month fields, `EnergyTracker` doesn't have `MonthStart` etc., and `Metrics` doesn't have the new gauges. The `pkg/breezy` baseline should be green.

- [ ] **Step 3: Extend `breezy.EnergyValues`**

In `pkg/breezy/energy.go`, replace the existing `EnergyValues` struct (~ lines 96–106) with:

```go
type EnergyValues struct {
	Supported           bool    `json:"supported"`
	InstantW            float64 `json:"instant_w"`
	ConsumedW           float64 `json:"consumed_w"`
	HeatingTodayKWh     float64 `json:"heating_today_kwh"`
	CoolingTodayKWh     float64 `json:"cooling_today_kwh"`
	ConsumedTodayKWh    float64 `json:"consumed_today_kwh"`
	HeatingMonthKWh     float64 `json:"heating_month_kwh"`
	CoolingMonthKWh     float64 `json:"cooling_month_kwh"`
	ConsumedMonthKWh    float64 `json:"consumed_month_kwh"`
	HeatingLifetimeKWh  float64 `json:"heating_lifetime_kwh"`
	CoolingLifetimeKWh  float64 `json:"cooling_lifetime_kwh"`
	ConsumedLifetimeKWh float64 `json:"consumed_lifetime_kwh"`
	Error               string  `json:"error,omitempty"`
}
```

(Three new lines inserted between today and lifetime.)

- [ ] **Step 4: Extend `persistedEnergy` and `EnergyTracker`**

In `cmd/breezyd/energy_tracker.go`, replace the existing `persistedEnergy` struct (~ lines 25–34) with:

```go
type persistedEnergy struct {
	TodayDate           string  `json:"today_date"`
	MonthStart          string  `json:"month_start"`
	HeatingTodayKWh     float64 `json:"heating_today_kwh"`
	CoolingTodayKWh     float64 `json:"cooling_today_kwh"`
	ConsumedTodayKWh    float64 `json:"consumed_today_kwh"`
	HeatingMonthKWh     float64 `json:"heating_month_kwh"`
	CoolingMonthKWh     float64 `json:"cooling_month_kwh"`
	ConsumedMonthKWh    float64 `json:"consumed_month_kwh"`
	HeatingLifetimeKWh  float64 `json:"heating_lifetime_kwh"`
	CoolingLifetimeKWh  float64 `json:"cooling_lifetime_kwh"`
	ConsumedLifetimeKWh float64 `json:"consumed_lifetime_kwh"`
	LastUpdated         string  `json:"last_updated"`
}
```

In the same file, extend the `EnergyTracker` struct (~ lines 38–73). Add the three counters and `MonthStart` next to their today equivalents. The mu-guarded fields section becomes:

```go
	// Today counters (reset on calendar-date rollover).
	HeatingTodayKWh  float64
	CoolingTodayKWh  float64
	ConsumedTodayKWh float64

	// Month counters (reset on calendar-month rollover, local TZ).
	HeatingMonthKWh  float64
	CoolingMonthKWh  float64
	ConsumedMonthKWh float64

	// Lifetime counters (monotonically increasing).
	HeatingLifetimeKWh  float64
	CoolingLifetimeKWh  float64
	ConsumedLifetimeKWh float64
```

And add `MonthStart` next to `Today`:

```go
	// Today is the YYYY-MM-DD date (system local TZ) of the current rolling day.
	Today string
	// MonthStart is the YYYY-MM month (system local TZ) of the current month counters.
	MonthStart string
```

- [ ] **Step 5: Extend `Load()` with month-restore branch**

In `cmd/breezyd/energy_tracker.go`, replace the section of `Load()` from "Restore lifetime counters unconditionally." through the date-restore branch (~ lines 109–126) with:

```go
	// Restore lifetime counters unconditionally.
	e.HeatingLifetimeKWh = p.HeatingLifetimeKWh
	e.CoolingLifetimeKWh = p.CoolingLifetimeKWh
	e.ConsumedLifetimeKWh = p.ConsumedLifetimeKWh

	// Restore today counters only if the stored date matches today.
	if p.TodayDate == today {
		e.HeatingTodayKWh = p.HeatingTodayKWh
		e.CoolingTodayKWh = p.CoolingTodayKWh
		e.ConsumedTodayKWh = p.ConsumedTodayKWh
		e.Today = p.TodayDate
	} else {
		// Calendar rollover: zero today counters, carry lifetime forward.
		e.HeatingTodayKWh = 0
		e.CoolingTodayKWh = 0
		e.ConsumedTodayKWh = 0
		e.Today = today
	}

	// Restore month counters only if the stored month matches this month.
	thisMonth := time.Now().Local().Format("2006-01")
	if p.MonthStart == thisMonth {
		e.HeatingMonthKWh = p.HeatingMonthKWh
		e.CoolingMonthKWh = p.CoolingMonthKWh
		e.ConsumedMonthKWh = p.ConsumedMonthKWh
		e.MonthStart = p.MonthStart
	} else {
		// Month rollover: zero month counters; persisted file with no
		// month_start (e.g. from before this version) lands here too.
		e.HeatingMonthKWh = 0
		e.CoolingMonthKWh = 0
		e.ConsumedMonthKWh = 0
		e.MonthStart = thisMonth
	}
```

(Note: the original `today := time.Now().Local().Format("2006-01-02")` at the top of `Load()` stays. The new `thisMonth :=` line goes inside the function, after the today-restore branch.)

- [ ] **Step 6: Extend `save()` to persist month state**

In `cmd/breezyd/energy_tracker.go`, replace the body of `save()` (~ lines 137–146) with:

```go
	p := persistedEnergy{
		TodayDate:           e.Today,
		MonthStart:          e.MonthStart,
		HeatingTodayKWh:     e.HeatingTodayKWh,
		CoolingTodayKWh:     e.CoolingTodayKWh,
		ConsumedTodayKWh:    e.ConsumedTodayKWh,
		HeatingMonthKWh:     e.HeatingMonthKWh,
		CoolingMonthKWh:     e.CoolingMonthKWh,
		ConsumedMonthKWh:    e.ConsumedMonthKWh,
		HeatingLifetimeKWh:  e.HeatingLifetimeKWh,
		CoolingLifetimeKWh:  e.CoolingLifetimeKWh,
		ConsumedLifetimeKWh: e.ConsumedLifetimeKWh,
		LastUpdated:         time.Now().UTC().Format(time.RFC3339),
	}
```

- [ ] **Step 7: Extend `Snapshot()` to return month fields**

In `cmd/breezyd/energy_tracker.go`, replace the return value of `Snapshot()` (~ lines 169–180) with:

```go
	return breezy.EnergyValues{
		Supported:           e.Error == "",
		InstantW:            e.InstantW,
		ConsumedW:           e.ConsumedW,
		HeatingTodayKWh:     e.HeatingTodayKWh,
		CoolingTodayKWh:     e.CoolingTodayKWh,
		ConsumedTodayKWh:    e.ConsumedTodayKWh,
		HeatingMonthKWh:     e.HeatingMonthKWh,
		CoolingMonthKWh:     e.CoolingMonthKWh,
		ConsumedMonthKWh:    e.ConsumedMonthKWh,
		HeatingLifetimeKWh:  e.HeatingLifetimeKWh,
		CoolingLifetimeKWh:  e.CoolingLifetimeKWh,
		ConsumedLifetimeKWh: e.ConsumedLifetimeKWh,
		Error:               e.Error,
	}
```

- [ ] **Step 8: Add the month-rollover branch and accumulation to `Tick()`**

In `cmd/breezyd/energy_tracker.go`, replace the date-rollover block (~ lines 209–218) with:

```go
	// Date rollover comes first: even if we skip the math below, the new
	// day's counters should be zero.
	today := now.Local().Format("2006-01-02")
	if e.Today != today {
		e.HeatingTodayKWh = 0
		e.CoolingTodayKWh = 0
		e.ConsumedTodayKWh = 0
		e.Today = today
		if err := e.save(); err != nil {
			slog.Warn("energy: rollover save failed", "device", e.Device, "err", err)
		}
	}
	// Month rollover, parallel to date rollover. Crossing a month boundary
	// also crosses a day boundary, so this fires after the date branch
	// (saving twice in that rare case is harmless).
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

And replace the accumulation block (~ lines 287–295) with:

```go
	if w > 0 {
		e.HeatingTodayKWh += deltaRecovered
		e.HeatingMonthKWh += deltaRecovered
		e.HeatingLifetimeKWh += deltaRecovered
	} else if w < 0 {
		e.CoolingTodayKWh += deltaRecovered
		e.CoolingMonthKWh += deltaRecovered
		e.CoolingLifetimeKWh += deltaRecovered
	}
	e.ConsumedTodayKWh += deltaConsumed
	e.ConsumedMonthKWh += deltaConsumed
	e.ConsumedLifetimeKWh += deltaConsumed
```

- [ ] **Step 9: Add the three Prometheus gauges**

In `cmd/breezyd/metrics.go`, extend the `Metrics` struct (~ lines 80–85) — insert three new fields between today and lifetime (matching the JSON ordering):

```go
	// Energy accounting (opt-in; only emitted for supported models).
	EnergyRecoveredWatts      *prometheus.GaugeVec
	EnergyConsumedWatts       *prometheus.GaugeVec
	EnergyHeatingTodayKWh     *prometheus.GaugeVec
	EnergyCoolingTodayKWh     *prometheus.GaugeVec
	EnergyConsumedTodayKWh    *prometheus.GaugeVec
	EnergyHeatingMonthKWh     *prometheus.GaugeVec
	EnergyCoolingMonthKWh     *prometheus.GaugeVec
	EnergyConsumedMonthKWh    *prometheus.GaugeVec
	EnergyHeatingLifetimeKWh  *prometheus.GaugeVec
	EnergyCoolingLifetimeKWh  *prometheus.GaugeVec
	EnergyConsumedLifetimeKWh *prometheus.GaugeVec
```

Register them — in `NewMetrics()` between the today and lifetime registrations (~ between lines 261 and 262), insert:

```go
	m.EnergyHeatingMonthKWh = energyGauge(
		"breezyd_energy_heating_month_kwh",
		"Heating energy recovered this calendar month (resets on first-of-month, local TZ).",
	)
	m.EnergyCoolingMonthKWh = energyGauge(
		"breezyd_energy_cooling_month_kwh",
		"Cooling energy recovered this calendar month (resets on first-of-month, local TZ).",
	)
	m.EnergyConsumedMonthKWh = energyGauge(
		"breezyd_energy_consumed_month_kwh",
		"Electric energy consumed by the fans this calendar month (resets on first-of-month, local TZ).",
	)
```

Add them to the collectors slice (~ lines 288–290):

```go
			m.EnergyRecoveredWatts, m.EnergyConsumedWatts,
			m.EnergyHeatingTodayKWh, m.EnergyCoolingTodayKWh, m.EnergyConsumedTodayKWh,
			m.EnergyHeatingMonthKWh, m.EnergyCoolingMonthKWh, m.EnergyConsumedMonthKWh,
			m.EnergyHeatingLifetimeKWh, m.EnergyCoolingLifetimeKWh, m.EnergyConsumedLifetimeKWh,
```

Update `SetEnergy()` (~ lines 452–472):

```go
func (m *Metrics) SetEnergy(device string, ev breezy.EnergyValues) {
	all := []*prometheus.GaugeVec{
		m.EnergyRecoveredWatts, m.EnergyConsumedWatts,
		m.EnergyHeatingTodayKWh, m.EnergyCoolingTodayKWh, m.EnergyConsumedTodayKWh,
		m.EnergyHeatingMonthKWh, m.EnergyCoolingMonthKWh, m.EnergyConsumedMonthKWh,
		m.EnergyHeatingLifetimeKWh, m.EnergyCoolingLifetimeKWh, m.EnergyConsumedLifetimeKWh,
	}
	if ev.Error != "" {
		for _, g := range all {
			g.DeleteLabelValues(device)
		}
		return
	}
	m.EnergyRecoveredWatts.WithLabelValues(device).Set(ev.InstantW)
	m.EnergyConsumedWatts.WithLabelValues(device).Set(ev.ConsumedW)
	m.EnergyHeatingTodayKWh.WithLabelValues(device).Set(ev.HeatingTodayKWh)
	m.EnergyCoolingTodayKWh.WithLabelValues(device).Set(ev.CoolingTodayKWh)
	m.EnergyConsumedTodayKWh.WithLabelValues(device).Set(ev.ConsumedTodayKWh)
	m.EnergyHeatingMonthKWh.WithLabelValues(device).Set(ev.HeatingMonthKWh)
	m.EnergyCoolingMonthKWh.WithLabelValues(device).Set(ev.CoolingMonthKWh)
	m.EnergyConsumedMonthKWh.WithLabelValues(device).Set(ev.ConsumedMonthKWh)
	m.EnergyHeatingLifetimeKWh.WithLabelValues(device).Set(ev.HeatingLifetimeKWh)
	m.EnergyCoolingLifetimeKWh.WithLabelValues(device).Set(ev.CoolingLifetimeKWh)
	m.EnergyConsumedLifetimeKWh.WithLabelValues(device).Set(ev.ConsumedLifetimeKWh)
}
```

- [ ] **Step 10: Run all backend tests; expect green**

```sh
go test ./pkg/breezy ./cmd/breezyd -v
```

Expected: all tests pass, including the four new/extended ones. If `TestEnergyTracker_Tick_HeatingAccumulation` or `TestEnergyTracker_Tick_CoolingAccumulation` fails because they didn't expect month accumulation — that's an extant test asserting on accumulated values; check whether they assert exact today/lifetime numbers (they should still match) and not month (which they don't reference). They should pass as-is. If a test breaks, diagnose: is it asserting an invariant the new month accumulation legitimately breaks (extend the test) or a scoping regression (fix the implementation)?

- [ ] **Step 11: Run `just check && just test-ui`**

```sh
just check
just test-ui
```

Expected: both green. UI tests still pass because the frontend hasn't changed yet — the new `*_month_kwh` JSON fields are simply ignored by `energyBody()` until Task 2.

- [ ] **Step 12: Commit Task 1**

```sh
git add pkg/breezy/energy.go cmd/breezyd/energy_tracker.go cmd/breezyd/energy_tracker_test.go cmd/breezyd/metrics.go cmd/breezyd/metrics_test.go cmd/breezyd/server_test.go
git commit -m "$(cat <<'EOF'
breezyd: per-month energy counters + Prometheus gauges (#30, part 1)

Adds Heating/Cooling/Consumed-MonthKWh + MonthStart to EnergyTracker,
parallel to the existing today/lifetime fields. Calendar-month rollover
mirrors the date-rollover logic in Tick() and Load(). Three new gauges
(breezyd_energy_*_month_kwh) join the SetEnergy and unsupported-clear
paths. JSON snapshots now expose *_month_kwh fields. UI rebuild lands
in part 2.
EOF
)"
```

---

## Task 2: Frontend rebuild + reposition

**Goal:** The dashboard ENERGY block renders as a 5×3 grid with COP cells and lives immediately above the Sensors `<details>`. Existing energy tests are replaced with new layout tests; new tests cover instantaneous COP, time-windowed COP, COP `—` divide-by-zero, and DOM position.

**Files:**
- Modify: `cmd/breezyd/ui/index.html` — `renderCard` call site (~ line 495), `energyBody` (~ lines 514–534).
- Modify: `tests/ui/dashboard.spec.ts` — replace and add ENERGY tests (~ lines 947–1057).
- Modify: `tests/ui/screenshot.ts` — fixture extension (~ lines 60–82).
- Regenerate: `tests/ui/screenshots/dashboard-1col.png`, `tests/ui/screenshots/dashboard-3col.png`.

**Acceptance Criteria:**
- [ ] In `renderCard()`, `${renderEnergy(name, snap)}` is emitted between the stale row and the sensors `<details>` IIFE — i.e. immediately above sensors.
- [ ] `energyBody(ev)` returns a single `<div class="sensor-grid energy-grid">` with exactly 15 `.sensor-cell` children; no standalone "now:" row.
- [ ] Row 1 cells: "regen power" / "regen cost" / "COP". Cell labels match.
- [ ] Row 2 cells: "COP today" / "COP month" / "COP lifetime".
- [ ] Rows 3, 4, 5: heating / cooling / consumed × today / month / lifetime, in that order.
- [ ] regen-power cell shows `{n} W heating` for `instant_w > 0`, `{n} W cooling` for `instant_w < 0`, `0 W` for `instant_w === 0`.
- [ ] regen-cost cell shows `{Math.round(consumed_w)} W` (no "consumed" suffix).
- [ ] COP cell shows `(Math.abs(num) / den).toFixed(1)` when `den > 0`; shows `—` when `den` is 0, undefined, or yields non-finite.
- [ ] kWh cells use the existing `fmtKwh` formatting (`{n.nn} kWh`).
- [ ] The error path (`ev.error` set) still renders the `.warn` message and skips the grid (unchanged).
- [ ] The hidden-when-missing path (`!ev`) still returns "" (unchanged).
- [ ] `details.energy[open]` survival across the 5 s grid re-render still works (existing test passes unchanged).
- [ ] Position test: `details.energy.boundingBox().y < details.block.sensors.boundingBox().y` for the same card.
- [ ] `tests/ui/screenshot.ts` includes `heating_month_kwh`, `cooling_month_kwh`, `consumed_month_kwh` in playroom and bedroom fixtures.
- [ ] `just check` passes.
- [ ] `just test-ui` passes (existing 55 minus the four replaced tests + four new tests = 55 still, give or take based on test count adjustments).
- [ ] Screenshots regenerated and committed.

**Verify:** `just check && just test-ui` → both green.

**Steps:**

- [ ] **Step 1: Replace and add the Playwright tests (TDD red)**

In `tests/ui/dashboard.spec.ts`, find the four ENERGY tests that need replacing (~ lines 972–1031): `"ENERGY block: collapsed by default, expanding shows now-line + 3-col grid"`, `"ENERGY block: cooling sign + sums in now-line"`, `"ENERGY block: not regen → '0 W (not regen)'"`. Replace those three tests, and add the new tests after them. The full replacement block (delete the three tests at 972–1031 and insert these in their place):

```typescript
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
```

Keep the existing tests at lines 947–970 (`"open state survives the 5 s grid re-render"`), 1033–1045 (`"error replaces grid"`), 1047–1057 (`"hidden when service.energy missing"`) unchanged. Their data path is unaffected by the rebuild — but extend the open-state test's fixture with the new month fields so it doesn't introduce ambiguity:

In the existing test at line 947, replace the `service.energy` literal with:

```typescript
service: {
  energy: { supported: true, instant_w: 100, consumed_w: 10,
            heating_today_kwh: 0.5, cooling_today_kwh: 0,
            consumed_today_kwh: 0.05,
            heating_month_kwh: 5, cooling_month_kwh: 0,
            consumed_month_kwh: 0.5,
            heating_lifetime_kwh: 50,
            cooling_lifetime_kwh: 0, consumed_lifetime_kwh: 5 },
},
```

- [ ] **Step 2: Run the new tests; expect them to fail**

```sh
cd tests/ui && pnpm exec playwright test --grep "ENERGY block:"
```

Expected: the six new/replaced tests **FAIL** (the grid still has 6 cells, no COP cell, energy still rendered below sensors). The two unchanged tests (open-state-survives, error-replaces, hidden-when-missing) should still pass.

- [ ] **Step 3: Move the `${renderEnergy(name, snap)}` call site**

In `cmd/breezyd/ui/index.html`, edit `renderCard()` to move the energy render call from after the controls to immediately before the sensors `<details>` IIFE. The relevant section currently looks like (~ lines 481–496):

```javascript
    ${(toasts[name] || {}).power ? `<div class="toast" role="alert">${esc(toasts[name].power)}</div>` : ""}
    ${stale ? `<div class="row"><span class="ts red">${lastPollMs ? humanAgo(ageMs) + " ago" : "no poll"}</span></div>` : ""}

    ${(() => {
      const sa = snap.live?.sensor_alerts || {};
      const sensorAlerting = sa.humidity === true || sa.co2 === true || sa.voc === true;
      const sensorsExpanded = sensorAlerting || !sensorsCollapsed[name];
      return `<details class="block sensors"${sensorsExpanded ? " open" : ""}>
      <summary><h3>Sensors</h3></summary>
      ${sensorsGrid(name, snap)}
    </details>`;
    })()}

    ${renderControls(name, snap, stale)}
    ${renderEnergy(name, snap)}
  </div>`;
```

Replace with:

```javascript
    ${(toasts[name] || {}).power ? `<div class="toast" role="alert">${esc(toasts[name].power)}</div>` : ""}
    ${stale ? `<div class="row"><span class="ts red">${lastPollMs ? humanAgo(ageMs) + " ago" : "no poll"}</span></div>` : ""}

    ${renderEnergy(name, snap)}

    ${(() => {
      const sa = snap.live?.sensor_alerts || {};
      const sensorAlerting = sa.humidity === true || sa.co2 === true || sa.voc === true;
      const sensorsExpanded = sensorAlerting || !sensorsCollapsed[name];
      return `<details class="block sensors"${sensorsExpanded ? " open" : ""}>
      <summary><h3>Sensors</h3></summary>
      ${sensorsGrid(name, snap)}
    </details>`;
    })()}

    ${renderControls(name, snap, stale)}
  </div>`;
```

(`${renderEnergy(name, snap)}` moves; the controls call stays last.)

- [ ] **Step 4: Rebuild `energyBody(ev)` as a 5×3 grid**

In `cmd/breezyd/ui/index.html`, replace the entire `energyBody(ev)` function (~ lines 514–534) with:

```javascript
function energyBody(ev) {
  const fmtKwh = (v) => `${(v ?? 0).toFixed(2)} kWh`;
  const fmtW   = (v) => `${Math.round(v ?? 0)} W`;
  // COP = useful-work / consumed. Renders as 1-dp number; "—" when
  // consumed is zero / non-positive / non-finite to avoid Infinity/NaN.
  const fmtCop = (num, den) => {
    if (!den || den <= 0) return "—";
    const r = Math.abs(num) / den;
    if (!isFinite(r)) return "—";
    return r.toFixed(1);
  };

  const w = ev.instant_w ?? 0;
  let regenPower;
  if (w > 0)      regenPower = `${fmtW(w)} heating`;
  else if (w < 0) regenPower = `${fmtW(-w)} cooling`;
  else            regenPower = "0 W";

  const regenCost = fmtW(ev.consumed_w ?? 0);
  const copNow    = fmtCop(ev.instant_w ?? 0, ev.consumed_w ?? 0);

  const copToday    = fmtCop((ev.heating_today_kwh ?? 0)    + (ev.cooling_today_kwh ?? 0),    ev.consumed_today_kwh);
  const copMonth    = fmtCop((ev.heating_month_kwh ?? 0)    + (ev.cooling_month_kwh ?? 0),    ev.consumed_month_kwh);
  const copLifetime = fmtCop((ev.heating_lifetime_kwh ?? 0) + (ev.cooling_lifetime_kwh ?? 0), ev.consumed_lifetime_kwh);

  const cell = (label, value) =>
    `<div class="sensor-cell"><div class="sensor-label">${esc(label)}</div><div>${esc(value)}</div></div>`;

  return `<div class="sensor-grid energy-grid">
    ${cell("regen power",    regenPower)}
    ${cell("regen cost",     regenCost)}
    ${cell("COP",            copNow)}
    ${cell("COP today",      copToday)}
    ${cell("COP month",      copMonth)}
    ${cell("COP lifetime",   copLifetime)}
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

- [ ] **Step 5: Extend the screenshot fixture**

In `tests/ui/screenshot.ts`, replace the playroom and bedroom energy literals (~ lines 60–82). The new playroom energy literal:

```typescript
        ? {
            supported: true,
            instant_w: 245,
            consumed_w: 18,
            heating_today_kwh: 1.23,
            cooling_today_kwh: 0.46,
            consumed_today_kwh: 0.12,
            heating_month_kwh: 28.5,
            cooling_month_kwh: 4.7,
            consumed_month_kwh: 2.9,
            heating_lifetime_kwh: 234.5,
            cooling_lifetime_kwh: 123.4,
            consumed_lifetime_kwh: 12.3,
          }
```

Bedroom energy literal:

```typescript
        : {
            supported: true,
            instant_w: 95,
            consumed_w: 12,
            heating_today_kwh: 0.62,
            cooling_today_kwh: 0.21,
            consumed_today_kwh: 0.08,
            heating_month_kwh: 14.2,
            cooling_month_kwh: 2.1,
            consumed_month_kwh: 1.6,
            heating_lifetime_kwh: 142.8,
            cooling_lifetime_kwh: 71.2,
            consumed_lifetime_kwh: 8.1,
          },
```

Office stays unsupported; no change.

- [ ] **Step 6: Re-run the new ENERGY tests; expect PASS**

```sh
cd tests/ui && pnpm exec playwright test --grep "ENERGY block:"
```

Expected: all ENERGY tests pass.

- [ ] **Step 7: Run the full UI suite**

```sh
just test-ui
```

Expected: all tests pass. The threshold tests (~ lines 775–860) and the sensors-block tests (~ lines 909–945) should all still pass — none of their selectors mention energy. If any sensor test fails because the moved energy block changed `.card > *:nth-child(N)` indexing, diagnose and fix the test selector; do NOT alter the implementation move.

- [ ] **Step 8: Run `just check`**

```sh
just check
```

Expected: lint and fast Go tests pass. (No Go code changed in Task 2; pre-commit gate per project rule.)

- [ ] **Step 9: Manually verify in a browser**

Per the project's "Playwright for UI visual checks" memory:

```sh
just build
./breezyd &
# open http://localhost:8080/ in a browser
```

Confirm:
- Each card shows ENERGY above Sensors.
- The ENERGY block, when expanded, displays a 5×3 grid (5 rows, 3 columns) with the labels laid out per the issue's table.
- Row 1 numbers update live (every 5 s) — regen power and regen cost change with fan state, COP recomputes.
- Time-windowed COP rows show `—` for windows where consumed is 0.
- Collapsing the ENERGY block hides the grid; chevron flips to ▶.

Stop the daemon: `kill %1`.

- [ ] **Step 10: Regenerate dashboard screenshots**

```sh
just screenshot
```

Expected: `tests/ui/screenshots/dashboard-1col.png` and `dashboard-3col.png` are rewritten. Cards are visibly taller (energy grew from 2×3 to 5×3) and energy moved up. Verify the screenshots open and look right before committing.

- [ ] **Step 11: Commit Task 2**

```sh
git add cmd/breezyd/ui/index.html tests/ui/dashboard.spec.ts tests/ui/screenshot.ts tests/ui/screenshots/dashboard-1col.png tests/ui/screenshots/dashboard-3col.png
git commit -m "$(cat <<'EOF'
ui: rebuild ENERGY block as 5×3 grid above Sensors (#30, part 2)

Resolves #30. The per-card ENERGY block is now a single 5×3 grid:
row 1 instantaneous (regen power / regen cost / COP), then COP /
heating / cooling / consumed across today / month / lifetime. The
block now sits immediately above the Sensors <details>. Backend
month counters from part 1 feed the new month column. COP shows "—"
when consumed is zero or non-finite. Existing open-state-survives,
error-replaces-grid, and hidden-when-missing tests preserved verbatim.
EOF
)"
```

---

## Self-Review

- **Spec coverage:**
  - Calendar-month rollover, local TZ — Task 1 Step 8 (Tick rollover branch + Step 5 Load month-restore branch). ✓
  - `EnergyValues` extended — Task 1 Step 3. ✓
  - `EnergyTracker` + `persistedEnergy` extended — Task 1 Steps 4, 6, 7, 8. ✓
  - Three new Prometheus gauges — Task 1 Step 9. ✓
  - JSON snapshots round-trip month — Task 1 Step 1 (server_test) + Step 7 (Snapshot). ✓
  - Energy block above Sensors — Task 2 Step 3 + position test in Step 1. ✓
  - 5×3 grid, instantaneous row, COP, label renames — Task 2 Step 4 (energyBody rebuild) + Step 1 (tests). ✓
  - COP formula and `—` divide-by-zero — Task 2 Step 4 (`fmtCop` helper) + Step 1 (test). ✓
  - Screenshot fixture extended — Task 2 Step 5. ✓
  - No state-file migration — Load's month-restore branch zeroes counters when persisted file lacks `month_start`, naturally handling pre-existing files (Task 1 Step 5). User-stated intent is to delete the file; code path is correct either way.

- **Placeholder scan:** every code step shows actual code; every command step shows actual commands. No "TBD"/"add validation"/"similar to Task N" / unguarded "fix as needed" steps.

- **Type / selector consistency:**
  - Field names: `HeatingMonthKWh`/`CoolingMonthKWh`/`ConsumedMonthKWh` consistent across `EnergyValues`, `EnergyTracker`, `persistedEnergy`, `Snapshot`, `SetEnergy`, all tests.
  - JSON keys: `heating_month_kwh`/`cooling_month_kwh`/`consumed_month_kwh` consistent across struct tags, server_test assertions, fixture literals, Playwright fixtures.
  - Prometheus gauge names: `breezyd_energy_heating_month_kwh` / `..._cooling_month_kwh` / `..._consumed_month_kwh` consistent across struct fields, registration, `SetEnergy`, `unsupported` test, `supported` test.
  - `MonthStart` (Go field) ↔ `month_start` (JSON tag) ↔ `MonthStart` accessor in tests — consistent.
  - Frontend cell label text: "regen power" / "regen cost" / "COP" / "COP today" / etc. — consistent across the rendered HTML and the Playwright `.sensor-label:text-is(...)` selectors.
  - `details.block.sensors` selector for the position test — matches the live wrapper class produced by the sensors-block IIFE.
