# Dependencies

Every direct dependency in the breezyd repo, what it does, and why we keep it.

Indirect deps (`// indirect` lines in `go.mod`, JS transitive packages in `pnpm-lock.yaml`, anything pulled through nixpkgs) are not enumerated — they're not choices, they're consequences. The lockfiles `go.sum`, `tests/ui/pnpm-lock.yaml`, and `flake.lock` pin every transitive version.

Tone: skeptical. Each entry includes a critical view if there's a plausible argument for dropping or replacing it. The summary at the end flags everything currently in "could be dropped" territory.

## Go (`go.mod`)

### Production

- **BurntSushi/toml** v1.6.0 — TOML decoder. One caller: `internal/config/config.go::Load` (`toml.DecodeFile`).
  - **Why this one**: original Go TOML implementation, well-maintained, zero transitive deps. `pelletier/go-toml/v2` is the only serious alternative; no compelling reason to swap.
  - **Critical view**: surface is one call site against a ~10-field schema. A hand-rolled parser is plausible but loses free validation diagnostics. Keep.

- **a-h/templ** v0.3.1001 — HTML templating DSL that compiles `.templ` files into Go. Used throughout `cmd/breezyd/ui/templates/`.
  - **Why this one**: type-checked at compile time, no runtime template engine, no template-cache invalidation. SSE patches consume `templ.Component` directly via `datastar.PatchElementTempl`.
  - **Critical view**: `html/template` would mean abandoning every `.templ` file for `*.tmpl` text templates — losing Go-syntax-in-markup and the component model. Not a trade we'd make. Required at build time (`just build` runs `templ generate` first).

- **brutella/hap** v0.0.35 — HomeKit Accessory Protocol server. Used in `pkg/homekit/` for the optional HomeKit bridge (opt-in via `[homekit].enabled`).
  - **Why this one**: only mature pure-Go HAP implementation. The protocol is hundreds of pages of pairing crypto + service definitions.
  - **Critical view**: only loaded when the user enables HomeKit. Rolling our own HAP is roughly a year of work. Keep.

- **prometheus/client_golang** v1.23.2 — Prometheus metrics. Used by `cmd/breezyd/metrics.go` to register counters/gauges; exposed at `/metrics`.
  - **Why this one**: canonical Prometheus Go client; matches the exposition format every scraper expects.
  - **Critical view**: `/metrics` is a documented integration point — see `nix/module.nix::services.breezyd.prometheus.enable`. Dropping it would mean dropping that integration entirely. Keep.

- **starfederation/datastar-go** v1.2.1 — Server SDK for the datastar hypermedia library. Used in every SSE-emitting handler (`handlers_ui_sse.go`, `handlers_ui_write.go`) and in templ helpers (`schedule_block.templ`, `sensor_threshold.templ`) for navigation expressions.
  - **Why this one**: matches the vendored client bundle at `cmd/breezyd/ui/vendor/datastar-1.0.1.min.js`. Hand-rolling the SSE wire format works (it's a few lines per event) but loses the typed patch-mode constants, signal-merge helpers, and `ReadSignals` request parser.
  - **Audit history**: see #192 — the repo is now ~100% SDK-idiomatic for SSE handling.

### Test-only

- **matryer/is** v1.4.1 — Tiny test-assertion helper. **2247 call sites across 20+ test files.**
  - **Why this one**: replaces `if got != want { t.Errorf("%v vs %v", got, want) }` with `is.Equal(got, want)`. Tiny library (~250 lines), zero transitive deps.
  - **Critical view**: this is a stylistic dep. The stdlib `testing.T` plus a few lines of boilerplate covers everything `is` does. Eliminating means rewriting 2247 call sites — net cost ~2000 lines of churn, benefit zero. Kept as the chosen test-assertion idiom; would only revisit if the lib went unmaintained.

- **pgregory.net/rapid** v1.3.0 — Property-based testing with shrinking. Used in **one file**: `pkg/breezy/frame_property_test.go` (23 references), pinning the FDFD/02 frame round-trip across random inputs.
  - **Why this one**: declarative generators (`rapid.StringMatching(...)`, `rapid.SliceOfN(...)`) plus shrinking on failure. Reproducible across CI runs.
  - **Critical view**: Go 1.18+ native `testing.F` fuzzing covers similar ground without an external dep. A migration is plausible but the property tests rely on rapid's strict generators (PRNG-driven, deterministic seed) — `testing.F` is corpus-driven and behaves differently. **Borderline. Re-evaluate when `frame_property_test.go` next changes substantively.**

## JS (`tests/ui/package.json`)

All three are `devDependencies`; nothing ships to users.

- **@playwright/test** ^1.49.0 — E2E test runner that drives a real Chromium against the running daemon. Used by every spec in `tests/ui/*.spec.ts`.
  - **Why this one**: the SSE / morph round-trip tests need a real browser; Playwright is the only option that's tractable for that.
  - **Critical view**: non-negotiable for the test suite we have. Keep.

- **typescript** ^5.6.0 — Type compiler. Pulled in only because the test files are `.ts`.
  - **Why this one**: editor inference for Playwright's `Page` / `Locator` / `Response` types is significantly better with TypeScript than with JSDoc.
  - **Critical view**: the test suite is ~600 lines. JSDoc on plain `.js` would buy roughly the same editor experience for non-trivially less node_modules churn. A one-day rewrite. **Borderline.**

- **tsx** ^4.19.0 — TypeScript runner for non-Playwright scripts (`screenshot.ts`).
  - **Why this one**: lets `just screenshot` run a `.ts` file directly without a separate build step.
  - **Critical view**: coupled to TypeScript above — if `.ts` goes, `tsx` goes (replaced by `node screenshot.js`).

## Nix (`flake.nix`)

- **nixpkgs** — `github:NixOS/nixpkgs/nixos-unstable`. The universe; provides `go`, `templ`, `just`, `playwright`, `gh`, everything in `nix develop`.
  - **Critical view**: load-bearing. Nothing to debate.

## Critical-assessment summary

| Dep | Verdict |
|---|---|
| `BurntSushi/toml`, `a-h/templ`, `brutella/hap`, `prometheus/client_golang`, `starfederation/datastar-go`, `@playwright/test`, `nixpkgs` | Load-bearing. Keep. |
| `matryer/is` | Stylistic. Replacing means churn for no win. Keep. |
| `pgregory.net/rapid` | One test file; Go's native `testing.F` could replace, but the semantics differ. Borderline. |
| `typescript`, `tsx` | Could drop with a `.ts` → `.js` rewrite of `tests/ui/*`. Borderline. |
