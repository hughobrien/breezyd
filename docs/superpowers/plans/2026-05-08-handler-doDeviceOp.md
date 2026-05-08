# Handler doDeviceOp Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract the `dialRecording → defer unlock → defer raw.Close → context.WithTimeout → defer cancel → op` scaffolding repeated across 21 device-dialing HTTP handlers into one helper (`doDeviceOp`) plus a read-only sibling (`doDeviceRead`).

**Architecture:** Pure refactor — no wire-API change, no new packages, no third-party dependencies. Two new method receivers on `*Handler` in `cmd/breezyd/server.go`; each migrated handler shrinks by 6–8 lines. Existing handler tests act as the migration safety net; two unit tests cover the helpers themselves.

**Tech Stack:** Go 1.26, existing breezyd package layout. No new tooling.

**Spec:** `docs/superpowers/specs/2026-05-08-handler-doDeviceOp-design.md`.

---

## File Structure

**Modified:**
- `cmd/breezyd/server.go` — add `doDeviceOp` and `doDeviceRead` method receivers near `dial` / `dialRecording`. ~30 LOC added.
- `cmd/breezyd/server_test.go` — add `TestDoDeviceOp_ReleasesLockOnSuccess`, `TestDoDeviceOp_ReleasesLockOnError`, `TestDoDeviceRead_ReleasesLockOnError`. ~80 LOC added.
- `cmd/breezyd/handlers_device.go` — migrate 10 handlers. ~70 LOC removed.
- `cmd/breezyd/handlers_service.go` — migrate 2 handlers. ~14 LOC removed.
- `cmd/breezyd/handlers_ui_write.go` — migrate 9 handlers. ~63 LOC removed.

**Net:** ≈ 140 LOC removed from handlers, ≈ 110 LOC added (helpers + tests).

---

## Task 1: Add `doDeviceOp` + `doDeviceRead` helpers

**Goal:** Two method receivers on `*Handler` that consolidate the dial+ctx+op scaffolding, plus three lock-release tests proving they don't leak the per-device UDP mutex on success or error.

**Files:**
- Modify: `cmd/breezyd/server.go`
- Modify: `cmd/breezyd/server_test.go`

**Acceptance Criteria:**
- [ ] `(h *Handler).doDeviceOp(r, name, op)` returns the dial error or the op error verbatim.
- [ ] `(h *Handler).doDeviceRead(r, name, op)` analogous, using `h.dial` (no recording wrapper).
- [ ] Both helpers release the per-device UDP mutex whether op returns nil or an error. Verified by holding the mutex from a second goroutine after the helper returns.
- [ ] `just check` clean, including `go test -race ./cmd/breezyd/`.

**Verify:** `go test ./cmd/breezyd/ -run 'TestDoDeviceOp|TestDoDeviceRead' -v -race`

**Steps:**

- [ ] **Step 1: Write the failing tests in `cmd/breezyd/server_test.go`**

Add these tests at the end of the file. They lean on the existing `newServerHandler(t)` and `newServerFakeDevice(t)` helpers already present in `server_test.go`.

```go
func TestDoDeviceOp_ReleasesLockOnSuccess(t *testing.T) {
	h := newServerHandler(t)
	// newServerHandler doesn't wire a Poller; install a real one so
	// LockUDP returns a real mutex (the no-op fallback in lockDevice
	// would mask a leak).
	h.Pollers = map[string]*Poller{"playroom": {Name: "playroom"}}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	called := false
	err := h.doDeviceOp(req, "playroom", func(ctx context.Context, rc *recordingClient) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("doDeviceOp: %v", err)
	}
	if !called {
		t.Error("op was not invoked")
	}
	// Lock must be released — taking it again must not block.
	done := make(chan struct{})
	go func() {
		unlock := h.Pollers["playroom"].LockUDP()
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lock not released after successful op")
	}
}

func TestDoDeviceOp_ReleasesLockOnError(t *testing.T) {
	h := newServerHandler(t)
	h.Pollers = map[string]*Poller{"playroom": {Name: "playroom"}}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	want := errors.New("op exploded")
	got := h.doDeviceOp(req, "playroom", func(ctx context.Context, rc *recordingClient) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Fatalf("doDeviceOp err: got %v, want %v", got, want)
	}
	done := make(chan struct{})
	go func() {
		unlock := h.Pollers["playroom"].LockUDP()
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lock not released after errored op")
	}
}

func TestDoDeviceRead_ReleasesLockOnError(t *testing.T) {
	h := newServerHandler(t)
	h.Pollers = map[string]*Poller{"playroom": {Name: "playroom"}}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	want := errors.New("read failed")
	got := h.doDeviceRead(req, "playroom", func(ctx context.Context, c HandlerClient) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Fatalf("doDeviceRead err: got %v, want %v", got, want)
	}
	done := make(chan struct{})
	go func() {
		unlock := h.Pollers["playroom"].LockUDP()
		unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("lock not released after errored read")
	}
}
```

If the existing `server_test.go` doesn't already import `errors`, `httptest`, or `time`, add them. Confirm `newServerHandler` exists; if its name differs in the current tree, mirror whichever helper the existing `TestServer_postPower` test uses.

- [ ] **Step 2: Run tests, verify they fail to compile**

```bash
go test ./cmd/breezyd/ -run 'TestDoDeviceOp|TestDoDeviceRead' -v
```

Expected: compile error — `doDeviceOp`, `doDeviceRead` undefined.

- [ ] **Step 3: Implement the helpers in `cmd/breezyd/server.go`**

Insert these two methods immediately after `dialRecording` (around line 376):

```go
// doDeviceOp acquires the per-device UDP lock, opens a recording
// client, runs op with a 5s timeout derived from r.Context(), and
// tears everything down (Close before unlock; LIFO defer order)
// before returning. Caller has already validated the device exists
// and any input fields, and is responsible for translating the
// returned error and emitting any success body.
//
// Returns nil on success, the dial error if the client could not be
// opened, or the op's error verbatim — including ctx.DeadlineExceeded
// when the 5s budget elapsed.
func (h *Handler) doDeviceOp(
	r *http.Request,
	name string,
	op func(ctx context.Context, rc *recordingClient) error,
) error {
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		return err
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	return op(ctx, rc)
}

// doDeviceRead is the read-only sibling used by getParam. Same shape
// as doDeviceOp but goes through h.dial (no recording wrapper) since
// reads have no writes to record.
func (h *Handler) doDeviceRead(
	r *http.Request,
	name string,
	op func(ctx context.Context, c HandlerClient) error,
) error {
	c, unlock, err := h.dial(name)
	if err != nil {
		return err
	}
	defer unlock()
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	return op(ctx, c)
}
```

Confirm `time` is already imported by `server.go` (it is — see line 14). No new imports needed.

- [ ] **Step 4: Run tests, verify they pass**

```bash
go test ./cmd/breezyd/ -run 'TestDoDeviceOp|TestDoDeviceRead' -v -race
```

Expected: three PASS lines.

- [ ] **Step 5: Run the full check**

```bash
just check
```

Clean. The helpers exist but no handler uses them yet — production behavior is unchanged.

- [ ] **Step 6: Commit**

```bash
git add cmd/breezyd/server.go cmd/breezyd/server_test.go
git commit -m "refactor: add doDeviceOp + doDeviceRead handler helpers"
```

```json:metadata
{"files": ["cmd/breezyd/server.go", "cmd/breezyd/server_test.go"], "verifyCommand": "go test ./cmd/breezyd/ -run 'TestDoDeviceOp|TestDoDeviceRead' -v -race && just check", "acceptanceCriteria": ["doDeviceOp returns dial err or op err", "doDeviceRead analogous with h.dial", "lock released on success and on error", "just check + race clean"]}
```

---

## Task 2: Migrate `/v1/*` JSON handlers

**Goal:** Migrate every device-dialing handler in `handlers_device.go` and `handlers_service.go` to use the new helpers. JSON envelope responses (`writeJSON` / `writeErr`) are unchanged.

**Files:**
- Modify: `cmd/breezyd/handlers_device.go`
- Modify: `cmd/breezyd/handlers_service.go`

**Acceptance Criteria:**
- [ ] All 12 handlers below use `doDeviceOp` (or `doDeviceRead` for `getParam`):
  - `getParam`, `postParam`, `postPower`, `postSpeed`, `postPreset`, `postMode`, `postHeater`, `postThreshold`, `postTimer`, `postRTC` (handlers_device.go)
  - `postFilterReset`, `postFaultsReset` (handlers_service.go)
- [ ] No handler still calls `h.dialRecording(name)` directly (grep returns zero hits in these two files).
- [ ] No handler still calls `context.WithTimeout(r.Context(), 5*time.Second)` directly in these two files.
- [ ] Existing tests pass with no assertion changes.
- [ ] `just check` and `go test -race ./cmd/breezyd/` clean.

**Verify:**
```bash
just check
go test -race ./cmd/breezyd/
grep -n 'h\.dialRecording\|context\.WithTimeout' cmd/breezyd/handlers_device.go cmd/breezyd/handlers_service.go
```
Last command output should be empty (or only show comments — verify by inspection).

**Steps:**

- [ ] **Step 1: Migrate `postPower` as the canonical example**

Find `postPower` in `cmd/breezyd/handlers_device.go` (around line 163). Replace the dial→ctx→op block.

Before:
```go
func (h *Handler) postPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.On == nil {
		writeErr(w, "bad_request", "missing 'on' field (true/false)")
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
	if err := breezy.Power(ctx, rc, *body.On); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

After:
```go
func (h *Handler) postPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.requireDevice(w, name); !ok {
		return
	}
	var body struct {
		On *bool `json:"on"`
	}
	if !readBody(w, r, &body) {
		return
	}
	if body.On == nil {
		writeErr(w, "bad_request", "missing 'on' field (true/false)")
		return
	}
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.Power(ctx, rc, *body.On)
	}); err != nil {
		writeErr(w, classifyClientErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

Run `go test ./cmd/breezyd/ -run TestServer_postPower -v` (or whichever test name matches). Should pass.

- [ ] **Step 2: Apply the same transformation to the other `handlers_device.go` writes**

Each of these handlers has the same shape — the `breezy.X(ctx, rc, ...)` call stays inside the closure; everything around it goes:

- `postParam` (line ~118) — note: writes raw bytes via `rc.WriteParams(ctx, []breezy.ParamWrite{{ID: id, Value: val}})`, not a `breezy.X` op.
- `postSpeed` (line ~199) — closure calls `breezy.SetSpeedPreset` or `breezy.SetSpeedManual`.
- `postPreset` (line ~241) — closure calls `breezy.SetPresetSpeed`.
- `postMode` (line ~274) — closure calls `breezy.SetMode`.
- `postHeater` (line ~301) — closure calls `breezy.SetHeater`.
- `postThreshold` (line ~338) — closure calls `breezy.SetThresholdConfig`.
- `postTimer` (line ~378) — closure calls `breezy.SetTimer`.
- `postRTC` (line ~412) — closure calls whatever the existing op is (likely `breezy.SetRTC` or similar; check the file).

For each, the pattern is: replace lines from `rc, raw, unlock, err := h.dialRecording(name)` through `defer cancel()` + the inner `if err := breezy.X(ctx, rc, ...)` block with a single `if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error { return breezy.X(ctx, rc, ...) }); err != nil { writeErr(...); return }`. Leave validation (the input checks) and the trailing `writeJSON(w, http.StatusOK, ...)` untouched.

- [ ] **Step 3: Migrate `getParam` using `doDeviceRead`**

`getParam` (line ~59) uses `h.dial`, not `h.dialRecording`. Use `doDeviceRead`.

Before:
```go
client, unlock, err := h.dial(name)
if err != nil {
	writeErr(w, classifyClientErr(err), err.Error())
	return
}
defer unlock()
defer func() { _ = client.Close() }()

ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
defer cancel()

out, err := client.ReadParams(ctx, []breezy.ParamID{id})
if err != nil {
	writeErr(w, classifyClientErr(err), err.Error())
	return
}
```

After:
```go
var out map[breezy.ParamID][]byte
if err := h.doDeviceRead(r, name, func(ctx context.Context, c HandlerClient) error {
	var rerr error
	out, rerr = c.ReadParams(ctx, []breezy.ParamID{id})
	return rerr
}); err != nil {
	writeErr(w, classifyClientErr(err), err.Error())
	return
}
```

- [ ] **Step 4: Migrate `handlers_service.go` writes**

Two handlers, same shape:
- `postFilterReset` (line ~93) — closure calls `breezy.ResetFilter`.
- `postFaultsReset` (line ~114) — closure calls `breezy.ResetFaults`.

Apply the postPower transformation. Both have empty form bodies, so there's no `readBody` call to preserve.

- [ ] **Step 5: Drop unused imports**

`handlers_device.go` and `handlers_service.go` may have imported `context` and `time` only for the inline timeouts. After migration, run `goimports` or just `go build ./...` and let Go report unused imports.

```bash
go build ./...
```

If complaints surface, remove the unused imports and re-run. (`context.Context` is still in scope inside closure parameters, so the `context` import likely stays. `time` was only used for the 5-second constant and may now be unused.)

- [ ] **Step 6: Run the existing handler tests**

```bash
go test ./cmd/breezyd/ -v -run 'TestServer|TestPostFilterReset|TestPostFaultsReset' 2>&1 | tail -40
```

All pass; no assertion changes were needed because the wire shape is unchanged.

- [ ] **Step 7: Race + full check**

```bash
go test -race ./cmd/breezyd/
just check
```

Both clean.

- [ ] **Step 8: Verify the negative grep**

```bash
grep -n 'h\.dialRecording\|context\.WithTimeout(r\.Context()' \
  cmd/breezyd/handlers_device.go cmd/breezyd/handlers_service.go
```

Should return zero hits. If anything appears, it's an un-migrated handler (or a closure that legitimately needs its own context — none should at this layer).

- [ ] **Step 9: Commit**

```bash
git add cmd/breezyd/handlers_device.go cmd/breezyd/handlers_service.go
git commit -m "refactor: migrate /v1/* handlers to doDeviceOp"
```

```json:metadata
{"files": ["cmd/breezyd/handlers_device.go", "cmd/breezyd/handlers_service.go"], "verifyCommand": "just check && go test -race ./cmd/breezyd/", "acceptanceCriteria": ["12 handlers migrated", "no h.dialRecording in these files", "no inline context.WithTimeout in these files", "existing tests pass unchanged"]}
```

---

## Task 3: Migrate `/ui/*` SSE handlers

**Goal:** Migrate every device-dialing handler in `handlers_ui_write.go` to use `doDeviceOp`. SSE response shapes (`notifyAfterWrite + WriteHeader`, `errorBannerSSE` for errors, `patchThresholdCellSSE` for the threshold PUT) are unchanged.

**Files:**
- Modify: `cmd/breezyd/handlers_ui_write.go`

**Acceptance Criteria:**
- [ ] All 9 handlers below use `doDeviceOp`:
  - `postUIPower`, `postUIMode`, `postUISpeed`, `postUIPreset`, `postUIHeater`, `postUITimer`, `postUIResetFilter`, `postUIResetFaults`, `putUIThreshold`.
- [ ] No handler still calls `h.dialRecording(name)` directly (grep returns zero hits in `handlers_ui_write.go`).
- [ ] Existing handler tests pass with no assertion changes (the fakePushHub-based tests added during the datastar migration still verify Notify is called per write).
- [ ] `just check` and `go test -race ./cmd/breezyd/` clean.

**Verify:**
```bash
just check
go test -race ./cmd/breezyd/
grep -n 'h\.dialRecording\|context\.WithTimeout(r\.Context()' cmd/breezyd/handlers_ui_write.go
```

Last grep output should be empty.

**Steps:**

- [ ] **Step 1: Migrate `postUIPower` as the canonical /ui/* example**

Find `postUIPower` (line ~542 in the current file). Apply the transformation; the only difference from `/v1/*` is the error and success paths.

Before:
```go
func (h *Handler) postUIPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	onStr := r.FormValue("on")
	if onStr != "true" && onStr != "false" {
		h.uiValidationError(w, r, name, "missing or invalid 'on' field (true/false)")
		return
	}
	on := onStr == "true"
	rc, raw, unlock, err := h.dialRecording(name)
	if err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	defer unlock()
	defer func() { _ = raw.Close() }()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := breezy.Power(ctx, rc, on); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}
```

After:
```go
func (h *Handler) postUIPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := h.Devices.Get(name); !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	onStr := r.FormValue("on")
	if onStr != "true" && onStr != "false" {
		h.uiValidationError(w, r, name, "missing or invalid 'on' field (true/false)")
		return
	}
	on := onStr == "true"
	if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
		return breezy.Power(ctx, rc, on)
	}); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.notifyAfterWrite(name)
	w.WriteHeader(http.StatusOK)
}
```

Run the existing `TestUIWritePower_Happy` and `TestUIWritePower_BadForm` etc. — they should all still pass.

- [ ] **Step 2: Apply the same transformation to the other postUI* handlers**

Each follows the postUIPower template; the only differences are validation logic and the `breezy.X` call inside the closure.

- `postUIMode` (line ~273) — closure calls `breezy.SetMode(ctx, rc, mode)`.
- `postUIPreset` (line ~312) — closure calls `breezy.SetPresetSpeed(ctx, rc, preset, supply, extract)`.
- `postUISpeed` (line ~358) — closure calls `breezy.SetSpeedPreset` or `breezy.SetSpeedManual` (the same XOR-validate logic stays in the handler body).
- `postUIHeater` (line ~414) — closure calls `breezy.SetHeater`.
- `postUITimer` (line ~455) — closure calls `breezy.SetTimer`.
- `postUIResetFilter` (line ~492) — closure calls `breezy.ResetFilter`. No form body.
- `postUIResetFaults` (line ~516) — closure calls `breezy.ResetFaults`. No form body.

For each: drop the `dialRecording → defers → context.WithTimeout` block; wrap the `breezy.X` call in `doDeviceOp`; keep the trailing `h.notifyAfterWrite(name); w.WriteHeader(http.StatusOK)`.

- [ ] **Step 3: Migrate `putUIThreshold` (different success path)**

`putUIThreshold` (line ~665) returns the read-variant SSE patch on success via `h.patchThresholdCellSSE` rather than 200 + empty body. The closure stays the same; only the trailing success line differs.

Before:
```go
rc, raw, unlock, err := h.dialRecording(name)
if err != nil {
	h.uiWriteError(w, r, err)
	return
}
defer unlock()
defer func() { _ = raw.Close() }()
ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
defer cancel()
if err := breezy.SetThresholdConfig(ctx, rc, kind, valuePtr, enabledPtr); err != nil {
	h.uiWriteError(w, r, err)
	return
}
h.notifyAfterWrite(name)
h.patchThresholdCellSSE(w, r, name, kind, false)
```

After:
```go
if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
	return breezy.SetThresholdConfig(ctx, rc, kind, valuePtr, enabledPtr)
}); err != nil {
	h.uiWriteError(w, r, err)
	return
}
h.notifyAfterWrite(name)
h.patchThresholdCellSSE(w, r, name, kind, false)
```

- [ ] **Step 4: Drop unused imports**

```bash
go build ./...
```

`time` is likely no longer needed in `handlers_ui_write.go` (the inline `5*time.Second` is gone). Remove it from the import list if Go complains. `context` stays — closures still take a `context.Context` parameter.

- [ ] **Step 5: Run the existing handler tests**

```bash
go test ./cmd/breezyd/ -run 'TestUIWrite|TestPostUI|TestUIThreshold' -v 2>&1 | tail -40
```

All pass. The `attachFakePushHub` helper added during the datastar migration verifies `notifyAfterWrite` still fires; no new tests needed.

- [ ] **Step 6: Race + full check**

```bash
go test -race ./cmd/breezyd/
just check
```

Both clean.

- [ ] **Step 7: Verify the negative grep**

```bash
grep -n 'h\.dialRecording\|context\.WithTimeout(r\.Context()' cmd/breezyd/handlers_ui_write.go
```

Empty output.

- [ ] **Step 8: Commit**

```bash
git add cmd/breezyd/handlers_ui_write.go
git commit -m "refactor: migrate /ui/* handlers to doDeviceOp"
```

```json:metadata
{"files": ["cmd/breezyd/handlers_ui_write.go"], "verifyCommand": "just check && go test -race ./cmd/breezyd/", "acceptanceCriteria": ["9 handlers migrated", "no h.dialRecording in handlers_ui_write.go", "no inline context.WithTimeout in handlers_ui_write.go", "existing tests pass unchanged including fakePushHub assertions"]}
```

---

## Self-Review Notes

- **Spec coverage:** ✓ helpers (Task 1), `/v1/*` migration (Task 2), `/ui/*` migration (Task 3). All 21 handlers from the spec's scope table land in one of the three tasks.
- **Type / symbol consistency:** `doDeviceOp` and `doDeviceRead` named consistently across tasks. `*recordingClient` and `HandlerClient` parameter types match the existing `dial` / `dialRecording` returns.
- **Test posture:** Task 1 covers the helpers' lock-release behavior directly. Tasks 2 and 3 lean on the existing handler test suite (race-tested in CI) as the migration safety net — no per-handler test churn, which is the point of a pure refactor.
- **Out-of-scope reaffirmed:** `putUISchedule` is NOT in any task because it doesn't dial UDP. The threshold/schedule GET fragment endpoints aren't either.
- **Failure modes:** if a handler is missed, the negative grep in each task's verify step catches it before commit. If `time` cannot be removed because the file still uses it for something else, leave it; the goal is the helpers, not import minimalism.
