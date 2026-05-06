// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"math"
	"os"
	"testing"
	"time"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

func TestEnergyTracker_Load_MissingFile(t *testing.T) {
	tr := &EnergyTracker{
		Device:   "test-device",
		StateDir: t.TempDir(),
	}

	if err := tr.Load(); err != nil {
		t.Fatalf("Load on missing file: got error %v, want nil", err)
	}

	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 || snap.CoolingTodayKWh != 0 || snap.ConsumedTodayKWh != 0 {
		t.Errorf("today counters not zero: heating=%v cooling=%v consumed=%v",
			snap.HeatingTodayKWh, snap.CoolingTodayKWh, snap.ConsumedTodayKWh)
	}
	if snap.HeatingLifetimeKWh != 0 || snap.CoolingLifetimeKWh != 0 || snap.ConsumedLifetimeKWh != 0 {
		t.Errorf("lifetime counters not zero: heating=%v cooling=%v consumed=%v",
			snap.HeatingLifetimeKWh, snap.CoolingLifetimeKWh, snap.ConsumedLifetimeKWh)
	}

	wantDate := time.Now().Local().Format("2006-01-02")
	if tr.Today != wantDate {
		t.Errorf("Today = %q, want %q", tr.Today, wantDate)
	}
}

func TestEnergyTracker_Load_MalformedFile(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{
		Device:   "test-device",
		StateDir: dir,
	}

	// Write malformed JSON to the state path.
	if err := os.WriteFile(tr.statePath(), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := tr.Load(); err != nil {
		t.Fatalf("Load on malformed file: got error %v, want nil", err)
	}

	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 || snap.CoolingTodayKWh != 0 || snap.ConsumedTodayKWh != 0 {
		t.Errorf("today counters not zero after malformed load: heating=%v cooling=%v consumed=%v",
			snap.HeatingTodayKWh, snap.CoolingTodayKWh, snap.ConsumedTodayKWh)
	}
	if snap.HeatingLifetimeKWh != 0 || snap.CoolingLifetimeKWh != 0 || snap.ConsumedLifetimeKWh != 0 {
		t.Errorf("lifetime counters not zero after malformed load: heating=%v cooling=%v consumed=%v",
			snap.HeatingLifetimeKWh, snap.CoolingLifetimeKWh, snap.ConsumedLifetimeKWh)
	}
}

func TestEnergyTracker_Load_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "playroom", StateDir: dir}
	// A zero-byte file at the state path simulates an interrupted write
	// that left a truncated artefact behind. Load should treat it as
	// malformed (warn + fresh state) rather than panic on the unmarshal.
	if err := os.WriteFile(tr.statePath(), nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := tr.Load(); err != nil {
		t.Fatalf("Load on empty file should succeed (start fresh), got %v", err)
	}
	if tr.HeatingLifetimeKWh != 0 {
		t.Errorf("expected fresh state on empty file, got HeatingLifetimeKWh=%v", tr.HeatingLifetimeKWh)
	}
	if tr.Today != time.Now().Local().Format("2006-01-02") {
		t.Errorf("Today not set on empty-file load")
	}
}

func TestEnergyTracker_RoundTrip(t *testing.T) {
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

	if err := tr.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	tr2 := &EnergyTracker{
		Device:   "living-room",
		StateDir: dir,
	}
	if err := tr2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if tr2.HeatingTodayKWh != tr.HeatingTodayKWh {
		t.Errorf("HeatingTodayKWh: got %v, want %v", tr2.HeatingTodayKWh, tr.HeatingTodayKWh)
	}
	if tr2.CoolingTodayKWh != tr.CoolingTodayKWh {
		t.Errorf("CoolingTodayKWh: got %v, want %v", tr2.CoolingTodayKWh, tr.CoolingTodayKWh)
	}
	if tr2.ConsumedTodayKWh != tr.ConsumedTodayKWh {
		t.Errorf("ConsumedTodayKWh: got %v, want %v", tr2.ConsumedTodayKWh, tr.ConsumedTodayKWh)
	}
	if tr2.HeatingLifetimeKWh != tr.HeatingLifetimeKWh {
		t.Errorf("HeatingLifetimeKWh: got %v, want %v", tr2.HeatingLifetimeKWh, tr.HeatingLifetimeKWh)
	}
	if tr2.CoolingLifetimeKWh != tr.CoolingLifetimeKWh {
		t.Errorf("CoolingLifetimeKWh: got %v, want %v", tr2.CoolingLifetimeKWh, tr.CoolingLifetimeKWh)
	}
	if tr2.ConsumedLifetimeKWh != tr.ConsumedLifetimeKWh {
		t.Errorf("ConsumedLifetimeKWh: got %v, want %v", tr2.ConsumedLifetimeKWh, tr.ConsumedLifetimeKWh)
	}
	if tr2.Today != tr.Today {
		t.Errorf("Today: got %q, want %q", tr2.Today, tr.Today)
	}
}

func TestEnergyTracker_Snapshot_IsCopy(t *testing.T) {
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

	if snap.HeatingTodayKWh != 5.5 {
		t.Errorf("snapshot HeatingTodayKWh mutated: got %v, want 5.5", snap.HeatingTodayKWh)
	}
	if snap.CoolingTodayKWh != 6.6 {
		t.Errorf("snapshot CoolingTodayKWh mutated: got %v, want 6.6", snap.CoolingTodayKWh)
	}
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
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("first tick should not accumulate: HeatingTodayKWh = %v, want 0", snap.HeatingTodayKWh)
	}
}

func TestEnergyTracker_Tick_HeatingAccumulation(t *testing.T) {
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
	if snap.InstantW < 669 || snap.InstantW > 671 {
		t.Errorf("InstantW = %v, want ≈670", snap.InstantW)
	}
	// Per-fan at 50%: 9 W. Total consumed = 9+9 = 18.
	if snap.ConsumedW < 17.5 || snap.ConsumedW > 18.5 {
		t.Errorf("ConsumedW = %v, want ≈18", snap.ConsumedW)
	}
	wantHeating := wantW * 5 / 3.6e6
	if !approxEqual(snap.HeatingTodayKWh, wantHeating, 0.01) {
		t.Errorf("HeatingTodayKWh = %v, want ≈%v", snap.HeatingTodayKWh, wantHeating)
	}
	if snap.CoolingTodayKWh != 0 {
		t.Errorf("CoolingTodayKWh = %v, want 0", snap.CoolingTodayKWh)
	}
	wantConsumed := 18.0 * 5 / 3.6e6
	if !approxEqual(snap.ConsumedTodayKWh, wantConsumed, 0.01) {
		t.Errorf("ConsumedTodayKWh = %v, want ≈%v", snap.ConsumedTodayKWh, wantConsumed)
	}
}

func TestEnergyTracker_Tick_CoolingAccumulation(t *testing.T) {
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	// outdoor=30, supply=25 → Δ = 25-30 = -5 °C.
	tr.Tick(makeRegenSnap(50, 30, 25), t0)
	tr.Tick(makeRegenSnap(50, 30, 25), t1)
	snap := tr.Snapshot()

	// W = 100×0.335×(-5) = -167.5.
	if snap.InstantW < -169 || snap.InstantW > -166 {
		t.Errorf("InstantW = %v, want ≈-167.5", snap.InstantW)
	}
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("HeatingTodayKWh = %v, want 0", snap.HeatingTodayKWh)
	}
	wantCooling := math.Abs(-167.5) * 5 / 3.6e6
	if !approxEqual(snap.CoolingTodayKWh, wantCooling, 0.01) {
		t.Errorf("CoolingTodayKWh = %v, want ≈%v", snap.CoolingTodayKWh, wantCooling)
	}
}

func TestEnergyTracker_Tick_AsymmetricFans(t *testing.T) {
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	// preset3: supply=70%, extract=100%.
	tr.Tick(makeRegenSnapAsymmetric(70, 100, 0, 20), t0)
	tr.Tick(makeRegenSnapAsymmetric(70, 100, 0, 20), t1)
	snap := tr.Snapshot()
	// FanW at 70% ≈ 14W, at 100% = 22W → total ≈ 36W (±1W tolerance).
	if snap.ConsumedW < 35 || snap.ConsumedW > 37 {
		t.Errorf("ConsumedW (asymmetric) = %v, want ≈36 (±1)", snap.ConsumedW)
	}
}

func TestEnergyTracker_Tick_NonRegenSkipped(t *testing.T) {
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
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("HeatingTodayKWh = %v, want 0 (non-regen should not accumulate)", snap.HeatingTodayKWh)
	}
	if snap.InstantW != 0 {
		t.Errorf("InstantW = %v, want 0 for non-regen", snap.InstantW)
	}
	if snap.ConsumedW != 0 {
		t.Errorf("ConsumedW = %v, want 0 for non-regen", snap.ConsumedW)
	}
}

func TestEnergyTracker_Tick_DtCap(t *testing.T) {
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour) // well beyond 300s cap
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap := tr.Snapshot()
	// capped at 300s: heating ≤ 670 × 300 / 3.6e6 × 1.01
	maxHeating := 670.0 * 300 / 3.6e6 * 1.01
	if snap.HeatingTodayKWh > maxHeating {
		t.Errorf("HeatingTodayKWh = %v exceeds dt-capped max %v", snap.HeatingTodayKWh, maxHeating)
	}
}

func TestEnergyTracker_Tick_NegativeDt(t *testing.T) {
	tr := newTracker(t)
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	// Clock jumped backwards.
	tr.Tick(makeRegenSnap(50, 0, 20), t0.Add(-30*time.Second))
	snap := tr.Snapshot()
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("HeatingTodayKWh = %v, want 0 on negative dt", snap.HeatingTodayKWh)
	}
}

func TestEnergyTracker_Tick_DateRollover(t *testing.T) {
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
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("HeatingTodayKWh = %v after rollover, want 0", snap.HeatingTodayKWh)
	}
	// Lifetime must not be touched.
	if snap.HeatingLifetimeKWh != 100.0 {
		t.Errorf("HeatingLifetimeKWh = %v, want 100 (lifetime must survive rollover)", snap.HeatingLifetimeKWh)
	}
	if tr.Today != "2026-05-06" {
		t.Errorf("Today = %q, want 2026-05-06", tr.Today)
	}
}

func TestEnergyTracker_Tick_RolloverPersists(t *testing.T) {
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

	// Reload from disk: today_date must be the new date; today counters zero.
	tr2 := &EnergyTracker{Device: "rollover-persist", StateDir: dir}
	if err := tr2.Load(); err != nil {
		t.Fatalf("Load after rollover: %v", err)
	}
	if tr2.Today != "2026-05-06" {
		t.Errorf("persisted Today = %q, want 2026-05-06", tr2.Today)
	}
	if tr2.HeatingTodayKWh != 0 {
		t.Errorf("persisted HeatingTodayKWh = %v, want 0 after rollover", tr2.HeatingTodayKWh)
	}
}

func TestEnergyTracker_Tick_UnsupportedModel(t *testing.T) {
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
	if snap.Supported {
		t.Errorf("Supported = true for unknown model, want false")
	}
	if snap.Error == "" {
		t.Errorf("Error is empty for unknown model, want a non-empty message")
	}
	if snap.HeatingTodayKWh != 0 {
		t.Errorf("HeatingTodayKWh = %v for unsupported model, want 0", snap.HeatingTodayKWh)
	}
}

func TestEnergyTracker_Tick_PersistsAfterEachTick(t *testing.T) {
	dir := t.TempDir()
	tr := &EnergyTracker{Device: "persist-test", StateDir: dir}
	tr.Load()
	t0 := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(5 * time.Second)
	tr.Tick(makeRegenSnap(50, 0, 20), t0)
	tr.Tick(makeRegenSnap(50, 0, 20), t1)
	snap1 := tr.Snapshot()
	if snap1.HeatingLifetimeKWh == 0 {
		t.Fatalf("no heating lifetime after tick — precondition failed")
	}

	// Reload from disk.
	tr2 := &EnergyTracker{Device: "persist-test", StateDir: dir}
	if err := tr2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	snap2 := tr2.Snapshot()
	if snap2.HeatingLifetimeKWh == 0 {
		t.Errorf("HeatingLifetimeKWh not persisted: got 0 after reload")
	}
	if snap2.HeatingLifetimeKWh != snap1.HeatingLifetimeKWh {
		t.Errorf("HeatingLifetimeKWh mismatch: reloaded %v, want %v", snap2.HeatingLifetimeKWh, snap1.HeatingLifetimeKWh)
	}
}
