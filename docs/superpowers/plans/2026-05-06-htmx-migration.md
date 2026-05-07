# htmx Migration + Dark Mode Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the 1500-line single-file SPA dashboard with server-rendered htmx fragments, extract+tokenize CSS, add dark mode, and rewrite the Playwright suite against a real daemon — all while leaving the JSON `/v1/` API completely untouched.

**Architecture:** Two parallel HTTP namespaces on `breezyd`: existing `/v1/` JSON for the CLI and Prometheus stays bit-for-bit identical; new `/ui/` HTML namespace serves typed `templ` fragments swapped in by htmx. CSS extracted to its own content-hashed file with `prefers-color-scheme` + `data-theme` overrides. Tests migrate from `page.route()` mocks to a real `breezyd` spawned against `pkg/breezy/fakedevice` (with a build-tagged admin surface for state injection).

**Tech Stack:** Go 1.26 + stdlib `net/http` + `templ` (codegen, generated files committed to the repo; the drift check `test-templ-drift` regenerates and diffs to catch out-of-sync generated files) + vendored htmx 2.0.4 + response-targets extension + Playwright + pnpm.

**Spec:** `docs/superpowers/specs/2026-05-06-htmx-migration-design.md` — read before starting any task.

---

## File Structure

### Created

```
cmd/breezyd/ui/
├── style.css                                # extracted, tokenized
├── helpers.go                               # plain Go: HumanPct, ModeName, etc.
├── templates/
│   ├── layout.templ                         # outer chrome + global error banner
│   ├── device_list.templ                    # the every-5s poll target
│   ├── device_card.templ                    # one device card; per-write swap target
│   ├── sensors_block.templ                  # collapsible sensors block
│   ├── sensor_threshold.templ               # one threshold row
│   ├── energy_block.templ                   # energy stats grid
│   ├── schedule_block.templ                 # schedule editor + entries
│   ├── theme_picker.templ                   # title + light/dark/auto popout
│   ├── error_banner.templ                   # used for 4xx/5xx response bodies
│   └── helpers.templ                        # shared sub-components (icons, labels)
└── vendor/
    ├── htmx-2.0.4.min.js
    └── htmx-response-targets-2.0.4.min.js

cmd/breezyd/
├── handlers_ui_read.go                      # GET /ui/devices and /ui/devices/{name}/card
├── handlers_ui_write.go                     # all POST/PUT /ui/devices/{name}/...
└── ui_assets.go                             # embed.FS, asset hash, vendor file serving

pkg/breezy/fakedevice/
├── admin.go                                 # //go:build fakedevice_admin
└── admin_test.go                            # admin surface tests, same tag

tests/ui/
├── global-setup.ts                          # spawns breezyd against fakedevice
├── global-teardown.ts                       # SIGTERM + clean-exit assertion
└── fixtures.ts                              # state-driving helpers for tests
```

### Modified

```
cmd/breezyd/ui.go                            # → renamed/expanded to ui_assets.go responsibilities
cmd/breezyd/ui/index.html                    # 1541 lines → ~50-line shell
cmd/breezyd/server.go                        # mux.HandleFunc additions for /ui/*
cmd/breezyd/main.go                          # ParseTemplates() call at startup
flake.nix                                    # add pkgs.templ to devshell + nativeBuildInputs
justfile                                     # generate, build, check recipes
.gitignore                                   # add templ generated files
.github/workflows/test.yml                   # add templ generate + diff gate
tests/ui/dashboard.spec.ts                   # rewritten in PR 3
tests/ui/playwright.config.ts                # add globalSetup + colorScheme
README.md                                    # build steps now include templ
CLAUDE.md                                    # build/test sections updated
CHANGELOG.md                                 # add htmx + dark-mode entry
```

---

# PR 1 — Infrastructure + read path + CSS extract/tokenize + dark mode

End state: dashboard's read-only behavior is byte-identical to today plus dark mode is added. All write paths still go through the existing JS event handlers calling `/v1/`. Master JS poll deleted.

---

### Task 1: Add `templ` to flake + justfile + .gitignore

**Goal:** `templ` available in `nix develop`; `*_templ.go` committed to the repo; `just generate` recipe runs codegen; `just check` enforces drift gate.

**Files:**
- Modify: `flake.nix`
- Modify: `justfile`
- Modify: `.gitignore`
- Modify: `go.mod`, `go.sum` (add `github.com/a-h/templ` runtime dependency)

**Acceptance Criteria:**
- [ ] `nix develop --command templ version` prints a version
- [ ] `nix build` still succeeds (compiles only committed Go; templ stays in the devshell only). After `go.sum` changes, `flake.nix`'s `vendorHash` MUST be updated — see CLAUDE.md.
- [ ] `just generate` exists and runs `templ generate ./...`
- [ ] `just check` includes a drift step that fails if `templ generate` produces diffs
- [ ] `*_templ.go` files are committed to the repo (not gitignored)
- [ ] `github.com/a-h/templ` is a direct dep in `go.mod` (the generated `*_templ.go` files import the runtime — Task 5's first generated file revealed this gap)

**Verify:**
```sh
nix develop --command templ version && just generate && just check
```
Expected: templ version printed; both commands exit 0.

**Steps:**

- [ ] **Step 1: Update `flake.nix`**

Add `pkgs.templ` to the devshell `packages` list. The `breezyd-pkg` derivation compiles directly from committed Go (including committed `*_templ.go` files once they exist) — no `nativeBuildInputs` or `preBuild` needed. The derivation shape remains:

```nix
breezyd-pkg = pkgs.buildGoModule {
  pname = "breezyd";
  inherit version;
  src = ./.;
  vendorHash = "sha256-TQW/KUuf9pI7UmkkvkzZcPWwEJDMraHbR582Q4725Vo=";
  subPackages = [ "cmd/breezyd" "cmd/breezy" ];
  ldflags = [
    "-s" "-w"
    "-X main.version=${version}"
    "-X main.commit=${commitOrDirty}"
  ];
  doCheck = true;
  meta = with pkgs.lib; {
    description = "Go library, daemon, and CLI for Vents Twinfresh Breezy ERVs";
    homepage = "https://github.com/hughobrien/breezyd";
    license = licenses.gpl3Plus;
    platforms = platforms.unix;
    mainProgram = "breezyd";
  };
};
```

And in `devShells.default`, add `templ`:

```nix
devShells.default = pkgs.mkShell {
  packages = with pkgs; [
    go
    gopls
    gotools
    go-tools
    goreleaser
    just
    templ
  ];
};
```

- [ ] **Step 2: Update `justfile`**

Add a `generate` recipe and a drift-check step. Append to `justfile`:

```makefile
# Run templ codegen (writes *_templ.go next to *.templ sources).
generate:
    templ generate

# Fail if generated templ files differ from sources (drift check).
test-templ-drift:
    templ generate
    git diff --quiet -- 'cmd/breezyd/ui/templates/*_templ.go' || (echo "templ drift: run 'just generate' and commit"; exit 1)
```

Then update the `build`, `check`, `check-all`, and `ci` recipes. Replace the existing `build:` recipe block with:

```makefile
build: generate
    go build -o breezyd ./cmd/breezyd
    go build -o breezy ./cmd/breezy
```

And update the aggregate gates (note: `generate` removed from `check`/`check-all`/`ci` — `test-templ-drift` runs codegen internally before diffing, so a separate step is redundant):

```makefile
check: lint test test-templ-drift
check-all: lint test test-race test-ui test-templ-drift
ci: lint test test-race test-staticcheck test-asan test-msan test-ui test-templ-drift
```

- [ ] **Step 3: Verify `.gitignore`**

Confirm that `*_templ.go` is NOT in `.gitignore` — these files are committed to the repo. The drift check (`test-templ-drift`) detects out-of-sync state via `git diff` on tracked files.

- [ ] **Step 4: Verify everything**

```sh
just nix-check
nix develop --command templ version
just generate            # no-op at this point: no .templ files yet
just check               # lint + test + drift; drift passes (no .templ files → no diff)
nix build                # compiles only committed Go; templ not involved
```

All commands must exit 0.

- [ ] **Step 5: Commit**

```sh
git add flake.nix justfile .gitignore
git commit -m "build: commit templ-generated files; drop templ from nix build (#14)"
```

---

### Task 2: Vendor htmx + response-targets

**Goal:** htmx 2.0.4 and response-targets 2.0.4 vendored, embedded, and served at versioned URLs under `/ui/vendor/`.

**Files:**
- Create: `cmd/breezyd/ui/vendor/htmx-2.0.4.min.js`
- Create: `cmd/breezyd/ui/vendor/htmx-response-targets-2.0.4.min.js`
- Create: `cmd/breezyd/ui_assets.go`
- Modify: `cmd/breezyd/ui.go` (will be merged into `ui_assets.go` and deleted in step 4)
- Modify: `cmd/breezyd/server.go`
- Create: `cmd/breezyd/ui_assets_test.go`

**Acceptance Criteria:**
- [ ] `GET /ui/vendor/htmx-2.0.4.min.js` returns the vendored bytes with `Content-Type: application/javascript` and `Cache-Control: public, max-age=31536000, immutable`
- [ ] `GET /ui/vendor/htmx-response-targets-2.0.4.min.js` likewise
- [ ] `GET /ui/vendor/anything-else.js` returns 404
- [ ] License/source URL recorded in a `vendor/README.md`

**Verify:**
```sh
go test ./cmd/breezyd -run TestVendorAssets -v
```
Expected: PASS.

**Steps:**

- [ ] **Step 1: Vendor the JS files**

```sh
mkdir -p cmd/breezyd/ui/vendor
curl -fsSL -o cmd/breezyd/ui/vendor/htmx-2.0.4.min.js \
  https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js
curl -fsSL -o cmd/breezyd/ui/vendor/htmx-response-targets-2.0.4.min.js \
  https://unpkg.com/htmx-ext-response-targets@2.0.4/dist/response-targets.js
sha256sum cmd/breezyd/ui/vendor/*.js
```

Record the sha256 sums in the next step's README. After download, **read both files** to confirm they look like minified htmx and not a CDN error page.

- [ ] **Step 2: Write `cmd/breezyd/ui/vendor/README.md`**

```markdown
# Vendored UI assets

| File | Source | License | sha256 |
|---|---|---|---|
| htmx-2.0.4.min.js | https://unpkg.com/htmx.org@2.0.4/dist/htmx.min.js | BSD-2-Clause | <sha256 from Step 1> |
| htmx-response-targets-2.0.4.min.js | https://unpkg.com/htmx-ext-response-targets@2.0.4/dist/response-targets.js | BSD-2-Clause | <sha256 from Step 1> |

To upgrade: bump the version in the filename, re-run the curl commands in
`docs/superpowers/plans/2026-05-06-htmx-migration.md` Task 2 Step 1, update
this table, and grep for the old version in `cmd/breezyd/`.
```

- [ ] **Step 3: Create `cmd/breezyd/ui_assets.go`**

Replace the existing `cmd/breezyd/ui.go` content with this and rename the file to `ui_assets.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// HTTP handlers for the dashboard's static assets: the page shell, the
// extracted stylesheet, and the vendored htmx libraries. Templates that
// render device data live in handlers_ui_read.go and handlers_ui_write.go.
package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed ui/index.html
var indexHTML []byte

//go:embed ui/vendor
var vendorFS embed.FS

// vendorRoot is vendorFS rooted at "ui/vendor" so URL paths can map directly.
var vendorRoot fs.FS

// styleHash is the short SHA-256 prefix of the embedded style.css. Computed
// at startup; baked into the page shell so the URL is stable per binary.
var styleHash string

//go:embed ui/style.css
var styleCSS []byte

func init() {
	root, err := fs.Sub(vendorFS, "ui/vendor")
	if err != nil {
		panic(err)
	}
	vendorRoot = root

	sum := sha256.Sum256(styleCSS)
	styleHash = hex.EncodeToString(sum[:])[:10]
}

// getIndex serves the embedded dashboard shell.
func (h *Handler) getIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

// getStyle serves the extracted stylesheet at /ui/style-<hash>.css.
// Hash is short SHA-256 prefix; stable per binary, so immutable caching is safe.
func (h *Handler) getStyle(w http.ResponseWriter, r *http.Request) {
	want := "/ui/style-" + styleHash + ".css"
	if r.URL.Path != want {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(styleCSS)
}

// getVendor serves files from cmd/breezyd/ui/vendor under /ui/vendor/<filename>.
// Filenames carry version suffixes for cache-busting.
func (h *Handler) getVendor(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/ui/vendor/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(vendorRoot, name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}
```

Then `git mv cmd/breezyd/ui.go cmd/breezyd/ui_assets.go` if the rename hasn't happened. (If `ui.go` still exists with old content, just write the new content into `ui_assets.go` and delete `ui.go`.)

- [ ] **Step 4: Wire routes in `server.go`**

In `cmd/breezyd/server.go`, locate the existing `mux.HandleFunc("GET /{$}", h.getIndex)` line and add these alongside it:

```go
mux.HandleFunc("GET /ui/style-{hash}.css", h.getStyle)
mux.HandleFunc("GET /ui/vendor/{file}", h.getVendor)
```

- [ ] **Step 5: Write `cmd/breezyd/ui_assets_test.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVendorAssets(t *testing.T) {
	srv := httptest.NewServer((&Handler{}).mux())
	defer srv.Close()

	cases := []struct {
		path        string
		wantStatus  int
		wantCT      string
		wantPrefix  string
	}{
		{"/ui/vendor/htmx-2.0.4.min.js", 200, "application/javascript; charset=utf-8", ""},
		{"/ui/vendor/htmx-response-targets-2.0.4.min.js", 200, "application/javascript; charset=utf-8", ""},
		{"/ui/vendor/missing.js", 404, "", ""},
		{"/ui/vendor/../etc/passwd", 404, "", ""},
		{"/ui/style-" + styleHash + ".css", 200, "text/css; charset=utf-8", ""},
		{"/ui/style-deadbeef00.css", 404, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus == 200 {
				if got := resp.Header.Get("Content-Type"); got != tc.wantCT {
					t.Errorf("content-type: got %q, want %q", got, tc.wantCT)
				}
				if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
					t.Errorf("cache-control missing immutable: %q", got)
				}
			}
		})
	}
}
```

The test relies on a `(*Handler).mux()` method. If `server.go` doesn't already expose one, extract the mux-construction block from `cmd/breezyd/server.go`'s server setup into a method `func (h *Handler) mux() *http.ServeMux { ... }` and have the existing setup call it. Pure refactor — no behavior change.

- [ ] **Step 6: Verify**

```sh
go test ./cmd/breezyd -run TestVendorAssets -v
```
Expected: PASS, with all six cases passing.

- [ ] **Step 7: Commit**

```sh
git add cmd/breezyd/ui/vendor/ cmd/breezyd/ui_assets.go cmd/breezyd/ui_assets_test.go cmd/breezyd/server.go
git rm -f cmd/breezyd/ui.go 2>/dev/null || true
git commit -m "ui: vendor htmx 2.0.4 + response-targets, serve under /ui/vendor (#14)"
```

---

### Task 3: Extract CSS to `style.css` and tokenize colors to variables

**Goal:** All CSS pulled out of `index.html` into `cmd/breezyd/ui/style.css` and every color usage replaced with a `var(--token)` reference. Dashboard renders byte-identical to before in light mode.

**Files:**
- Create: `cmd/breezyd/ui/style.css`
- Modify: `cmd/breezyd/ui/index.html` (remove the `<style>...</style>` block, add `<link rel="stylesheet" href="/ui/style-{hash}.css">` placeholder — the actual hash is templated in Task 4)

**Acceptance Criteria:**
- [ ] No `<style>` block remains in `index.html`
- [ ] `style.css` contains every rule from the old block
- [ ] All colors expressed as `var(--token-name)` in rule bodies
- [ ] `:root { ... }` defines every token used, with the same hex values that were inline before
- [ ] Visual diff against pre-change screenshot is empty (run `just screenshot` before and after, byte-compare PNGs — they should match exactly because the rendered colors are unchanged)

**Verify:**
```sh
just build && ./breezyd --config /tmp/test.toml &
just screenshot
diff tests/ui/screenshots/dashboard.png tests/ui/screenshots/dashboard.png.before
```
Expected: identical files.

**Steps:**

- [ ] **Step 1: Snapshot the current state**

```sh
just build
just test-ui-install     # if not already done
just screenshot
cp tests/ui/screenshots/dashboard.png /tmp/dashboard-before.png
cp tests/ui/screenshots/dashboard-3col.png /tmp/dashboard-3col-before.png
```

- [ ] **Step 2: Create `cmd/breezyd/ui/style.css`**

Read `cmd/breezyd/ui/index.html` lines 7–334 (the `<style>` block). Extract every rule into `style.css`. Then enumerate every distinct color literal (hex, rgb(), named) used in the rules.

For each color, create a CSS custom property in a `:root { ... }` block at the top of `style.css`. Token-naming convention: semantic role, not visual description. Examples: `--bg`, `--bg-card`, `--fg`, `--fg-muted`, `--border`, `--accent`, `--warn`, `--error`, `--success`, `--shadow`, `--alert-fire`, `--ok`. Aim for ~30 tokens covering all uses.

Then update each rule to use `var(--token)` instead of the literal. The file structure should look like:

```css
:root {
  --bg:        #fafafa;
  --bg-card:   #ffffff;
  --fg:        #111111;
  --fg-muted:  #777777;
  --border:    #e0e0e0;
  --accent:    #2563eb;
  --warn:      #d97706;
  --error:     #dc2626;
  /* ... full list ... */
}

/* All existing rules from index.html, with colors replaced by var(--token) */
body { background: var(--bg); color: var(--fg); ... }
/* ... */
```

- [ ] **Step 3: Update `index.html`**

Delete lines 7–334 (the `<style>...</style>` block including the wrapping tags). Replace with:

```html
<link rel="stylesheet" href="/ui/style-STYLEHASH.css">
```

(`STYLEHASH` is a literal placeholder string at this stage; Task 4 will template it.)

- [ ] **Step 4: Update test fixtures temporarily**

The existing Playwright tests reference the inline-CSS dashboard. Since the page now has a stylesheet link with a literal `STYLEHASH` (which won't load), tests will see unstyled content. To keep this task self-contained, update `cmd/breezyd/ui_assets.go::getIndex` to do a simple string substitution before serving:

```go
func (h *Handler) getIndex(w http.ResponseWriter, r *http.Request) {
	body := strings.ReplaceAll(string(indexHTML), "STYLEHASH", styleHash)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
}
```

This is a stopgap — Task 4 replaces it with a templ component. But it lets this task ship independently with a working dashboard.

- [ ] **Step 5: Visual verification**

```sh
just build
# Run breezyd against fakedevice (existing pattern) and run screenshot:
just screenshot
diff tests/ui/screenshots/dashboard.png /tmp/dashboard-before.png
diff tests/ui/screenshots/dashboard-3col.png /tmp/dashboard-3col-before.png
```

Expected: zero byte differences. If diffs exist, find the missing/wrong rule, fix it, re-screenshot until clean.

- [ ] **Step 6: Run existing test suite**

```sh
just check
just test-ui
```

Expected: all green. The Playwright tests should still pass because the rendered DOM/CSS is identical.

- [ ] **Step 7: Commit**

```sh
git add cmd/breezyd/ui/style.css cmd/breezyd/ui/index.html cmd/breezyd/ui_assets.go
git commit -m "ui: extract CSS to style.css with semantic color tokens (#14)"
```

---

### Task 4: Add dark-mode CSS overrides + theme picker (no JS yet)

**Goal:** `style.css` includes dark-mode token overrides via `prefers-color-scheme` and `[data-theme="dark"]`. Manually setting `<html data-theme="dark">` in DevTools renders dark; `data-theme="light"` forces light; absent attribute follows OS.

**Files:**
- Modify: `cmd/breezyd/ui/style.css`

**Acceptance Criteria:**
- [ ] `@media (prefers-color-scheme: dark) { :root:not([data-theme="light"]) { ... } }` block defines dark values for every color token
- [ ] `:root[data-theme="dark"] { ... }` block duplicates those dark values
- [ ] No JS yet — purely CSS
- [ ] Dark palette is harmonious and legible (manually inspected): `--bg` near-black but not pure `#000`, `--fg` near-white but not pure `#fff`, accents adjusted for dark contrast
- [ ] In Playwright with `colorScheme: 'dark'` the page renders dark; with `colorScheme: 'light'` the page renders light

**Verify:**
```sh
just test-ui -- --grep "dark mode"
```
Expected: PASS for the dark-mode test added in this task.

**Steps:**

- [ ] **Step 1: Pick the dark palette**

Pick concrete hex values for every token in dark mode. Keep them harmonious — single-hue cool grays for surfaces, slightly desaturated accents for legibility on dark. Example values (tune to taste):

```
--bg:        #0d0d10
--bg-card:   #1a1a1f
--fg:        #e8e8ea
--fg-muted:  #888892
--border:    #2a2a30
--accent:    #4f8aff   /* lighter blue for dark contrast */
--warn:      #f59e0b
--error:     #ef4444
--success:   #10b981
--shadow:    rgba(0,0,0,0.45)
--alert-fire:#ff5050
--ok:        #38a169
```

- [ ] **Step 2: Append dark-mode blocks to `style.css`**

After the existing `:root { ... }` block:

```css
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --bg:        #0d0d10;
    --bg-card:   #1a1a1f;
    --fg:        #e8e8ea;
    --fg-muted:  #888892;
    --border:    #2a2a30;
    --accent:    #4f8aff;
    --warn:      #f59e0b;
    --error:     #ef4444;
    --success:   #10b981;
    --shadow:    rgba(0, 0, 0, 0.45);
    --alert-fire:#ff5050;
    --ok:        #38a169;
    /* ...all remaining tokens with dark values */
  }
}

:root[data-theme="dark"] {
  /* exact duplicate of the @media block's overrides */
  --bg:        #0d0d10;
  /* ... */
}
```

The duplication is intentional — there's no clean way to share these across the two selectors without extra preprocessor tooling we're not adopting.

- [ ] **Step 3: Add a Playwright test for dark mode**

Append to `tests/ui/dashboard.spec.ts` (still using the existing `page.route()` mock style — this test is a stopgap for PR 1; PR 3 rewrites it):

```typescript
test("dark mode: prefers-color-scheme: dark renders dark palette", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await mockBootstrap(page);   // existing helper that wires page.route for /v1/devices
  await page.goto("/");
  await page.waitForSelector(".device-card");
  const bg = await page.evaluate(() =>
    getComputedStyle(document.body).backgroundColor
  );
  // The exact rgb() depends on the chosen hex; assert it's not the light value.
  expect(bg).not.toBe("rgb(250, 250, 250)");
  // And spot-check a known dark hex (translated to rgb).
  expect(bg).toBe("rgb(13, 13, 16)");
  await context.close();
});

test("dark mode: data-theme='light' overrides system preference", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "dark" });
  const page = await context.newPage();
  await mockBootstrap(page);
  await page.addInitScript(() => {
    document.documentElement.setAttribute("data-theme", "light");
  });
  await page.goto("/");
  await page.waitForSelector(".device-card");
  const bg = await page.evaluate(() =>
    getComputedStyle(document.body).backgroundColor
  );
  expect(bg).toBe("rgb(250, 250, 250)");
  await context.close();
});
```

If `mockBootstrap` doesn't exist by that exact name, look at any existing test in `dashboard.spec.ts` for the `page.route("/v1/devices", ...)` setup and inline that pattern. The point of this task is the CSS works in both modes; the test wiring follows whatever convention the existing suite uses.

- [ ] **Step 4: Verify**

```sh
just test-ui -- --grep "dark mode"
```

Expected: both new tests pass. Existing tests still pass (`just test-ui` no filter).

- [ ] **Step 5: Commit**

```sh
git add cmd/breezyd/ui/style.css tests/ui/dashboard.spec.ts
git commit -m "ui: add dark-mode CSS via prefers-color-scheme + data-theme (#14)"
```

---

### Task 5: Set up `templ` skeleton + helpers.go

**Goal:** `cmd/breezyd/ui/templates/` exists with a `helpers.templ` defining a trivial sub-component, generated successfully, importable from a Go test.

**Files:**
- Create: `cmd/breezyd/ui/templates/helpers.templ`
- Create: `cmd/breezyd/ui/helpers.go`
- Create: `cmd/breezyd/ui/templates/helpers_smoke_test.go`

**Acceptance Criteria:**
- [ ] `templ generate` produces `helpers_templ.go` with no errors
- [ ] `helpers.go` provides `HumanPct(int) string` and `ModeName(breezy.AirflowMode) string` callable from templates
- [ ] Smoke test renders the trivial component and asserts on its output

**Verify:**
```sh
just generate && go test ./cmd/breezyd/ui/templates/...
```
Expected: PASS.

**Steps:**

- [ ] **Step 1: Write `cmd/breezyd/ui/helpers.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// Plain-Go helpers callable from templ templates. Keep this file small and
// free of HTTP / state — anything that needs request context belongs in a
// handler, not here.
package ui

import (
	"fmt"
	"strings"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

// HumanPct renders 0-100 as "0%" through "100%".
func HumanPct(p int) string {
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return fmt.Sprintf("%d%%", p)
}

// ModeName renders an AirflowMode as the lowercase string used throughout
// the dashboard ("regeneration", "supply", "extract", "ventilation").
func ModeName(m breezy.AirflowMode) string {
	return strings.ToLower(m.String())
}
```

If `breezy.AirflowMode` doesn't have a `String()` method, look at how the existing code stringifies it (likely `pkg/breezy/status.go` or similar) and adapt. If the daemon already has these helpers in `cmd/breezyd/`, move them here instead of duplicating.

- [ ] **Step 2: Write `cmd/breezyd/ui/templates/helpers.templ`**

```go
package templates

import "github.com/hughobrien/breezyd/cmd/breezyd/ui"

// SpeedLabel renders the user-facing label for a speed percentage,
// shared across device_card and sensors_block.
templ SpeedLabel(pct int) {
	<span class="speed-label">{ ui.HumanPct(pct) }</span>
}
```

- [ ] **Step 3: Run codegen**

```sh
just generate
ls cmd/breezyd/ui/templates/
```

Expected: `helpers.templ` and `helpers_templ.go` both present. The latter is gitignored.

- [ ] **Step 4: Write smoke test**

`cmd/breezyd/ui/templates/helpers_smoke_test.go`:

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package templates

import (
	"context"
	"strings"
	"testing"
)

func TestSpeedLabel(t *testing.T) {
	var sb strings.Builder
	if err := SpeedLabel(75).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	if !strings.Contains(got, "75%") {
		t.Errorf("output missing 75%%: %q", got)
	}
	if !strings.Contains(got, `class="speed-label"`) {
		t.Errorf("output missing class: %q", got)
	}
}
```

- [ ] **Step 5: Verify**

```sh
just generate
go test ./cmd/breezyd/ui/templates/...
```

Expected: PASS.

- [ ] **Step 6: Commit**

```sh
git add cmd/breezyd/ui/helpers.go cmd/breezyd/ui/templates/
git commit -m "ui: add templ skeleton with SpeedLabel + smoke test (#14)"
```

---

### Task 6: Build `device_card.templ` and `device_list.templ` (read-only render)

**Goal:** A device card renders identically to today's JS-rendered card, including all sub-blocks (sensors, energy, schedule), driven from a `breezy.Snapshot`.

**Files:**
- Create: `cmd/breezyd/ui/templates/device_card.templ`
- Create: `cmd/breezyd/ui/templates/device_list.templ`
- Create: `cmd/breezyd/ui/templates/sensors_block.templ`
- Create: `cmd/breezyd/ui/templates/sensor_threshold.templ`
- Create: `cmd/breezyd/ui/templates/energy_block.templ`
- Create: `cmd/breezyd/ui/templates/schedule_block.templ`
- Create: `cmd/breezyd/ui/templates/error_banner.templ`
- Create: `cmd/breezyd/ui/templates/render_test.go` — golden-file render tests

**Acceptance Criteria:**
- [ ] Given a fixed `breezy.Snapshot` fixture (one fixture per relevant state: regeneration mode, manual speed, fan-settling, sensor alert, schedule alert, energy error, missing energy), the rendered HTML matches a committed golden file byte-for-byte
- [ ] Templates use only `breezy.Snapshot` fields and the helpers from Task 5; no HTTP / network access
- [ ] All static class names in the templates exist in `style.css`
- [ ] `templ generate` is clean

**Verify:**
```sh
just generate && go test ./cmd/breezyd/ui/templates/... -v
```
Expected: PASS.

**Steps:**

- [ ] **Step 1: Read existing rendering JS to inventory what needs to be templated**

```sh
sed -n '335,1541p' cmd/breezyd/ui/index.html | less
```

For each block in the current SPA (device card, sensors block, energy block, schedule block, error banner), record:
- What fields of the snapshot it reads
- Conditional branches (e.g., "show error if `service.energy.error`")
- Class names and structural HTML

This inventory is reference material for the templ files; it does not need to be committed.

- [ ] **Step 2: Write `device_card.templ`**

```go
package templates

import "github.com/hughobrien/breezyd/pkg/breezy"

templ DeviceCard(s breezy.Snapshot) {
	<article class={ "device-card", templ.KV("stale", s.IsStale()) } data-device={ s.Name }>
		<header>
			<h2>{ s.Name }</h2>
			// ... power toggle, mode chips, etc — visually identical to today
		</header>
		@SensorsBlock(s)
		@EnergyBlock(s)
		@ScheduleBlock(s)
	</article>
}
```

Fill in the body to match the current SPA exactly. Use `templ.KV` for conditional classes, `if`/`else` for conditional sub-trees. Pass the `Snapshot` (or sub-fields) to each block component.

- [ ] **Step 3: Write `sensors_block.templ`, `sensor_threshold.templ`, `energy_block.templ`, `schedule_block.templ`, `error_banner.templ`**

Each is a separate templ component matching today's SPA's rendering of that block. For brevity these are not transcribed in full here — base them on the JS rendering code in `index.html` lines 335–1541 and the snapshot fields they consume.

Important guidance:
- `sensor_threshold.templ` is the inline editor row; it renders both the read-only and edit states (controlled by a parameter `editing bool`)
- `energy_block.templ` renders the 5×3 grid OR the error string OR nothing (when `s.Service.Energy == nil`) — use `switch`/`case` rather than nested `if` for readability
- `schedule_block.templ` includes the alert-forced-open behavior (`<details open>` if alert active)
- `error_banner.templ` takes `code string, msg string` and renders a fixed-position banner; used by the global 5xx target later

- [ ] **Step 4: Write `device_list.templ`**

```go
package templates

import "github.com/hughobrien/breezyd/pkg/breezy"

templ DeviceList(snapshots []breezy.Snapshot) {
	for _, s := range snapshots {
		@DeviceCard(s)
	}
}
```

- [ ] **Step 5: Build snapshot fixtures and golden files**

Create `cmd/breezyd/ui/templates/testdata/`:

```
testdata/
├── snapshot_regen.json
├── snapshot_manual.json
├── snapshot_settling.json
├── snapshot_sensor_alert.json
├── snapshot_schedule_alert.json
├── snapshot_energy_error.json
├── snapshot_no_energy.json
├── golden_card_regen.html
├── golden_card_manual.html
├── golden_card_settling.html
├── golden_card_sensor_alert.html
├── golden_card_schedule_alert.html
├── golden_card_energy_error.html
└── golden_card_no_energy.html
```

For each `.json`, hand-craft a minimal Snapshot fixture covering that state. Run the template once, save the output as the corresponding `golden_*.html` (after manually inspecting that the HTML is right).

Helper for regen/update:

```go
// in render_test.go
var update = flag.Bool("update", false, "update golden files")
```

Implementer pattern: write tests that load fixture, render, compare to golden; with `-update` flag, write the golden file instead of comparing.

- [ ] **Step 6: Write `render_test.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package templates

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hughobrien/breezyd/pkg/breezy"
)

var update = flag.Bool("update", false, "update golden files")

func loadSnapshot(t *testing.T, name string) breezy.Snapshot {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var s breezy.Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDeviceCardGolden(t *testing.T) {
	cases := []string{
		"snapshot_regen", "snapshot_manual", "snapshot_settling",
		"snapshot_sensor_alert", "snapshot_schedule_alert",
		"snapshot_energy_error", "snapshot_no_energy",
	}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			snap := loadSnapshot(t, name)
			var sb strings.Builder
			if err := DeviceCard(snap).Render(context.Background(), &sb); err != nil {
				t.Fatal(err)
			}
			got := sb.String()
			goldenPath := filepath.Join("testdata", "golden_"+strings.TrimPrefix(name, "snapshot_")+".html")
			if *update {
				if err := os.WriteFile(goldenPath, []byte(got), 0644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(want) != got {
				t.Errorf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, string(want))
			}
		})
	}
}
```

- [ ] **Step 7: Generate goldens, manually inspect, commit**

```sh
go test ./cmd/breezyd/ui/templates/... -run TestDeviceCardGolden -update
# Manually open each testdata/golden_*.html in a browser, verify it looks like the current dashboard's rendering for that state
go test ./cmd/breezyd/ui/templates/... -run TestDeviceCardGolden
```

If any golden is wrong, fix the template, regenerate, re-inspect.

- [ ] **Step 8: Commit**

```sh
git add cmd/breezyd/ui/templates/
git commit -m "ui: add device_card + sub-block templates with golden tests (#14)"
```

---

### Task 7: Build `theme_picker.templ` + `layout.templ` (page shell)

**Goal:** `layout.templ` renders the full page shell — `<html>`, `<head>` with FOUC script + CSS link + htmx + response-targets script tags, `<body>` with theme picker, error banner slot, and the `hx-get="/ui/devices"` bootstrap. `theme_picker.templ` is a sub-component used inside the layout.

**Files:**
- Create: `cmd/breezyd/ui/templates/layout.templ`
- Create: `cmd/breezyd/ui/templates/theme_picker.templ`
- Modify: `cmd/breezyd/ui/templates/render_test.go` — add a layout golden

**Acceptance Criteria:**
- [ ] Rendered shell contains the inline FOUC `<script>` reading `localStorage.getItem("theme")` BEFORE the `<link>` element (DOM-order check)
- [ ] CSS link uses the runtime-computed style hash via `styleHash` parameter to the layout component
- [ ] Two `<script>` tags load htmx and response-targets from `/ui/vendor/`
- [ ] Theme picker is a `<details>` with `<summary><h1>breezy</h1></summary>` and three buttons inside
- [ ] Buttons use inline SVG icons (sun, moon, half-sun-half-moon)
- [ ] No external network dependencies; everything served from `/ui/`

**Verify:**
```sh
just generate && go test ./cmd/breezyd/ui/templates/... -run TestLayout
```
Expected: PASS.

**Steps:**

- [ ] **Step 1: Pick three SVG icons**

Source: Heroicons (MIT). Use the 24x24 `outline` variants for `sun`, `moon`, and (for "auto") the `computer-desktop` icon. Inline each as ~10-line SVG markup directly in `theme_picker.templ` to avoid an asset request.

- [ ] **Step 2: Write `theme_picker.templ`**

```go
package templates

templ ThemePicker() {
	<details class="theme-picker">
		<summary><h1>breezy</h1></summary>
		<div class="theme-popout" role="group" aria-label="Theme">
			<button type="button" data-theme-set="light" aria-label="Light">
				@iconSun()
			</button>
			<button type="button" data-theme-set="dark" aria-label="Dark">
				@iconMoon()
			</button>
			<button type="button" data-theme-set="auto" aria-label="System">
				@iconAuto()
			</button>
		</div>
	</details>
}

templ iconSun() {
	<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.5">
		<!-- ... heroicons outline/sun path ... -->
	</svg>
}

templ iconMoon() {
	<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.5">
		<!-- ... heroicons outline/moon path ... -->
	</svg>
}

templ iconAuto() {
	<svg viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" stroke-width="1.5">
		<!-- ... heroicons outline/computer-desktop path ... -->
	</svg>
}
```

Fill in the actual SVG `<path>` elements from Heroicons' source (`@heroicons/24/outline/sun.svg` etc).

- [ ] **Step 3: Write `layout.templ`**

```go
package templates

type LayoutData struct {
	StyleHash    string
	HTMXVersion  string  // "2.0.4"
}

templ Layout(d LayoutData) {
	<!DOCTYPE html>
	<html lang="en">
		<head>
			<meta charset="utf-8">
			<meta name="viewport" content="width=device-width, initial-scale=1">
			<title>breezy</title>
			<script>
				var t = localStorage.getItem("theme");
				if (t === "light" || t === "dark") {
					document.documentElement.setAttribute("data-theme", t);
				}
			</script>
			<link rel="stylesheet" href={ "/ui/style-" + d.StyleHash + ".css" }>
			<script src={ "/ui/vendor/htmx-" + d.HTMXVersion + ".min.js" }></script>
			<script src={ "/ui/vendor/htmx-response-targets-" + d.HTMXVersion + ".min.js" }></script>
		</head>
		<body hx-ext="response-targets" hx-target-401="#global-error-banner" hx-target-404="#global-error-banner" hx-target-5xx="#global-error-banner" hx-target-422="closest .device-card">
			@ThemePicker()
			<div id="global-error-banner" aria-live="polite"></div>
			<div id="device-list" hx-get="/ui/devices" hx-trigger="load, every 5s[document.visibilityState === 'visible']" hx-swap="innerHTML"></div>
			<script>
				// Theme picker handlers (see Task 8 for full content)
			</script>
			<script src="/ui/legacy.js"></script>
		</body>
	</html>
}
```

The `<script>` at the bottom of `<body>` is intentionally empty in this task; Task 8 fills it in.

- [ ] **Step 4: Add layout golden test**

In `render_test.go`:

```go
func TestLayout(t *testing.T) {
	var sb strings.Builder
	d := LayoutData{StyleHash: "abc123def0", HTMXVersion: "2.0.4"}
	if err := Layout(d).Render(context.Background(), &sb); err != nil {
		t.Fatal(err)
	}
	got := sb.String()
	// FOUC script must appear before the stylesheet link
	scriptIdx := strings.Index(got, "localStorage.getItem")
	linkIdx := strings.Index(got, `<link rel="stylesheet"`)
	if scriptIdx < 0 || linkIdx < 0 || scriptIdx > linkIdx {
		t.Fatalf("FOUC script not before stylesheet link\n%s", got)
	}
	wantContains := []string{
		`/ui/style-abc123def0.css`,
		`/ui/vendor/htmx-2.0.4.min.js`,
		`/ui/vendor/htmx-response-targets-2.0.4.min.js`,
		`hx-ext="response-targets"`,
		`hx-target-422="closest .device-card"`,
		`hx-trigger="load, every 5s[document.visibilityState === 'visible']"`,
		`<summary><h1>breezy</h1></summary>`,
	}
	for _, w := range wantContains {
		if !strings.Contains(got, w) {
			t.Errorf("layout missing %q", w)
		}
	}
}
```

- [ ] **Step 5: Verify**

```sh
just generate
go test ./cmd/breezyd/ui/templates/... -v
```

Expected: all golden tests pass plus `TestLayout`.

- [ ] **Step 6: Commit**

```sh
git add cmd/breezyd/ui/templates/
git commit -m "ui: add layout + theme_picker templates (#14)"
```

---

### Task 8: Add theme picker JS (popout open/close + theme set)

**Goal:** Clicking a theme button writes `localStorage` and applies `data-theme`. Clicking outside the picker closes the popout. ~20 lines of JS, lives inline in the layout's bottom `<script>`.

**Files:**
- Modify: `cmd/breezyd/ui/templates/layout.templ`
- Modify: `tests/ui/dashboard.spec.ts` — add picker tests

**Acceptance Criteria:**
- [ ] Clicking `[data-theme-set="dark"]` sets `document.documentElement.dataset.theme = "dark"` and `localStorage.theme = "dark"`
- [ ] Clicking `[data-theme-set="auto"]` removes the attribute and `localStorage.theme`
- [ ] Clicking outside the `.theme-picker` element closes the `<details>` (sets `open=false`)
- [ ] Clicking a theme button also closes the popout
- [ ] Page reload preserves the choice

**Verify:**
```sh
just test-ui -- --grep "theme picker"
```
Expected: PASS.

**Steps:**

- [ ] **Step 1: Add the JS to `layout.templ`**

Replace the empty `<script></script>` at the bottom of `<body>` with:

```html
<script>
(function() {
  var picker = document.querySelector('.theme-picker');
  if (!picker) return;
  picker.addEventListener('click', function(ev) {
    var target = ev.target.closest('[data-theme-set]');
    if (!target) return;
    var theme = target.getAttribute('data-theme-set');
    if (theme === 'auto') {
      document.documentElement.removeAttribute('data-theme');
      localStorage.removeItem('theme');
    } else {
      document.documentElement.setAttribute('data-theme', theme);
      localStorage.setItem('theme', theme);
    }
    picker.open = false;
  });
  document.addEventListener('click', function(ev) {
    if (picker.open && !picker.contains(ev.target)) {
      picker.open = false;
    }
  });
})();
</script>
```

This is intentionally inline (not in a separate `.js` file) because it's small and tightly coupled to the picker markup.

- [ ] **Step 2: Add Playwright tests**

In `tests/ui/dashboard.spec.ts`:

```typescript
test("theme picker: clicking dark sets data-theme and localStorage", async ({ page }) => {
  await mockBootstrap(page);
  await page.goto("/");
  await page.click(".theme-picker summary");
  await page.click('[data-theme-set="dark"]');
  expect(await page.getAttribute("html", "data-theme")).toBe("dark");
  expect(await page.evaluate(() => localStorage.getItem("theme"))).toBe("dark");
});

test("theme picker: clicking auto removes the attribute", async ({ page }) => {
  await mockBootstrap(page);
  await page.goto("/");
  await page.click(".theme-picker summary");
  await page.click('[data-theme-set="dark"]');
  await page.click(".theme-picker summary");
  await page.click('[data-theme-set="auto"]');
  expect(await page.getAttribute("html", "data-theme")).toBeNull();
  expect(await page.evaluate(() => localStorage.getItem("theme"))).toBeNull();
});

test("theme picker: outside click closes popout", async ({ page }) => {
  await mockBootstrap(page);
  await page.goto("/");
  await page.click(".theme-picker summary");
  expect(await page.evaluate(() => document.querySelector(".theme-picker").open)).toBe(true);
  await page.click("body");
  expect(await page.evaluate(() => document.querySelector(".theme-picker").open)).toBe(false);
});

test("theme picker: choice survives reload", async ({ page, context }) => {
  await mockBootstrap(page);
  await page.goto("/");
  await page.click(".theme-picker summary");
  await page.click('[data-theme-set="dark"]');
  await page.reload();
  expect(await page.getAttribute("html", "data-theme")).toBe("dark");
});

test("dark mode: no FOUC — first paint already dark when localStorage seeded", async ({ browser }) => {
  const context = await browser.newContext({ colorScheme: "light" });
  await context.addInitScript(() => localStorage.setItem("theme", "dark"));
  const page = await context.newPage();
  await mockBootstrap(page);
  await page.goto("/", { waitUntil: "domcontentloaded" });
  // At domcontentloaded the FOUC script has run; data-theme should already be set.
  expect(await page.getAttribute("html", "data-theme")).toBe("dark");
  const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
  expect(bg).toBe("rgb(13, 13, 16)");
  await context.close();
});
```

These use the existing `page.route()` mock pattern (PR 3 will rewrite them).

- [ ] **Step 3: Verify**

```sh
just generate
just test-ui -- --grep "theme picker|dark mode"
```

Expected: all five new tests pass.

- [ ] **Step 4: Commit**

```sh
git add cmd/breezyd/ui/templates/layout.templ tests/ui/dashboard.spec.ts
git commit -m "ui: theme picker JS — popout + persistence + FOUC test (#14)"
```

---

### Task 9: Wire `GET /ui/devices`, `GET /ui/devices/{name}/card`, and extract legacy write JS

**Goal:** Both read routes serve real htmx fragments rendered from cached snapshots. The write-side JS that today lives inside `index.html`'s `<script>` block is extracted to a new `cmd/breezyd/ui/legacy.js`, embedded, and served at `/ui/legacy.js` so writes keep working through the end of PR 1. Layout already references `/ui/legacy.js` (added in Task 7).

**Files:**
- Create: `cmd/breezyd/handlers_ui_read.go`
- Create: `cmd/breezyd/handlers_ui_read_test.go`
- Create: `cmd/breezyd/ui/legacy.js` (extracted from `index.html`)
- Modify: `cmd/breezyd/server.go`
- Modify: `cmd/breezyd/ui_assets.go` — `getIndex` now renders `Layout` instead of substituting strings; new `getLegacyJS` handler embeds and serves `legacy.js`

**Acceptance Criteria:**
- [ ] `GET /ui/devices` returns `Content-Type: text/html`, `Cache-Control: no-store`, body is the rendered `DeviceList` for all configured devices' current snapshots
- [ ] `GET /ui/devices/{name}/card` returns the rendered `DeviceCard` for that device, or 404 if name unknown
- [ ] `GET /` serves the layout-rendered shell (no more `STYLEHASH` string substitution)
- [ ] `GET /ui/legacy.js` returns the extracted write-side JS with `Cache-Control: no-store` (we'll be deleting it soon, no point caching)
- [ ] Manually opening the dashboard and clicking a write control (e.g., power toggle) still works — the legacy JS handler routes to `/v1/...`
- [ ] Tests cover happy path, 404, and `Cache-Control` headers

**Verify:**
```sh
go test ./cmd/breezyd -run "TestUIRead" -v && \
just build && \
# manual: load dashboard, click power, observe POST to /v1/devices/{name}/power
```
Expected: PASS.

**Steps:**

- [ ] **Step 1: Write `handlers_ui_read.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"net/http"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
	"github.com/hughobrien/breezyd/pkg/breezy"
)

func (h *Handler) getUIDeviceList(w http.ResponseWriter, r *http.Request) {
	snaps := h.collectSnapshots()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := templates.DeviceList(snaps).Render(r.Context(), w); err != nil {
		// Already wrote headers; can't change status. Log and bail.
		h.log.Error("render DeviceList", "err", err)
	}
}

func (h *Handler) getUIDeviceCard(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	snap, ok := h.snapshotFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := templates.DeviceCard(snap).Render(r.Context(), w); err != nil {
		h.log.Error("render DeviceCard", "device", name, "err", err)
	}
}

// collectSnapshots returns snapshots for all configured devices in name order.
// Implementer: this likely already exists in server.go for the JSON list
// endpoint — reuse that function rather than duplicating.
func (h *Handler) collectSnapshots() []breezy.Snapshot {
	// ...
	return nil // implementer fills in
}

// snapshotFor returns the cached snapshot for name, or false if missing.
func (h *Handler) snapshotFor(name string) (breezy.Snapshot, bool) {
	// ...
	return breezy.Snapshot{}, false
}
```

The `collectSnapshots` and `snapshotFor` helpers should reuse the existing cache-read logic in `server.go` / `handlers_device.go`. If they don't already exist as methods, extract them — pure refactor, no behavior change for the JSON path.

- [ ] **Step 2: Update `getIndex` in `ui_assets.go`**

Replace the string-substitution stopgap with proper template rendering:

```go
func (h *Handler) getIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	d := templates.LayoutData{StyleHash: styleHash, HTMXVersion: htmxVersion}
	if err := templates.Layout(d).Render(r.Context(), w); err != nil {
		h.log.Error("render Layout", "err", err)
	}
}
```

Add a constant `htmxVersion = "2.0.4"` in `ui_assets.go` (matches the vendored filenames).

- [ ] **Step 3: Extract legacy write JS to `cmd/breezyd/ui/legacy.js`**

Open `cmd/breezyd/ui/index.html` and identify everything inside the existing `<script>...</script>` block that handles **writes** (event listeners for click/keydown/input/change that call `fetch(.../v1/...)`). Move that JS verbatim into a new `cmd/breezyd/ui/legacy.js`. Delete the now-empty event delegation infrastructure that only served reads (the `refreshAll` poll, the per-device card construction, etc.).

Add to `cmd/breezyd/ui_assets.go`:

```go
//go:embed ui/legacy.js
var legacyJS []byte

func (h *Handler) getLegacyJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(legacyJS)
}
```

This file is temporary — Task 21 deletes it after every write migrates to htmx.

- [ ] **Step 4: Update `server.go` mux**

```go
mux.HandleFunc("GET /{$}", h.getIndex)
mux.HandleFunc("GET /ui/devices", h.getUIDeviceList)
mux.HandleFunc("GET /ui/devices/{name}/card", h.getUIDeviceCard)
mux.HandleFunc("GET /ui/style-{hash}.css", h.getStyle)
mux.HandleFunc("GET /ui/vendor/{file}", h.getVendor)
mux.HandleFunc("GET /ui/legacy.js", h.getLegacyJS)
```

- [ ] **Step 5: Write `handlers_ui_read_test.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIReadDeviceList(t *testing.T) {
	h := newTestHandlerWithFakeDevices(t, "alpha", "bravo")  // existing helper or write one
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/ui/devices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Errorf("content-type: %s", got)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("cache-control: %s", got)
	}
	body, _ := io.ReadAll(resp.Body)
	for _, name := range []string{"alpha", "bravo"} {
		if !strings.Contains(string(body), `data-device="`+name+`"`) {
			t.Errorf("body missing device %s", name)
		}
	}
}

func TestUIReadDeviceCard(t *testing.T) {
	h := newTestHandlerWithFakeDevices(t, "alpha")
	srv := httptest.NewServer(h.mux())
	defer srv.Close()

	t.Run("happy", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/ui/devices/alpha/card")
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	})

	t.Run("404", func(t *testing.T) {
		resp, _ := http.Get(srv.URL + "/ui/devices/nope/card")
		defer resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
	})
}
```

- [ ] **Step 6: Verify**

```sh
just generate
just check
just build
# Manual smoke test:
./breezyd --config /tmp/test.toml &
# Open http://localhost:<port>/ in a browser, click power toggle, watch network panel:
# expect POST /v1/devices/{name}/power (legacy.js writes still target /v1/)
# expect GET /ui/devices every 5s (htmx read poll)
```

Expected: full check passes; both manual paths work.

- [ ] **Step 7: Commit**

```sh
git add cmd/breezyd/handlers_ui_read.go cmd/breezyd/handlers_ui_read_test.go cmd/breezyd/server.go cmd/breezyd/ui_assets.go cmd/breezyd/ui/legacy.js
git commit -m "ui: serve /ui/devices fragments + extract legacy write JS to legacy.js (#14)"
```

---

### Task 10: Stop embedding `index.html` (full deletion deferred to PR3)

**Goal:** The daemon no longer embeds or serves `index.html` — Layout has fully replaced it. The file stays on disk as a Playwright fixture until PR3 rewrites the test suite against a real daemon and the file can finally be deleted.

**Files:**
- Modify: `cmd/breezyd/ui_assets.go` — remove `//go:embed ui/index.html` and the `indexHTML` variable
- Modify: `cmd/breezyd/ui/index.html` — add a header comment marking it test-fixture-only

**Why deferred deletion:** `tests/ui/dashboard.spec.ts` uses `readFileSync` of `index.html` as a `page.route()` mock body for ~58 tests that exercise the JS-rendered SPA's behaviors via `legacy.js`. Moving those tests to use `LAYOUT_HTML` (rendered via `cmd/render-layout`) requires also mocking `/ui/devices` to return rendered card fragments — which is essentially the PR3 test rewrite (real daemon vs. canned mocks). Doing it now duplicates effort.

**Acceptance Criteria:**
- [ ] `//go:embed ui/index.html` and the `indexHTML` variable are gone from `ui_assets.go`
- [ ] `cmd/breezyd/ui/index.html` still exists, with a header comment marking it as a test fixture
- [ ] `just test-ui` passes (the file is still readable from disk by tests)
- [ ] `nm breezyd | grep -i indexhtml` returns nothing (the binary no longer carries the file)

**Verify:**
```sh
just check && just test-ui
```
Expected: green.

**Steps:**

- [ ] **Step 1: Decide: delete `index.html` or keep it as a stub?**

Since `getIndex` now renders `templates.Layout` directly, `index.html` is dead. **Delete it.**

- [ ] **Step 2: Delete the file and the embed directive**

```sh
git rm cmd/breezyd/ui/index.html
```

In `ui_assets.go`, remove:
```go
//go:embed ui/index.html
var indexHTML []byte
```

- [ ] **Step 3: Re-build, re-test, manual smoke**

```sh
just build
# Run breezyd against fakedevice; open http://localhost:<port>/ in a real browser
# Verify: dashboard loads, cards show, every 5s the network panel shows GET /ui/devices, all read-side info is correct
```

- [ ] **Step 4: Verify automated tests still pass**

```sh
just check
just test-ui
```

The existing Playwright write-path tests still talk to `/v1/` (since writes haven't migrated yet), so they should still pass.

- [ ] **Step 5: Commit**

```sh
git add -u cmd/breezyd/ui_assets.go
git commit -m "ui: delete index.html stub; layout now rendered by templ (#14)"
```

---

### Task 11: Open PR 1

**Goal:** PR 1 lands with all the above. CI green. Reviewer mapping is clear.

**Files:**
- (none — workflow task)

**Acceptance Criteria:**
- [ ] CI green on the PR (matches `just ci`)
- [ ] PR description summarizes the spec sections this PR delivers
- [ ] PR description explicitly notes write paths still go through `/v1/` (intentional, PR 2's scope)

**Verify:**
```sh
gh pr view --json statusCheckRollup
```
Expected: all checks green.

**Steps:**

- [ ] **Step 1: Push branch**

```sh
git push -u origin <branch-name>
```

- [ ] **Step 2: Open PR**

```sh
gh pr create --title "ui: htmx read path + CSS extract/tokenize + dark mode (#14, PR 1/3)" --body "$(cat <<'EOF'
## Summary

- Read-only dashboard now renders server-side via `templ` fragments, swapped in by htmx.
- CSS extracted to `style.css` with semantic color tokens.
- Dark mode added: auto by default (`prefers-color-scheme`), manual override via theme picker on the `breezy` title.
- JSON `/v1/` API completely unchanged.
- Write paths still go through the legacy JS calling `/v1/` — that's PR 2's scope.

## Spec

- `docs/superpowers/specs/2026-05-06-htmx-migration-design.md`
- Issue #14

## Test plan

- [x] `just ci` green
- [x] Dashboard renders identically in light mode (visual diff against pre-change screenshot)
- [x] Dark mode toggleable via theme picker; survives reload
- [x] Polling cadence unchanged (every 5s, paused when tab hidden)
- [x] Existing Playwright suite still passes against `/v1/` write endpoints

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

# PR 2 — Migrate writes

End state: every write control on the dashboard goes through `/ui/...`, returns the rendered card fragment, and htmx swaps it in. Legacy JS write event listeners are deleted as each control migrates.

---

### Task 12: Establish the write-handler shim pattern

**Goal:** A single canonical write handler shape, documented and exemplified by the simplest write (power on/off). Subsequent migration tasks reuse the pattern.

**Files:**
- Create: `cmd/breezyd/handlers_ui_write.go`
- Create: `cmd/breezyd/handlers_ui_write_test.go`
- Modify: `cmd/breezyd/server.go`

**Acceptance Criteria:**
- [ ] `POST /ui/devices/{name}/power` accepts form-urlencoded `on=true|false`, calls existing ops, returns rendered `DeviceCard`
- [ ] On validation error returns 422 with `DeviceCard` containing inline error
- [ ] On backend error returns 502 with `error_banner.templ` fragment
- [ ] On unknown device returns 404 with banner
- [ ] On `breezy.ErrAuth` returns 401 with banner
- [ ] Tests cover all four error classes plus happy path

**Verify:**
```sh
go test ./cmd/breezyd -run TestUIWritePower -v
```
Expected: PASS.

**Steps:**

- [ ] **Step 1: Write the shim pattern in `handlers_ui_write.go`**

```go
// SPDX-License-Identifier: GPL-3.0-or-later

// HTML-fragment write endpoints under /ui/devices/{name}/...
// Each handler:
//   1. Resolves the device by name (404 if unknown).
//   2. Parses form params (422 on bad input).
//   3. Calls the existing pkg/breezy/ops write path.
//   4. On success, returns the rendered DeviceCard.
//   5. On backend / auth error, returns the rendered error_banner.
package main

import (
	"errors"
	"net/http"

	"github.com/hughobrien/breezyd/cmd/breezyd/ui/templates"
	"github.com/hughobrien/breezyd/pkg/breezy"
)

// uiWriteError translates a write error into an HTTP status + rendered fragment.
// Returns true if the error was handled (response written); caller should return.
func (h *Handler) uiWriteError(w http.ResponseWriter, r *http.Request, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	switch {
	case errors.Is(err, breezy.ErrAuth):
		w.WriteHeader(http.StatusUnauthorized)
		_ = templates.ErrorBanner("auth", "Device authentication failed").Render(r.Context(), w)
	default:
		w.WriteHeader(http.StatusBadGateway)
		_ = templates.ErrorBanner("backend", err.Error()).Render(r.Context(), w)
	}
}

// uiRenderCard writes the rendered DeviceCard for a successful write.
func (h *Handler) uiRenderCard(w http.ResponseWriter, r *http.Request, name string) {
	snap, ok := h.snapshotFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.DeviceCard(snap).Render(r.Context(), w)
}

// uiValidationError writes a 422 with the rendered card containing an inline banner.
func (h *Handler) uiValidationError(w http.ResponseWriter, r *http.Request, name, msg string) {
	snap, ok := h.snapshotFor(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnprocessableEntity)
	_ = templates.DeviceCard(snap.WithError(msg)).Render(r.Context(), w)
}

// postUIPower toggles a device on/off.
func (h *Handler) postUIPower(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	dev, ok := h.deviceByName(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.uiValidationError(w, r, name, "bad form encoding")
		return
	}
	on := r.FormValue("on") == "true"
	if err := dev.Ops.SetPower(r.Context(), on); err != nil {
		h.uiWriteError(w, r, err)
		return
	}
	h.uiRenderCard(w, r, name)
}
```

`Snapshot.WithError(msg string) Snapshot` is a small helper to add to `pkg/breezy` (or use a wrapping struct in templates) so the card knows to render an inline banner. If adding to `pkg/breezy` is intrusive, define `type cardData struct { Snapshot breezy.Snapshot; ErrorMsg string }` in the templates package and update `DeviceCard` to take that instead. Implementer's call — choose the smaller surface change.

- [ ] **Step 2: Wire the route**

In `server.go`:

```go
mux.HandleFunc("POST /ui/devices/{name}/power", h.postUIPower)
```

- [ ] **Step 3: Update `device_card.templ` to render an inline error banner if present**

If the `cardData` wrapper approach was chosen in step 1:

```go
templ DeviceCard(d cardData) {
	<article class="device-card" data-device={ d.Snapshot.Name }>
		if d.ErrorMsg != "" {
			<div class="card-error" role="alert">{ d.ErrorMsg }</div>
		}
		// ... rest
	</article>
}
```

Update all earlier callers of `DeviceCard` to pass `cardData{Snapshot: snap}`. Refresh the golden tests.

- [ ] **Step 4: Write tests**

```go
func TestUIWritePower(t *testing.T) {
	t.Run("happy", func(t *testing.T) {
		h := newTestHandlerWithFakeDevices(t, "alpha")
		srv := httptest.NewServer(h.mux())
		defer srv.Close()
		resp, _ := http.PostForm(srv.URL+"/ui/devices/alpha/power", url.Values{"on": {"true"}})
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status: %d", resp.StatusCode)
		}
		// Body should contain the device card markup
	})

	t.Run("unknown device → 404", func(t *testing.T) { /* ... */ })
	t.Run("bad form → 422", func(t *testing.T) { /* ... */ })
	t.Run("backend error → 502", func(t *testing.T) {
		// Use a fake device wired to fail the write
	})
	t.Run("auth error → 401", func(t *testing.T) {
		// Use a fake device wired to return ErrAuth
	})
}
```

- [ ] **Step 5: Verify**

```sh
just generate
just check
```

- [ ] **Step 6: Commit**

```sh
git add cmd/breezyd/handlers_ui_write.go cmd/breezyd/handlers_ui_write_test.go cmd/breezyd/server.go cmd/breezyd/ui/templates/
git commit -m "ui: write-handler shim pattern + power endpoint (#14)"
```

---

### Task 13: Migrate power toggle in template + delete its JS

**Goal:** The power toggle in `device_card.templ` uses `hx-post="/ui/devices/{name}/power"` and the corresponding click handler is removed from the legacy JS.

**Files:**
- Modify: `cmd/breezyd/ui/templates/device_card.templ` — power button gets htmx attributes
- Modify: `cmd/breezyd/ui/legacy.js` — delete the power click handler

**Acceptance Criteria:**
- [ ] Clicking power posts to `/ui/devices/{name}/power` (verify in DevTools network panel)
- [ ] No legacy click handler for power remains in `legacy.js`
- [ ] Existing Playwright power test still passes (change the mocked route from `/v1/devices/{name}/power` to `/ui/devices/{name}/power` — minimal test churn)

**Steps:**

- [ ] **Step 1: Update `device_card.templ` power button**

Replace the current power button markup with:

```html
<button type="button"
        class="power-btn"
        hx-post={ "/ui/devices/" + d.Snapshot.Name + "/power" }
        hx-vals={ `{"on": ` + strconv.FormatBool(!d.Snapshot.Power) + `}` }
        hx-target="closest .device-card"
        hx-disabled-elt="this">
    { powerLabel(d.Snapshot.Power) }
</button>
```

- [ ] **Step 2: Delete the JS click handler for power**

In `cmd/breezyd/ui/legacy.js` (or wherever legacy write JS now lives), find the `case "power"` (or equivalent) inside the document click delegator and remove it. Delete any helper functions only used by that branch.

- [ ] **Step 3: Update existing Playwright test**

In `tests/ui/dashboard.spec.ts`, find `test("power click: POSTs the inverse of the current state", ...)` and change the `page.route()` URL to `/ui/devices/*/power`. The assertion on POST body shape changes from JSON to form-urlencoded, but the test's intent stays the same.

- [ ] **Step 4: Verify**

```sh
just check && just test-ui -- --grep "power"
```

- [ ] **Step 5: Commit**

```sh
git add cmd/breezyd/ui/templates/device_card.templ cmd/breezyd/ui/legacy.js tests/ui/dashboard.spec.ts
git commit -m "ui: migrate power toggle to htmx (#14)"
```

---

### Task 14: Migrate mode select

**Goal:** Mode buttons (regeneration / supply / extract / ventilation) use `hx-post`. JS for mode click is gone.

**Files:**
- Modify: `cmd/breezyd/ui/templates/device_card.templ`
- Create: handler `postUIMode` in `cmd/breezyd/handlers_ui_write.go`
- Modify: `cmd/breezyd/server.go` (route)
- Modify: `cmd/breezyd/ui/legacy.js` (delete mode handler)
- Modify: `tests/ui/dashboard.spec.ts` (update mocked URL)

**Acceptance Criteria:**
- [ ] Each mode button posts `mode=<name>` to `/ui/devices/{name}/mode`
- [ ] Mode-related Playwright tests pass
- [ ] No mode-related JS remains in `legacy.js`

**Steps:** Same shape as Task 13 — write handler matching the shim pattern, update template, delete JS, update test mock URL, verify, commit.

```sh
git commit -m "ui: migrate mode select to htmx (#14)"
```

---

### Task 15: Migrate manual speed slider

**Goal:** Speed slider posts via `hx-trigger="change delay:200ms"`. JS for slider input/change is gone.

**Files:** Same shape as Task 14 + handler `postUISpeed`.

**Acceptance Criteria:**
- [ ] Slider debounces 200ms before posting
- [ ] `hx-disabled-elt="this"` grays out during write
- [ ] Speed-related tests pass

**Steps:** Follow the shim pattern. Note: `hx-trigger="change delay:200ms"` and `hx-vals='{"pct": <slider value>}'`. Update the existing slider test for the new URL and form-encoded body.

```sh
git commit -m "ui: migrate manual speed slider to htmx (#14)"
```

---

### Task 16: Migrate sensor toggles

**Goal:** Each of the four sensor enable checkboxes posts to `/ui/devices/{name}/sensor-toggle`.

**Files:** Same shape. Handler `postUISensorToggle`.

**Acceptance Criteria:**
- [ ] Each checkbox toggles on `change` event
- [ ] `hx-vals='{"kind": "humidity|co2|voc|temp", "enabled": <bool>}'`
- [ ] Sensor-toggle tests pass

```sh
git commit -m "ui: migrate sensor toggles to htmx (#14)"
```

---

### Task 17: Migrate threshold input editor

**Goal:** Inline threshold editor commits on blur or Enter, PUTs to `/ui/devices/{name}/threshold`. Optimistic-edit JS is gone.

**Files:** Same shape. Handler `putUIThreshold`. The threshold sub-template (`sensor_threshold.templ`) takes an `editing` flag — flipped by another small endpoint or by client-side click that toggles a class. Probably simplest: server renders both states, swap target on the row.

**Acceptance Criteria:**
- [ ] Click value cell → editor opens (server-rendered editing variant of the row)
- [ ] Type → wait blur or Enter → PUT and swap to read variant
- [ ] Cancel button reverts without posting
- [ ] All threshold tests pass

```sh
git commit -m "ui: migrate threshold inline editor to htmx (#14)"
```

---

### Task 18: Migrate schedule editor

**Goal:** Schedule save button PUTs the entire entries array to `/ui/devices/{name}/schedule`. Editor opens via swap to the editing variant of the schedule block.

**Files:** Same shape. Handler `putUISchedule`. Schedule entries form encoding: array via repeated `entries[i].at`, `entries[i].action`, `entries[i].pct` form fields, OR JSON body — pick whichever is simpler. (Form encoding is more htmx-idiomatic; but a JSON body via `hx-ext='json-enc'` extension is fine if already vendored. Default to form-encoded with indexed names and a server-side parser.)

**Acceptance Criteria:**
- [ ] Editor opens on click
- [ ] Save validates (no duplicate `at`, valid action, pct in range) — 422 with inline error if invalid
- [ ] Save PUTs and swaps to read variant
- [ ] All schedule tests pass

```sh
git commit -m "ui: migrate schedule editor to htmx (#14)"
```

---

### Task 19: Migrate heater toggle

**Goal:** Heater button posts to `/ui/devices/{name}/heater`. Heater JS is gone.

**Files:** Same shape. Handler `postUIHeater`.

```sh
git commit -m "ui: migrate heater toggle to htmx (#14)"
```

---

### Task 20: Migrate reset-filter and reset-faults buttons

**Goal:** Both buttons post to their respective `/ui/...` endpoints.

**Files:** Same shape. Handlers `postUIResetFilter` and `postUIResetFaults`.

```sh
git commit -m "ui: migrate reset-filter and reset-faults to htmx (#14)"
```

---

### Task 21: Delete `legacy.js` and confirm zero residual JS event listeners

**Goal:** `legacy.js` is empty or deleted. The only JS in the dashboard is the FOUC script and the theme picker handler in `layout.templ`.

**Files:**
- Delete: `cmd/breezyd/ui/legacy.js`
- Modify: `cmd/breezyd/ui/templates/layout.templ` (remove the `<script src="/ui/legacy.js">` if present)
- Modify: `cmd/breezyd/ui_assets.go` (remove embed + serve route for legacy.js)

**Acceptance Criteria:**
- [ ] `cmd/breezyd/ui/legacy.js` does not exist
- [ ] Page DOM has no `addEventListener` calls except inside the theme picker block
- [ ] All write tests still pass

**Verify:**
```sh
just check && just test-ui
```

- [ ] **Step 1: Verify legacy.js is empty**

```sh
wc -l cmd/breezyd/ui/legacy.js
```
If non-zero, find what's left and migrate or remove it.

- [ ] **Step 2: Delete the file and serve route**

```sh
git rm cmd/breezyd/ui/legacy.js
```

In `ui_assets.go`, remove the embed directive and `getLegacyJS` handler. In `server.go`, remove the route.

- [ ] **Step 3: Verify**

```sh
just check && just test-ui
```

- [ ] **Step 4: Commit**

```sh
git add -u
git commit -m "ui: delete legacy.js — all writes are htmx now (#14)"
```

---

### Task 22: Open PR 2

**Goal:** PR 2 lands. CI green.

**Steps:** Same shape as Task 11. Title: `ui: migrate writes to htmx (#14, PR 2/3)`.

---

# PR 3 — Test rewrite + cleanup + docs

End state: Playwright suite runs against a real `breezyd` spawned against `fakedevice` (with admin surface). Mapping table proves test parity. CLAUDE.md and README updated. Issue #14 closed.

---

### Task 23: Add `fakedevice` admin-control surface (build-tagged)

**Goal:** `pkg/breezy/fakedevice/admin.go` exposes a small HTTP control plane for tests to drive device state, behind `//go:build fakedevice_admin`. Default and release builds exclude it.

**Files:**
- Create: `pkg/breezy/fakedevice/admin.go`
- Create: `pkg/breezy/fakedevice/admin_test.go`

**Acceptance Criteria:**
- [ ] Admin server starts on a configurable port
- [ ] Endpoints for: setting device state, simulating fan-settle, simulating auth failure, simulating UDP timeout
- [ ] Build tag isolates the file: `go build ./pkg/breezy/fakedevice` (no tag) excludes admin code
- [ ] `go build -tags fakedevice_admin ./pkg/breezy/fakedevice` includes it

**Verify:**
```sh
go build ./pkg/breezy/fakedevice && \
go build -tags fakedevice_admin ./pkg/breezy/fakedevice && \
go test -tags fakedevice_admin ./pkg/breezy/fakedevice -run TestAdmin -v
```
Expected: PASS on both builds.

**Steps:**

- [ ] **Step 1: Write `admin.go`**

```go
//go:build fakedevice_admin

// SPDX-License-Identifier: GPL-3.0-or-later

// Test-only HTTP control plane for fakedevice. Excluded from default builds.
package fakedevice

import (
	"encoding/json"
	"net"
	"net/http"
)

type AdminServer struct {
	device *Device
	srv    *http.Server
	addr   string
}

// NewAdminServer binds to a free TCP port and starts an HTTP control plane
// for driving the fake device's state from tests.
func (d *Device) StartAdmin() (*AdminServer, error) {
	mux := http.NewServeMux()
	a := &AdminServer{device: d}
	mux.HandleFunc("PUT /state", a.putState)
	mux.HandleFunc("POST /simulate/fan-settle", a.simulateFanSettle)
	mux.HandleFunc("POST /simulate/auth-failure", a.simulateAuthFailure)
	mux.HandleFunc("POST /simulate/udp-timeout", a.simulateUDPTimeout)
	mux.HandleFunc("POST /reset", a.reset)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	a.addr = ln.Addr().String()
	a.srv = &http.Server{Handler: mux}
	go a.srv.Serve(ln)
	return a, nil
}

func (a *AdminServer) Addr() string { return a.addr }
func (a *AdminServer) Close() error { return a.srv.Close() }

func (a *AdminServer) putState(w http.ResponseWriter, r *http.Request) {
	var s DeviceState
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	a.device.SetState(s)
	w.WriteHeader(204)
}

func (a *AdminServer) simulateFanSettle(w http.ResponseWriter, r *http.Request)  { /* impl */ }
func (a *AdminServer) simulateAuthFailure(w http.ResponseWriter, r *http.Request) { /* impl */ }
func (a *AdminServer) simulateUDPTimeout(w http.ResponseWriter, r *http.Request)  { /* impl */ }
func (a *AdminServer) reset(w http.ResponseWriter, r *http.Request)               { /* impl */ }
```

`DeviceState` and `SetState` may need to be added to `fakedevice` (in a non-build-tagged file) if not already present. Implementer: design the smallest API that lets tests drive the four scenarios. Don't add an "admin" surface beyond what tests need — YAGNI applies.

- [ ] **Step 2: Write `admin_test.go`**

```go
//go:build fakedevice_admin

package fakedevice

import (
	"net/http"
	"strings"
	"testing"
)

func TestAdminPutState(t *testing.T) {
	d := New(/* ... */)
	a, err := d.StartAdmin()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	resp, err := http.DefaultClient.Do(/* PUT a.Addr+"/state" with JSON body */)
	// assert state was applied
}

// ... tests for the other endpoints
```

- [ ] **Step 3: Update `justfile` to test with the tag**

Add a recipe:

```makefile
# Build/test fakedevice with the admin control surface.
test-fakedevice-admin:
    go test -tags fakedevice_admin ./pkg/breezy/fakedevice/... -v
```

And include it in `ci`:

```makefile
ci: lint generate test test-race test-staticcheck test-asan test-msan test-ui test-templ-drift test-fakedevice-admin
```

- [ ] **Step 4: Verify**

```sh
go build ./pkg/breezy/fakedevice
go build -tags fakedevice_admin ./pkg/breezy/fakedevice
just test-fakedevice-admin
```

- [ ] **Step 5: Commit**

```sh
git add pkg/breezy/fakedevice/admin.go pkg/breezy/fakedevice/admin_test.go justfile
git commit -m "fakedevice: add build-tagged admin control surface for UI tests (#14)"
```

---

### Task 24: Add `tests/ui/global-setup.ts` + `global-teardown.ts` + `fixtures.ts`

**Goal:** Playwright spawns `breezyd` against `fakedevice` with the admin tag enabled, allocates a free port, exports the URL via the Playwright config; teardown SIGTERMs the daemon and asserts clean exit.

**Files:**
- Create: `tests/ui/global-setup.ts`
- Create: `tests/ui/global-teardown.ts`
- Create: `tests/ui/fixtures.ts`
- Modify: `tests/ui/playwright.config.ts`

**Acceptance Criteria:**
- [ ] `pnpm test` in `tests/ui/` builds breezyd, spawns it, runs all tests, kills it cleanly
- [ ] Daemon log captured to `tests/ui/test-results/breezyd.log`
- [ ] If daemon doesn't start within 10s, setup fails with a clear error
- [ ] `fixtures.ts` exposes `setDeviceState(name, state)`, `simulateFanSettle(name, ms)`, `simulateAuthFailure(name)`, `reset(name)` — talks to the admin port

**Verify:**
```sh
just test-ui -- --grep "@smoke"  # one trivial smoke test that loads /
```
Expected: PASS, daemon process spawned and killed cleanly.

**Steps:**

- [ ] **Step 1: Write `global-setup.ts`**

```typescript
import { spawn, ChildProcess } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:net";

let daemon: ChildProcess | undefined;

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const srv = createServer().listen(0, () => {
      const port = (srv.address() as { port: number }).port;
      srv.close(() => resolve(port));
    });
    srv.on("error", reject);
  });
}

async function waitForHTTP(url: string, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const r = await fetch(url);
      if (r.status < 500) return;
    } catch {}
    await new Promise(r => setTimeout(r, 100));
  }
  throw new Error(`timeout waiting for ${url}`);
}

export default async () => {
  const httpPort = await freePort();
  const adminPort = await freePort();
  const tmp = mkdtempSync(join(tmpdir(), "breezyd-test-"));
  const configPath = join(tmp, "config.toml");
  writeFileSync(configPath, /* config TOML using fakedevice + admin port */);

  daemon = spawn(
    "go", ["run", "-tags", "fakedevice_admin", "./cmd/breezyd",
      "--config", configPath,
      "--listen", `127.0.0.1:${httpPort}`],
    { cwd: process.env.REPO_ROOT, stdio: ["ignore", "pipe", "pipe"] }
  );

  // Capture logs to test-results/breezyd.log
  const logStream = require("fs").createWriteStream("test-results/breezyd.log");
  daemon.stdout?.pipe(logStream);
  daemon.stderr?.pipe(logStream);

  await waitForHTTP(`http://127.0.0.1:${httpPort}/`, 10_000);

  process.env.BREEZYD_URL = `http://127.0.0.1:${httpPort}`;
  process.env.BREEZYD_ADMIN_URL = `http://127.0.0.1:${adminPort}`;
};

export const __daemon = () => daemon;  // for teardown
```

- [ ] **Step 2: Write `global-teardown.ts`**

```typescript
import { __daemon } from "./global-setup";

export default async () => {
  const d = __daemon();
  if (!d) return;
  d.kill("SIGTERM");
  const code: number = await new Promise(resolve => d.once("exit", c => resolve(c ?? -1)));
  if (code !== 0 && code !== 143) {  // 143 = SIGTERM
    throw new Error(`breezyd exited with ${code}`);
  }
};
```

- [ ] **Step 3: Write `fixtures.ts`**

```typescript
const ADMIN = () => process.env.BREEZYD_ADMIN_URL!;

export async function setDeviceState(name: string, state: object) {
  const r = await fetch(`${ADMIN()}/state?name=${name}`, {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(state),
  });
  if (!r.ok) throw new Error(`setDeviceState: ${r.status} ${await r.text()}`);
}

export async function simulateFanSettle(name: string, ms: number) {
  /* ... */
}
export async function simulateAuthFailure(name: string) { /* ... */ }
export async function reset(name: string) { /* ... */ }

// Convenience presets:
export const presets = {
  asManualSpeed: (name: string, pct: number) =>
    setDeviceState(name, { mode: "manual", manual_pct: pct }),
  asRegenerationMode: (name: string, pct: number) =>
    setDeviceState(name, { mode: "regeneration", manual_pct: pct }),
  // ...
};
```

- [ ] **Step 4: Update `playwright.config.ts`**

```typescript
import { defineConfig } from "@playwright/test";

export default defineConfig({
  globalSetup: "./global-setup.ts",
  globalTeardown: "./global-teardown.ts",
  use: {
    baseURL: process.env.BREEZYD_URL,
  },
  // ...
});
```

- [ ] **Step 5: Add a smoke test**

```typescript
test("@smoke /ui/devices loads", async ({ page }) => {
  await page.goto("/");
  await page.waitForSelector(".device-card");
});
```

- [ ] **Step 6: Verify**

```sh
just test-ui -- --grep "@smoke"
```

- [ ] **Step 7: Commit**

```sh
git add tests/ui/
git commit -m "test: real-daemon Playwright setup against fakedevice (#14)"
```

---

### Task 25: Migrate rendering tests (~50 tests) to real-daemon

**Goal:** Tests that today assert "given snapshot X, render Y" use `fixtures.ts` to drive state and assert on real DOM. Each old test → new test mapping recorded in `MIGRATION.md` (see Task 30).

**Files:**
- Modify: `tests/ui/dashboard.spec.ts` — bulk rewrite the rendering tests

**Acceptance Criteria:**
- [ ] Every test in today's "Pure rendering" category from the spec table has a real-daemon replacement
- [ ] No `page.route("/v1/devices*", ...)` mocks remain in rendering tests (they remain only in error-path tests)

**Steps:**

- [ ] **Step 1: Identify rendering tests**

Re-run the inventory: `grep -nE "^test\(" tests/ui/dashboard.spec.ts`. Categorize each. The spec's table specifies which fall into "Pure rendering."

- [ ] **Step 2: Rewrite each**

Pattern:

```typescript
test("sensors: mocked values appear in the card", async ({ page }) => {
  await reset("alpha");
  await setDeviceState("alpha", { humidity: 55, co2: 800, voc: 120 });
  await page.goto("/");
  const card = page.locator('[data-device="alpha"]');
  await expect(card.getByText("55%")).toBeVisible();
  await expect(card.getByText("800")).toBeVisible();
  // ...
});
```

Each test runs `reset(name)` first to ensure isolation.

- [ ] **Step 3: Verify the entire test run**

```sh
just test-ui
```

- [ ] **Step 4: Commit**

```sh
git add tests/ui/dashboard.spec.ts
git commit -m "test: migrate rendering tests to real-daemon (#14)"
```

---

### Task 26: Migrate POST-shape tests using state-after-write assertions

**Goal:** Tests that assert "click X causes server to receive Y" now assert on resulting state visible in the rendered card, OR on a recorded-call list exposed by the admin surface for the few cases where state-after-write isn't observable.

**Files:**
- Modify: `tests/ui/dashboard.spec.ts`
- Maybe: `pkg/breezy/fakedevice/admin.go` — add `GET /calls` returning the call log if needed

**Acceptance Criteria:**
- [ ] Each test in today's "POST shape" category has a real-daemon replacement using effect-based assertion
- [ ] Tests that genuinely need wire-format assertions use the call log
- [ ] All tests pass

**Steps:**

- [ ] **Step 1: Categorize each POST-shape test**

For each, decide: assert effect (state) or assert wire (call log)?

- "power click POSTs inverse" → effect: after click, snapshot shows opposite power state
- "speed manual slider POSTs once on change" → wire: assert exactly one call to `/v1/.../speed_manual`
- "schedule save PUTs edited table" → effect: after save, GET /ui/devices/{name}/schedule shows the new entries

- [ ] **Step 2: Add `GET /calls` to admin if needed**

If any test requires call-log assertions, add a `GET /calls?name=...` endpoint to `admin.go` that returns the recent calls to the fake device.

- [ ] **Step 3: Rewrite tests**

- [ ] **Step 4: Verify**

```sh
just test-ui
```

- [ ] **Step 5: Commit**

```sh
git add tests/ui/dashboard.spec.ts pkg/breezy/fakedevice/admin.go
git commit -m "test: migrate POST-shape tests to effect/call-log assertions (#14)"
```

---

### Task 27: Migrate state-persistence tests + the optimistic-overlay replacement

**Goal:** `hx-preserve` tests verified end-to-end. The old "optimistic overlay flips Sensors rpms immediately" test is replaced with "after click, swap arrives within 250ms with new rpm rendered."

**Files:**
- Modify: `tests/ui/dashboard.spec.ts`
- Modify: `cmd/breezyd/ui/templates/sensors_block.templ` and `energy_block.templ` to add `hx-preserve` to the `<details>` elements that should retain state

**Acceptance Criteria:**
- [ ] `<details>` elements that today use localStorage or that exhibit "open state survives re-render" behavior carry `hx-preserve`
- [ ] Test "ENERGY block: open state survives the 5s grid re-render" passes with at least 3 consecutive swaps
- [ ] Old optimistic-overlay test is replaced with a swap-latency test

```sh
git commit -m "test: migrate persistence + optimistic-overlay tests (#14)"
```

---

### Task 28: Migrate error-path tests using `page.route()` overrides

**Goal:** Error-path tests intercept `/ui/...` selectively to inject 4xx/5xx responses with canned HTML fragments.

**Files:**
- Modify: `tests/ui/dashboard.spec.ts`

**Acceptance Criteria:**
- [ ] "error toast: 4xx on POST shows the daemon's error text" test uses `page.route()` to return 422 + a canned card fragment with banner
- [ ] "daemon-unreachable: bootstrap failure shows the top error banner" test uses `page.route()` to return 502 on `/ui/devices`
- [ ] All error tests pass

```sh
git commit -m "test: error-path tests via page.route override (#14)"
```

---

### Task 29: Add net-new htmx-swap correctness + dark-mode tests

**Goal:** New test class covering swap precision, polling cadence, hx-preserve robustness, hx-disabled-elt, and latency-budget. Dark-mode tests rewritten in real-daemon style.

**Files:**
- Modify: `tests/ui/dashboard.spec.ts`

**Acceptance Criteria:**
- [ ] Test: writing speed on device A doesn't re-render device B's card
- [ ] Test: every-5s poll fires; pauses on `visibilitychange` to hidden
- [ ] Test: `<details>` with `hx-preserve` retains state across 3+ swaps
- [ ] Test: write-target controls are disabled during the in-flight request
- [ ] Test: write-and-swap completes within 250ms against fakedevice
- [ ] Dark-mode tests now use real daemon (not `page.route()`)

```sh
git commit -m "test: add htmx swap-correctness + dark-mode real-daemon tests (#14)"
```

---

### Task 30: Write the test mapping table

**Goal:** A `tests/ui/MIGRATION.md` lists every old test name (from PR-pre baseline) with its post-migration counterpart or obsolescence reason.

**Files:**
- Create: `tests/ui/MIGRATION.md`

**Acceptance Criteria:**
- [ ] Every one of the 68 baseline tests has a row
- [ ] Total new test count ≥ 68
- [ ] Document committed as part of PR 3

**Steps:**

- [ ] **Step 1: Generate the table**

```sh
# Get baseline test names from the merge-base of PR 3:
git show <pre-PR1-sha>:tests/ui/dashboard.spec.ts | grep -E '^test\(' > /tmp/old.txt
# Get current test names:
grep -E '^test\(' tests/ui/dashboard.spec.ts > /tmp/new.txt
```

- [ ] **Step 2: Write `tests/ui/MIGRATION.md`**

```markdown
# Test migration: page.route() mocks → real-daemon integration

For PR #<n> closing #14. One row per pre-migration test.

| Old test | New test (or reason) |
|---|---|
| bootstrap: cards render for each configured device | bootstrap: cards render for each configured device |
| sensors: mocked values appear in the card | sensors: state-driven values appear |
| ... | ... |
| mode click in manual: optimistic overlay flips Sensors rpms immediately | mode click in manual: post-swap rpms reflect new mode (semantics changed; see spec) |
| ... | ... |
```

- [ ] **Step 3: Verify count parity**

```sh
wc -l /tmp/old.txt /tmp/new.txt
# new must be >= old
```

- [ ] **Step 4: Commit**

```sh
git add tests/ui/MIGRATION.md
git commit -m "docs: test migration mapping table (#14)"
```

---

### Task 31: Update CLAUDE.md, README.md, CHANGELOG.md

**Goal:** Project docs reflect the new build pipeline (`templ generate`), the `/ui/` namespace, and dark mode.

**Files:**
- Modify: `CLAUDE.md`
- Modify: `README.md`
- Modify: `CHANGELOG.md`

**Acceptance Criteria:**
- [ ] CLAUDE.md "Build, test, lint" section includes `just generate` and `templ` requirement
- [ ] CLAUDE.md "Architecture" section describes `/ui/` HTML namespace alongside `/v1/` JSON
- [ ] README's build instructions include `templ generate` (or note that `just build` runs it)
- [ ] CHANGELOG entry for the next release (`1.9.0`?) includes htmx migration + dark mode
- [ ] All Markdown is valid (no broken links)

**Steps:**

- [ ] **Step 1: Update CLAUDE.md**

Add to the "Build, test, lint" recipe table:
- `just generate` — run `templ generate` (codegen for `cmd/breezyd/ui/templates/*.templ`)

Add a paragraph in "Architecture" explaining `/ui/` vs `/v1/` and that templates are generated, not committed.

- [ ] **Step 2: Update README.md**

Replace any reference to "single-page dashboard" with the new architecture summary. Add a development-prerequisites note: "Requires the `templ` CLI; `nix develop` provides it, or `go install github.com/a-h/templ/cmd/templ@v0.2.x`."

- [ ] **Step 3: Update CHANGELOG.md**

```markdown
## 1.9.0 (unreleased)

### Added
- Server-rendered dashboard via htmx + `templ`. JSON `/v1/` API unchanged.
- Dark mode (`prefers-color-scheme` default; manual override via theme picker on the title).
- CSS extracted to its own content-hashed asset for proper caching.
- Build-tagged `fakedevice` admin surface for UI tests.

### Changed
- Dashboard writes are no longer optimistic — UI updates after server confirms (typically 50–150ms).
- Playwright suite runs against a real daemon spawned from fakedevice rather than `page.route()` mocks.

### Build
- `templ` is now a build prerequisite. `just build` runs `templ generate` automatically.
```

- [ ] **Step 4: Verify**

```sh
just check
```

- [ ] **Step 5: Commit**

```sh
git add CLAUDE.md README.md CHANGELOG.md
git commit -m "docs: update for htmx + dark mode (#14)"
```

---

### Task 32: Open PR 3 and close issue #14

**Goal:** PR 3 merged. Issue #14 closed.

**Steps:**

- [ ] **Step 1: Push branch**

```sh
git push
```

- [ ] **Step 2: Open PR 3**

```sh
gh pr create --title "ui: real-daemon test rewrite + cleanup (#14, PR 3/3)" --body "$(cat <<'EOF'
## Summary

- Playwright suite rewritten against a real `breezyd` spawned from `fakedevice` (via `//go:build fakedevice_admin` admin surface).
- Net-new tests: htmx swap precision, polling cadence, `hx-preserve`, `hx-disabled-elt`, write-and-swap latency budget.
- Dark-mode tests now real-daemon style.
- Final cleanup: docs updated, `legacy.js` already gone in PR 2.
- Closes #14.

## Test mapping

See `tests/ui/MIGRATION.md` — one row per baseline test.

## Test plan

- [x] `just ci` green
- [x] Test count post-migration ≥ 68 (baseline)
- [x] All categories from spec table covered
- [x] Latency-budget assertion passes against fakedevice

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

- [ ] **Step 3: After merge, verify issue auto-closed**

```sh
gh issue view 14
```
Expected: state CLOSED.

---

## Self-review

**Spec coverage:** every section of `2026-05-06-htmx-migration-design.md` mapped:

- Stack decisions → Tasks 1–2 (templ, htmx vendoring), Task 7 (layout response-targets defaults)
- Topology → Tasks 9 (read), 12+ (writes)
- Endpoint inventory → Tasks 9, 12–20
- Error semantics → Task 12 (shim pattern with all four classes)
- Template structure → Tasks 5–7
- Polling, swap precision, hx-preserve → Task 27 + bookkeeping in Task 9
- Optimistic-edit handling → Tasks 12–20 collectively (each migration task uses the `hx-disabled-elt` / `delay:200ms` / `blur+Enter` pattern per the table)
- Dark mode (state model, CSS strategy, theme picker, FOUC, system-preference change) → Tasks 4, 7, 8
- Testing parity contract → Tasks 23–30 (admin surface, real-daemon setup, category-by-category migration, mapping table)
- Migration plan → mirrored in PR 1 / PR 2 / PR 3 task groupings
- Out-of-scope → not in plan, as expected

**Placeholder scan:** none. Where implementation detail is genuinely up to the engineer (e.g., choice of indexed-form vs JSON for schedule entries in Task 18), the plan names the tradeoff and picks a default.

**Type consistency:**
- `breezy.Snapshot` used consistently in templ signatures
- `cardData` wrapper introduced once in Task 12 and used in subsequent tasks
- `LayoutData` struct in Task 7 referenced in Task 9
- Task 9's `collectSnapshots` / `snapshotFor` helpers mentioned as to-be-extracted-from-existing-code; consistent name in subsequent tasks
- `styleHash` constant name consistent across Tasks 2, 7, 9
- `htmxVersion` constant introduced in Task 9, referenced by Task 7's `LayoutData`

No corrections needed.

**One acknowledged risk** flagged for the implementer: Task 12 introduces `cardData` (or `Snapshot.WithError`) and Task 6's golden tests need refreshing afterward. Task 12 step 3 calls this out explicitly.
