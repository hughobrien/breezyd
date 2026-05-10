# Simplification pass — design

Date: 2026-05-10
Status: Shipped 2026-05-10.

## Goal

One bundled cleanup PR. Trim ~245 LoC of mechanical duplication in production Go, ~30 LoC of templ, and ~1000 lines of stale design/plan docs. No behaviour changes; tests must pass unchanged.

## Scope

Six items, ordered so the diff review starts with zero-risk deletions and ends with the highest-leverage code refactor.

### 1. Doc deletions (zero-risk)

Delete:

- `docs/superpowers/specs/2026-05-06-htmx-migration-design.md` — htmx is gone; superseded by the 2026-05-08 datastar migration. Confirmed zero htmx references in active code/templates.
- `docs/superpowers/specs/2026-05-07-preset-editor-restoration-design.md` — described the cookie-based UI-state protocol that the datastar migration removed.
- `docs/superpowers/specs/2026-05-08-tests-ui-htmx-migration-archive.md` — self-marked "archived".

### 2. Plan deletions (ship records for stable subsystems)

For shipped, stable subsystems, the design doc is the evergreen reference; the plan is a one-time ship record and can go.

Delete:

- `docs/superpowers/plans/2026-05-04-homekit-bridge.md`
- `docs/superpowers/plans/2026-05-04-standalone-mode-phase-1-ops-extract.md`
- `docs/superpowers/plans/2026-05-04-standalone-mode-phase-2-cli-backend.md`
- `docs/superpowers/plans/2026-05-06-energy-tracking.md`
- `docs/superpowers/plans/2026-05-06-schedule-system.md`

### 3. CLAUDE.md "Spec & design docs" tightening

Update the bullet list at `CLAUDE.md:161-169` to group references by subsystem instead of by chronology:

- v1.0 protocol + CLI: `2026-05-03-twinfresh-cli-design.md`, `2026-05-03-param-map.md`, vendor manual PDF.
- v1.1 dashboard (motivation): `2026-05-04-basic-ui-design.md`.
- v1.4 dashboard substrate (current): `2026-05-08-datastar-migration-design.md`.
- v1.2 device backend interface: `2026-05-08-device-backend-interface-design.md`.

Drop dead links (htmx, preset-editor-restoration). Skip introducing an INDEX file — 26 dated specs are still scannable.

### 4. Metrics table-driven refactor — `cmd/breezyd/metrics.go`

Today: 34 hand-stamped `prometheus.NewGaugeVec(prometheus.GaugeOpts{Name, Help}, labels)` instantiations (525 LoC).

After: a `[]metricDef{name, help, labels}` table plus one loop in `NewMetrics`. The collector fields on `*Metrics` stay public for callers (`metricsHandler` in main.go, tests in metrics_test.go); they just get populated via a builder loop rather than line-by-line.

Estimated saving: ~95 LoC. metrics_test.go must continue to pass without edits.

### 5. Collapse UI write handlers — `cmd/breezyd/handlers_ui_write.go`

Today: 8 `postUI*` handlers (Mode, Preset, Speed, Heater, Timer, Power, ResetFilter, ResetFaults) at lines 296-540, each ~30-50 lines following the same shape:

1. Resolve device name from path.
2. `decodeJSONBody` into a request struct.
3. Validate payload.
4. Call a write op via `doDeviceOp`-equivalent.
5. On error: `uiWriteError` → SSE error banner; on success: empty 200 + `notifyAfterWrite`.

After: extract a parametric `postUIWrite` helper that takes a decode + op closure. Each handler shrinks to ~5-8 lines. The `doDeviceOp` helper used by the /v1 JSON handlers is the model; the /ui equivalent differs only in response shape (SSE patch vs JSON error envelope), so it's a sibling helper, not a shared one.

Estimated saving: ~80 LoC.

### 6. Drop redundant /ui value validation; trust ops.ErrInvalidArg

Re-audit during planning revealed that /v1 handlers do NOT duplicate validation — they delegate to `pkg/breezy/ops.go`, which already returns `ErrInvalidArg` with well-worded messages ("mode must be one of ventilation/regeneration/supply/extract, got %q"). Only /ui pre-validates, for nicer SSE banner messages. So there's nothing to share between /v1 and /ui.

The real simplification: route `ErrInvalidArg` through `uiWriteError` as HTTP 422 with `err.Error()` as the banner text. Then delete the per-handler value-range/enum checks in /ui handlers — ops returns the same message anyway. Shape checks (nil-pointer for required fields like `on`, `mode`) stay at the handler since they precede the op call.

Estimated saving: ~40 LoC across the 6 /ui handlers that currently re-validate. This dovetails with item 5 — the `postUIWriteJSON` helper from item 5 is the natural place to add the `ErrInvalidArg → 422` branch.

No new file. Protocol validation lives in one place (`pkg/breezy/ops.go`) as it already does for /v1.

## Out of scope

Items audited but explicitly not pursued:

- CLI daemon-backend wrappers in `cmd/breezy/backend.go:178-220` — already collapsed to one-liners via `postWrite` in #26. Further consolidation costs grep-ability for ~30 LoC; not worth it.
- `params.go` (595 LoC parameter table) — verbose by necessity.
- `main.go` (667 LoC) — wiring already well-factored.
- `fakedevice/fake.go` — test-only, isolated, leave alone.
- Test-helper consolidation across 36 `*_test.go` files — defer; no acute pain.
- `schedule_block.templ` read/edit dedup — MED, ~30 templ LoC; defer to a templ-focused pass.

## Verification

After each item:

- `just check` (vet + fast tests + templ-drift).
- For item 4 (metrics): confirm Prometheus output is byte-identical against a captured `/metrics` snapshot before/after.
- For items 5+6: `just test` covers the handler-level behaviour; `just test-ui` covers the dashboard flow.

Before merging the bundle: `just check-all` (adds race + Playwright + templ-drift).

## Risk

All items are mechanical. The only non-trivial risk is item 4 — accidentally renaming a metric breaks Grafana/Prometheus consumers. The byte-identical `/metrics` snapshot check above is the guardrail.

## Result

- Production Go: 8,727 → 8,548 LoC (-179 / -2.1%). `cmd/breezyd/metrics.go` 525 → 406. `cmd/breezyd/handlers_ui_write.go` 679 → 619.
- Templ: unchanged this pass.
- Docs: 8 fewer files. 10,302 lines of doc deleted (vs. ~1000 estimated — the original estimate was off; the actual doc volume removed was an order of magnitude larger).
- Diff vs. main: 14 files changed, 1,125 insertions, 10,685 deletions.

## Outcome

- 5 implementation commits + spec + plan = 8 commits on `chore/simplification-pass`.
- Item 6 was revised in-place during planning: /v1 doesn't actually duplicate /ui's validation — both delegate to `pkg/breezy/ops.go`. The simplification became "trust `ops.ErrInvalidArg` through `uiWriteError`" (422), removing per-handler value-range pre-validation and aligning /ui with /v1's existing pattern. No new `validators.go`.
- `counterDef`/`gaugeDef` started as a symmetric pair; code-quality review caught the single-instance YAGNI on `counterDef` and it was inlined.
