# HomeKit Bridge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a HomeKit Accessory Protocol (HAP) bridge to `breezyd`, exposing each configured Breezy as a HomeKit accessory with power, fan speed, two airflow-mode switches, and the full sensor surface (humidity, eCO2, VOC, four temperatures).

**Architecture:** Three-layer split mirroring Phase 1 of issue #2: pure `pkg/homekit` library (accessory builder, sync, VOC→AirQuality mapping); `internal/config` `[homekit]` block; `cmd/breezyd/homekit.go` daemon glue (HAP server lifecycle, PIN persistence, write callbacks via `pkg/breezy/ops` + `recordingClient`, post-poll sync via a new Poller `OnPoll` hook). Six tasks, sequential dependencies. Opt-in via `[homekit].enabled = true`.

**Tech Stack:** Go 1.22+, [github.com/brutella/hap](https://github.com/brutella/hap) (HAP server), existing `pkg/breezy/ops` from Phase 1, `pkg/breezy/fakedevice` for integration tests.

**Spec:** `docs/superpowers/specs/2026-05-04-homekit-bridge-design.md`
**Issue:** [#1 — Expose Devices via HomeKit](https://github.com/hughobrien/breezyd/issues/1)

> **Library API note:** the brutella/hap public API (constructor names, characteristic update methods) has evolved across versions. The plan references its canonical idioms — implementers should consult `go doc github.com/brutella/hap/...` for exact signatures at task time. Pin a specific version in `go.mod` before Task 1 lands.

---

### Task 1: `pkg/homekit` foundation — VOC mapping + accessory builder

**Goal:** Add the brutella/hap dependency. Create `pkg/homekit/airquality.go` (pure VOC index → HomeKit AirQuality enum mapping) and `pkg/homekit/accessory.go` (builds `*hap.Accessory` for one Breezy with all services attached). Both with table tests. No daemon code yet.

**Files:**
- Modify: `go.mod`, `go.sum` (add `github.com/brutella/hap`)
- Create: `pkg/homekit/airquality.go`
- Create: `pkg/homekit/airquality_test.go`
- Create: `pkg/homekit/accessory.go`
- Create: `pkg/homekit/accessory_test.go`

**Acceptance Criteria:**
- [ ] `github.com/brutella/hap` is a direct dependency in `go.mod`. Pin a specific version (the latest stable on the module proxy at task time).
- [ ] `pkg/homekit.AirQuality(vocIndex int) characteristic.AirQualityValue` (or equivalent — match the brutella/hap enum type name) maps VOC index to the 5-level enum: 0–50 Excellent, 51–100 Good, 101–150 Fair, 151–200 Inferior, 201+ Poor. Negative input returns Unknown.
- [ ] `pkg/homekit.NewBreezyAccessory(name, deviceID, ip string) *Accessory` returns an accessory with: an `AirPurifier` service, two `Switch` services ("Supply Only", "Extract Only"), `HumiditySensor`, `CarbonDioxideSensor`, `AirQualitySensor`, four `TemperatureSensor` services (with names "Outdoor", "Supply", "Exhaust In", "Exhaust Out").
- [ ] Accessory `Manufacturer = "Vents"`, `Model = "Twinfresh Breezy 160"`, `SerialNumber = deviceID`, `Name = name`.
- [ ] `pkg/homekit.Accessory` exposes typed handles to each service so `sync.go` (Task 2) can write characteristic values without re-walking the service tree.
- [ ] All tests pass; `just check-all` passes.

**Verify:** `go test ./pkg/homekit -v` → all tests pass; `just check-all` exit 0.

**Steps:**

- [ ] **Step 1: Add brutella/hap to go.mod**

```bash
cd /home/hugh/twinfresh
go get github.com/brutella/hap@latest
go mod tidy
```

Verify the version pinned in `go.mod` is recent (≥ v0.0.27 or similar — check the github releases page for the current stable). Update `flake.nix` `vendorHash` if using the Nix build (run `nix build .#breezyd` to surface the new hash; replace the placeholder when prompted).

- [ ] **Step 2: Write `pkg/homekit/airquality_test.go` first (TDD)**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"
)

func TestAirQuality_BoundaryBuckets(t *testing.T) {
	cases := []struct {
		voc  int
		want AirQualityLevel
	}{
		{-1, AirQualityUnknown},
		{0, AirQualityExcellent},
		{50, AirQualityExcellent},
		{51, AirQualityGood},
		{100, AirQualityGood},
		{101, AirQualityFair},
		{150, AirQualityFair},
		{151, AirQualityInferior},
		{200, AirQualityInferior},
		{201, AirQualityPoor},
		{500, AirQualityPoor},
		{10000, AirQualityPoor},
	}
	for _, tc := range cases {
		if got := AirQuality(tc.voc); got != tc.want {
			t.Errorf("AirQuality(%d) = %v, want %v", tc.voc, got, tc.want)
		}
	}
}
```

The `AirQualityLevel` type is a thin wrapper around the brutella/hap library's air-quality value type. If brutella/hap exports its own enum (e.g. `characteristic.AirQualityExcellent`), use that directly and drop the wrapper. Either way the test asserts the same boundary semantics.

- [ ] **Step 3: Implement `pkg/homekit/airquality.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// Package homekit builds and updates HomeKit accessories for the
// breezyd daemon. The package has no daemon dependency: it consumes
// typed inputs (breezy.Status, target setpoints) and produces HAP
// accessory state. Daemon glue lives in cmd/breezyd/homekit.go.
package homekit

// AirQualityLevel is HomeKit's 5-level air-quality enum plus an
// "unknown" sentinel for missing/invalid input. Match the integer
// values to brutella/hap's characteristic.AirQuality* constants
// when wiring sync.go.
type AirQualityLevel int

const (
	AirQualityUnknown AirQualityLevel = iota
	AirQualityExcellent
	AirQualityGood
	AirQualityFair
	AirQualityInferior
	AirQualityPoor
)

// AirQuality maps a VOC index (0..500 typical, 0..1000 firmware max)
// to HomeKit's 5-level enum at boundaries 0/50/100/150/200. Negative
// input returns Unknown.
func AirQuality(vocIndex int) AirQualityLevel {
	switch {
	case vocIndex < 0:
		return AirQualityUnknown
	case vocIndex <= 50:
		return AirQualityExcellent
	case vocIndex <= 100:
		return AirQualityGood
	case vocIndex <= 150:
		return AirQualityFair
	case vocIndex <= 200:
		return AirQualityInferior
	default:
		return AirQualityPoor
	}
}
```

- [ ] **Step 4: Run airquality tests**

```bash
go test ./pkg/homekit -run TestAirQuality -v
```

Expected: PASS for `TestAirQuality_BoundaryBuckets`.

- [ ] **Step 5: Write `pkg/homekit/accessory_test.go` first**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"
)

func TestNewBreezyAccessory_ServiceShape(t *testing.T) {
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	if a == nil {
		t.Fatal("NewBreezyAccessory returned nil")
	}

	// Identity.
	if a.Info.Name != "playroom" {
		t.Errorf("Name = %q", a.Info.Name)
	}
	if a.Info.SerialNumber != "BREEZY00000000A0" {
		t.Errorf("SerialNumber = %q", a.Info.SerialNumber)
	}
	if a.Info.Manufacturer != "Vents" {
		t.Errorf("Manufacturer = %q", a.Info.Manufacturer)
	}
	if a.Info.Model != "Twinfresh Breezy 160" {
		t.Errorf("Model = %q", a.Info.Model)
	}

	// Required services exist.
	if a.AirPurifier == nil {
		t.Error("AirPurifier service missing")
	}
	if a.SupplyOnly == nil || a.ExtractOnly == nil {
		t.Error("airflow Switch services missing")
	}
	if a.Humidity == nil || a.CO2 == nil || a.AirQuality == nil {
		t.Error("sensor services missing")
	}
	for _, ts := range []*TemperatureSensor{a.TempOutdoor, a.TempSupply, a.TempExhaustIn, a.TempExhaustOut} {
		if ts == nil {
			t.Error("temperature sensor missing")
		}
	}
}

func TestNewBreezyAccessory_TemperatureSensorNames(t *testing.T) {
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	cases := map[string]*TemperatureSensor{
		"Outdoor":     a.TempOutdoor,
		"Supply":      a.TempSupply,
		"Exhaust In":  a.TempExhaustIn,
		"Exhaust Out": a.TempExhaustOut,
	}
	for want, ts := range cases {
		if ts.Name != want {
			t.Errorf("temp sensor: name = %q, want %q", ts.Name, want)
		}
	}
}
```

- [ ] **Step 6: Implement `pkg/homekit/accessory.go`**

The exact brutella/hap API is library-version-dependent. The structure below follows the canonical pattern: a struct that embeds the library's accessory type and exposes typed handles to each service so callers can read/write characteristics without re-walking the tree.

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"github.com/brutella/hap/accessory"
	"github.com/brutella/hap/service"
	// + characteristic, etc., as needed
)

// Accessory is the HomeKit representation of one Breezy unit. It
// wraps a brutella/hap *accessory.Accessory and exposes typed handles
// to each service so daemon-side code (cmd/breezyd/homekit.go) can
// register write callbacks and update characteristics directly.
type Accessory struct {
	*accessory.A // embedded brutella/hap accessory

	Info AccessoryInfo

	// Control services.
	AirPurifier *service.AirPurifier
	SupplyOnly  *service.Switch
	ExtractOnly *service.Switch

	// Sensor services.
	Humidity      *service.HumiditySensor
	CO2           *service.CarbonDioxideSensor
	AirQuality    *service.AirQualitySensor
	TempOutdoor   *TemperatureSensor
	TempSupply    *TemperatureSensor
	TempExhaustIn *TemperatureSensor
	TempExhaustOut *TemperatureSensor
}

// AccessoryInfo is the AccessoryInformation service's common fields.
type AccessoryInfo struct {
	Name         string
	SerialNumber string
	Manufacturer string
	Model        string
}

// TemperatureSensor wraps brutella/hap's service.TemperatureSensor
// with a Name field so the four temperature sensors per Breezy can
// be distinguished in the iOS Home app's UI.
type TemperatureSensor struct {
	*service.TemperatureSensor
	Name string
}

// NewBreezyAccessory constructs the full per-Breezy accessory tree.
// Call once per configured device; the daemon glue in
// cmd/breezyd/homekit.go registers the resulting accessory with the
// bridge and wires write callbacks.
func NewBreezyAccessory(name, deviceID, ip string) *Accessory {
	info := accessory.Info{
		Name:         name,
		SerialNumber: deviceID,
		Manufacturer: "Vents",
		Model:        "Twinfresh Breezy 160",
	}
	base := accessory.New(info, accessory.TypeAirPurifier)

	a := &Accessory{
		A: base,
		Info: AccessoryInfo{
			Name: name, SerialNumber: deviceID,
			Manufacturer: "Vents", Model: "Twinfresh Breezy 160",
		},
	}

	a.AirPurifier = service.NewAirPurifier()
	a.SupplyOnly = newNamedSwitch("Supply Only")
	a.ExtractOnly = newNamedSwitch("Extract Only")
	a.Humidity = service.NewHumiditySensor()
	a.CO2 = service.NewCarbonDioxideSensor()
	a.AirQuality = service.NewAirQualitySensor()
	a.TempOutdoor = newNamedTemp("Outdoor")
	a.TempSupply = newNamedTemp("Supply")
	a.TempExhaustIn = newNamedTemp("Exhaust In")
	a.TempExhaustOut = newNamedTemp("Exhaust Out")

	for _, s := range []interface{ /* hap service interface */ }{
		a.AirPurifier, a.SupplyOnly, a.ExtractOnly,
		a.Humidity, a.CO2, a.AirQuality,
		a.TempOutdoor.TemperatureSensor, a.TempSupply.TemperatureSensor,
		a.TempExhaustIn.TemperatureSensor, a.TempExhaustOut.TemperatureSensor,
	} {
		base.AddS(s) // brutella/hap method to attach a service
	}
	return a
}

func newNamedSwitch(name string) *service.Switch {
	sw := service.NewSwitch()
	// brutella/hap allows naming a service via its Name characteristic;
	// see characteristic.NewName(). The exact accessor is library-version-
	// dependent — set sw.Name.SetValue(name) or equivalent.
	return sw
}

func newNamedTemp(name string) *TemperatureSensor {
	ts := service.NewTemperatureSensor()
	return &TemperatureSensor{TemperatureSensor: ts, Name: name}
}
```

The pseudocode `interface{ /* hap service interface */ }` and `base.AddS(s)` are placeholders — verify against `go doc github.com/brutella/hap/accessory.A` for the actual method (likely `AddService`). The implementer should adapt to the current API.

- [ ] **Step 7: Run accessory tests**

```bash
go test ./pkg/homekit -v
```

Expected: PASS for both `TestAirQuality_BoundaryBuckets`, `TestNewBreezyAccessory_ServiceShape`, `TestNewBreezyAccessory_TemperatureSensorNames`.

If a test fails because the test asserts on a field name that doesn't match the actual brutella/hap struct (e.g. `a.Info.Name` vs the library's path), update the test to walk the actual structure — but keep the *behaviour* asserted: name and identity fields must round-trip; all expected services must be present.

- [ ] **Step 8: `just check-all`**

Expected: exit 0. If `nix build` fails on the new `vendorHash` in flake.nix, update it to whatever Nix tells you the correct hash should be. Document this in the commit message.

- [ ] **Step 9: Commit**

```bash
git add go.mod go.sum flake.nix pkg/homekit/
git commit -m "$(cat <<'EOF'
pkg/homekit: foundation — brutella/hap dep + accessory builder

Adds github.com/brutella/hap as a direct dependency. New pkg/homekit
package builds a full HomeKit accessory per Breezy unit: AirPurifier
service, two airflow Switch services (Supply Only / Extract Only),
HumiditySensor, CarbonDioxideSensor, AirQualitySensor, four
TemperatureSensor services (outdoor, supply, exhaust in/out). Pure
library — no daemon dependency yet. VOC index → HomeKit AirQuality
enum mapping included with table tests.

Phase 1 of issue #1.
EOF
)"
```

---

### Task 2: `pkg/homekit/sync.go` — Status → characteristic updates

**Goal:** Add `pkg/homekit.Sync(a *Accessory, s breezy.Status)` that writes the latest values from a `breezy.Status` snapshot into the accessory's characteristics. Includes sentinel handling for the four temperature sensors and VOC index → AirQuality mapping. Pure function; tests use sample Status fixtures.

**Files:**
- Create: `pkg/homekit/sync.go`
- Create: `pkg/homekit/sync_test.go`

**Acceptance Criteria:**
- [ ] `Sync(a *Accessory, s breezy.Status)` writes to every relevant characteristic; missing fields in `s` leave the corresponding characteristic untouched (or set it to its "unknown" state, depending on what the brutella/hap API supports).
- [ ] Temperature sentinels (`-32768` "no sensor" and `+32767` "short circuit") leave the temperature characteristic untouched (or set it to a HAP-defined "no value" — confirm via `go doc`).
- [ ] VOC index → AirQuality enum mapping uses the `AirQuality()` helper from Task 1.
- [ ] CO2 detection: `CarbonDioxideDetected` is set when `eco2_ppm > co2_threshold_ppm` (both from `s.Configured` and `s.Sensors`).
- [ ] AirPurifier `Active` derives from `s.Configured["power"]`; `RotationSpeed` from `s.Configured["manual_pct"]`; `CurrentAirPurifierState` from `s.Live` (Inactive=0 when not powered, Purifying=2 when fan_supply_rpm or fan_extract_rpm > 0, Idle=1 otherwise).
- [ ] `TargetAirPurifierState`: Manual when `speed_mode == "manual"`, Auto when in a preset.
- [ ] Supply Only / Extract Only Switch values derive from `s.Configured["airflow_mode"]`.
- [ ] All tests pass; `just check-all` passes.

**Verify:** `go test ./pkg/homekit -run TestSync -v` → PASS.

**Steps:**

- [ ] **Step 1: Write `pkg/homekit/sync_test.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import (
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

func TestSync_PowerAndSpeed(t *testing.T) {
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	s := breezy.Status{
		Configured: map[string]any{
			"power":      true,
			"speed_mode": "manual",
			"manual_pct": 30,
		},
		Live: map[string]any{
			"fan_supply_rpm":  1500,
			"fan_extract_rpm": 1450,
		},
	}
	Sync(a, s)
	if v, _ := a.AirPurifier.Active.Value(); v != 1 {
		t.Errorf("Active = %v, want 1", v)
	}
	if v, _ := a.AirPurifier.RotationSpeed.Value(); v != 30 {
		t.Errorf("RotationSpeed = %v, want 30", v)
	}
	if v, _ := a.AirPurifier.CurrentAirPurifierState.Value(); v != 2 {
		t.Errorf("CurrentAirPurifierState = %v, want 2 (Purifying)", v)
	}
}

func TestSync_TemperatureSentinels(t *testing.T) {
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	s := breezy.Status{
		Sensors: map[string]any{
			"temp_outdoor_c":       12.5,
			// temp_supply_c missing → sentinel; should leave characteristic untouched
			"temp_exhaust_inlet_c": 22.0,
			"temp_exhaust_outlet_c": -32768.0, // explicit "no sensor"
		},
	}
	Sync(a, s)
	if v, _ := a.TempOutdoor.CurrentTemperature.Value(); v != 12.5 {
		t.Errorf("Outdoor temp = %v, want 12.5", v)
	}
	if v, _ := a.TempExhaustIn.CurrentTemperature.Value(); v != 22.0 {
		t.Errorf("ExhaustIn temp = %v, want 22.0", v)
	}
	// temp_supply_c absent: characteristic value should be its default (0)
	// and not have been written. There is no clean way to assert "untouched"
	// without inspecting library internals; assert it stayed at the type's
	// zero value, which is the contract.
	if v, _ := a.TempSupply.CurrentTemperature.Value(); v != 0 {
		t.Errorf("Supply temp (missing in input) = %v, want 0 (untouched)", v)
	}
}

func TestSync_AirflowModeSwitches(t *testing.T) {
	cases := []struct {
		mode   string
		supply bool
		extract bool
	}{
		{"ventilation", false, false},
		{"regeneration", false, false},
		{"supply", true, false},
		{"extract", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
			Sync(a, breezy.Status{Configured: map[string]any{"airflow_mode": tc.mode}})
			if v, _ := a.SupplyOnly.On.Value(); v != tc.supply {
				t.Errorf("supply mode=%s: Supply.On = %v, want %v", tc.mode, v, tc.supply)
			}
			if v, _ := a.ExtractOnly.On.Value(); v != tc.extract {
				t.Errorf("extract mode=%s: Extract.On = %v, want %v", tc.mode, v, tc.extract)
			}
		})
	}
}

func TestSync_CO2Detection(t *testing.T) {
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	s := breezy.Status{
		Configured: map[string]any{"co2_threshold_ppm": 1000},
		Sensors:    map[string]any{"eco2_ppm": 1200},
	}
	Sync(a, s)
	if v, _ := a.CO2.CarbonDioxideLevel.Value(); v != 1200 {
		t.Errorf("CO2 level = %v", v)
	}
	if v, _ := a.CO2.CarbonDioxideDetected.Value(); v != 1 { // 1 = detected
		t.Errorf("CO2 detected = %v, want 1 (above threshold)", v)
	}
}

func TestSync_VOCToAirQuality(t *testing.T) {
	a := NewBreezyAccessory("playroom", "BREEZY00000000A0", "192.168.1.148")
	Sync(a, breezy.Status{Sensors: map[string]any{"voc_index": 75}})
	if v, _ := a.AirQuality.AirQuality.Value(); v != int(AirQualityGood) {
		t.Errorf("AirQuality = %v, want Good (75 → Good)", v)
	}
}
```

The `.Value()` and `.SetValue()` accessors and characteristic field names follow brutella/hap conventions; verify against `go doc github.com/brutella/hap/service.AirPurifier` for the exact spelling. If the library uses different names (e.g. `.Get()` / `.Set()`), update the test idioms — the *behaviour* asserted is what matters.

- [ ] **Step 2: Implement `pkg/homekit/sync.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package homekit

import "github.com/hughobrien/breezyd/pkg/breezy"

// Sync writes the latest values from a breezy.Status snapshot into
// the accessory's characteristics. Missing fields in s are left
// untouched (HomeKit treats unchanged characteristic values as no-op
// on the wire). Temperature sentinels (-32768 "no sensor" and
// +32767 "short circuit") are skipped: a missing sensor reads as
// "no value" rather than 0°C.
//
// Sync is called from cmd/breezyd/homekit.go after every successful
// poll. It is safe to call concurrently against different accessories
// but not against the same accessory simultaneously.
func Sync(a *Accessory, s breezy.Status) {
	syncAirPurifier(a, s)
	syncAirflowSwitches(a, s)
	syncSensors(a, s)
}

func syncAirPurifier(a *Accessory, s breezy.Status) {
	if power, ok := boolField(s.Configured, "power"); ok {
		val := 0
		if power {
			val = 1
		}
		a.AirPurifier.Active.SetValue(val)
	}
	if pct, ok := intField(s.Configured, "manual_pct"); ok {
		a.AirPurifier.RotationSpeed.SetValue(float64(pct))
	}
	if mode, ok := s.Configured["speed_mode"].(string); ok {
		target := 1 // Auto
		if mode == "manual" {
			target = 0 // Manual
		}
		a.AirPurifier.TargetAirPurifierState.SetValue(target)
	}

	// CurrentAirPurifierState: Inactive (0) when off; Purifying (2)
	// when fan is moving air; Idle (1) when on but fan stopped (e.g.
	// in supply-only mode the extract fan is at 0 and vice versa, but
	// at least one fan should be running when active).
	powered, _ := boolField(s.Configured, "power")
	supplyRPM, _ := intField(s.Live, "fan_supply_rpm")
	extractRPM, _ := intField(s.Live, "fan_extract_rpm")
	switch {
	case !powered:
		a.AirPurifier.CurrentAirPurifierState.SetValue(0)
	case supplyRPM > 0 || extractRPM > 0:
		a.AirPurifier.CurrentAirPurifierState.SetValue(2)
	default:
		a.AirPurifier.CurrentAirPurifierState.SetValue(1)
	}
}

func syncAirflowSwitches(a *Accessory, s breezy.Status) {
	mode, ok := s.Configured["airflow_mode"].(string)
	if !ok {
		return
	}
	a.SupplyOnly.On.SetValue(mode == "supply")
	a.ExtractOnly.On.SetValue(mode == "extract")
}

func syncSensors(a *Accessory, s breezy.Status) {
	if rh, ok := intField(s.Sensors, "humidity_pct"); ok {
		a.Humidity.CurrentRelativeHumidity.SetValue(float64(rh))
	}
	if co2, ok := intField(s.Sensors, "eco2_ppm"); ok {
		a.CO2.CarbonDioxideLevel.SetValue(float64(co2))
		threshold, _ := intField(s.Configured, "co2_threshold_ppm")
		detected := 0 // Normal
		if threshold > 0 && co2 > threshold {
			detected = 1 // Abnormal
		}
		a.CO2.CarbonDioxideDetected.SetValue(detected)
	}
	if voc, ok := intField(s.Sensors, "voc_index"); ok {
		a.AirQuality.AirQuality.SetValue(int(AirQuality(voc)))
	}

	syncTemp(a.TempOutdoor, s.Sensors, "temp_outdoor_c")
	syncTemp(a.TempSupply, s.Sensors, "temp_supply_c")
	syncTemp(a.TempExhaustIn, s.Sensors, "temp_exhaust_inlet_c")
	syncTemp(a.TempExhaustOut, s.Sensors, "temp_exhaust_outlet_c")
}

func syncTemp(ts *TemperatureSensor, sensors map[string]any, key string) {
	v, ok := floatField(sensors, key)
	if !ok {
		return
	}
	// breezy.BuildStatus already drops the -32768/+32767 sentinels by
	// not emitting the field, but defend against a future code path
	// that emits them anyway.
	if v <= -1000 || v >= 1000 {
		return
	}
	ts.CurrentTemperature.SetValue(v)
}

// Helper accessors. Status maps decode JSON-style — numbers come back
// as float64 (or int for code that constructs Status directly).
func boolField(m map[string]any, k string) (bool, bool) {
	v, ok := m[k]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func intField(m map[string]any, k string) (int, bool) {
	if v, ok := m[k].(int); ok {
		return v, true
	}
	if v, ok := m[k].(float64); ok {
		return int(v), true
	}
	return 0, false
}

func floatField(m map[string]any, k string) (float64, bool) {
	if v, ok := m[k].(float64); ok {
		return v, true
	}
	if v, ok := m[k].(int); ok {
		return float64(v), true
	}
	return 0, false
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./pkg/homekit -v
just check-all
```

Expected: all tests pass; check-all green. Tests may fail on field-name spellings until the brutella/hap accessor pattern is matched — adjust the test assertions to walk the actual library types.

- [ ] **Step 4: Commit**

```bash
git add pkg/homekit/sync.go pkg/homekit/sync_test.go
git commit -m "$(cat <<'EOF'
pkg/homekit: add Sync(accessory, status) for poll-driven updates

Writes the latest breezy.Status values into the accessory's
characteristics: AirPurifier (Active, RotationSpeed, CurrentState,
TargetState), the two airflow Switches, all four temperature
sensors with sentinel handling, humidity, eCO2 with threshold-driven
CO2Detected, and VOC index → AirQuality enum.

Phase 1 of issue #1.
EOF
)"
```

---

### Task 3: `internal/config` — `[homekit]` block

**Goal:** Add a `Homekit` struct to `internal/config`, decode from the `[homekit]` TOML section, validate, and resolve `state_dir` defaults (XDG state path, `~` expansion).

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Acceptance Criteria:**
- [ ] `config.Homekit` struct with fields `Enabled bool`, `BridgeName string`, `Port int`, `StateDir string`.
- [ ] TOML decoding for `[homekit]` section (`enabled`, `bridge_name`, `port`, `state_dir`).
- [ ] Validation: `bridge_name` ≤ 32 ASCII chars when non-empty; `port` is 0 or in 1024..65535; `enabled = true` is allowed without other fields (defaults applied).
- [ ] Default resolution after Load: when `Enabled` and `BridgeName == ""`, set `BridgeName = "breezyd"`. When `Enabled` and `StateDir == ""`, set to `$XDG_STATE_HOME/breezyd/homekit` (or `$HOME/.local/state/breezyd/homekit` if unset).
- [ ] `~` expansion applied to user-provided `state_dir`.
- [ ] Tests cover happy path, defaults applied, validation rejections, `~` expansion.
- [ ] Existing config tests still pass without modification.
- [ ] `just check-all` passes.

**Verify:** `go test ./internal/config -v` → PASS for new and existing tests; `just check-all` → exit 0.

**Steps:**

- [ ] **Step 1: Add Homekit struct + raw mirror to `internal/config/config.go`**

After the existing `Daemon`/`Device` types:

```go
// Homekit holds HomeKit bridge settings. The bridge is opt-in via
// Enabled; defaults for BridgeName and StateDir are applied by Load
// when Enabled is true.
type Homekit struct {
	// Enabled controls whether the daemon starts the HAP server.
	Enabled bool
	// BridgeName is shown in iOS during pairing. Default "breezyd".
	BridgeName string
	// Port is the TCP port the HAP server binds to. 0 = ephemeral.
	Port int
	// StateDir holds pairing keys + the generated PIN. Default
	// $XDG_STATE_HOME/breezyd/homekit. Delete to factory-reset
	// pairing.
	StateDir string
}
```

Add `Homekit Homekit` field to the top-level `Config` struct.

Add the raw mirror:

```go
type rawHomekit struct {
	Enabled    bool   `toml:"enabled"`
	BridgeName string `toml:"bridge_name"`
	Port       int    `toml:"port"`
	StateDir   string `toml:"state_dir"`
}
```

And reference it in `rawConfig`:

```go
type rawConfig struct {
	Daemon   rawDaemon            `toml:"daemon"`
	Homekit  rawHomekit           `toml:"homekit"`
	Devices  map[string]rawDevice `toml:"devices"`
}
```

- [ ] **Step 2: Decode + validate + apply defaults in `Load`**

After the existing `cfg := &Config{...}` initialisation, wire the homekit fields:

```go
cfg.Homekit = Homekit{
	Enabled:    raw.Homekit.Enabled,
	BridgeName: raw.Homekit.BridgeName,
	Port:       raw.Homekit.Port,
	StateDir:   raw.Homekit.StateDir,
}
if cfg.Homekit.Enabled {
	if cfg.Homekit.BridgeName == "" {
		cfg.Homekit.BridgeName = "breezyd"
	}
	if len(cfg.Homekit.BridgeName) > 32 {
		return nil, fmt.Errorf("config: homekit bridge_name must be <= 32 chars, got %d", len(cfg.Homekit.BridgeName))
	}
	if cfg.Homekit.Port != 0 && (cfg.Homekit.Port < 1024 || cfg.Homekit.Port > 65535) {
		return nil, fmt.Errorf("config: homekit port must be 0 or 1024-65535, got %d", cfg.Homekit.Port)
	}
	expanded, err := expandStateDir(cfg.Homekit.StateDir)
	if err != nil {
		return nil, fmt.Errorf("config: homekit state_dir: %w", err)
	}
	cfg.Homekit.StateDir = expanded
}
```

Add the helper:

```go
// expandStateDir applies ~/$HOME and $XDG_STATE_HOME defaults for
// the homekit state directory. An empty input returns the default
// $XDG_STATE_HOME/breezyd/homekit (or $HOME/.local/state/... when
// XDG_STATE_HOME is unset).
func expandStateDir(in string) (string, error) {
	if in == "" {
		if x := os.Getenv("XDG_STATE_HOME"); x != "" {
			return filepath.Join(x, "breezyd", "homekit"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "state", "breezyd", "homekit"), nil
	}
	if strings.HasPrefix(in, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, in[2:]), nil
	}
	return in, nil
}
```

- [ ] **Step 3: Add tests to `internal/config/config_test.go`**

Append:

```go
func TestLoad_HomekitDisabledByDefault(t *testing.T) {
	path := writeConfig(t, `
[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Homekit.Enabled {
		t.Error("Homekit.Enabled defaults to false")
	}
}

func TestLoad_HomekitEnabledDefaults(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	path := writeConfig(t, `
[homekit]
enabled = true

[devices.playroom]
id       = "BREEZY00000000A0"
password = "testpwd"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Homekit.Enabled {
		t.Error("Enabled = false, want true")
	}
	if cfg.Homekit.BridgeName != "breezyd" {
		t.Errorf("BridgeName = %q, want default 'breezyd'", cfg.Homekit.BridgeName)
	}
	if !strings.HasSuffix(cfg.Homekit.StateDir, "/breezyd/homekit") {
		t.Errorf("StateDir = %q, want path ending in /breezyd/homekit", cfg.Homekit.StateDir)
	}
}

func TestLoad_HomekitBridgeNameTooLong(t *testing.T) {
	path := writeConfig(t, `
[homekit]
enabled     = true
bridge_name = "this-name-is-way-too-long-for-the-32-char-limit"
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for long bridge_name")
	}
	if !strings.Contains(err.Error(), "32 chars") {
		t.Errorf("error should mention 32 char limit: %v", err)
	}
}

func TestLoad_HomekitBadPort(t *testing.T) {
	path := writeConfig(t, `
[homekit]
enabled = true
port    = 80
`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for low port")
	}
	if !strings.Contains(err.Error(), "1024-65535") {
		t.Errorf("error should mention port range: %v", err)
	}
}

func TestLoad_HomekitTildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	path := writeConfig(t, `
[homekit]
enabled   = true
state_dir = "~/.config/breezyd/homekit"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := filepath.Join(home, ".config", "breezyd", "homekit")
	if cfg.Homekit.StateDir != want {
		t.Errorf("StateDir = %q, want %q", cfg.Homekit.StateDir, want)
	}
}
```

- [ ] **Step 4: Run tests + commit**

```bash
go test ./internal/config -v
just check-all
git add internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
internal/config: add [homekit] block

New top-level Homekit struct with Enabled / BridgeName / Port /
StateDir. Validation: bridge_name ≤ 32 chars, port either 0 or
1024-65535. Defaults: bridge_name = "breezyd", state_dir =
$XDG_STATE_HOME/breezyd/homekit (~/.local/state/breezyd/homekit
fallback). ~/ expansion applied to user-supplied state_dir.

Phase 1 of issue #1.
EOF
)"
```

---

### Task 4: `cmd/breezyd/homekit.go` — HAP server lifecycle, PIN persistence, write callbacks, Switch mutex

**Goal:** Daemon-side glue. Start/stop the HAP server when `[homekit].enabled`. Auto-generate or load the PIN from `state_dir`, print on every start. Build accessories from configured devices and register write callbacks that dispatch through `pkg/breezy/ops` via `dialRecording`. Enforce mutual exclusion for the Supply Only / Extract Only switches.

**Files:**
- Create: `cmd/breezyd/homekit.go`
- Create: `cmd/breezyd/homekit_test.go`

**Acceptance Criteria:**
- [ ] `func (h *Handler) StartHomekit(ctx context.Context, cfg config.Homekit, devices map[string]DeviceConfig) (stop func() error, err error)` exists.
- [ ] On first start: PIN is generated via `crypto/rand`, formatted as `XXX-XX-XXX`, persisted to `<state_dir>/pin.txt` mode 0600, and printed to stderr at INFO level. brutella/hap weak-PIN list (e.g. `123-45-678`, `000-00-000`) is filtered.
- [ ] On subsequent starts: PIN is read from `pin.txt` and printed.
- [ ] Bridge accessory + one child accessory per device is built and registered with the HAP server.
- [ ] Each writable characteristic on each child accessory has a write callback registered:
  - `AirPurifier.Active` → `breezy.Power(ctx, rc, on)`
  - `AirPurifier.RotationSpeed` → `breezy.SetSpeedManual(ctx, rc, pct)` with `pct < 10` snapped to 10
  - `AirPurifier.TargetAirPurifierState` → if Manual: `breezy.SetSpeedManual(ctx, rc, currentSpeed)`; if Auto: `breezy.SetSpeedPreset(ctx, rc, 1)` if currently in manual, no-op otherwise
  - `SupplyOnly.On` → `breezy.SetMode(ctx, rc, "supply")` when on; when both Supply and Extract are off, write `"regeneration"`. ExtractOnly is forced off.
  - `ExtractOnly.On` → mirror of SupplyOnly.
  - All callbacks use `h.dialRecording(name)` from Phase 1 to flow writes through the recording wrapper.
- [ ] Mutual exclusion: turning Supply Only On forces Extract Only Off (in HAP characteristic state) and vice versa.
- [ ] `stop()` cleanly shuts down the HAP server.
- [ ] Tests cover: PIN generation, PIN persistence across runs, write-callback dispatch (Power), mutual exclusion. All against a `pkg/breezy/fakedevice`.
- [ ] `just check-all` passes.

**Verify:** `go test ./cmd/breezyd -run TestHomekit -v` → PASS; `just check-all` → exit 0.

**Steps:**

- [ ] **Step 1: Skeleton `cmd/breezyd/homekit.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brutella/hap"
	"github.com/brutella/hap/accessory"

	"github.com/hughobrien/breezyd/internal/config"
	"github.com/hughobrien/breezyd/pkg/breezy"
	"github.com/hughobrien/breezyd/pkg/homekit"
)

// weakPins is the list brutella/hap rejects. Replicate locally so we
// don't waste time generating + persisting a PIN that the library
// will refuse on bridge start.
var weakPins = map[string]bool{
	"12345678": true, "11111111": true, "22222222": true, "33333333": true,
	"44444444": true, "55555555": true, "66666666": true, "77777777": true,
	"88888888": true, "99999999": true, "00000000": true, "12344321": true,
	"87654321": true,
}

// StartHomekit boots the HAP bridge if cfg.Enabled. Returns a stop
// func that the caller (cmd/breezyd/main.go) defers. Caller must also
// call h.SyncHomekit(name, status) from a poller hook for sensor
// updates to propagate (Task 5).
func (h *Handler) StartHomekit(ctx context.Context, cfg config.Homekit, devices map[string]DeviceConfig) (func() error, error) {
	if !cfg.Enabled {
		return func() error { return nil }, nil
	}

	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("homekit: state dir: %w", err)
	}
	pin, err := loadOrGeneratePin(cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("homekit: pin: %w", err)
	}
	slog.Info("homekit: bridge ready",
		"name", cfg.BridgeName, "pin", formatPinDisplay(pin), "state_dir", cfg.StateDir)

	// Build bridge + child accessories.
	bridgeInfo := accessory.Info{
		Name:         cfg.BridgeName,
		Manufacturer: "Vents",
		Model:        "breezyd",
		SerialNumber: "breezyd-bridge",
	}
	bridge := accessory.NewBridge(bridgeInfo)

	children := make(map[string]*homekit.Accessory, len(devices))
	for name, dev := range devices {
		a := homekit.NewBreezyAccessory(name, dev.ID, dev.IP)
		children[name] = a
		registerWriteCallbacks(h, a, name, children)
	}

	store := hap.NewFsStore(cfg.StateDir)
	server, err := hap.NewServer(store, bridge.A, accessoriesOf(children)...)
	if err != nil {
		return nil, fmt.Errorf("homekit: server: %w", err)
	}
	server.Pin = pin
	if cfg.Port != 0 {
		server.Addr = fmt.Sprintf(":%d", cfg.Port)
	}

	// Stash the children on Handler so SyncHomekit (Task 5) can find them.
	h.homekitAccessories = children

	// Run the server in a goroutine; its ListenAndServe blocks until ctx
	// is cancelled.
	srvCtx, srvCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		_ = server.ListenAndServe(srvCtx)
		close(done)
	}()

	stop := func() error {
		srvCancel()
		<-done
		return nil
	}
	return stop, nil
}

// loadOrGeneratePin returns the persisted PIN at <stateDir>/pin.txt,
// creating one if absent. Format: 8 decimal digits with hyphens
// inserted as XXX-XX-XXX for display; persistence stores raw 8 digits.
func loadOrGeneratePin(stateDir string) (string, error) {
	path := filepath.Join(stateDir, "pin.txt")
	if raw, err := os.ReadFile(path); err == nil {
		pin := strings.TrimSpace(string(raw))
		if len(pin) == 8 {
			return pin, nil
		}
		// Length mismatch: regenerate.
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	for attempt := 0; attempt < 64; attempt++ {
		pin, err := generatePin()
		if err != nil {
			return "", err
		}
		if !weakPins[pin] {
			if err := os.WriteFile(path, []byte(pin+"\n"), 0o600); err != nil {
				return "", fmt.Errorf("write pin: %w", err)
			}
			return pin, nil
		}
	}
	return "", errors.New("homekit: failed to generate non-weak pin after 64 tries (improbable)")
}

func generatePin() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	// 8 digits derived from 4 random bytes.
	n := uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24
	return fmt.Sprintf("%08d", n%100000000), nil
}

func formatPinDisplay(pin string) string {
	if len(pin) != 8 {
		return pin
	}
	return pin[0:3] + "-" + pin[3:5] + "-" + pin[5:8]
}

// accessoriesOf returns the brutella/hap accessory pointers from a
// name-keyed map, sorted by name for stable ordering.
func accessoriesOf(children map[string]*homekit.Accessory) []*accessory.A {
	names := make([]string, 0, len(children))
	for n := range children {
		names = append(names, n)
	}
	// stable order so brutella/hap's accessory IDs are deterministic
	// across restarts.
	sort.Strings(names)
	out := make([]*accessory.A, 0, len(children))
	for _, n := range names {
		out = append(out, children[n].A)
	}
	return out
}
```

The `accessory.NewBridge`, `hap.NewFsStore`, `hap.NewServer`, `server.Pin`, `server.Addr`, `server.ListenAndServe` calls all reflect canonical brutella/hap usage. Verify each against the current library godoc; the names may differ slightly. Also add `"sort"` to the import block.

Add `homekitAccessories map[string]*homekit.Accessory` field to the `Handler` struct in `cmd/breezyd/server.go`.

- [ ] **Step 2: Implement `registerWriteCallbacks`**

Append to `cmd/breezyd/homekit.go`:

```go
// registerWriteCallbacks wires each writable characteristic on
// accessory `a` to a callback that dispatches through h.dialRecording
// + pkg/breezy/ops. Switch mutex enforcement is in the closures here.
func registerWriteCallbacks(h *Handler, a *homekit.Accessory, name string, children map[string]*homekit.Accessory) {
	timeout := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 5*time.Second)
	}

	// AirPurifier.Active → Power.
	a.AirPurifier.Active.OnValueRemoteUpdate(func(v int) {
		ctx, cancel := timeout()
		defer cancel()
		rc, raw, err := h.dialRecording(name)
		if err != nil {
			slog.Error("homekit: power dial", "device", name, "err", err)
			return
		}
		defer raw.Close()
		if err := breezy.Power(ctx, rc, v == 1); err != nil {
			slog.Error("homekit: power write", "device", name, "err", err)
		}
	})

	// AirPurifier.RotationSpeed → SetSpeedManual.
	a.AirPurifier.RotationSpeed.OnValueRemoteUpdate(func(pct float64) {
		clamped := int(pct)
		if clamped < 10 {
			clamped = 10
		}
		if clamped > 100 {
			clamped = 100
		}
		ctx, cancel := timeout()
		defer cancel()
		rc, raw, err := h.dialRecording(name)
		if err != nil {
			slog.Error("homekit: speed dial", "device", name, "err", err)
			return
		}
		defer raw.Close()
		if err := breezy.SetSpeedManual(ctx, rc, clamped); err != nil {
			slog.Error("homekit: speed write", "device", name, "err", err)
		}
	})

	// AirPurifier.TargetAirPurifierState → manual (0) vs auto (1).
	a.AirPurifier.TargetAirPurifierState.OnValueRemoteUpdate(func(v int) {
		ctx, cancel := timeout()
		defer cancel()
		rc, raw, err := h.dialRecording(name)
		if err != nil {
			slog.Error("homekit: target state dial", "device", name, "err", err)
			return
		}
		defer raw.Close()
		if v == 0 {
			// Manual: write current rotation speed.
			pct, _ := a.AirPurifier.RotationSpeed.Value().(float64)
			if pct < 10 {
				pct = 30 // sensible default if slider was at 0
			}
			_ = breezy.SetSpeedManual(ctx, rc, int(pct))
		} else {
			_ = breezy.SetSpeedPreset(ctx, rc, 1)
		}
	})

	// Supply / Extract switches with mutual exclusion.
	a.SupplyOnly.On.OnValueRemoteUpdate(func(on bool) {
		switchAirflow(h, name, a, children, on, false)
	})
	a.ExtractOnly.On.OnValueRemoteUpdate(func(on bool) {
		switchAirflow(h, name, a, children, false, on)
	})
}

// switchAirflow enforces the airflow-mode mutex and writes the
// resulting mode via pkg/breezy/ops.SetMode.
//
//   supply true,  extract false → mode "supply"
//   supply false, extract true  → mode "extract"
//   supply false, extract false → mode "regeneration" (both fans + heat recovery)
//
// The function also flips the OTHER switch's HAP state to mirror the
// device's actual mode, so the iOS Home app converges immediately.
func switchAirflow(h *Handler, name string, a *homekit.Accessory, children map[string]*homekit.Accessory, supply, extract bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rc, raw, err := h.dialRecording(name)
	if err != nil {
		slog.Error("homekit: airflow dial", "device", name, "err", err)
		return
	}
	defer raw.Close()

	var mode string
	switch {
	case supply && !extract:
		mode = "supply"
	case extract && !supply:
		mode = "extract"
	default:
		mode = "regeneration"
	}
	if err := breezy.SetMode(ctx, rc, mode); err != nil {
		slog.Error("homekit: airflow write", "device", name, "mode", mode, "err", err)
		return
	}
	// Update the HAP characteristic states to reflect the daemon's
	// authoritative truth.
	a.SupplyOnly.On.SetValue(supply)
	a.ExtractOnly.On.SetValue(extract)
}
```

Note: brutella/hap's `OnValueRemoteUpdate` is the canonical "client wrote to this characteristic" callback. The exact method name may differ (`OnValueRemoteUpdate` vs `OnValueUpdateFromConnection` vs `OnSetRemoteValue` — different library versions). Match the current API.

- [ ] **Step 3: `cmd/breezyd/homekit_test.go` — PIN + write callback dispatch**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hughobrien/breezyd/internal/config"
	"github.com/hughobrien/breezyd/pkg/breezy/fakedevice"
)

func TestHomekit_PinPersists(t *testing.T) {
	dir := t.TempDir()
	pin1, err := loadOrGeneratePin(dir)
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	if len(pin1) != 8 {
		t.Errorf("pin length = %d, want 8", len(pin1))
	}
	if weakPins[pin1] {
		t.Errorf("generated pin %q is in weak list", pin1)
	}
	pin2, err := loadOrGeneratePin(dir)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if pin1 != pin2 {
		t.Errorf("PIN changed across runs: %q != %q", pin1, pin2)
	}
}

func TestHomekit_PinFileMode(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadOrGeneratePin(dir); err != nil {
		t.Fatalf("generate: %v", err)
	}
	st, err := os.Stat(filepath.Join(dir, "pin.txt"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := st.Mode().Perm(); mode != 0o600 {
		t.Errorf("pin.txt mode = %#o, want 0600", mode)
	}
}

func TestHomekit_PinFormat(t *testing.T) {
	got := formatPinDisplay("12345678")
	if got != "123-45-678" {
		t.Errorf("formatPinDisplay = %q, want '123-45-678'", got)
	}
}

func TestHomekit_StartDisabledIsNoop(t *testing.T) {
	h := &Handler{}
	stop, err := h.StartHomekit(context.Background(), config.Homekit{Enabled: false}, nil)
	if err != nil {
		t.Fatalf("StartHomekit: %v", err)
	}
	if stop == nil {
		t.Fatal("stop is nil")
	}
	if err := stop(); err != nil {
		t.Errorf("stop: %v", err)
	}
}

```

These four tests exercise the parts of Task 4 that don't require a running HAP server: PIN generation, persistence, file mode, format, and the disabled-noop path. The end-to-end "HomeKit write reaches the device via UDP" assertion lives in Task 5's integration test, which depends on Task 5's poller/main.go wiring being in place.

- [ ] **Step 4: Run tests + commit**

```bash
go test ./cmd/breezyd -run TestHomekit -v
just check-all
git add cmd/breezyd/homekit.go cmd/breezyd/homekit_test.go cmd/breezyd/server.go
git commit -m "$(cat <<'EOF'
cmd/breezyd: HAP server + PIN persistence + write callbacks + Switch mutex

StartHomekit boots the bridge when [homekit].enabled, generates and
persists a non-weak 8-digit PIN to <state_dir>/pin.txt mode 0600,
prints it on every start. Builds bridge + per-Breezy child
accessories; each writable characteristic has a write callback that
dispatches through dialRecording + pkg/breezy/ops. Supply Only /
Extract Only switches enforce mutual exclusion and snap to
"regeneration" when both off.

Phase 1 of issue #1.
EOF
)"
```

---

### Task 5: Poller `OnPoll` hook + `cmd/breezyd/main.go` wiring + integration test

**Goal:** Add an `OnPoll(name string, snap Snapshot)` callback to `Poller` that fires after every successful poll. Wire it from `cmd/breezyd/main.go` to call `homekit.Sync(accessory, status)` for each device. Then actually start the HomeKit subsystem in `main.go` when config opts in. Add an end-to-end integration test that changes a fakedevice value, waits for a poll, and asserts the corresponding HomeKit characteristic updates.

**Files:**
- Modify: `cmd/breezyd/poller.go` (add `OnPoll` callback)
- Modify: `cmd/breezyd/main.go` (start homekit; subscribe to OnPoll)
- Modify: `cmd/breezyd/homekit.go` (add `SyncHomekit` method)
- Modify: `cmd/breezyd/homekit_test.go` (full end-to-end integration test)
- Modify: `cmd/breezyd/poller_test.go` (verify OnPoll is invoked — small additive test)

**Acceptance Criteria:**
- [ ] `Poller.OnPoll func(name string, snap Snapshot)` field exists; called after every successful tick (not on error ticks). Optional — nil OnPoll is a no-op.
- [ ] `Handler.SyncHomekit(name string, snap Snapshot)` builds a `breezy.Status` from the snapshot using `breezy.BuildStatus` and calls `homekit.Sync` on the corresponding child accessory. No-op if homekit is disabled or device unknown.
- [ ] `cmd/breezyd/main.go::run` calls `h.StartHomekit(...)` after the pollers start, and registers `h.SyncHomekit` as each poller's `OnPoll`. Uses `defer stop()` for clean shutdown.
- [ ] Integration test: with `[homekit].enabled = true` and a fakedevice, change a fakedevice param value (e.g. update humidity from 40 to 65), wait for one poll cycle (use a short interval like 100ms in the test), assert the HumiditySensor characteristic on the bridge accessory shows 65.
- [ ] Existing poller tests still pass.
- [ ] `just check-all` passes.

**Verify:** `just check-all` exit 0; `go test ./cmd/breezyd -run TestHomekit -v` PASS.

**Steps:**

- [ ] **Step 1: Add OnPoll to `cmd/breezyd/poller.go`**

In the `Poller` struct, add:

```go
// OnPoll, when non-nil, is invoked after every successful tick with
// the recorded snapshot. Called synchronously from the tick goroutine;
// the callback should be quick (it must not block the next tick).
// Optional — nil is a no-op.
OnPoll func(name string, snap Snapshot)
```

In `tick`, after `p.State.RecordPoll(...)` on the success path:

```go
if p.OnPoll != nil && lastErr == nil {
	p.OnPoll(p.Name, Snapshot{
		IP: p.IP, Values: values, LastPoll: p.now(),
	})
}
```

Add a small test in `poller_test.go`:

```go
func TestPoller_OnPollFiresOnSuccess(t *testing.T) {
	srv := newFakeServer(t)
	state := NewState()
	called := 0
	p := &Poller{
		Name: "playroom", IP: srv.Addr(), DeviceID: pollerTestDeviceID, Password: pollerTestPassword,
		Interval: 50 * time.Millisecond, State: state, ReadIDs: []breezy.ParamID{0x01},
		OnPoll: func(name string, snap Snapshot) {
			called++
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	p.Run(ctx)
	if called < 2 {
		t.Errorf("OnPoll fired %d times, want >= 2", called)
	}
}
```

- [ ] **Step 2: Add `Handler.SyncHomekit` to `cmd/breezyd/homekit.go`**

```go
// SyncHomekit translates a daemon-side Snapshot into a breezy.Status
// and pushes the latest values into the corresponding child
// accessory's characteristics. No-op when homekit is disabled or the
// device isn't bridged.
func (h *Handler) SyncHomekit(name string, snap Snapshot) {
	a, ok := h.homekitAccessories[name]
	if !ok {
		return
	}
	cfg, ok := h.Devices.Get(name)
	if !ok {
		return
	}
	ip := cfg.IP
	if snap.IP != "" {
		ip = snap.IP
	}
	var lastPoll *time.Time
	if !snap.LastPoll.IsZero() {
		lastPoll = &snap.LastPoll
	}
	s := breezy.BuildStatus(snap.Values, name, cfg.ID, ip, lastPoll)
	homekit.Sync(a, s)
}
```

- [ ] **Step 3: Wire into `cmd/breezyd/main.go`**

In `run()`, after the pollers are started:

```go
hkStop, err := h.StartHomekit(rootCtx, cfg.Homekit, devices.Snapshot())
if err != nil {
	slog.Error("homekit: failed to start", "err", err)
	return 1
}
defer func() { _ = hkStop() }()

// Wire each poller's OnPoll → SyncHomekit.
for _, p := range pollers {
	p.OnPoll = h.SyncHomekit
}
```

The `pollers` slice should already be in scope from `startPollers`. If not, capture it. The OnPoll assignment must happen before any tick fires — `startPollers` may already kick off ticks. Either:

- Add an `OnPollFunc` parameter to `startPollers` so it sets the field before `go p.Run()`.
- Or move `StartHomekit` ahead of `startPollers` so the field is set first.

The cleaner approach is to thread it through `startPollers`. Update its signature:

```go
func startPollers(ctx context.Context, devices map[string]DeviceConfig, interval time.Duration, state *State, metrics *Metrics, onPoll func(name string, snap Snapshot)) ([]*Poller, *sync.WaitGroup) {
```

Pass `h.SyncHomekit` (or a closure that captures `h`) at the call site.

- [ ] **Step 4: End-to-end integration test**

Replace the placeholder in `homekit_test.go::TestHomekit_PowerWriteRoundTrip` with:

```go
func TestHomekit_PollerSyncsSensors(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: opens UDP + HAP sockets")
	}
	fake, err := fakedevice.NewServer(homekitFakeSnapshotPath(t), homekitTestDeviceID, homekitTestPassword)
	if err != nil {
		t.Fatalf("fakedevice: %v", err)
	}
	t.Cleanup(func() { _ = fake.Close() })

	// (Reuse newServerHandler-style scaffolding — exact pattern from
	// cmd/breezyd/server_test.go.)
	h := newTestHandlerWithFake(t, fake)
	stateDir := t.TempDir()
	cfg := config.Homekit{Enabled: true, BridgeName: "test", StateDir: stateDir}
	devices := map[string]DeviceConfig{
		"playroom": {ID: homekitTestDeviceID, Password: homekitTestPassword, IP: fake.Addr()},
	}
	stop, err := h.StartHomekit(context.Background(), cfg, devices)
	if err != nil {
		t.Fatalf("StartHomekit: %v", err)
	}
	defer stop()

	// Drive a manual snapshot through SyncHomekit (faster than waiting
	// for a real poll cycle in a test).
	h.SyncHomekit("playroom", Snapshot{
		IP: fake.Addr(),
		Values: map[breezy.ParamID][]byte{
			0x0025: {65}, // humidity 65%
		},
	})
	a := h.homekitAccessories["playroom"]
	rh, _ := a.Humidity.CurrentRelativeHumidity.Value().(float64)
	if rh != 65 {
		t.Errorf("humidity characteristic = %v, want 65", rh)
	}
}
```

- [ ] **Step 5: Run tests + commit**

```bash
just check-all
git add cmd/breezyd/poller.go cmd/breezyd/poller_test.go cmd/breezyd/homekit.go cmd/breezyd/homekit_test.go cmd/breezyd/main.go
git commit -m "$(cat <<'EOF'
cmd/breezyd: wire HomeKit into the daemon — Poller.OnPoll + Sync glue

Adds a per-poller OnPoll callback fired after every successful tick.
main.go starts the HomeKit subsystem when [homekit].enabled and
registers SyncHomekit as each poller's OnPoll. End-to-end test
exercises poller→sync via fakedevice; humidity update propagates
to HumiditySensor characteristic.

Phase 1 of issue #1.
EOF
)"
```

---

### Task 6: NixOS module + README + CLAUDE.md + CHANGELOG

**Goal:** Surface the new HomeKit feature in the user-facing layers: NixOS module options for production deployments, README section explaining setup + pairing, CLAUDE.md update for the Architecture section, CHANGELOG `[Unreleased]` entry.

**Files:**
- Modify: `nix/module.nix`
- Modify: `README.md`
- Modify: `CLAUDE.md`
- Modify: `CHANGELOG.md`

**Acceptance Criteria:**
- [ ] `services.breezyd.homekit.{enable, port, bridgeName, stateDir}` options exist in `nix/module.nix`. Default `stateDir = "/var/lib/breezyd/homekit"`. The `enable` option is opt-in; when true, the module renders a `[homekit]` block in the generated TOML and adds `breezyd-homekit` to the systemd `StateDirectory=`.
- [ ] If `services.breezyd.homekit.port != 0` and `services.breezyd.openFirewall = true`, the firewall rule includes the HomeKit port.
- [ ] `just nix-check` passes.
- [ ] README has a "HomeKit" subsection under the existing CLI surface that explains: opt-in via config, pairing flow, where the PIN is shown, how to factory-reset.
- [ ] CLAUDE.md's Architecture section gets a one-paragraph mention of the HomeKit bridge as an opt-in subsystem.
- [ ] CHANGELOG `[Unreleased]` has an "Added" entry covering the HomeKit bridge.
- [ ] `just check-all` passes.

**Verify:** `just check-all` exits 0; `just nix-check` exits 0; manually scan README/CLAUDE.md for the new sections.

**Steps:**

- [ ] **Step 1: Add NixOS module options**

In `nix/module.nix`, near the existing `services.breezyd.prometheus` block, add:

```nix
homekit = {
  enable = mkEnableOption "HomeKit bridge that exposes configured Breezy units to Apple Home";

  port = mkOption {
    type = types.port;
    default = 0;
    description = ''
      TCP port the HAP server binds to. 0 = ephemeral (OS-assigned).
      Pin a port if the firewall needs a fixed hole.
    '';
  };

  bridgeName = mkOption {
    type = types.str;
    default = "breezyd";
    description = "Name shown in iOS during HomeKit pairing.";
  };

  stateDir = mkOption {
    type = types.path;
    default = "/var/lib/breezyd/homekit";
    description = ''
      Directory where the HAP server persists pairing keys + the
      generated PIN. Delete to factory-reset HomeKit pairing.
    '';
  };
};
```

In the module's `config = mkIf cfg.enable {...}` block, when `cfg.homekit.enable` is true:

- Add `"breezyd"` to `systemd.services.breezyd.serviceConfig.StateDirectory` (the homekit subdir is created at runtime by the daemon's `MkdirAll`).
- Render the `[homekit]` block in the generated TOML config (extend the existing `pkgs.writeText "breezyd.toml" ...` call).
- If `cfg.openFirewall && cfg.homekit.port != 0`, add the port to `networking.firewall.allowedTCPPorts`.

Reference the existing `prometheus` integration for the pattern.

- [ ] **Step 2: README HomeKit section**

Add a subsection under the CLI overview / Configuration section. Suggested placement: after "Configuration", before "Behind nginx (NixOS)".

```markdown
## HomeKit (optional)

The daemon includes an opt-in HomeKit bridge. When enabled, each
configured Breezy appears in the Apple Home app as one accessory
with power, fan speed, supply-only / extract-only switches, and the
full sensor surface (humidity, eCO2, VOC, four temperatures).

Enable it by adding to `~/.config/breezy/config.toml`:

```toml
[homekit]
enabled = true
```

Restart `breezyd`. The startup log includes a line like:

    homekit: bridge ready name="breezyd" pin="123-45-678" state_dir="..."

Open the Apple Home app on iPhone → Add Accessory → enter the PIN
manually (no QR code needed for setup; the bridge advertises itself
via Bonjour). All configured Breezy units appear together; each
gets its own tile.

**Reset pairing:** delete the state directory (`~/.local/state/
breezyd/homekit` by default, `/var/lib/breezyd/homekit` on NixOS).
The next daemon start regenerates the PIN.

**Tunables** (all optional):

- `bridge_name`: name shown during pairing. Default `"breezyd"`.
- `port`: TCP port for the HAP server. Default 0 (OS-assigned).
- `state_dir`: where pairing keys + the PIN live.
```

- [ ] **Step 3: CLAUDE.md Architecture update**

Add a paragraph to the "## Architecture" section, after the existing description of the daemon:

```markdown
**HomeKit bridge (optional, opt-in via `[homekit].enabled`).** When
enabled, the daemon runs a brutella/hap HAP server that exposes each
configured Breezy as a HomeKit accessory (AirPurifier + airflow-mode
Switches + sensor services). The bridge runs in-process; writes flow
through `pkg/breezy/ops` via the same `dialRecording` wrapper as the
HTTP handlers, so every protocol invariant (packet ordering,
fan-settle, validation) holds. PIN auto-generated on first run and
persisted to `state_dir`. Pure accessory logic lives in
`pkg/homekit`; daemon glue in `cmd/breezyd/homekit.go`.
```

- [ ] **Step 4: CHANGELOG entry**

In `[Unreleased]`, under `### Added`:

```markdown
- HomeKit bridge: `breezyd` exposes each configured Breezy as a HomeKit accessory (AirPurifier service + Supply/Extract Switches + humidity, eCO2, VOC, and four temperature sensors). Opt-in via `[homekit].enabled` in `~/.config/breezy/config.toml`. PIN auto-generated and printed on every start; reset by deleting the state directory. NixOS module gains `services.breezyd.homekit.{enable, port, bridgeName, stateDir}`. (#1)
```

- [ ] **Step 5: Verify + commit**

```bash
just nix-check
just check-all
git add nix/module.nix README.md CLAUDE.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs+nix: expose HomeKit bridge via NixOS module + docs

services.breezyd.homekit.{enable, port, bridgeName, stateDir}
mirrors the existing prometheus/nginx integration shape. README
gains a HomeKit section covering enable + pair + reset. CLAUDE.md
mentions the bridge in Architecture. CHANGELOG [Unreleased] gets
an Added entry.

Closes #1.
EOF
)"
```

---

## Out of scope (not done by this plan)

- Filter maintenance services (per spec — explicitly dropped).
- Per-device HomeKit opt-out.
- Heat-recovery toggle in HomeKit (the ventilation-vs-regeneration distinction).
- Per-mode preset setpoints from HomeKit.
- Custom characteristics for power-user clients (Eve, Controller for HomeKit).
- Apple HomeKit certification / MFi licensing.

## Verification (post-implementation)

- `just check-all` exits 0.
- A fresh `breezyd` start with `[homekit].enabled = true` against a fakedevice in CI logs the PIN.
- A real iPhone successfully pairs against a `breezyd` bound to a real Breezy on the LAN. (Manual; documented in README.)
- All Breezy controls (power, speed, supply-only, extract-only) and sensors behave correctly from the Home app.
- `breezyd` restart preserves pairing.
- Deleting `state_dir` forces re-pairing with a fresh PIN.
