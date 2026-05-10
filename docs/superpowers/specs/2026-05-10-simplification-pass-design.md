# Simplification pass — design

Date: 2026-05-10
Status: Drafted, awaiting plan.

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

### 6. Shared payload validators

Today: `handlers_device.go` (/v1) and `handlers_ui_write.go` (/ui) re-validate the same payload shapes — speed manual range, airflow mode enum, heater bool, threshold tuple bounds.

After: a `validators.go` in `cmd/breezyd` with typed parse-funcs (`parseSpeedManual(any) (uint8, error)`, etc.). Both handler families call them.

Estimated saving: ~30-40 LoC, plus single source of truth for protocol validation. This is item 6 because it dovetails with item 5 — the validators are most naturally extracted while item 5's `postUIWrite` helper is being shaped.

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

- Production Go: 8,727 → ~8,480 LoC (-2.8%).
- Templ: unchanged this pass.
- Docs: 8 fewer files, ~1000 fewer lines.
