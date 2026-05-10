# Simplification pass round 2 — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land six self-contained simplification commits on `chore/simplification-pass-2`, each `just check`-green, totalling ~200-220 LoC removed and two functions retired.

**Architecture:** All work happens in the existing module. No new packages. Tasks are independent — they can land in any order — but the spec orders them E1, E2, A, B, C, D so review starts with zero-risk concept deletions and ends with the broader test-fixture churn.

**Tech Stack:** Go 1.26, templ v0.3.x, datastar-go v1.2.x, matryer/is for tests. Tooling: `just generate`, `just check`, `just test-ui`.

---

## Task 0: Set up branch

**Goal:** Working tree on a fresh branch off main.

**Files:** none (branch operation only).

**Acceptance Criteria:**
- [ ] On branch `chore/simplification-pass-2`
- [ ] Branch tracks `origin/main`
- [ ] Working tree clean

**Verify:** `git status` shows clean tree on the new branch.

**Steps:**

- [ ] **Step 1: Sync main and branch.**

```bash
git checkout main
git pull --ff-only origin main
git checkout -b chore/simplification-pass-2
git status
```

Expected: clean working tree on the new branch.

---

## Task 1: E1 — Inline `BuildStatusWithEnergy`

**Goal:** Delete `BuildStatusWithEnergy` and its dedicated test; inline its two lines of logic at the one production caller.

**Files:**
- Modify: `pkg/breezy/status.go` (delete `BuildStatusWithEnergy` at lines 223-235)
- Modify: `pkg/breezy/status_test.go` (delete the test that asserts `BuildStatusWithEnergy(nil) == BuildStatus`; line ~202)
- Modify: `cmd/breezyd/handlers_device.go::getDevice` (line 42; replace the wrapped call with `BuildStatus` + two follow-up lines)

**Acceptance Criteria:**
- [ ] `BuildStatusWithEnergy` no longer defined anywhere in `pkg/breezy/`
- [ ] `getDevice` produces byte-identical JSON for the `energy` block (when present and when nil)
- [ ] `pkg/breezy/...` tests pass
- [ ] `cmd/breezyd/...` tests pass

**Verify:** `just check` → all green.

**Steps:**

- [ ] **Step 1: Edit `getDevice` to inline the energy attachment.**

In `cmd/breezyd/handlers_device.go`, the current site is:

```go
resp := breezy.BuildStatusWithEnergy(snap.Values, name, cfg.ID, ip, lastPoll, ev)
```

Replace with:

```go
resp := breezy.BuildStatus(snap.Values, name, cfg.ID, ip, lastPoll)
if ev != nil {
    resp.Service["energy"] = *ev
}
```

- [ ] **Step 2: Delete `BuildStatusWithEnergy` from `pkg/breezy/status.go`.**

Remove the function (the doc-comment + the func body, 13 lines including the blank line that separates it from the previous function).

- [ ] **Step 3: Delete the wrapper-equivalence test.**

In `pkg/breezy/status_test.go`, the test that calls `BuildStatusWithEnergy(values, ..., nil)` and asserts `reflect.DeepEqual(base, withNil)` (around line 202) becomes meaningless once the wrapper is gone. Delete that single `func TestBuildStatusWithEnergy_NilEqualsBase` (or however the file names it). Other tests that use `BuildStatusWithEnergy` with a non-nil energy block need updating: replace those calls with `s := BuildStatus(...); s.Service["energy"] = energy` inline, or delete them if the dedicated function being gone makes them redundant with adjacent BuildStatus tests.

Inspect first:

```bash
grep -n "BuildStatusWithEnergy" pkg/breezy/status_test.go
```

For each call: if the test's purpose was "the wrapper attaches energy correctly", that purpose is gone — delete. If the purpose was "BuildStatus + energy attachment renders correctly", keep with inline replacement.

- [ ] **Step 4: Run check.**

```bash
just check
```

Expected: all green, no templ drift.

- [ ] **Step 5: Commit.**

```bash
git add pkg/breezy/status.go pkg/breezy/status_test.go cmd/breezyd/handlers_device.go
git commit -m "$(cat <<'EOF'
refactor(breezy): delete BuildStatusWithEnergy; inline at sole caller

The wrapper exists to spare 2 lines at exactly 1 call site
(handlers_device.go::getDevice). Inlining removes one function from
the public pkg/breezy surface and the dedicated equivalence test.
EOF
)"
```

---

## Task 2: E2 — Inline `State.RecordPoll`

**Goal:** Delete `State.RecordPoll` and replace its single call site with `State.Set`.

**Files:**
- Modify: `cmd/breezyd/state.go` (delete `RecordPoll` at lines 81-85)
- Modify: `cmd/breezyd/poller.go` (replace `state.RecordPoll(...)` with `state.Set(...)`)
- Modify: `cmd/breezyd/state_test.go` if any test directly invokes `RecordPoll` (the behaviour is already covered by `Set` tests, so just delete those tests rather than rewriting)

**Acceptance Criteria:**
- [ ] `RecordPoll` no longer defined on `*State`
- [ ] `grep -rn "RecordPoll" cmd/ pkg/` returns no production hits
- [ ] All tests pass

**Verify:** `just check` → all green.

**Steps:**

- [ ] **Step 1: Find the caller(s).**

```bash
grep -rn "RecordPoll" cmd/ pkg/ --include='*.go'
```

Expected: one call in `poller.go`, the definition in `state.go`, and possibly some test calls.

- [ ] **Step 2: Replace the poller's call.**

In `cmd/breezyd/poller.go`, change `state.RecordPoll(name, snap)` to `state.Set(name, snap)`. The surrounding comment in the poller already makes "this is a poll" clear — no replacement comment needed.

- [ ] **Step 3: Delete `RecordPoll` from `state.go`.**

Remove the function and its doc comment (5 lines total).

- [ ] **Step 4: Handle test references.**

```bash
grep -n "RecordPoll" cmd/breezyd/state_test.go
```

For each test that directly tests `RecordPoll`: since `RecordPoll` was literally `Set(...)`, the dedicated tests duplicate the `Set` tests and can go. If a test is named for clarity but exercises a poller code-path, change the call to `Set` and keep the test.

- [ ] **Step 5: Run check.**

```bash
just check
```

- [ ] **Step 6: Commit.**

```bash
git add cmd/breezyd/state.go cmd/breezyd/poller.go cmd/breezyd/state_test.go
git commit -m "$(cat <<'EOF'
refactor(daemon): delete State.RecordPoll; the poller calls Set directly

RecordPoll was a comment-only alias for Set. The "clarity at the call
site" justification doesn't hold up — the poller's surrounding code
already makes "this is a poll" obvious.
EOF
)"
```

---

## Task 3: A — `postV1WriteJSON` helper for /v1 write handlers

**Goal:** Mirror the shipped `postUIWriteJSON` collapse on the /v1 surface. Six handlers shrink to ~5-8 lines each.

**Files:**
- Modify: `cmd/breezyd/handlers_device.go` (add helper, refactor `postPower`, `postSpeed`, `postPreset`, `postMode`, `postHeater`, `postTimer`; decide on `postThreshold` and `postRTC` during implementation)
- Tests in `cmd/breezyd/handlers_device_test.go` (or wherever the /v1 handler tests live) should pass unchanged.

**Acceptance Criteria:**
- [ ] `postV1WriteJSON[T any](...)` helper exists, signature mirrors `postUIWriteJSON`
- [ ] 6 handlers (Power, Speed, Preset, Mode, Heater, Timer) collapsed to one-screen bodies
- [ ] All /v1 handler tests pass without modification
- [ ] No regression in JSON response shape (curl-snapshot equivalence)

**Verify:** `just check` → all green; `just test ./cmd/breezyd -run 'TestPost.*'` → all green.

**Steps:**

- [ ] **Step 1: Add the helper near the existing `doDeviceOp` definition in `server.go`, or at the top of `handlers_device.go` if it's only used there.**

Place is a judgment call — match where `postUIWriteJSON` lives in `handlers_ui_write.go`. Likely top of `handlers_device.go`.

```go
// postV1WriteJSON is the spine shared by every /v1/devices/{name}/...
// write handler. Mirrors postUIWriteJSON for /ui. Resolves the device,
// decodes JSON into a fresh T, runs an optional shape-validator, executes
// the op via doDeviceOp, surfaces errors through writeErr, and on success
// writes {"ok": true}. Pass nil shapeOK if no pre-op shape check is needed.
// When the shape check returns false it MUST have already written its own
// error response via writeErr.
func postV1WriteJSON[T any](
    h *Handler,
    w http.ResponseWriter,
    r *http.Request,
    shapeOK func(req *T, w http.ResponseWriter) bool,
    op func(ctx context.Context, rc *recordingClient, req *T) error,
) {
    name := r.PathValue("name")
    if _, ok := h.requireDevice(w, name); !ok {
        return
    }
    var req T
    if !readBody(w, r, &req) {
        return
    }
    if shapeOK != nil && !shapeOK(&req, w) {
        return
    }
    if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
        return op(ctx, rc, &req)
    }); err != nil {
        writeErr(w, classifyClientErr(err), err.Error())
        return
    }
    writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
```

- [ ] **Step 2: Refactor `postPower`.**

Original (lines 149-171):

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

Replace with:

```go
func (h *Handler) postPower(w http.ResponseWriter, r *http.Request) {
    type req struct {
        On *bool `json:"on"`
    }
    postV1WriteJSON(h, w, r,
        func(q *req, w http.ResponseWriter) bool {
            if q.On == nil {
                writeErr(w, "bad_request", "missing 'on' field (true/false)")
                return false
            }
            return true
        },
        func(ctx context.Context, rc *recordingClient, q *req) error {
            return breezy.Power(ctx, rc, *q.On)
        },
    )
}
```

- [ ] **Step 3: Repeat for `postSpeed`, `postPreset`, `postMode`, `postHeater`, `postTimer`.**

Apply the same pattern. The shape-check moves into the `shapeOK` closure; the op stays in the `op` closure. Each handler should drop from ~22 lines to ~12-14 lines.

- [ ] **Step 4: Refactor `postThreshold`. Leave `postRTC` as-is.**

`postThreshold` has compound shape checks (kind + at-least-one-of value/enabled) that work cleanly in a `shapeOK` closure — include it. Final handler count: 7 collapsed.

`postRTC` interleaves `time.Parse` between decode and op. Threading the parsed time through the `shapeOK → op` boundary requires either a wrapper struct with a `parsedTime time.Time` field (ugly) or moving the parse into `op` and re-parsing the same string from the request body (silly). Neither saves lines over the current form. Leave `postRTC` untouched — note in the commit message that the parse-step shape excluded it.

- [ ] **Step 5: Verify tests.**

```bash
just test ./cmd/breezyd -run 'TestPost'
```

All tests should pass without modification. If any test fails, the refactor has changed observable behaviour — investigate before claiming success.

- [ ] **Step 6: Sanity-check JSON shape against main.**

The /v1 JSON envelope (`{ok: true}` on success, `{error, code}` on failure) is documented behaviour. Compare a curl output against pre-refactor:

```bash
# Build and start daemon with memory backend (in a separate terminal):
just build
./breezyd --config <test-config> --backend=memory --seed pkg/breezy/fakedevice/snapshot_148.json &
DAEMON=$!
sleep 0.5
curl -sS -X POST -H 'Content-Type: application/json' -d '{"on":true}' http://localhost:8080/v1/devices/<name>/power
# Expected: {"ok":true}
curl -sS -X POST -H 'Content-Type: application/json' -d '{}' http://localhost:8080/v1/devices/<name>/power
# Expected: {"code":"bad_request","error":"missing 'on' field (true/false)"}
kill $DAEMON
```

(If the build infrastructure already covers this via tests, skip — handler tests typically pin both shapes.)

- [ ] **Step 7: Commit.**

```bash
git add cmd/breezyd/handlers_device.go
git commit -m "$(cat <<'EOF'
refactor(daemon): collapse /v1 write handlers via postV1WriteJSON

Mirror the postUIWriteJSON collapse from the first simplification
pass on the /v1 surface. Six handlers (Power/Speed/Preset/Mode/
Heater/Timer) shrink to ~12-14 lines each; the per-handler shape
check moves into a typed shapeOK closure.
EOF
)"
```

---

## Task 4: B — `runOpAndAck` helper for CLI cmdXxx

**Goal:** Extract the ctx-with-timeout + op + "ok"/"error" ack pattern from 13 of the 21 `cmdXxx` functions.

**Files:**
- Modify: `cmd/breezy/commands.go`

**Acceptance Criteria:**
- [ ] `runOpAndAck(op func(ctx context.Context) error, stdout, stderr io.Writer) int` exists in `commands.go`
- [ ] 13 commands collapsed to one-line bodies (or near it)
- [ ] All `cmd/breezy/main_test.go` tests pass without modification
- [ ] CLI exit codes preserved (0 / 1 / 2)

**Verify:** `just check` → all green; `just test ./cmd/breezy/...` → all green.

**Steps:**

- [ ] **Step 1: Add the helper at the top of `commands.go`, immediately after the imports.**

```go
// runOpAndAck is the shape shared by every ack-only cmdXxx: bound the
// op by a 10s timeout, run it, print "error: <msg>" + return 1 on
// failure, print "ok" + return 0 on success. Used by cmdPower,
// cmdMode, cmdHeater, cmdTimer, cmdResetFilter, cmdResetFaults, and
// the post-validation tails of cmdSpeed / cmdThreshold / cmdAutoFan /
// cmdRtc / cmdSet.
func runOpAndAck(op func(ctx context.Context) error, stdout, stderr io.Writer) int {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := op(ctx); err != nil {
        _, _ = fmt.Fprintf(stderr, "error: %s\n", err)
        return 1
    }
    _, _ = fmt.Fprintln(stdout, "ok")
    return 0
}
```

- [ ] **Step 2: Refactor `cmdPower`.**

Original:

```go
func cmdPower(b backend, name string, on bool, stdout, stderr io.Writer) int {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()
    if err := b.Power(ctx, name, on); err != nil {
        _, _ = fmt.Fprintf(stderr, "error: %s\n", err)
        return 1
    }
    _, _ = fmt.Fprintln(stdout, "ok")
    return 0
}
```

Replace with:

```go
func cmdPower(b backend, name string, on bool, stdout, stderr io.Writer) int {
    return runOpAndAck(func(ctx context.Context) error {
        return b.Power(ctx, name, on)
    }, stdout, stderr)
}
```

- [ ] **Step 3: Repeat for `cmdMode`, `cmdHeater`, `cmdTimer`, `cmdResetFilter`, `cmdResetFaults`, `cmdSet`.**

These all have the same shape: validate args (keep that code), then call `runOpAndAck` for the backend op + ack.

- [ ] **Step 4: Apply to the post-validation tails of compound commands.**

`cmdSpeed`, `cmdThreshold`, `cmdAutoFan`, `cmdRtc` all have:

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := b.OpXxx(ctx, name, ...); err != nil {
    _, _ = fmt.Fprintf(stderr, "error: %s\n", err)
    return 1
}
_, _ = fmt.Fprintln(stdout, "ok")
return 0
```

at the end. Replace with `return runOpAndAck(...)`.

For `cmdSpeed`, this happens in two places (the `manual:` branch and the preset branch).

- [ ] **Step 5: Verify tests.**

```bash
just test ./cmd/breezy/...
```

`main_test.go` (1367 lines) covers the CLI exit codes / stdout / stderr in depth — if any test fails, behaviour has drifted.

- [ ] **Step 6: Commit.**

```bash
git add cmd/breezy/commands.go
git commit -m "$(cat <<'EOF'
refactor(cli): collapse cmdXxx ack pattern via runOpAndAck helper

13 of 21 cmdXxx funcs share the ctx-timeout + op + 'ok'/'error' shape.
Helper retains the exact exit codes (0/1) and stderr/stdout formatting
that main_test.go pins.
EOF
)"
```

---

## Task 5: C — `scheduleTableShell` templ helper

**Goal:** Extract the shared `<table class="schedule-table">…</table>` chrome used by both `ScheduleBlock` (read) and `ScheduleBlockEdit`.

**Files:**
- Modify: `cmd/breezyd/ui/templates/schedule_block.templ`
- Will regenerate: `cmd/breezyd/ui/templates/schedule_block_templ.go`
- May affect: `cmd/breezyd/ui/templates/render_test.go` (if it asserts the exact `<thead>`/`<tbody>` markup)

**Acceptance Criteria:**
- [ ] `templ scheduleTableShell()` exists, accepts children for `<tbody>`
- [ ] Both `ScheduleBlock` and `ScheduleBlockEdit` use it
- [ ] `just generate` produces no drift in committed `_templ.go` after the change is committed
- [ ] `just test ./cmd/breezyd/ui/templates -run TestSchedule` → all green
- [ ] Playwright schedule tests still pass

**Verify:** `just generate && just check && just test-ui --grep 'schedule'`.

**Steps:**

- [ ] **Step 1: Read the current `schedule_block.templ` to identify the exact duplicated chunk.**

The shared chunk (around lines 41-48 in `ScheduleBlock` and 111-118 in `ScheduleBlockEdit`):

```templ
<table class="schedule-table">
    <thead><tr><th>at</th><th>mode</th><th>fan</th><th></th></tr></thead>
    <tbody>
        for _, e := range s.Entries {
            @scheduleReadRow(e)
        }
    </tbody>
</table>
```

vs

```templ
<table class="schedule-table">
    <thead><tr><th>at</th><th>mode</th><th>fan</th><th></th></tr></thead>
    <tbody class="schedule-edit-tbody">
        for _, e := range s.Entries {
            @ScheduleEditRow(e)
        }
    </tbody>
</table>
```

The `<thead>` is identical. The `<tbody>` differs in class and row-template. So the shareable shell is the `<table>` + `<thead>`; the `<tbody>` stays per-variant. **Honest reassessment: this saves only ~3 LoC, not 10.** Decision rule: if the diff after Step 5 shows ≤ 2 LoC net saving in `schedule_block.templ` (ignoring `_templ.go` churn), skip the commit and note item C as a no-op in the round-2 wrap-up. Don't ship indirection for indirection's sake.

- [ ] **Step 2: If proceeding, add `scheduleTableHead()`.**

```templ
// scheduleTableHead renders the <table> opening tag + <thead>. The
// <tbody> stays per-variant because the class and row-template differ
// between read and edit shapes.
templ scheduleTableHead() {
    <table class="schedule-table">
        <thead><tr><th>at</th><th>mode</th><th>fan</th><th></th></tr></thead>
}
```

Hmm — templ doesn't allow returning unbalanced markup. Pivot to the children-pattern: `templ scheduleTable(rowsClass string) { children... }`.

```templ
templ scheduleTable(tbodyClass string) {
    <table class="schedule-table">
        <thead><tr><th>at</th><th>mode</th><th>fan</th><th></th></tr></thead>
        <tbody class={ tbodyClass }>
            { children... }
        </tbody>
    </table>
}
```

Read variant becomes:

```templ
@scheduleTable("") {
    for _, e := range s.Entries {
        @scheduleReadRow(e)
    }
}
```

Edit variant becomes:

```templ
@scheduleTable("schedule-edit-tbody") {
    for _, e := range s.Entries {
        @ScheduleEditRow(e)
    }
}
```

- [ ] **Step 3: Run generation.**

```bash
just generate
```

- [ ] **Step 4: Run tests.**

```bash
just check
just test-ui --grep 'schedule'
```

If `render_test.go` asserts the exact `<thead>` HTML, those asserts should still pass — `scheduleTable` emits identical markup. If a test fails, the helper changed the emitted whitespace; reconcile and re-record goldens via `go test -update`.

- [ ] **Step 5: Commit.**

If the diff in `schedule_block.templ` shows > 2 LoC saved (excluding regenerated `_templ.go`), commit:

```bash
git add cmd/breezyd/ui/templates/schedule_block.templ cmd/breezyd/ui/templates/schedule_block_templ.go
git commit -m "$(cat <<'EOF'
refactor(ui): extract scheduleTable shell for read/edit dedup

The <table class="schedule-table"> + <thead> chrome is identical
across both variants of the SCHEDULE block. Lift it into a templ
helper that accepts the tbody class as parameter; the per-variant
row iteration stays in the caller via templ's children block.
EOF
)"
```

If the diff is wash or worse (a real risk given templ's children syntax adds boilerplate), skip the commit and note in the round-2 wrap-up that item C didn't pay out.

---

## Task 6: D — Shared test fixtures

**Goal:** Introduce `newTestHandler(t, devices, opts...)` and `newTestState(t, snaps)` helpers. Migrate 5 high-duplication test files. Leave remaining test files for opportunistic future migration.

**Files:**
- Create: `cmd/breezyd/handler_test_helpers_test.go`
- Modify: `cmd/breezyd/server_test.go`
- Modify: `cmd/breezyd/handlers_ui_write_test.go`
- Modify: `cmd/breezyd/main_test.go`
- Modify: `cmd/breezyd/poller_test.go`
- Modify: `cmd/breezyd/state_test.go`

**Acceptance Criteria:**
- [ ] Helper file exists with `newTestHandler` + `newTestState`
- [ ] 5 named files use the helper for their `Handler`/`State` setup blocks
- [ ] All migrated tests pass with identical assertions (diff shows only setup compression, not assertion changes)
- [ ] Unmigrated test files unchanged

**Verify:** `just check-all` (adds race + Playwright + templ-drift) → all green.

**Steps:**

- [ ] **Step 1: Inspect the current setup boilerplate.**

```bash
grep -B2 -A20 "Handler{" cmd/breezyd/server_test.go cmd/breezyd/handlers_ui_write_test.go cmd/breezyd/main_test.go cmd/breezyd/poller_test.go cmd/breezyd/state_test.go | head -80
```

Note the recurring fields: `Devices`, `State`, `ClientFactory`, `Pollers`, `Schedulers`, `PushHub`, `Metrics`, `PollInterval`. Note which tests need to override which — that's the option set.

- [ ] **Step 2: Design the helper signature.**

```go
// handlerOpt configures a *Handler built by newTestHandler.
type handlerOpt func(*Handler)

func newTestHandler(t *testing.T, devices map[string]DeviceConfig, opts ...handlerOpt) *Handler {
    t.Helper()
    h := &Handler{
        Devices:        NewDeviceRegistry(devices),
        State:          NewState(),
        ClientFactory:  func(name string) (breezy.DeviceClient, error) { return nil, fmt.Errorf("no client for %s", name) },
        PollInterval:   30 * time.Second,
    }
    for _, opt := range opts {
        opt(h)
    }
    return h
}

// Option helpers — keep small; tests can compose.
func withState(s *State) handlerOpt        { return func(h *Handler) { h.State = s } }
func withClient(f ClientFactory) handlerOpt { return func(h *Handler) { h.ClientFactory = f } }
func withPushHub(p *PushHub) handlerOpt    { return func(h *Handler) { h.PushHub = p } }
// ...etc per-need; add as the migration surfaces requirements.
```

```go
func newTestState(t *testing.T, snaps map[string]Snapshot) *State {
    t.Helper()
    s := NewState()
    for name, snap := range snaps {
        s.Set(name, snap)
    }
    return s
}
```

- [ ] **Step 3: Create `cmd/breezyd/handler_test_helpers_test.go`.**

File is named `_test.go` so it only compiles under `go test`. Contains both helpers and option-funcs.

- [ ] **Step 4: Migrate `state_test.go` first (smallest, lowest risk).**

For each test, replace inline `&Handler{...}` or `NewState()` + populate with `newTestHandler(t, ...)` / `newTestState(t, snaps)`. Diff should show only the setup block compressing. Assertions stay identical.

```bash
just test ./cmd/breezyd -run 'TestState'
```

- [ ] **Step 5: Migrate `poller_test.go`.**

Same pattern. The poller tests construct a full Handler with ClientFactory pointed at fakedevice; the helper's default factory returns an error, so each poller test passes `withClient(fakedeviceFactory)`.

- [ ] **Step 6: Migrate `main_test.go`, `handlers_ui_write_test.go`, `server_test.go`.**

Same pattern, biggest files. Pace: one file per commit so reviewer can stop at any commit if the diff feels off.

- [ ] **Step 7: Verify everything green.**

```bash
just check-all
```

This adds the Playwright suite — important because the test fixtures change the construction path of `Handler`, and a subtle field-defaulting drift could break the dashboard tests in a way `just test` doesn't catch.

- [ ] **Step 8: Commit each migration as a separate commit.**

```bash
# Per-file commit, e.g.:
git add cmd/breezyd/handler_test_helpers_test.go cmd/breezyd/state_test.go
git commit -m "test(daemon): introduce newTestHandler/newTestState; migrate state_test"

git add cmd/breezyd/poller_test.go
git commit -m "test(daemon): migrate poller_test to newTestHandler"
# ...etc
```

Final commit message mentions the remaining test files as candidates for opportunistic future migration.

---

## Task 7: Pre-push verification + PR

**Goal:** Ship the bundle as one PR with auto-merge.

**Files:** none (CI/PR operations only).

**Acceptance Criteria:**
- [ ] `just check-all` green locally
- [ ] PR opened against `main` with summary listing the 6 items
- [ ] Auto-merge enabled

**Verify:** PR shows all CI checks green; `gh pr view <n> --json mergeStateStatus` returns `MERGEABLE`.

**Steps:**

- [ ] **Step 1: Final pre-push gate.**

```bash
just check-all
```

Expected: all green including race + Playwright + templ-drift.

- [ ] **Step 2: Push.**

```bash
git push -u origin chore/simplification-pass-2
```

- [ ] **Step 3: Open PR.**

```bash
gh pr create --base main --title "chore: simplification pass round 2 (E1+E2 concept deletions, A+B helper collapses, C+D follow-ups)" --body "$(cat <<'EOF'
## Summary

Round 2 of the simplification effort. See \`docs/superpowers/specs/2026-05-10-simplification-pass-2-design.md\` for the audit and rationale.

Six commits, ordered for reviewer comfort:

- **E1**: Inline \`BuildStatusWithEnergy\` (delete 6-line wrapper + dedicated test).
- **E2**: Inline \`State.RecordPoll\` (delete cosmetic alias).
- **A**: \`postV1WriteJSON\` collapses 6 /v1 write handlers (mirrors shipped \`postUIWriteJSON\`).
- **B**: \`runOpAndAck\` collapses 13 of 21 CLI \`cmdXxx\` funcs.
- **C**: \`scheduleTable\` templ shell — modest dedup; lands only if positive diff.
- **D**: \`newTestHandler\` / \`newTestState\` introduced; 5 high-duplication test files migrated. Remaining test files left for opportunistic future migration.

## Test plan
- [x] \`just check-all\` green (vet + tests + race + Playwright + templ-drift)
- [x] Item A: /v1 JSON response shape unchanged (existing tests pin)
- [x] Item B: CLI exit codes + stdout/stderr formatting unchanged (\`main_test.go\` pins)
- [x] Item D: each migration's diff shows only setup compression, no assertion changes

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 4: Enable auto-merge.**

```bash
gh pr merge <pr-number> --squash --auto
```

---

## Tasks file

After the plan lands, write `docs/superpowers/plans/2026-05-10-simplification-pass-2.md.tasks.json` so future sessions can resume.
