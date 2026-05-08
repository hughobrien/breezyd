# Handler refactor: extract `doDeviceOp` for device-dialing handlers

**Issue:** [#40](https://github.com/hughobrien/breezyd/issues/40) (rescoped — see [comment](https://github.com/hughobrien/breezyd/issues/40#issuecomment-4403366389)).

**Goal:** Reduce repetition in the 21 HTTP handlers that dial a device. Pure refactor; no wire-API change. Existing test suite is the safety net.

## Background

`cmd/breezyd/server.go` already houses four small helpers (`writeJSON`, `writeErr`, `requireDevice`, `readBody`) that the original wording of #40 proposed extracting. Every handler already calls them. What the issue's LOC estimate was implicitly counting — but didn't name — is the repeated `dialRecording → defer unlock → defer raw.Close → context.WithTimeout → defer cancel` scaffolding around each `breezy.X(ctx, rc, ...)` call.

The datastar migration (PR #58) added a second handler family (`postUI*` in `handlers_ui_write.go`) with the same scaffolding shape, roughly doubling the surface area. One unified abstraction now covers both surfaces.

## Architecture

Two new method receivers on `*Handler`, in `cmd/breezyd/server.go` next to `dial` / `dialRecording`:

```go
// doDeviceOp acquires the per-device UDP lock, opens a recording
// client, runs op with a 5s timeout derived from r.Context(), and
// tears everything down (Close before unlock; LIFO defer order) before
// returning. Caller has already validated the device exists and any
// input fields, and is responsible for translating the returned error
// and emitting any success body.
func (h *Handler) doDeviceOp(
    r *http.Request,
    name string,
    op func(ctx context.Context, rc *recordingClient) error,
) error

// doDeviceRead is the read-only sibling used by getParam. Same shape
// but goes through h.dial (no recording wrapper) for parity with the
// existing read path.
func (h *Handler) doDeviceRead(
    r *http.Request,
    name string,
    op func(ctx context.Context, c HandlerClient) error,
) error
```

Both helpers return the dial error or the op error verbatim. Translation to `writeErr` (JSON envelope) or `h.uiWriteError` (SSE envelope) stays at the call site, where the response shape is decided.

## Migration shape

Per handler: drop the seven-line dial+defers+context block; wrap the single `breezy.X(ctx, rc, ...)` call in `h.doDeviceOp(r, name, func(ctx, rc) error { return breezy.X(ctx, rc, ...) })`. Error translation and success body remain at the call site.

`postPower` before:

```go
rc, raw, unlock, err := h.dialRecording(name)
if err != nil { writeErr(w, classifyClientErr(err), err.Error()); return }
defer unlock()
defer func() { _ = raw.Close() }()
ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
defer cancel()
if err := breezy.Power(ctx, rc, *body.On); err != nil {
    writeErr(w, classifyClientErr(err), err.Error())
    return
}
writeJSON(w, http.StatusOK, map[string]any{"ok": true})
```

After:

```go
if err := h.doDeviceOp(r, name, func(ctx context.Context, rc *recordingClient) error {
    return breezy.Power(ctx, rc, *body.On)
}); err != nil {
    writeErr(w, classifyClientErr(err), err.Error())
    return
}
writeJSON(w, http.StatusOK, map[string]any{"ok": true})
```

## Scope

Migrate every handler that currently dials a device:

| File | Handlers |
|---|---|
| `cmd/breezyd/handlers_device.go` | `getParam` (uses `doDeviceRead`), `postParam`, `postPower`, `postSpeed`, `postPreset`, `postMode`, `postHeater`, `postThreshold`, `postTimer`, `postRTC` |
| `cmd/breezyd/handlers_service.go` | `postFilterReset`, `postFaultsReset` |
| `cmd/breezyd/handlers_ui_write.go` | `postUIPower`, `postUIMode`, `postUISpeed`, `postUIPreset`, `postUIHeater`, `postUITimer`, `postUIResetFilter`, `postUIResetFaults`, `putUIThreshold` |

21 handlers total (one read-only via `doDeviceRead`, 20 write-side via `doDeviceOp`). Each migrated handler shrinks by 6–8 lines; aggregate ≈ 140 LOC dropped from `handlers_*.go`.

## Out of scope

- Moving the four existing helpers (`writeJSON` / `writeErr` / `requireDevice` / `readBody`) to a new `internal/httpx/` package. The original issue proposed this; in practice they work where they are and a relocation just churns import lists.
- `putUISchedule` (does not dial — `scheduler.Replace` records the schedule; the scheduler goroutine dials asynchronously when an entry fires).
- The threshold/schedule fragment GET endpoints (`getUIThresholdRead`, `getUIThresholdEdit`, `getUIScheduleRead`, `getUIScheduleEdit`, `getUIScheduleNewRow`) — they don't dial; they emit SSE patches from cached state.
- Consolidating the divergent success/error response shapes (`writeJSON` vs `notifyAfterWrite + WriteHeader` vs `patchThresholdCellSSE` vs `errorBannerSSE`). Those genuinely differ per surface and are easier to read at the handler than buried in a helper.

## Testing

Two new unit tests for the helpers themselves, in `cmd/breezyd/server_test.go` (or a sibling `handlers_helpers_test.go` if `server_test.go` is over-full):

- `TestDoDeviceOp_ReleasesLockOnSuccess` — fakedevice-backed handler; assert the per-device mutex is unlocked after a successful op.
- `TestDoDeviceOp_ReleasesLockOnError` — same but the op returns an error; assert mutex unlocked.

Existing handler tests cover behavior end-to-end and act as the migration safety net. No test churn expected beyond the two new ones.

## Plan structure

1. Add the two helpers + their unit tests. `just check` clean.
2. Migrate `handlers_device.go` (10 handlers) and `handlers_service.go` (2 handlers). `just check` + race clean.
3. Migrate `handlers_ui_write.go` (9 handlers). `just check` + race clean.

Each commit is independently reviewable and self-contained.

## Risks

- **Lock leak on panic.** The current handlers and the proposed helper share the same `defer unlock(); defer raw.Close()` pattern, so behavior is unchanged. Worth a defer-ordering review during code review.
- **Helper hides intent at the call site.** Mitigated by the helpers being short, well-named, and method receivers on `*Handler` (so they appear in the same scope as `dial`/`dialRecording`).
- **Generic-looking signature accepting any closure** can encourage callers to do too much inside the closure. Mitigated by docstring guidance: validate inputs and translate errors at the call site, not inside the closure.

## Why this is worth it

- Twice the handler count of when the issue was filed, post-datastar migration.
- One place to fix any future bug in the dial+timeout+lock+close dance — currently 21 places.
- Each migrated handler reads as: validate → run-op → translate-result. Cleaner shape; the UDP plumbing stops competing with the validation logic for attention.
