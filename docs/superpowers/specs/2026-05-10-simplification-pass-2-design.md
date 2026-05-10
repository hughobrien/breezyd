# Simplification pass — round 2 — design

Date: 2026-05-10
Status: Designed.

## Goal

One bundled cleanup PR. Trim further duplication left over from the first simplification pass (`2026-05-10-simplification-pass-design.md`, shipped same day) by **(a)** mirroring its helper-collapse pattern on the /v1 handlers and the CLI verb wrappers, and **(b)** eliminating two pure-YAGNI wrapper functions that never paid their way. No behaviour changes; tests must pass unchanged (except where they covered removed wrappers).

The "every LoC is debt" framing: items A–D compress boilerplate, items E1–E2 delete concepts. The latter are the cheaper wins.

## Scope

Six items, ordered so the diff review starts with the zero-risk concept deletions, then proceeds to the higher-leverage helper-collapses, then optional follow-ups.

### E1. Inline `BuildStatusWithEnergy` (concept deletion — zero risk)

`pkg/breezy/status.go::BuildStatusWithEnergy` is a 6-line wrapper:

```go
func BuildStatusWithEnergy(values map[ParamID][]byte, name, id, ip string, lastPoll *time.Time, energy *EnergyValues) Status {
    s := BuildStatus(values, name, id, ip, lastPoll)
    if energy != nil {
        s.Service["energy"] = *energy
    }
    return s
}
```

Single production caller: `cmd/breezyd/handlers_device.go::getDevice` (line 42). The wrapper exists to spare two inline lines at exactly one site.

**Change:**
- Delete `BuildStatusWithEnergy` from `pkg/breezy/status.go`.
- In `getDevice`, replace `breezy.BuildStatusWithEnergy(...)` with `breezy.BuildStatus(...)` plus two follow-up lines (`if ev != nil { resp.Service["energy"] = *ev }`).
- Delete the dedicated test in `pkg/breezy/status_test.go` that verifies `BuildStatusWithEnergy(nil) == BuildStatus(...)` — once the wrapper is gone the assertion is meaningless.

**Saving:** −6 LoC + −1 exported function + −1 test (~10 LoC of test) = ~16 LoC and one concept.

### E2. Inline `State.RecordPoll` (concept deletion — zero risk)

`cmd/breezyd/state.go::RecordPoll` is a cosmetic alias:

```go
// RecordPoll sets all fields of the snapshot atomically. It is equivalent to
// Set but named for clarity at the poller call site.
func (s *State) RecordPoll(name string, snap Snapshot) {
    s.Set(name, snap)
}
```

The "clarity" argument is weak — the poller's call site already has surrounding context.

**Change:**
- Delete `RecordPoll` from `cmd/breezyd/state.go`.
- Replace the one call in the poller with `s.Set(name, snap)` plus a short surrounding comment if the call site loses readability.
- Tests for `RecordPoll` (if any) are deleted; tests for `Set` already cover the behaviour.

**Saving:** −5 LoC + −1 method + ~5 LoC of test = ~10 LoC and one concept.

### A. `postV1WriteJSON` helper for /v1 write handlers (mechanical)

`cmd/breezyd/handlers_device.go` has six write handlers that follow the same envelope:

1. Resolve device name from path.
2. `readBody` into a request struct (typed body literal).
3. Shape-check required fields (nil-pointer checks; not value-range — that lives in `pkg/breezy/ops.go`).
4. Call `h.doDeviceOp(r, name, closure)`.
5. On error: `writeErr(w, classifyClientErr(err), err.Error())`. On success: `writeJSON(w, http.StatusOK, map[string]any{"ok": true})`.

This is the same shape that the first simplification pass collapsed in `handlers_ui_write.go` via `postUIWriteJSON`. The /v1 sibling never got the same treatment.

**Change:** Introduce a generic helper in `handlers_device.go`:

```go
func postV1WriteJSON[T any](
    h *Handler,
    w http.ResponseWriter,
    r *http.Request,
    shapeOK func(req *T, w http.ResponseWriter) bool,
    op func(ctx context.Context, rc *recordingClient, req *T) error,
)
```

Six handlers (`postPower`, `postSpeed`, `postPreset`, `postMode`, `postHeater`, `postTimer`) collapse to ~5-8 lines each.

`postThreshold` and `postRTC` have extra steps between decode and op (kind validation and `time.Parse` respectively). They either get included with bespoke `shapeOK` closures, or stay as-is. **Decide during implementation; whichever produces less code wins.**

**Saving:** ~80 LoC. Same risk profile as `postUIWriteJSON` (low).

### B. `runOpAndAck` helper for CLI cmdXxx (mechanical)

`cmd/breezy/commands.go` has 13 of 21 `cmdXxx` functions that follow:

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := b.Op(ctx, name, ...); err != nil {
    _, _ = fmt.Fprintf(stderr, "error: %s\n", err)
    return 1
}
_, _ = fmt.Fprintln(stdout, "ok")
return 0
```

**Change:** Extract:

```go
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

`cmdPower`, `cmdMode`, `cmdHeater`, `cmdTimer`, `cmdResetFilter`, `cmdResetFaults`, `cmdSet`, the post-validation tails of `cmdSpeed` / `cmdThreshold` / `cmdAutoFan` / `cmdRtc` all collapse to `return runOpAndAck(func(ctx) error { return b.Op(ctx, ...) }, stdout, stderr)`.

Commands with extra rendering (`cmdStatus`, `cmdFaults`, `cmdFirmware`, `cmdEfficiency`, `cmdGet`, `cmdLs`, `cmdRtcShow`) keep their bespoke shape — the ack pattern doesn't fit reads.

**Saving:** ~50-65 LoC. Low risk.

### C. `schedule_block.templ` table-shell dedup (marginal)

Honest scope correction from the first pass's estimate (~30 LoC) after re-reading the templ: the `<details>` openings differ enough across read/edit (`id`, `class`, `data-attr:open`, click handler) that a unified wrapper adds indirection. The actually-shareable chunk is:

```templ
<table class="schedule-table">
    <thead><tr><th>at</th><th>mode</th><th>fan</th><th></th></tr></thead>
    <tbody class="schedule-edit-tbody">{ children... }</tbody>
</table>
```

Extract `@scheduleTableShell()` that accepts children for the `<tbody>`.

**Saving:** ~10 LoC. Lowest-priority item in the bundle.

### D. Shared test fixtures (broad churn)

New file `cmd/breezyd/handler_test_helpers_test.go` (lives in test-only build) with:

```go
func newTestHandler(t *testing.T, devices map[string]DeviceConfig, opts ...handlerOpt) *Handler
func newTestState(t *testing.T, snaps map[string]Snapshot) *State
```

Migrate test files incrementally — partial migration is acceptable. Files keep their existing setup until they're modified for unrelated reasons; new tests use the helper.

**Saving:** ~150-300 LoC across 36 files, but most of that lands over time, not in this PR. The PR introduces the helper + migrates 5 high-duplication test files (the ones with the most boilerplate today: `server_test.go`, `handlers_ui_write_test.go`, `main_test.go`, `poller_test.go`, `state_test.go`).

**Risk:** medium — helpers can drift; per-test inlines never break. Mitigation: keep helpers minimal, with sensible defaults that tests can override via `opts ...handlerOpt`.

## Order of operations

1. **E1, E2** (concept deletions) — smallest diff, zero risk, lands first.
2. **A** (`postV1WriteJSON`) — mechanical, mirrors shipped pattern.
3. **B** (`runOpAndAck`) — mechanical.
4. **C** (`scheduleTableShell`) — small templ touch.
5. **D** (test fixtures) — biggest blast radius; lands last so reviewers can stop here if items 1-4 already feel like enough.

Each item is a separate commit on the branch so review can land items selectively if needed.

## Out of scope

Items audited but explicitly not pursued:

- **Dual-mount /v1 + /ui handlers from one definition.** Would save the most LoC (~200-300) but the two surfaces can legitimately diverge (different error envelopes, validation messages, response shapes). The previous spec's item 6 already noted that /v1 and /ui only *appear* duplicate; forcing them through a shared table erases the affordance to diverge and creates pain at the next op-specific tweak.
- **CLI verb-table dispatch instead of giant switch.** The current `switch verb { case "power": ... }` is the same shape as a `map[string]verbHandler`, just with grep-able names. Refactoring saves a few lines but loses the navigability of "find the case for verb X."
- **Drop the `/test/...` admin surface.** Build-tagged out of production already; the cost is contained.
- **`pkg/breezy/params.go` parameter table** (still verbose by necessity).
- **`cmd/breezyd/main.go`** wiring (still well-factored).
- **`cmd/breezyd/server.go`** at 644 LoC is a kitchen-sink file but its content (Registry + Handler + dial helpers + JSON writers) is appropriate co-location. Splitting is organisation, not simplification.

## Verification

After each commit:

- `just check` (vet + fast tests + templ-drift).
- Items A, E1: confirm `GET /v1/devices/{name}` JSON output is byte-identical against a snapshot taken before/after (the energy-block placement is the load-bearing detail).
- Items A, B: confirm Go test counts unchanged; only test contents change (or no test changes for A/B).
- Item D: confirm that migrated tests assert the same things they did before — `git diff` should show *only* setup-block compression, not assertion changes.

Before merging the bundle: `just check-all` (adds race + Playwright + templ-drift).

## Risk

All items are mechanical or pure deletions. The only non-trivial risk is item D — helper drift over time. Mitigated by keeping the helpers minimal and option-based.

## Result (estimated)

- E1 + E2: −2 functions, ~−25 LoC including tests.
- A + B + C: ~−140 LoC.
- D: ~−50 LoC in this PR, ~−250 LoC potential over time.
- Total bundle: roughly ~200-220 LoC removed from production Go, plus two concepts retired.

Compared to the first pass's ~179 LoC of production Go removed, round 2 is similar in magnitude with a smaller blast radius (no metrics-table rewrite this time).
