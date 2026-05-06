# Sensor auto-fan toggle + threshold input width — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Resolve issue #34 by surfacing the firmware's three per-sensor enable flags (`0x000F`, `0x0011`, `0x0315`) through the JSON snapshot, the HTTP API, the dashboard's inline threshold editor, and the CLI; and bumping the inline-editor input width to fit 4-digit CO₂ values.

**Architecture:** Three-phase change with a clean dependency chain. Task 1 extends `breezy.EnergyValues`-style coverage to sensor-enable: adds three keys to `Configured`, replaces `SetThreshold(value)` with `SetThresholdConfig(value*, enabled*)` (1-line wrapper preserves the old signature for trivial callers), and extends the `/threshold` HTTP body with optional `value` and `enabled`. Task 2 rebuilds the dashboard's inline threshold editor to add an `auto fan` checkbox + 4.5rem width and emits one POST that includes only the changed fields. Task 3 adds two CLI verbs (`threshold` and `auto-fan`) that map cleanly onto `SetThresholdConfig`. Tasks 2 and 3 are independent of each other; both depend on Task 1.

**Tech Stack:** Go (`pkg/breezy`, `cmd/breezyd`, `cmd/breezy`), Prometheus client_golang for metrics, vanilla HTML/CSS/JS for the dashboard, Playwright (`@playwright/test`) for UI regression tests, `just` recipes for build/test/screenshot.

**Spec:** `docs/superpowers/specs/2026-05-06-sensor-auto-fan-design.md`

---

## File Structure

### Task 1 — backend
- **Modify** `pkg/breezy/status.go` — `BuildStatus` populates `humidity_sensor_enabled` / `co2_sensor_enabled` / `voc_sensor_enabled` in the `Configured` map.
- **Modify** `pkg/breezy/ops.go` — replace `SetThreshold(value)` with `SetThresholdConfig(value *int, enabled *bool)`; keep `SetThreshold` as a one-line wrapper for backward compatibility.
- **Modify** `cmd/breezyd/handlers_device.go` — `postThreshold` body becomes `{kind, value?, enabled?}`; require at least one of value/enabled; route to `SetThresholdConfig`.
- **Modify** `pkg/breezy/status_test.go` — extend `TestBuildStatus_JSONShape` (or add a new test) to assert the three enable keys when present.
- **Modify** `pkg/breezy/ops_test.go` — extend tests for `SetThreshold`, add tests for `SetThresholdConfig` (value-only, enabled-only, both, neither, out-of-range, unknown kind).
- **Modify** `cmd/breezyd/server_test.go` — extend `TestHandler_PostThreshold` and `TestHandler_PostThreshold_BadInputs` to cover enabled-only / value+enabled bodies and the new "missing both" 400 case.

### Task 2 — frontend
- **Modify** `cmd/breezyd/ui/index.html`:
  - CSS: bump `.thresh-edit-inline .thresh-input { width }` from 3rem to 4.5rem.
  - Inline editor markup gets a `<label class="thresh-auto-fan"><input type="checkbox" class="thresh-auto-fan-input" …>auto fan</label>`.
  - `saveThreshold` reads both the input value and the checkbox state, builds a `{kind, value?, enabled?}` body that includes only fields that changed from the snapshot; cancels when nothing changed.
  - Cell renderer reads the current enable state from `snap.configured.<kind>_sensor_enabled` to populate the checkbox's `checked` attribute.
- **Modify** `tests/ui/dashboard.spec.ts`:
  - Extend `baseSnapshot` defaults so `configured.humidity_sensor_enabled` / `co2_sensor_enabled` / `voc_sensor_enabled` are all `true` (matches firmware default; existing tests still pass).
  - Update existing `"threshold: save POSTs {kind, value} to /threshold"` test — the body includes `value` but NOT `enabled` (when the checkbox state didn't change).
  - Add three new tests: checkbox state reflects snapshot; toggling-only POSTs `{kind, enabled}`; editing both POSTs `{kind, value, enabled}`.
- **Regenerate** `tests/ui/screenshots/dashboard-1col.png`, `tests/ui/screenshots/dashboard-3col.png`.

### Task 3 — CLI
- **Modify** `cmd/breezy/main.go` — add `case "threshold":` and `case "auto-fan":` in the per-device verb switch.
- **Modify** `cmd/breezy/commands.go` — add `cmdThreshold` and `cmdAutoFan`.
- **Modify** `cmd/breezy/backend.go` — extend the `backend` interface with one method `ThresholdConfig(ctx, name, kind, value *int, enabled *bool)`; implement it on `daemonBackend` (POST `/threshold` with the unified body) and `directBackend` (call `breezy.SetThresholdConfig`).
- **Modify** `cmd/breezy/main_test.go` — add `TestCLI_Threshold_*` and `TestCLI_AutoFan_*` parallel to existing verb tests.

No HomeKit / config / state-storage / metrics changes. The Prometheus gauges for the enable flags already exist (`breezy_*_sensor_enabled`); they continue to be populated by the existing poller.

---

## Task 1: Backend — surface + write the three enable flags

**Goal:** `BuildStatus` exposes `humidity_sensor_enabled` / `co2_sensor_enabled` / `voc_sensor_enabled` in the JSON `configured` block. `SetThresholdConfig(value *int, enabled *bool)` writes 1–2 params atomically. `POST /threshold` body becomes `{kind, value?, enabled?}` with "at least one must be present" validation. Existing UI + CLI keep working without changes.

**Files:**
- Modify: `pkg/breezy/status.go` (Configured population block ~ lines 72–80)
- Modify: `pkg/breezy/ops.go` (`SetThreshold` ~ lines 135–167)
- Modify: `cmd/breezyd/handlers_device.go` (`postThreshold` ~ lines 329–366)
- Modify: `pkg/breezy/status_test.go` (extend `TestBuildStatus_JSONShape` ~ line 195)
- Modify: `pkg/breezy/ops_test.go` (existing `SetThreshold` tests; add new for `SetThresholdConfig`)
- Modify: `cmd/breezyd/server_test.go` (`TestHandler_PostThreshold` ~ line 847, `_BadInputs` ~ line 875)

**Acceptance Criteria:**
- [ ] `BuildStatus` populates `humidity_sensor_enabled` (bool from `0x000F`), `co2_sensor_enabled` (bool from `0x0011`), `voc_sensor_enabled` (bool from `0x0315`) in `Configured`.
- [ ] `SetThresholdConfig(ctx, c, kind, value *int, enabled *bool)` exists with the signature above; returns `ErrInvalidArg` when both pointers are nil; writes 1 or 2 params via a single `WriteParams` call.
- [ ] Validation preserved: humidity 40..80; co2 400..2000 step 10; voc 50..250; unknown kind rejected.
- [ ] `SetThreshold(ctx, c, kind, value)` is a 1-line wrapper that calls `SetThresholdConfig(ctx, c, kind, &value, nil)`.
- [ ] `POST /v1/devices/<name>/threshold` body shape `{kind: "humidity"|"co2"|"voc", value?: int, enabled?: bool}`. Missing both fields returns 400 with `code: "bad_request"`. Missing only value (when enabled is supplied) is valid. Missing only enabled (when value is supplied) is valid.
- [ ] No existing UI test fails (frontend unchanged; the JSON snapshot just gains three new keys ignored by the current dashboard).
- [ ] `just check` passes.
- [ ] `just test-ui` still passes (52 + 3 sensors-block + 3 ENERGY net = 58 baseline expected).

**Verify:** `just check && just test-ui` → both green.

**Steps:**

- [ ] **Step 1: Write the failing tests (TDD red)**

In `pkg/breezy/status_test.go`, add a new test after `TestBuildStatus_JSONShape` (~ line 206):

```go
func TestBuildStatus_SensorEnabledFlags(t *testing.T) {
	values := map[ParamID][]byte{
		0x000F: {1}, // humidity sensor enabled
		0x0011: {0}, // co2 sensor disabled
		0x0315: {1}, // voc sensor enabled
	}
	s := BuildStatus(values, "n", "i", "ip", nil)
	if s.Configured["humidity_sensor_enabled"] != true {
		t.Errorf("humidity_sensor_enabled = %v, want true", s.Configured["humidity_sensor_enabled"])
	}
	if s.Configured["co2_sensor_enabled"] != false {
		t.Errorf("co2_sensor_enabled = %v, want false", s.Configured["co2_sensor_enabled"])
	}
	if s.Configured["voc_sensor_enabled"] != true {
		t.Errorf("voc_sensor_enabled = %v, want true", s.Configured["voc_sensor_enabled"])
	}
}
```

In `pkg/breezy/ops_test.go`, add tests after the existing `SetThreshold` tests (locate via `grep -n 'TestSetThreshold' pkg/breezy/ops_test.go`):

```go
func TestSetThresholdConfig_ValueOnly(t *testing.T) {
	rec := &recordingClient{}
	v := 65
	if err := SetThresholdConfig(context.Background(), rec, "humidity", &v, nil); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rec.writes) != 1 || rec.writes[0].ID != 0x0019 || rec.writes[0].Value[0] != 65 {
		t.Errorf("writes = %+v; want one write to 0x0019 with byte 65", rec.writes)
	}
}

func TestSetThresholdConfig_EnabledOnly(t *testing.T) {
	rec := &recordingClient{}
	enable := false
	if err := SetThresholdConfig(context.Background(), rec, "co2", nil, &enable); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(rec.writes) != 1 || rec.writes[0].ID != 0x0011 || rec.writes[0].Value[0] != 0 {
		t.Errorf("writes = %+v; want one write to 0x0011 with byte 0", rec.writes)
	}
}

func TestSetThresholdConfig_Both(t *testing.T) {
	rec := &recordingClient{}
	v := 200
	enable := true
	if err := SetThresholdConfig(context.Background(), rec, "voc", &v, &enable); err != nil {
		t.Fatalf("err: %v", err)
	}
	// One WriteParams call carrying both writes (atomic from device's POV).
	if len(rec.writes) != 2 {
		t.Fatalf("got %d writes, want 2", len(rec.writes))
	}
	// Order is value-then-enable per implementation; just assert presence by ID.
	idsSeen := map[ParamID]bool{}
	for _, w := range rec.writes {
		idsSeen[w.ID] = true
	}
	if !idsSeen[0x031F] || !idsSeen[0x0315] {
		t.Errorf("writes = %+v; want both 0x031F and 0x0315", rec.writes)
	}
}

func TestSetThresholdConfig_Neither(t *testing.T) {
	rec := &recordingClient{}
	if err := SetThresholdConfig(context.Background(), rec, "humidity", nil, nil); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("err = %v, want ErrInvalidArg", err)
	}
	if len(rec.writes) != 0 {
		t.Errorf("got %d writes after invalid-arg, want 0", len(rec.writes))
	}
}

func TestSetThresholdConfig_OutOfRange(t *testing.T) {
	rec := &recordingClient{}
	v := 90 // humidity max is 80
	if err := SetThresholdConfig(context.Background(), rec, "humidity", &v, nil); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("err = %v, want ErrInvalidArg", err)
	}
}

func TestSetThresholdConfig_UnknownKind(t *testing.T) {
	rec := &recordingClient{}
	v := 50
	if err := SetThresholdConfig(context.Background(), rec, "temperature", &v, nil); !errors.Is(err, ErrInvalidArg) {
		t.Errorf("err = %v, want ErrInvalidArg", err)
	}
}
```

Note: `recordingClient` is the existing test helper in `ops_test.go`. If its name differs, swap accordingly — find via `grep -n 'recordingClient\|fakeClient\|stubClient' pkg/breezy/ops_test.go`.

In `cmd/breezyd/server_test.go`, update `TestHandler_PostThreshold_BadInputs` (~ line 875) — the existing "missing value" case at line 881 must be removed (it's no longer a 400 case if `enabled` is present), and a new "missing both" case must be added. Replace the `cases` block:

```go
for _, c := range []struct {
	name string
	body map[string]any
}{
	{"missing kind", map[string]any{"value": 50}},
	{"missing both value and enabled", map[string]any{"kind": "humidity"}},
	{"unknown kind", map[string]any{"kind": "temperature", "value": 50}},
	{"out of range humidity", map[string]any{"kind": "humidity", "value": 90}},
	{"co2 not multiple of 10", map[string]any{"kind": "co2", "value": 1505}},
	{"out of range voc", map[string]any{"kind": "voc", "value": 300}},
}
```

Add a new test after `TestHandler_PostThreshold_BadInputs` (~ after line 900):

```go
func TestHandler_PostThreshold_EnabledOnly(t *testing.T) {
	for _, c := range []struct {
		kind     string
		enabled  bool
		paramHex string // expected hex byte at the corresponding enable param
		paramID  string
	}{
		{"humidity", true, "01", "0x000F"},
		{"co2", false, "00", "0x0011"},
		{"voc", true, "01", "0x0315"},
	} {
		t.Run(c.kind, func(t *testing.T) {
			h, _, _ := newServerHandler(t)
			rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold",
				map[string]any{"kind": c.kind, "enabled": c.enabled})
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/"+c.paramID, nil)
			var resp map[string]any
			_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
			if resp["hex"] != c.paramHex {
				t.Errorf("%s enable = %v, want %s", c.kind, resp["hex"], c.paramHex)
			}
		})
	}
}

func TestHandler_PostThreshold_ValueAndEnabled(t *testing.T) {
	h, _, _ := newServerHandler(t)
	rec := doRequest(t, h, http.MethodPost, "/v1/devices/playroom/threshold",
		map[string]any{"kind": "humidity", "value": 65, "enabled": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	// Both params should reflect the write.
	rec2 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x0019", nil)
	var resp map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp)
	if resp["hex"] != "41" {
		t.Errorf("humidity threshold hex = %v, want 41", resp["hex"])
	}
	rec3 := doRequest(t, h, http.MethodGet, "/v1/devices/playroom/params/0x000F", nil)
	var resp3 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp3)
	if resp3["hex"] != "00" {
		t.Errorf("humidity enable hex = %v, want 00", resp3["hex"])
	}
}
```

- [ ] **Step 2: Run the new + updated tests; expect FAIL**

```sh
go test ./pkg/breezy -run 'TestBuildStatus_SensorEnabledFlags|TestSetThresholdConfig' -v
go test ./cmd/breezyd -run 'TestHandler_PostThreshold' -v
```

Expected: the new `SensorEnabledFlags`, `SetThresholdConfig_*`, `EnabledOnly`, `ValueAndEnabled` tests **FAIL** (compile error or assertion failure), because `SetThresholdConfig` doesn't exist, the handler doesn't accept `enabled`, and `BuildStatus` doesn't surface enable keys yet. The "BadInputs" test also fails on the new "missing both" naming until the handler is updated.

- [ ] **Step 3: Surface the three enable flags in `BuildStatus`**

In `pkg/breezy/status.go`, immediately after the existing voc-threshold population (the `if v, ok := Uint16At(values, 0x031F); ok { resp.Configured["voc_threshold_index"] = int(v) }` block at ~ line 78–80), add:

```go
	if b, ok := Uint8At(values, 0x000F); ok {
		resp.Configured["humidity_sensor_enabled"] = b == 1
	}
	if b, ok := Uint8At(values, 0x0011); ok {
		resp.Configured["co2_sensor_enabled"] = b == 1
	}
	if b, ok := Uint8At(values, 0x0315); ok {
		resp.Configured["voc_sensor_enabled"] = b == 1
	}
```

- [ ] **Step 4: Add `SetThresholdConfig` and reduce `SetThreshold` to a wrapper**

In `pkg/breezy/ops.go`, replace the existing `SetThreshold` function (~ lines 144–167) with the unified version + a thin wrapper:

```go
// SetThresholdConfig writes one or both of: the per-sensor over-threshold
// setpoint and the per-sensor enable flag (the firmware's "trigger fan
// boost when this sensor is over its threshold"). At least one of value
// and enabled must be non-nil; otherwise ErrInvalidArg with no write.
// Both writes (when supplied) land in a single WriteParams call so the
// device sees them atomically.
//
// Kinds (case-insensitive):
//   - "humidity": value 40..80 RH%; enable flag at 0x000F
//   - "co2":      value 400..2000 ppm step 10; enable flag at 0x0011
//   - "voc":      value 50..250 index; enable flag at 0x0315
//
// Out-of-range values and unknown kinds return ErrInvalidArg with no write.
func SetThresholdConfig(ctx context.Context, c DeviceClient, kind string, value *int, enabled *bool) error {
	if value == nil && enabled == nil {
		return fmt.Errorf("%w: at least one of value or enabled must be supplied", ErrInvalidArg)
	}
	enableByte := func(b bool) byte {
		if b {
			return 1
		}
		return 0
	}
	var writes []ParamWrite
	switch strings.ToLower(kind) {
	case "humidity":
		if value != nil {
			if *value < 40 || *value > 80 {
				return fmt.Errorf("%w: humidity threshold must be 40..80, got %d", ErrInvalidArg, *value)
			}
			writes = append(writes, ParamWrite{ID: 0x0019, Value: []byte{byte(*value)}})
		}
		if enabled != nil {
			writes = append(writes, ParamWrite{ID: 0x000F, Value: []byte{enableByte(*enabled)}})
		}
	case "co2":
		if value != nil {
			if *value < 400 || *value > 2000 {
				return fmt.Errorf("%w: co2 threshold must be 400..2000, got %d", ErrInvalidArg, *value)
			}
			if *value%10 != 0 {
				return fmt.Errorf("%w: co2 threshold must be a multiple of 10, got %d", ErrInvalidArg, *value)
			}
			writes = append(writes, ParamWrite{ID: 0x001A, Value: []byte{byte(*value), byte(*value >> 8)}})
		}
		if enabled != nil {
			writes = append(writes, ParamWrite{ID: 0x0011, Value: []byte{enableByte(*enabled)}})
		}
	case "voc":
		if value != nil {
			if *value < 50 || *value > 250 {
				return fmt.Errorf("%w: voc threshold must be 50..250, got %d", ErrInvalidArg, *value)
			}
			writes = append(writes, ParamWrite{ID: 0x031F, Value: []byte{byte(*value), byte(*value >> 8)}})
		}
		if enabled != nil {
			writes = append(writes, ParamWrite{ID: 0x0315, Value: []byte{enableByte(*enabled)}})
		}
	default:
		return fmt.Errorf("%w: threshold kind must be one of humidity/co2/voc, got %q", ErrInvalidArg, kind)
	}
	return c.WriteParams(ctx, writes)
}

// SetThreshold writes only the per-sensor over-threshold setpoint. Kept
// as a one-line wrapper for callers that don't touch the enable flag.
func SetThreshold(ctx context.Context, c DeviceClient, kind string, value int) error {
	return SetThresholdConfig(ctx, c, kind, &value, nil)
}
```

- [ ] **Step 5: Extend `postThreshold` to accept optional value + optional enabled**

In `cmd/breezyd/handlers_device.go`, replace `postThreshold` (~ lines 329–366) with:

```go
// postThreshold writes one or both of: the per-sensor over-threshold
// setpoint (humidity 0x0019, co2 0x001A, voc 0x031F) and the per-sensor
// enable flag (humidity 0x000F, co2 0x0011, voc 0x0315). Body:
// {"kind":"humidity|co2|voc", "value":N?, "enabled":bool?}. At least one
// of value/enabled must be present. Validation lives in
// breezy.SetThresholdConfig.
func (h *Handler) postThreshold(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		Kind    string `json:"kind"`
		Value   *int   `json:"value"`
		Enabled *bool  `json:"enabled"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.Kind == "" {
		writeErr(w, "bad_request", "missing 'kind' field (humidity|co2|voc)")
		return
	}
	if body.Value == nil && body.Enabled == nil {
		writeErr(w, "bad_request", "must supply at least one of 'value' or 'enabled'")
		return
	}
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.SetThresholdConfig(ctx, rc, body.Kind, body.Value, body.Enabled); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

- [ ] **Step 6: Run the new + updated tests; expect PASS**

```sh
go test ./pkg/breezy ./cmd/breezyd -run 'TestBuildStatus_SensorEnabledFlags|TestSetThresholdConfig|TestSetThreshold|TestHandler_PostThreshold' -v
```

Expected: all green. The existing `TestSetThreshold` passes via the wrapper; the existing `TestHandler_PostThreshold` (value-only) passes; the new `EnabledOnly`, `ValueAndEnabled`, and `_BadInputs` cases pass.

- [ ] **Step 7: Run `just check && just test-ui`**

```sh
just check
just test-ui
```

Expected: both green. UI tests still pass — the new JSON keys are simply ignored by the current frontend until Task 2.

- [ ] **Step 8: Commit Task 1**

```sh
git add pkg/breezy/status.go pkg/breezy/status_test.go pkg/breezy/ops.go pkg/breezy/ops_test.go cmd/breezyd/handlers_device.go cmd/breezyd/server_test.go
git commit -m "$(cat <<'EOF'
breezyd: per-sensor enable flag in snapshot + ops + HTTP (#34, part 1)

Adds humidity_sensor_enabled / co2_sensor_enabled / voc_sensor_enabled
to the JSON Configured block (read from 0x000F / 0x0011 / 0x0315).
Replaces SetThreshold(value) with SetThresholdConfig(value*, enabled*)
that writes 1-2 params atomically; SetThreshold is now a 1-line
wrapper. POST /threshold body becomes {kind, value?, enabled?} with
"at least one must be present" validation. UI + CLI surfaces land in
parts 2 and 3.
EOF
)"
```

---

## Task 2: Frontend — auto-fan checkbox + width fix

**Goal:** Inline threshold editor renders an `auto fan` checkbox alongside the input; checkbox state reflects `configured.<kind>_sensor_enabled` from the snapshot; ✓ commits whichever fields changed (value, enabled, or both); the input is wide enough to hold 4-digit CO₂ values.

**Files:**
- Modify: `cmd/breezyd/ui/index.html` (CSS `.thresh-edit-inline .thresh-input { width }` ~ line 261; CSS for `.thresh-auto-fan` new; `thresholdCell()` markup; `saveThreshold` body; default sensor-enable values used in render)
- Modify: `tests/ui/dashboard.spec.ts` (`baseSnapshot` defaults; existing save test; three new tests)
- Regenerate: `tests/ui/screenshots/dashboard-1col.png`, `tests/ui/screenshots/dashboard-3col.png`

**Acceptance Criteria:**
- [ ] `.thresh-edit-inline .thresh-input` width is `4.5rem`.
- [ ] Inline editor markup contains a `<label class="thresh-auto-fan"><input type="checkbox" class="thresh-auto-fan-input" data-name=… data-kind=… [checked]>auto fan</label>` between the input and the ✓ button.
- [ ] Checkbox `checked` reflects `snap.configured.<kind>_sensor_enabled` (true → checked, false → unchecked, missing → checked = treat as default-on).
- [ ] On ✓ click: build POST body `{kind, value?, enabled?}` containing only fields whose current state differs from the snapshot. Empty change set closes the editor without POSTing.
- [ ] When only the value changed: body is `{kind, value}` (no `enabled`).
- [ ] When only the checkbox changed: body is `{kind, enabled}` (no `value`).
- [ ] When both changed: body is `{kind, value, enabled}`.
- [ ] On ✕ click or Escape: cancels both fields, no POST.
- [ ] `baseSnapshot` in `tests/ui/dashboard.spec.ts` defaults all three `*_sensor_enabled` to `true`.
- [ ] All existing UI tests pass (the value-only test asserts `body == {kind: "humidity", value: 55}` — no `enabled` key when checkbox state didn't change).
- [ ] Three new tests pass (state reflects snapshot; toggle-only POSTs; both-changed POSTs).
- [ ] `just check` passes.
- [ ] `just test-ui` passes.
- [ ] Screenshots regenerated.

**Verify:** `just check && just test-ui` → both green.

**Steps:**

- [ ] **Step 1: Update `baseSnapshot` defaults + add three new tests + update existing save test (TDD red)**

In `tests/ui/dashboard.spec.ts`, find the `configured` block in `baseSnapshot` (~ lines 20–30 — has `humidity_threshold_pct: 60`, etc.). Add three lines:

```typescript
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
```

Now find the existing test `"threshold: save POSTs {kind, value} to /threshold and exits edit mode"` (~ around line 834). Update its assertion so it explicitly asserts the body is `{kind: "humidity", value: 55}` and does NOT have an `enabled` key. Replace the assertion block (the `expect(post!.body).toEqual(...)` line) with:

```typescript
expect(post!.body).toEqual({ kind: "humidity", value: 55 });
expect(post!.body).not.toHaveProperty("enabled");
```

Add three new tests immediately after the existing `"threshold: cancel reverts without POSTing"` test (~ after line 860):

```typescript
test("auto-fan: checkbox state reflects configured.<kind>_sensor_enabled", async ({ page }) => {
  await loadDashboard(page, {
    devices: [{ name: "playroom" }],
    snapshot: (n) => baseSnapshot(n, {
      configured: { humidity_sensor_enabled: false },
    }),
  });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const cb = page.locator('.thresh-auto-fan-input[data-name="playroom"][data-kind="humidity"]');
  await expect(cb).not.toBeChecked();

  // co2 default is true; opening that editor should show a checked checkbox.
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="co2"]');
  const cbCo2 = page.locator('.thresh-auto-fan-input[data-name="playroom"][data-kind="co2"]');
  await expect(cbCo2).toBeChecked();
});

test("auto-fan: toggling-only POSTs {kind, enabled}", async ({ page }) => {
  const { requests } = await loadDashboard(page, { devices: [{ name: "playroom" }] });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  // Default is enabled=true; uncheck.
  await page.locator('.thresh-auto-fan-input[data-name="playroom"][data-kind="humidity"]').uncheck();
  await page.click('button[data-action="threshold-save"][data-name="playroom"][data-kind="humidity"]');
  await page.waitForTimeout(200);
  const post = requests.find((r) => r.method === "POST" && r.url.endsWith("/threshold"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ kind: "humidity", enabled: false });
  expect(post!.body).not.toHaveProperty("value");
});

test("auto-fan: editing both value and checkbox POSTs {kind, value, enabled}", async ({ page }) => {
  const { requests } = await loadDashboard(page, { devices: [{ name: "playroom" }] });
  await page.click('[data-action="edit-threshold"][data-name="playroom"][data-kind="humidity"]');
  const input = page.locator('.thresh-input[data-name="playroom"][data-kind="humidity"]');
  await input.fill("55");
  await page.locator('.thresh-auto-fan-input[data-name="playroom"][data-kind="humidity"]').uncheck();
  await page.click('button[data-action="threshold-save"][data-name="playroom"][data-kind="humidity"]');
  await page.waitForTimeout(200);
  const post = requests.find((r) => r.method === "POST" && r.url.endsWith("/threshold"));
  expect(post).toBeTruthy();
  expect(post!.body).toEqual({ kind: "humidity", value: 55, enabled: false });
});
```

- [ ] **Step 2: Run the three new + updated tests; expect FAIL**

```sh
cd tests/ui && pnpm exec playwright test --grep "threshold|auto-fan"
```

Expected: the new auto-fan tests FAIL (no checkbox in DOM), and the updated value-only test FAILS the `not.toHaveProperty("enabled")` assertion only if the implementation accidentally adds `enabled: true` to the body — actually it should still pass since the current code never emits `enabled`. Document fail vs pass per test in the implementer's report.

- [ ] **Step 3: Bump the input width**

In `cmd/breezyd/ui/index.html`, find `.thresh-edit-inline .thresh-input` (~ line 260). Change `width: 3rem` to `width: 4.5rem`. Other declarations in the rule unchanged.

- [ ] **Step 4: Add `.thresh-auto-fan` CSS**

Adjacent to the `.thresh-edit-inline` rules, add:

```css
  .thresh-auto-fan {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    margin-left: 0.4rem;
    font-size: 0.85rem;
    color: #555;
    cursor: pointer;
    user-select: none;
  }
  .thresh-auto-fan-input { margin: 0; }
  .thresh-auto-fan-input:disabled { cursor: not-allowed; }
```

- [ ] **Step 5: Add the checkbox to the inline editor markup**

Find `thresholdCell` (~ line 528) — specifically the `editing` branch where the inline editor span is constructed. Insert the checkbox between the closing `>` of the input and the ✓ button:

Current (in plan-form pseudo-diff terms, the relevant inline editor template literal):

```js
body = `<span class="thresh-edit-inline">
  <input type="number" min="${cfg.min}" max="${cfg.max}" step="${cfg.step}"
         value="${inputVal}"
         data-name="${esc(name)}" data-kind="${esc(kind)}"
         class="thresh-input" ${dis}>
  <button data-action="threshold-save" data-name="${esc(name)}" data-kind="${esc(kind)}" ${dis}>✓</button>
  <button data-action="threshold-cancel" data-name="${esc(name)}" data-kind="${esc(kind)}" ${dis}>✕</button>
</span>`;
```

Replace with:

```js
const enabledKey = cfg.enabledKey;          // see Step 6 — extend THRESHOLD_KINDS to carry this
const enabledNow = snap.configured?.[enabledKey];
const checkedAttr = (enabledNow === false) ? "" : " checked";  // default to checked when unknown
body = `<span class="thresh-edit-inline">
  <input type="number" min="${cfg.min}" max="${cfg.max}" step="${cfg.step}"
         value="${inputVal}"
         data-name="${esc(name)}" data-kind="${esc(kind)}"
         class="thresh-input" ${dis}>
  <label class="thresh-auto-fan">
    <input type="checkbox" class="thresh-auto-fan-input"
           data-name="${esc(name)}" data-kind="${esc(kind)}"${checkedAttr} ${dis}>
    auto fan
  </label>
  <button data-action="threshold-save" data-name="${esc(name)}" data-kind="${esc(kind)}" ${dis}>✓</button>
  <button data-action="threshold-cancel" data-name="${esc(name)}" data-kind="${esc(kind)}" ${dis}>✕</button>
</span>`;
```

- [ ] **Step 6: Extend `THRESHOLD_KINDS` with `enabledKey`**

Find `THRESHOLD_KINDS` (~ around line 311–319) and add the `enabledKey` field for each kind. Replace the constant:

```js
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
```

- [ ] **Step 7: Update `saveThreshold` to compute the change set**

Find `saveThreshold` (~ around line 1001). Replace the function body so it builds a body containing only fields that changed:

```js
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
  const prevEnabled = snap.configured?.[cfg.enabledKey];
  const enabledNow = cb.checked;
  const body = { kind };
  let dirty = false;
  if (value !== prevValue) { body.value = value; dirty = true; }
  if (enabledNow !== prevEnabled) { body.enabled = enabledNow; dirty = true; }
  if (!dirty) {
    delete editingThreshold[name];
    render();
    return;
  }
  const ok = await postWrite(name, "threshold-" + kind,
    "/v1/devices/" + encodeURIComponent(name) + "/threshold", body);
  if (ok) {
    delete editingThreshold[name];
    render();
  }
}
```

Note: `lastSnapshots` is the existing per-card snapshot cache (declared near the top of the script — see line 294). Do not introduce a new state map.

- [ ] **Step 8: Run the new + updated tests; expect PASS**

```sh
cd tests/ui && pnpm exec playwright test --grep "threshold|auto-fan"
```

Expected: all pass.

- [ ] **Step 9: Run the full UI suite**

```sh
just test-ui
```

Expected: 58 + 3 = 61 passed (or wherever the count lands depending on whether the implementer's metric on prior changes matches; in any case all-green).

- [ ] **Step 10: Run `just check`**

```sh
just check
```

Expected: green.

- [ ] **Step 11: Manually verify in a browser**

Per the project's "Playwright for UI visual checks" memory:

```sh
just build
./breezyd &
# open http://localhost:8080/ in a browser
```

Confirm:
- An RH/eCO₂/VOC value is clickable; the editor opens with `[input(4.5rem)] [☐ auto fan] ✓ ✕`.
- Width is enough that "1500" fully shows.
- Initial checkbox state reflects the firmware's current setting (most users will have all three on).
- Toggling the checkbox + ✓ commits without changing the value.
- Editing the value + ✓ commits without changing the enable.
- Clicking ✕ or pressing Escape cancels both.

Stop the daemon: `kill %1`.

- [ ] **Step 12: Regenerate dashboard screenshots**

```sh
just screenshot
```

Expected: PNGs are rewritten. Default-state cards are unchanged (the editor isn't open by default), so the visual diff is minimal — anti-aliasing only.

- [ ] **Step 13: Commit Task 2**

```sh
git add cmd/breezyd/ui/index.html tests/ui/dashboard.spec.ts tests/ui/screenshots/dashboard-1col.png tests/ui/screenshots/dashboard-3col.png
git commit -m "$(cat <<'EOF'
ui: per-sensor "auto fan" checkbox + 4.5rem inline input (#34, part 2)

Resolves the visible part of #34. The inline threshold editor now
contains an "auto fan" checkbox that mirrors and writes the firmware's
per-sensor enable flag (0x000F / 0x0011 / 0x0315). The input grows
from 3rem to 4.5rem so 4-digit CO2 values fit. ✓ commits whichever
fields changed; an empty change set just closes the editor.
EOF
)"
```

---

## Task 3: CLI — `threshold` and `auto-fan` verbs

**Goal:** Two new per-device CLI verbs that map onto `SetThresholdConfig`. Daemon mode hits `POST /threshold`; standalone mode opens UDP via `pkg/breezy/ops`.

**Files:**
- Modify: `cmd/breezy/main.go` (verb dispatch ~ line 138–169)
- Modify: `cmd/breezy/commands.go` (add `cmdThreshold` and `cmdAutoFan` near the heater/timer family)
- Modify: `cmd/breezy/backend.go` (add interface method + two impls, parallel to the existing `Heater` shape)
- Modify: `cmd/breezy/main_test.go` (add `TestCLI_Threshold_*` and `TestCLI_AutoFan_*`)

**Acceptance Criteria:**
- [ ] `breezy <device> threshold humidity 65` succeeds; underlying call writes only `0x0019`.
- [ ] `breezy <device> threshold co2 1500` succeeds; writes only `0x001A`.
- [ ] `breezy <device> threshold voc 200` succeeds; writes only `0x031F`.
- [ ] `breezy <device> threshold humidity 90` exits with code 1 and prints a validation error.
- [ ] `breezy <device> auto-fan humidity off` succeeds; writes only `0x000F` with byte 0.
- [ ] `breezy <device> auto-fan co2 on` succeeds; writes only `0x0011` with byte 1.
- [ ] `breezy <device> auto-fan voc on` succeeds; writes only `0x0315` with byte 1.
- [ ] `breezy <device> auto-fan humidity yes` exits with code 2 (usage error, before any I/O).
- [ ] `breezy <device> threshold` (no args) exits with code 2 (usage error).
- [ ] Both verbs work in daemon mode and standalone mode.
- [ ] `just check` passes.
- [ ] `just test-ui` still passes (frontend untouched in this task).

**Verify:** `just check && go test ./cmd/breezy -run 'TestCLI_Threshold|TestCLI_AutoFan' -v` → both green.

**Steps:**

- [ ] **Step 1: Add the failing CLI tests (TDD red)**

In `cmd/breezy/main_test.go`, add tests (placement: after the existing per-verb test family — find via `grep -n 'TestCLI_Heater\|TestCLI_Timer' cmd/breezy/main_test.go` and add nearby):

```go
func TestCLI_Threshold(t *testing.T) {
	for _, c := range []struct {
		kind  string
		value string
		hex   string
		id    breezy.ParamID
	}{
		{"humidity", "65", "41", 0x0019},
		{"co2", "1500", "dc05", 0x001A},
		{"voc", "200", "c800", 0x031F},
	} {
		t.Run(c.kind, func(t *testing.T) {
			srv := startFakeDevice(t)
			code, _, stderr := runCLI(t, srv, "playroom", "threshold", c.kind, c.value)
			if code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			got := srv.LastWriteHex(c.id)
			if got != c.hex {
				t.Errorf("hex at 0x%04X = %q, want %q", c.id, got, c.hex)
			}
		})
	}
}

func TestCLI_Threshold_Usage(t *testing.T) {
	srv := startFakeDevice(t)
	code, _, _ := runCLI(t, srv, "playroom", "threshold")
	if code != 2 {
		t.Errorf("code = %d, want 2 (usage error)", code)
	}
}

func TestCLI_Threshold_OutOfRange(t *testing.T) {
	srv := startFakeDevice(t)
	code, _, _ := runCLI(t, srv, "playroom", "threshold", "humidity", "90")
	if code != 1 {
		t.Errorf("code = %d, want 1 (validation rejected by daemon/ops)", code)
	}
}

func TestCLI_AutoFan(t *testing.T) {
	for _, c := range []struct {
		kind  string
		state string
		hex   string
		id    breezy.ParamID
	}{
		{"humidity", "on", "01", 0x000F},
		{"humidity", "off", "00", 0x000F},
		{"co2", "on", "01", 0x0011},
		{"voc", "off", "00", 0x0315},
	} {
		t.Run(c.kind+"_"+c.state, func(t *testing.T) {
			srv := startFakeDevice(t)
			code, _, stderr := runCLI(t, srv, "playroom", "auto-fan", c.kind, c.state)
			if code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			got := srv.LastWriteHex(c.id)
			if got != c.hex {
				t.Errorf("hex at 0x%04X = %q, want %q", c.id, got, c.hex)
			}
		})
	}
}

func TestCLI_AutoFan_BadState(t *testing.T) {
	srv := startFakeDevice(t)
	code, _, _ := runCLI(t, srv, "playroom", "auto-fan", "humidity", "yes")
	if code != 2 {
		t.Errorf("code = %d, want 2 (usage error)", code)
	}
}
```

If the test helpers `startFakeDevice`, `runCLI`, and `LastWriteHex` have different names in this codebase, locate the equivalents via `grep -n 'func runCLI\|func startFake\|LastWriteHex' cmd/breezy/main_test.go` and adjust accordingly.

- [ ] **Step 2: Run the new tests; expect FAIL**

```sh
go test ./cmd/breezy -run 'TestCLI_Threshold|TestCLI_AutoFan' -v
```

Expected: all FAIL (compile or "unknown verb"), because the verbs don't exist yet.

- [ ] **Step 3: Add the backend interface method**

In `cmd/breezy/backend.go`, add to the `backend` interface (~ inside the `// Write-style operations.` block, around line 51):

```go
	// ThresholdConfig writes one or both of: the per-sensor over-threshold
	// setpoint and the per-sensor enable flag. At least one of value and
	// enabled must be non-nil; the daemon and direct backends route to
	// breezy.SetThresholdConfig with the same constraints.
	ThresholdConfig(ctx context.Context, name string, kind string, value *int, enabled *bool) error
```

Add the daemon impl (parallel to existing `Heater` ~ line 184):

```go
func (d *daemonBackend) ThresholdConfig(ctx context.Context, name string, kind string, value *int, enabled *bool) error {
	body := map[string]any{"kind": kind}
	if value != nil {
		body["value"] = *value
	}
	if enabled != nil {
		body["enabled"] = *enabled
	}
	status, raw, err := d.httpJSON(ctx, http.MethodPost, "/v1/devices/"+name+"/threshold", body)
	if err != nil || status >= 400 {
		return envelopeErr(status, raw, err)
	}
	return nil
}
```

Add the direct impl (parallel to existing `Heater` ~ line 438):

```go
func (d *directBackend) ThresholdConfig(ctx context.Context, name string, kind string, value *int, enabled *bool) error {
	c, err := d.dial(name)
	if err != nil {
		return err
	}
	return breezy.SetThresholdConfig(ctx, c, kind, value, enabled)
}
```

- [ ] **Step 4: Add `cmdThreshold` and `cmdAutoFan`**

In `cmd/breezy/commands.go`, add near `cmdHeater` (~ line 137) and `cmdTimer`:

```go
// validThresholdKinds mirrors the daemon's accepted set so a typo doesn't
// waste a round-trip and produce a vaguer error.
var validThresholdKinds = map[string]bool{
	"humidity": true,
	"co2":      true,
	"voc":      true,
}

func cmdThreshold(b backend, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "usage: breezy <name> threshold <humidity|co2|voc> <value>")
		return 2
	}
	kind := strings.ToLower(args[0])
	if !validThresholdKinds[kind] {
		_, _ = fmt.Fprintf(stderr, "threshold: %q is not one of: humidity, co2, voc\n", args[0])
		return 2
	}
	value, err := strconv.Atoi(args[1])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "threshold: value must be an integer, got %q\n", args[1])
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.ThresholdConfig(ctx, name, kind, &value, nil); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return 0
}

func cmdAutoFan(b backend, name string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "usage: breezy <name> auto-fan <humidity|co2|voc> <on|off>")
		return 2
	}
	kind := strings.ToLower(args[0])
	if !validThresholdKinds[kind] {
		_, _ = fmt.Fprintf(stderr, "auto-fan: %q is not one of: humidity, co2, voc\n", args[0])
		return 2
	}
	var enabled bool
	switch strings.ToLower(args[1]) {
	case "on":
		enabled = true
	case "off":
		enabled = false
	default:
		_, _ = fmt.Fprintf(stderr, "auto-fan: state must be on or off, got %q\n", args[1])
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.ThresholdConfig(ctx, name, kind, nil, &enabled); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "ok")
	return 0
}
```

If `strconv` is not already imported in `commands.go`, add it to the import block.

- [ ] **Step 5: Wire the verbs into the dispatch switch**

In `cmd/breezy/main.go`, in the per-device verb switch (~ line 138–169), add two new cases between `case "set":` (line 167) and the closing brace (line 169):

```go
	case "threshold":
		return cmdThreshold(b, name, vargs, stdout, stderr)
	case "auto-fan":
		return cmdAutoFan(b, name, vargs, stdout, stderr)
```

- [ ] **Step 6: Update CLI help text**

Find the `usage` constant (search via `grep -n 'const usage\|usage =' cmd/breezy/main.go`). Add two lines to the per-device verbs section, immediately above the `get`/`set` lines:

```
  threshold KIND VAL    set sensor threshold (KIND=humidity|co2|voc)
  auto-fan KIND on|off  toggle sensor's "trigger fan boost" flag
```

Match the existing indentation and column alignment.

- [ ] **Step 7: Run the new tests; expect PASS**

```sh
go test ./cmd/breezy -run 'TestCLI_Threshold|TestCLI_AutoFan' -v
```

Expected: all green.

- [ ] **Step 8: Run `just check`**

```sh
just check
```

Expected: green.

- [ ] **Step 9: Run `just test-ui` to confirm no UI regression**

```sh
just test-ui
```

Expected: still 61 passed (Task 3 doesn't touch the dashboard).

- [ ] **Step 10: Manually verify CLI**

```sh
just build
./breezy --help | grep -E "threshold|auto-fan"
# confirm two new verbs listed

# Daemon mode (assuming breezyd is running):
./breezy playroom threshold humidity 60
./breezy playroom auto-fan humidity on

# Standalone mode (no daemon URL):
BREEZY_DAEMON= ./breezy playroom threshold co2 1500
BREEZY_DAEMON= ./breezy playroom auto-fan co2 off
```

If standalone mode requires a config flag instead of an env var, use whatever the existing pattern is. The point is to exercise both code paths once.

- [ ] **Step 11: Commit Task 3**

```sh
git add cmd/breezy/main.go cmd/breezy/commands.go cmd/breezy/backend.go cmd/breezy/main_test.go
git commit -m "$(cat <<'EOF'
breezy: threshold + auto-fan CLI verbs (#34, part 3)

Adds two per-device verbs:
  breezy <device> threshold <kind> <value>
  breezy <device> auto-fan <kind> on|off
Both route through the new backend.ThresholdConfig method which maps
to breezy.SetThresholdConfig in standalone mode and POST /threshold
in daemon mode. <kind> is humidity / co2 / voc.
EOF
)"
```

---

## Self-Review

- **Spec coverage:**
  - JSON snapshot exposes 3 enable keys — Task 1 Step 3. ✓
  - `SetThresholdConfig(value*, enabled*)` — Task 1 Step 4. ✓
  - `SetThreshold` wrapper preserved — Task 1 Step 4. ✓
  - HTTP body `{kind, value?, enabled?}` with "at least one required" — Task 1 Step 5. ✓
  - Inline editor checkbox + width fix — Task 2 Steps 3, 4, 5, 6, 7. ✓
  - Dashboard sends only changed fields — Task 2 Step 7 (`saveThreshold` change-set logic). ✓
  - `breezy <device> threshold <kind> <value>` — Task 3 Step 4 (`cmdThreshold`). ✓
  - `breezy <device> auto-fan <kind> on|off` — Task 3 Step 4 (`cmdAutoFan`). ✓
  - Daemon + standalone CLI paths — Task 3 Step 3 (both backend impls). ✓
  - Three new Playwright tests — Task 2 Step 1. ✓
  - CLI tests in `main_test.go` — Task 3 Step 1. ✓
  - No CHANGELOG / version bump (out of scope). ✓
  - No status-line render of thresholds or auto-fan (out of scope). ✓
  - No alert-color suppression code (firmware handles naturally — explicit in spec, no plan task needed). ✓

- **Placeholder scan:** every step has actual code or actual commands; "find via grep" appears twice (Task 1 Step 1 for `recordingClient`; Task 3 Step 1 for test helpers) — these are deliberate offers to the implementer in case the helper name has drifted, and the grep command is supplied. No "TBD"/"add validation"/"similar to Task N".

- **Type / selector consistency:**
  - Function name `SetThresholdConfig` — used identically in Task 1 Steps 1, 4, 5; Task 3 Step 3 (`directBackend.ThresholdConfig` calls it).
  - Backend interface method name `ThresholdConfig` — used in Task 3 Steps 3, 4, and tests reference it indirectly.
  - JSON keys `humidity_sensor_enabled`, `co2_sensor_enabled`, `voc_sensor_enabled` — consistent across Task 1 Steps 1, 3; Task 2 Steps 1, 6, 7; spec.
  - Param IDs `0x000F` (humidity), `0x0011` (co2), `0x0315` (voc) — consistent across all places (Task 1 Step 4 ops, Task 1 Step 1 status test, Task 3 Step 1 CLI test).
  - DOM class `thresh-auto-fan-input` and selectors `[data-name="…"][data-kind="…"]` — consistent across Task 2 Steps 4, 5, 7, and the new Playwright tests in Step 1.
  - `THRESHOLD_KINDS[…].enabledKey` field — declared in Task 2 Step 6, consumed in Task 2 Step 7's `saveThreshold` and the cell-renderer markup in Step 5.
  - The HTTP body shape `{kind, value?, enabled?}` — consistent across Task 1 Step 5 (handler), Task 2 Step 7 (frontend builder), Task 3 Step 3 (daemon backend serializer), Task 1 Step 1 (handler tests), Task 2 Step 1 (Playwright tests), Task 3 Step 1 (CLI tests via the daemon path).
