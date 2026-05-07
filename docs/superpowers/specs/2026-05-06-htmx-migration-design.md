# htmx migration + dark mode — design

**Date:** 2026-05-06
**Issue:** [#14 — consider htmx](https://github.com/hughobrien/breezyd/issues/14)
**Status:** design draft pending user approval

## Why

The dashboard is one 1541-line file: `cmd/breezyd/ui/index.html`. ~330 lines of CSS, ~1200 lines of JS, the rest HTML. By cloc:

- All Go production code (daemon + library + config): **5266 LOC**
- The single dashboard file: **1464 LOC** (~28% of production code)
- Largest Go files top out at ~440 LOC

For a daemon that already polls each device every 5s and holds the truth in cache, the client-side state machine is incidental complexity. Almost all of the JS is plumbing — a fetch loop, document-level event delegation, optimistic-edit re-fetches, and DOM construction — that exists because the server doesn't render HTML.

Goals:

1. Reduce the JS surface dramatically by letting the server render fragments and let htmx swap them in.
2. Keep the same dashboard behavior end-to-end (functional parity).
3. Keep the JSON `/v1/...` API completely unchanged so the CLI and any future programmatic consumer are unaffected.
4. Keep test power equivalent or better post-migration (explicit parity contract below).
5. Add dark mode in the same pass, riding on the CSS refactor that the migration already requires.

Non-goals are listed at the end.

### LOC expectations (honest)

The win is primarily **redistribution and cognitive load**, not a dramatic line-count reduction. Back-of-envelope estimate after all three PRs:

| Surface | Today | After |
|---|---:|---:|
| `cmd/breezyd/ui/index.html` | 1464 | ~50 (page shell only) |
| `cmd/breezyd/ui/style.css` | 0 (inline) | ~360 (extracted + ~30 dark overrides) |
| `cmd/breezyd/ui/templates/*.templ` | 0 | ~450 across ~10 files |
| `cmd/breezyd/ui/helpers.go` | 0 | ~50 |
| New Go handlers (`handlers_ui_*.go`) | 0 | ~200 (thin shims to `pkg/breezy/ops`) |
| Surviving JS (FOUC + theme picker, in shell) | ~1200 (in shell) | ~25 |
| **Total dashboard surface** | **1464** | **~1135** |

So roughly **300 lines saved**, but the bigger change is that no single file is over ~150 LOC, and each file has one job. The 1500-line monolith — the actual readability problem — is gone.

## Stack decisions (locked)

| Decision | Choice | Why |
|---|---|---|
| Templating | [`templ`](https://templ.guide) | Type-safe components, compile-time field-rename safety, ergonomic composition. The codegen tax is accepted. |
| Generated files | **not committed**; treated as build artifacts | `.gitignore`s `*_templ.go`. `just build` runs `templ generate` first. `flake.nix` derivation has `pkgs.templ` in `nativeBuildInputs` and runs `templ generate` in `preBuild`. |
| Client library | [`htmx`](https://htmx.org) 2.x + the `response-targets` extension | Vendored, embedded, no CDN. Pinned via filename: `cmd/breezyd/ui/vendor/htmx-2.0.4.min.js`, `htmx-response-targets-2.0.4.min.js`. |
| Error responses | Real HTTP status codes | `422` for validation, `5xx` for backend, `401` for auth, `404` for typos. `response-targets` extension routes 4xx/5xx to the right swap target. |
| Read polling | Master poll, every 5s, on the device-list container | One render per cycle, paused via `[document.visibilityState === 'visible']`. Cards render from cache, near-instant. |
| Optimistic edits | None — server is source of truth | Writes round-trip ~50–150ms typical. `hx-disabled-elt="this"` during the call to prevent double-clicks. Sliders debounced via `hx-trigger="change delay:200ms"`. |
| CSS | Extracted to its own file, tokenized to variables, content-hashed for `Cache-Control: public, max-age=31536000, immutable` | Done as part of this work because we're already touching every color for dark mode. |
| Dark mode | Auto by default (`prefers-color-scheme`); manual override via theme picker popout (light/dark/auto) anchored on the "breezy" title | localStorage persists user choice. Inline FOUC script in `<head>` prevents first-paint flash. |

## Topology

Two parallel HTTP namespaces on the daemon:

- `/v1/...` — JSON. **Unchanged.** Continues to serve the CLI in daemon mode and Prometheus. Zero change to wire shape, status codes, error envelope, or paths.
- `/ui/...` — HTML fragments. New. Used exclusively by the embedded dashboard via htmx. `Content-Type: text/html`.

`GET /` keeps serving the page shell. The shell is now ~50 lines: doctype, head (with the FOUC theme script and CSS link), htmx + response-targets `<script>` tags, top-level layout that bootstraps via `<div hx-get="/ui/devices" hx-trigger="load, every 5s[document.visibilityState === 'visible']" hx-swap="innerHTML">`, plus the theme picker `<details>`.

### Endpoint inventory (sidecar HTML, all under `/ui/`)

Reads:

- `GET /ui/devices` — top-level: list of device cards (master poll target)
- `GET /ui/devices/{name}/card` — single device card (per-write swap target)
- `GET /ui/style-{hash}.css` — extracted dashboard stylesheet (hash computed once at startup as a short prefix of the SHA-256 of the embedded bytes; baked into the page-shell template at startup so the URL is stable for the daemon's lifetime and changes across releases)
- `GET /ui/vendor/htmx-2.0.4.min.js`, `GET /ui/vendor/htmx-response-targets-2.0.4.min.js` — vendored client libraries; version pinned in the URL path for cache-busting on upgrade

Writes (each returns the affected fragment for htmx to swap):

- `POST /ui/devices/{name}/power` — on/off
- `POST /ui/devices/{name}/mode` — airflow mode
- `POST /ui/devices/{name}/speed` — manual %
- `POST /ui/devices/{name}/heater` — heater toggle
- `POST /ui/devices/{name}/reset-filter`
- `POST /ui/devices/{name}/reset-faults`
- `POST /ui/devices/{name}/sensor-toggle` — humidity/CO2/VOC/temp enable
- `PUT  /ui/devices/{name}/threshold` — sensor threshold value
- `PUT  /ui/devices/{name}/schedule` — replace schedule

Each handler is a thin shim: parse form params, call the existing write path in `pkg/breezy/ops` (same path the JSON handlers use today), then render `DeviceCard(snapshot)`. Protocol invariants — fan-settle, validation, `dialRecording` — are inherited automatically.

### Error semantics

| Class | Status | Body | `hx-target-*` default |
|---|---|---|---|
| Validation (out-of-range threshold, malformed schedule) | `422 Unprocessable Content` | Rendered card with inline error banner | `hx-target-422="closest .device-card"` |
| Backend (UDP timeout, fan-settle violation, device unreachable) | `502 Bad Gateway` (or `503`) | Rendered banner-only fragment | `hx-target-5xx="#global-error-banner"` |
| Auth (`ErrAuth`) | `401 Unauthorized` | Re-auth-needed fragment | `hx-target-401="#global-error-banner"` |
| Not found (device name typo) | `404` | Fragment explaining the error | `hx-target-404="#global-error-banner"` |

The `response-targets` extension is what makes htmx swap content from non-2xx responses; without it, htmx ignores them. This is why we vendor and load it.

## Template structure on disk

```
cmd/breezyd/ui/
├── index.html                          # ~50-line page shell
├── style.css                           # extracted, tokenized, content-hashed at serve time
├── templates/
│   ├── layout.templ                    # outer chrome, error banner slot, hx-target-* defaults
│   ├── device_list.templ               # list of cards (every-5s poll target)
│   ├── device_card.templ               # one device card (per-write swap target)
│   ├── sensors_block.templ             # sensors collapsible block
│   ├── sensor_threshold.templ          # one threshold input row
│   ├── energy_block.templ              # energy stats
│   ├── schedule_block.templ            # schedule editor + entries
│   ├── theme_picker.templ              # title + popout (rendered into the shell)
│   ├── error_banner.templ              # banner used for 4xx/5xx response bodies
│   └── helpers.templ                   # shared sub-components
├── vendor/
│   ├── htmx-2.0.4.min.js
│   └── htmx-response-targets-2.0.4.min.js
└── helpers.go                          # plain Go: HumanPct, ModeName, etc., callable from templates
```

Templates use typed Go signatures: e.g. `templ DeviceCard(s breezy.Snapshot, w WriteResult)` where `WriteResult` carries any post-write error state to render inside the card.

## Polling, swap precision, and state preservation

- **Polling.** One `hx-trigger="every 5s[document.visibilityState === 'visible']"` on the device-list container. Polling pauses when the tab is hidden — saves UDP round-trips. Same 5s cadence as today.
- **Swap precision.** Writes target the affected card via `hx-target="closest .device-card"`. Other cards are not re-rendered.
- **`hx-preserve` for collapsible state.** `<details>` elements that should retain open/closed state across swaps (sensors block, schedule entries, device-info) carry `hx-preserve`. Same UX as today; no localStorage; no JS.
- **Fan-settle suppression simplifies.** Today the JS tracks "we just wrote at T, suppress fan-RPM and air-quality reads until T+12s." With server-rendered fragments, the daemon already knows `service.fan_settled_until > time.Now()` and the template renders `—` or `(settling…)` for those fields. ~50 lines of JS deleted, no new logic anywhere.

## Optimistic-edit handling

Writes go through htmx; the slider/toggle moves only after the server confirms. Latency budget per write: **50–150ms typical** against a real device on the LAN.

| Control | Pattern |
|---|---|
| Toggles (power, heater, sensor enables) | `hx-trigger="change"` + `hx-disabled-elt="this"` |
| Sliders (manual speed %) | `hx-trigger="change delay:200ms"` (debounce until drag stops) |
| Text inputs (thresholds) | `hx-trigger="blur, keydown[key=='Enter'] from:closest input"` |

This is a real behavior change from today — the dashboard was optimistic, the new version is post-confirm. Acceptable because (a) the round trip is sub-perceptual on a healthy LAN, (b) optimistic UI hides genuine errors that we'd rather surface, (c) `hx-disabled-elt` plus the in-flight htmx CSS class give visible "writing…" feedback.

## Dark mode

### State model

Three states: **light**, **dark**, **auto**. Default is **auto** (follows OS via `prefers-color-scheme`). User's choice persists to `localStorage`. Theme is purely client-side state; the daemon doesn't know or care, which means htmx-swapped fragments inherit it automatically.

### CSS strategy

All colors in `style.css` move to CSS custom properties. One source of truth, three palettes:

```css
:root {
  --bg:     #fafafa;
  --fg:     #111;
  --card:   #fff;
  --border: #e0e0e0;
  --accent: #2563eb;
  --warn:   #d97706;
  --error:  #dc2626;
  --muted:  #777;
  /* ~30 named tokens, one per semantic role */
}

@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --bg:     #0d0d10;
    --fg:     #e8e8ea;
    --card:   #1a1a1f;
    /* dark overrides */
  }
}

:root[data-theme="dark"] {
  --bg:     #0d0d10;
  /* same dark values */
}
```

Three modes covered with no JS branching:

- `data-theme="light"` → light wins regardless of OS
- `data-theme="dark"` → dark always
- `data-theme="auto"` (or attribute absent) → matches OS

Final palettes (light + dark hex values) are tuned in implementation; the design only commits to the variable-driven approach.

### Theme picker popout

Click the "breezy" title → small popout with three buttons: ☀ light, ☾ dark, ⌬ auto (rendered as half-sun/half-moon). Active state visually highlighted.

```html
<details class="theme-picker">
  <summary><h1>breezy</h1></summary>
  <div class="theme-popout" role="group" aria-label="Theme">
    <button data-theme-set="light"  aria-label="Light">{{ inline-svg sun }}</button>
    <button data-theme-set="dark"   aria-label="Dark">{{ inline-svg moon }}</button>
    <button data-theme-set="auto"   aria-label="System">{{ inline-svg auto }}</button>
  </div>
</details>
```

Implementation notes:

- `summary::-webkit-details-marker { display: none }` and `details > summary { list-style: none }` to hide the disclosure triangle — the title looks identical to today.
- Inline SVGs, ~20 lines each, vendored as inline markup inside the template. Source: [Heroicons](https://heroicons.com) (MIT) or any compatibly licensed icon set; final choice is implementation detail. Unicode glyphs were considered and rejected for cross-platform rendering inconsistency.
- ~15 lines of JS handle: writing localStorage on button click, applying the `data-theme` attribute, closing the popout on outside click. Bound by event delegation to keep it small.

### FOUC prevention

A blocking inline `<script>` in `<head>`, **before** the stylesheet `<link>`, reads localStorage and sets `data-theme` on `<html>` before any styles compute:

```html
<script>
  var t = localStorage.getItem("theme");
  if (t === "light" || t === "dark") {
    document.documentElement.setAttribute("data-theme", t);
  }
</script>
<link rel="stylesheet" href="/ui/style-{{.AssetHash}}.css">
```

This is the only place an inline blocking script is genuinely needed. Without it, a user with dark preference and no override sees a flash of the light palette before localStorage is read.

### System-preference change while page is open

If the user toggles OS dark mode in `auto` mode, the `@media (prefers-color-scheme: dark)` rule re-evaluates automatically. No JS listener required. Confirmed via Playwright by emulating the change mid-test.

## Testing parity contract

The migration is **not done** until the following are true:

1. **Test count is ≥ 68** (current count). Each test in today's suite has either (a) a direct replacement in the new suite, or (b) an explicit obsolescence note in the PR description with reason.
2. **PR 3 includes a mapping table:** old test name → new test name (or "obsoleted because: …"). One row per old test.
3. **Categorical coverage matches** the table below.
4. **One new test class is added: htmx-swap correctness** (described below).
5. **A latency-budget assertion is added.** A test confirms a typical write-and-swap completes within 250ms against `fakedevice`. Bound is generous — it's a regression canary, not a perf benchmark.

### Categorical coverage

| Category | Examples (today) | Post-migration approach |
|---|---|---|
| Pure rendering (data → DOM) | sensors values, rpm=0 reads "off", stale-indicator desaturation, ENERGY grid, schedule rows | Real daemon + fake device with controllable state. Drive state into `fakedevice`, assert on rendered DOM. |
| POST shape (click → server gets X) | "power click POSTs inverse", "speed manual slider POSTs once on change", "schedule save PUTs edited table" | Two viable options, picked per test: (a) record the call inside `fakedevice`, assert on it; (b) assert on resulting state after the daemon applies the write. (b) is preferred — tests effect, not wire format. |
| State persistence across re-render | "ENERGY block: open state survives the 5s grid re-render" | `hx-preserve` on the relevant `<details>`. Test preserved verbatim; this is the canary for swap correctness. |
| Error paths (4xx/5xx, daemon unreachable) | "error toast: 4xx on POST", "daemon-unreachable: bootstrap failure shows banner" | `page.route()` retained as an **override mechanism for error tests only**. Selectively intercept `/ui/...` to return canned 4xx/5xx HTML fragments. Hybrid; not religious about technique. |
| Optimistic overlay | "mode click in manual: optimistic overlay flips Sensors rpms immediately" | **Semantics change.** With htmx, UI updates after swap. Test replaced with: "after click, the swap arrives within ~250ms with new rpm rendered." Same user-visible outcome, different assertion. |

### Net-new test class: htmx-swap correctness

- Swap target precision: writing speed updates only the affected card, not other devices' cards.
- Polling cadence: the every-5s poll fires; it pauses when the tab is hidden (`visibilitychange`).
- `hx-preserve` correctness: each preserved `<details>` survives at least 3 consecutive swaps.
- Disabled-during-write: `hx-disabled-elt` actually disables the control and re-enables on response.
- Latency budget: write-and-swap completes within 250ms against `fakedevice`.

### Net-new test class: dark mode

- Renders correctly with `data-theme="light"` (computed-style assertions on `--fg`/`--bg`).
- Renders correctly with `data-theme="dark"`.
- Renders correctly with `data-theme` absent + `prefers-color-scheme: dark` emulated via Playwright's `colorScheme: 'dark'` context option.
- Theme picker opens on title click, closes on outside click, closes after selection.
- Clicking a theme button writes localStorage and applies the attribute.
- Persistence: page reload preserves the choice.
- No FOUC: with `colorScheme: 'dark'` and localStorage seeded, the first painted frame already has dark colors (Playwright `page.screenshot` immediately after navigation, no flash visible).
- System-preference change while open in `auto` mode reflects without page reload.

### Test infrastructure plumbing (added in PR 3)

- `tests/ui/global-setup.ts`: spawns `breezyd` against `fakedevice`, allocates a free port, writes a temp config, waits for `GET /` to 200, exports the URL via Playwright config.
- `tests/ui/global-teardown.ts`: SIGTERMs the daemon, asserts clean exit (catches goroutine leaks).
- `pkg/breezy/fakedevice` gains a small admin-control surface (HTTP on a separate test-only port) for tests to set device state, simulate fan-settle, simulate auth failure, simulate UDP timeouts. Lives in files guarded by `//go:build fakedevice_admin` and is excluded from default and release builds; tests opt in via `-tags fakedevice_admin`.
- `tests/ui/fixtures.ts`: helpers to drive the fake device into common states (`asManualSpeed(80)`, `asRegenerationMode()`, etc.) without each test redoing setup.

## Migration plan

Three PRs, kept small enough to review independently:

### PR 1 — Infrastructure + read path + CSS extract/tokenize + dark mode

- Add `templ` to `flake.nix` (devshell + derivation `nativeBuildInputs`)
- Add `just generate` recipe; `just build` runs `templ generate` first
- `.gitignore` adds `cmd/breezyd/ui/templates/*_templ.go`
- Add `just check` regenerate-and-diff gate (matches the existing `gofmt` drift check)
- Add CI step running `templ generate && git diff --exit-code`
- Vendor `htmx-2.0.4.min.js` and `htmx-response-targets-2.0.4.min.js` under `cmd/breezyd/ui/vendor/`
- Write read-path templates: `layout`, `device_list`, `device_card`, `sensors_block`, `energy_block`, `schedule_block`, `theme_picker`, `error_banner`, `helpers`
- Add `GET /ui/devices`, `GET /ui/devices/{name}/card`, `GET /ui/style-{hash}.css`, `GET /ui/htmx*.min.js`
- Slim `index.html` to ~50-line shell with htmx bootstrap, FOUC theme script, theme picker `<details>`
- Delete the master JS poll (`refreshAll`, `setInterval`)
- **Extract CSS to `style.css`**
- **Tokenize all colors to CSS variables** (~30 named tokens)
- **Add dark-mode overrides** (`@media` + `[data-theme="dark"]`)
- **Add theme picker popout** with inline SVG icons + ~15 lines of JS for set/click-outside-close
- Tests for dark mode added in PR 1 using existing `page.route()` style (test rewrite is PR 3); will be rewritten alongside everything else there

End state: read-only dashboard behavior is identical (same data, same layout, same cadence); dark mode is added. All write paths still go through the existing JS event handlers calling `/v1/`. JSON API completely unchanged.

### PR 2 — Writes

- Add `/ui/devices/{name}/...` write handlers, each calling existing `pkg/breezy/ops` paths and returning the rendered card
- Migrate the dashboard's write controls one block at a time (power → mode → speed → sensor toggles → thresholds → schedule → heater → resets), deleting the matching JS as each lands
- By end of PR, document.addEventListener for click/keydown/input/change is gone (modulo the theme picker handlers)

### PR 3 — Cleanup + test rewrite

- Delete remaining JS scaffolding (the main `<script>` tag itself disappears; only the FOUC and theme picker scripts remain)
- Rewrite Playwright suite against the real daemon per the test infrastructure plumbing above
- Mapping table in the PR description: old test name → new test name (or obsolescence reason)
- Update `CLAUDE.md` and `README.md`
- Close issue #14

## Out of scope (deliberate, not bugs)

- No SSE or WebSockets. Polling at 5s is fine for residential ERV control.
- No theme system beyond light/dark/auto. No custom palettes, accent picker, etc.
- No CLI changes. `breezy daemon-url` remains the only daemon-aware verb.
- HomeKit bridge unaffected. It uses brutella/hap directly, not the dashboard's HTTP surface.
- `/v1/` JSON API is **not** being changed, deprecated, or hidden. The CLI continues to use it. Future programmatic consumers continue to use it.
- No internationalization.
- No mobile-responsive overhaul beyond what exists today.
- No build-time minification of the CSS or templates beyond what htmx ships with.
