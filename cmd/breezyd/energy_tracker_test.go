// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/matryer/is"
)

func TestEnergyTracker_Load_MissingFile(t *testing.T) {
	is := is.New(t)
	tr := &EnergyTracker{
		Device:   "test-device",
		StateDir: t.TempDir(),
	}

	tr.Load()

	snap := tr.Snapshot()
	is.Equal(snap.HeatingTodayKWh, float64(0))  // today counters zero on missing file
	is.Equal(snap.CoolingTodayKWh, float64(0))  // today counters zero on missing file
	is.Equal(snap.ConsumedTodayKWh, float64(0)) // today counters zero on missing file
	is.Equal(snap.HeatingLifetimeKWh, float64(0))
	is.Equal(snap.CoolingLifetimeKWh, float64(0))
	is.Equal(snap.ConsumedLifetimeKWh, float64(0))

	wantDate := time.Now().Local().Format("2006-01-02")
	is.Equal(tr.Today, wantDate) // Today seeded from local wall clock
}

func TestEnergyTracker_Load_MalformedFile(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:   "test-device",
		StateDir: dir,
	}

	// Write malformed JSON to the state path.
	is.NoErr(os.WriteFile(tr.statePath(), []byte("{not json"), 0o600))

	tr.Load()

	snap := tr.Snapshot()
	is.Equal(snap.HeatingTodayKWh, float64(0)) // malformed load yields fresh state
	is.Equal(snap.CoolingTodayKWh, float64(0))
	is.Equal(snap.ConsumedTodayKWh, float64(0))
	is.Equal(snap.HeatingLifetimeKWh, float64(0))
	is.Equal(snap.CoolingLifetimeKWh, float64(0))
	is.Equal(snap.ConsumedLifetimeKWh, float64(0))
}

func TestEnergyTracker_Load_EmptyFile(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "playroom", StateDir: dir}
	// A zero-byte file at the state path simulates an interrupted write
	// that left a truncated artefact behind. Load should treat it as
	// malformed (warn + fresh state) rather than panic on the unmarshal.
	is.NoErr(os.WriteFile(tr.statePath(), nil, 0o600))
	tr.Load()
	is.Equal(tr.HeatingLifetimeKWh, float64(0))                 // fresh state on empty file
	is.Equal(tr.Today, time.Now().Local().Format("2006-01-02")) // Today seeded even on empty-file load
}

func TestEnergyTracker_RoundTrip(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	today := time.Now().Local().Format("2006-01-02")

	tr := &EnergyTracker{
		Device:              "living-room",
		StateDir:            dir,
		HeatingTodayKWh:     1.1,
		CoolingTodayKWh:     2.2,
		ConsumedTodayKWh:    3.3,
		HeatingLifetimeKWh:  100.1,
		CoolingLifetimeKWh:  200.2,
		ConsumedLifetimeKWh: 300.3,
		Today:               today,
	}

	is.NoErr(tr.save())

	tr2 := &EnergyTracker{
		Device:   "living-room",
		StateDir: dir,
	}
	tr2.Load()

	is.Equal(tr2.HeatingTodayKWh, tr.HeatingTodayKWh)
	is.Equal(tr2.CoolingTodayKWh, tr.CoolingTodayKWh)
	is.Equal(tr2.ConsumedTodayKWh, tr.ConsumedTodayKWh)
	is.Equal(tr2.HeatingLifetimeKWh, tr.HeatingLifetimeKWh)
	is.Equal(tr2.CoolingLifetimeKWh, tr.CoolingLifetimeKWh)
	is.Equal(tr2.ConsumedLifetimeKWh, tr.ConsumedLifetimeKWh)
	is.Equal(tr2.Today, tr.Today)
}

func TestEnergyTracker_Snapshot_IsCopy(t *testing.T) {
	is := is.New(t)
	tr := &EnergyTracker{
		Device:          "office",
		StateDir:        t.TempDir(),
		HeatingTodayKWh: 5.5,
		CoolingTodayKWh: 6.6,
		Today:           "2026-05-06",
	}

	snap := tr.Snapshot()

	// Mutate the tracker after taking the snapshot.
	tr.HeatingTodayKWh = 999.9
	tr.CoolingTodayKWh = 888.8
	tr.Today = "1970-01-01"

	is.Equal(snap.HeatingTodayKWh, 5.5) // snapshot must be a copy, not a live view
	is.Equal(snap.CoolingTodayKWh, 6.6)
}

// ---------------------------------------------------------------------------
// Tick helpers
// ---------------------------------------------------------------------------

// makeRegenSnap builds a values map representing a Breezy 160 in
// manual-mode regeneration at the given fan % and outdoor/supply temps.
// CommandedFanPct returns manual_pct in manual mode, so both supply and
// extract resolve to fanPct. Temperatures are int16 little-endian, in
// tenths of a degree.
func makeRegenSnap(fanPct int, outdoorC, supplyC float64) map[breezy.ParamID][]byte {
	return map[breezy.ParamID][]byte{
		0x00B7: {1},               // airflow_mode = regeneration (value used by Tick)
		0x0002: {0xFF},            // speed_mode = manual
		0x0044: {byte(fanPct)},    // manual_pct
		0x004A: {0xC8, 0x00},      // fan_supply_rpm = 200
		0x004B: {0xC8, 0x00},      // fan_extract_rpm = 200
		0x0020: encTemp(supplyC),  // temp_supply_c
		0x001F: encTemp(outdoorC), // temp_outdoor_c
		0x00B9: {17, 0},           // device_type = Breezy 160
	}
}

// makeRegenSnapAsymmetric exercises the asymmetric-preset path: supply
// runs at supplyPct (preset3's 0x3E), extract at extractPct (0x3F).
func makeRegenSnapAsymmetric(supplyPct, extractPct int, outdoorC, supplyC float64) map[breezy.ParamID][]byte {
	return map[breezy.ParamID][]byte{
		0x00B7: {1},
		0x0002: {3}, // speed_mode = preset3
		0x003E: {byte(supplyPct)},
		0x003F: {byte(extractPct)},
		0x004A: {0xC8, 0x00},
		0x004B: {0xC8, 0x00},
		0x0020: encTemp(supplyC),
		0x001F: encTemp(outdoorC),
		0x00B9: {17, 0},
	}
}

func encTemp(c float64) []byte {
	v := int16(c * 10)
	return []byte{byte(v), byte(uint16(v) >> 8)}
}

func newTracker(t *testing.T) *EnergyTracker {
	t.Helper()
	return &EnergyTracker{
		Device:   "test",
		StateDir: t.TempDir(),
	}
}

// approxEqual returns true when got is within tolFrac of want.
func approxEqual(got, want, tolFrac float64) bool {
	if want == 0 {
		return math.Abs(got) < 1e-12
	}
	return math.Abs(got-want)/math.Abs(want) <= tolFrac
}

// ---------------------------------------------------------------------------
// Tick tests
// ---------------------------------------------------------------------------

func TestEnergyTracker_Tick_FirstTickNoAccumulation(t *testing.T) {
	is := is.New(t)
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	snap := tr.Snapshot()
	is.Equal(snap.HeatingTodayKWh, float64(0)) // first tick primes LastTick, must not accumulate
}

func TestEnergyTracker_Tick_HeatingAccumulation(t *testing.T) {
	is := is.New(t)
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	// Prime.
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	// Accumulate.
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap := tr.Snapshot()

	// At 50% on Breezy 160: airflow = 100 m³/h. Δ = 20 °C. W = 100×0.335×20 = 670.
	wantW := 670.0
	is.True(snap.InstantW >= 669 && snap.InstantW <= 671) // InstantW ≈ 670
	// Per-fan at 50%: 9 W. Total consumed = 9+9 = 18.
	is.True(snap.ConsumedW >= 17.5 && snap.ConsumedW <= 18.5) // ConsumedW ≈ 18
	wantHeating := wantW * 5 / 3.6e6
	is.True(approxEqual(snap.HeatingTodayKWh, wantHeating, 0.01))
	is.Equal(snap.CoolingTodayKWh, float64(0))
	wantConsumed := 18.0 * 5 / 3.6e6
	is.True(approxEqual(snap.ConsumedTodayKWh, wantConsumed, 0.01))
}

func TestEnergyTracker_Tick_CoolingAccumulation(t *testing.T) {
	is := is.New(t)
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	// outdoor=30, supply=25 → Δ = 25-30 = -5 °C.
	tr.Tick(makeRegenSnap(50, 30, 25), t0)
	tr.Tick(makeRegenSnap(50, 30, 25), t1)
	snap := tr.Snapshot()

	// W = 100×0.335×(-5) = -167.5.
	is.True(snap.InstantW >= -169 && snap.InstantW <= -166) // InstantW ≈ -167.5
	is.Equal(snap.HeatingTodayKWh, float64(0))              // cooling tick must not accumulate heating
	wantCooling := math.Abs(-167.5) * 5 / 3.6e6
	is.True(approxEqual(snap.CoolingTodayKWh, wantCooling, 0.01))
}

func TestEnergyTracker_Tick_AsymmetricFans(t *testing.T) {
	is := is.New(t)
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	// preset3: supply=70%, extract=100%.
	tr.Tick(makeRegenSnapAsymmetric(70, 100, 0, 20), t0)
	tr.Tick(makeRegenSnapAsymmetric(70, 100, 0, 20), t1)
	snap := tr.Snapshot()
	// FanW at 70% ≈ 14W, at 100% = 22W → total ≈ 36W (±1W tolerance).
	is.True(snap.ConsumedW >= 35 && snap.ConsumedW <= 37) // ConsumedW (asymmetric) ≈ 36
}

func TestEnergyTracker_Tick_NonRegenSkipped(t *testing.T) {
	is := is.New(t)
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	// Prime in regen.
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	// Second tick in ventilation mode (0x00B7 = 0).
	notRegen := makeRegenSnap(50, 0, 20)
	notRegen[0x00B7] = []byte{0}
	tr.Tick(notRegen, t1)
	snap := tr.Snapshot()
	is.Equal(snap.HeatingTodayKWh, float64(0)) // non-regen mode must not accumulate
	is.Equal(snap.InstantW, float64(0))        // non-regen zeros instantaneous power
	is.Equal(snap.ConsumedW, float64(0))
}

func TestEnergyTracker_Tick_DtCap(t *testing.T) {
	is := is.New(t)
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour) // well beyond 300s cap
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap := tr.Snapshot()
	// capped at 300s: heating ≤ 670 × 300 / 3.6e6 × 1.01
	maxHeating := 670.0 * 300 / 3.6e6 * 1.01
	is.True(snap.HeatingTodayKWh <= maxHeating) // dt cap protects against long stalls
}

func TestEnergyTracker_Tick_NegativeDt(t *testing.T) {
	is := is.New(t)
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	// Clock jumped backwards.
	tr.Tick(makeRegenSnap(50, 0, 20), t0.Add(-30*time.Second))
	snap := tr.Snapshot()
	is.Equal(snap.HeatingTodayKWh, float64(0)) // negative dt must not accumulate
}

func TestEnergyTracker_Tick_DateRollover(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:             "rollover",
		StateDir:           dir,
		HeatingTodayKWh:    1.5,
		HeatingLifetimeKWh: 100.0,
		Today:              "2026-05-05",
	}
	// Tick at just after midnight on 2026-05-06.
	t1 := time.Date(2026, 5, 6, 0, 0, 5, 0, time.Local)
	// Prime first (LastTick zero → sets LastTick, no accumulation).
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap := tr.Snapshot()
	// HeatingTodayKWh should have been zeroed by the rollover before priming.
	is.Equal(snap.HeatingTodayKWh, float64(0))        // rollover zeros today counter
	is.Equal(snap.HeatingLifetimeKWh, float64(100.0)) // lifetime must survive rollover
	is.Equal(tr.Today, "2026-05-06")                  // Today advanced to current calendar day
}

// TestEnergyTracker_Tick_RolloverDoesNotDoubleAccumulate pins that
// after a date rollover, today counters reflect only post-midnight
// ticks — they don't carry the full pre-midnight → post-midnight gap
// (capped at dtCap, but still a measurable spike in today's totals).
//
// Setup:
//  1. Prime at 23:59:50 (day A) — LastTick set, no accumulation.
//  2. Tick at 00:00:05 (day B, 15s later, across midnight) — rollover
//     zeros today counters. The accumulation from this tick must NOT
//     include any dt: rollover should reset LastTick so the first
//     post-rollover tick re-primes.
//  3. Tick at 00:00:35 (day B, 30s after step 2) — accumulation here
//     reflects exactly that 30s.
//
// Today's HeatingTodayKWh must therefore reflect only the 30s gap
// from step 2 → step 3, not the 15s pre-midnight gap.
func TestEnergyTracker_Tick_RolloverDoesNotDoubleAccumulate(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:   "rollover-no-doublecount",
		StateDir: dir,
		Today:    "2026-05-05",
	}

	// Prime at 23:59:50 day A.
	t1 := time.Date(2026, 5, 5, 23, 59, 50, 0, time.Local)
	tr.Tick(makeRegenSnap(80, 0, 20), t1)
	is.Equal(tr.Today, "2026-05-05")

	// First post-rollover tick at 00:00:05 day B.
	// Rollover zeroes today counters and (per spec) re-primes LastTick.
	t2 := time.Date(2026, 5, 6, 0, 0, 5, 0, time.Local)
	tr.Tick(makeRegenSnap(80, 0, 20), t2)
	mid := tr.Snapshot()
	is.Equal(tr.Today, "2026-05-06")          // rollover advanced today
	is.Equal(mid.HeatingTodayKWh, float64(0)) // post-rollover today must start at 0 — no carry-over from pre-midnight

	// Second post-rollover tick at 00:00:35 day B (30s later).
	t3 := time.Date(2026, 5, 6, 0, 0, 35, 0, time.Local)
	tr.Tick(makeRegenSnap(80, 0, 20), t3)
	end := tr.Snapshot()

	// Today's accumulation reflects only the 30s gap (t2 → t3), not
	// the 15s pre-midnight (t1 → t2) plus the 30s. Concretely: end -
	// mid is the 30s-worth, and that should match end (since mid was 0).
	is.Equal(mid.HeatingTodayKWh, float64(0))
	is.True(end.HeatingTodayKWh > 0) // 30s of heating must accumulate
}

func TestEnergyTracker_Tick_RolloverPersists(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:          "rollover-persist",
		StateDir:        dir,
		HeatingTodayKWh: 2.5,
		Today:           "2026-05-05",
	}
	// Tick at just after midnight on 2026-05-06 with a non-regen mode so
	// the function returns early after the rollover block — no accumulation,
	// no end-of-function save. The rollover save must have written the new
	// date before we hit that early return.
	t1 := time.Date(2026, 5, 6, 0, 0, 5, 0, time.Local)
	notRegen := makeRegenSnap(50, 0, 20)
	notRegen[0x00B7] = []byte{0} // ventilation
	tr.Tick(notRegen, t1)

	// Read the persisted file directly (rather than via Load(), which uses
	// wall-clock time.Now() to derive "today" and would mask the rollover
	// save by overwriting the loaded Today with the current wall date).
	data, err := os.ReadFile(filepath.Join(dir, "energy_rollover-persist.json"))
	is.NoErr(err)
	var p persistedEnergy
	is.NoErr(json.Unmarshal(data, &p))
	is.Equal(p.TodayDate, "2026-05-06")     // rollover save persisted before early return
	is.Equal(p.HeatingTodayKWh, float64(0)) // today counter zeroed in persisted state
}

func TestEnergyTracker_Tick_MonthRollover(t *testing.T) {
	is := is.New(t)
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
	is.Equal(snap.HeatingTodayKWh, float64(0))
	is.Equal(snap.HeatingMonthKWh, float64(0))
	is.Equal(snap.HeatingLifetimeKWh, float64(100.0)) // lifetime must survive rollover
	is.Equal(tr.MonthStart, "2026-05")
}

func TestEnergyTracker_Tick_MonthRolloverPersists(t *testing.T) {
	is := is.New(t)
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
	tr2.Load()
	is.Equal(tr2.MonthStart, "2026-05")
	is.Equal(tr2.HeatingMonthKWh, float64(0))
}

func TestEnergyTracker_Tick_UnsupportedModel(t *testing.T) {
	is := is.New(t)
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	// Build a regen snap with an unknown device type (99).
	unknownModel := makeRegenSnap(50, 0, 20)
	unknownModel[0x00B9] = []byte{99, 0}
	tr.Tick(unknownModel, t0)
	tr.Tick(unknownModel, t1)
	snap := tr.Snapshot()
	is.Equal(snap.Supported, false)            // unknown model must surface as unsupported
	is.True(snap.Error != "")                  // unsupported snapshot must carry an error message
	is.Equal(snap.HeatingTodayKWh, float64(0)) // unsupported model must not accumulate
}

func TestEnergyTracker_Tick_PersistsAfterEachTick(t *testing.T) {
	is := is.New(t)
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "persist-test", StateDir: dir}
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap1 := tr.Snapshot()
	is.True(snap1.HeatingLifetimeKWh != 0) // precondition: tick must accumulate heating

	// Reload from disk.
	tr2 := &EnergyTracker{Device: "persist-test", StateDir: dir}
	tr2.Load()
	snap2 := tr2.Snapshot()
	is.True(snap2.HeatingLifetimeKWh != 0)                       // lifetime persisted
	is.Equal(snap2.HeatingLifetimeKWh, snap1.HeatingLifetimeKWh) // round-trip preserves value
}
