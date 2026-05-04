# Justfile Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers-extended-cc:subagent-driven-development (recommended) or superpowers-extended-cc:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the project's `Makefile` with a `justfile` covering parity targets plus race / integration / run / format / release helpers, and update README + CLAUDE.md to reference the new commands.

**Architecture:** Two commits. (1) Build interface swap: add `justfile`, delete `Makefile`, add `just` to the Nix devShell. (2) Doc updates: rewrite the affected sections of `README.md` and `CLAUDE.md` so every `make ...` reference becomes its `just` equivalent. CI is unaffected — `.github/workflows/*.yml` already invoke `go` directly.

**Tech Stack:** `just` 1.40, Go toolchain, Nix flake, plain Markdown.

**Spec:** `docs/superpowers/specs/2026-05-04-justfile-migration-design.md`

---

### Task 1: Add justfile, delete Makefile, update flake.nix devShell

**Goal:** Replace the build interface. After this task, `just --list` shows every recipe with its doc-comment, `just build` produces both binaries, `just test` runs the fast suite, and `nix develop` brings `just` along. The `Makefile` no longer exists.

**Files:**
- Create: `justfile`
- Delete: `Makefile`
- Modify: `flake.nix` (lines 68–76 — add `just` to `devShells.default.packages`)

**Acceptance Criteria:**
- [ ] `justfile` exists at repo root with the recipe set from the spec.
- [ ] `Makefile` is removed from the working tree.
- [ ] `just --list` enumerates all recipes with their `#` doc-comments.
- [ ] `just build` produces `./breezyd` and `./breezy`.
- [ ] `just test` exits 0.
- [ ] `just check` exits 0 (vet + fast tests).
- [ ] `just clean` removes both binaries.
- [ ] `flake.nix` devShell `packages` list includes `just` in alphabetical-adjacent position (after `goreleaser`, before the closing `]`).
- [ ] No remaining references to `make ` in tracked files outside `docs/superpowers/plans/2026-05-03-twinfresh-cli.md` (historical artifact — leave alone).

**Verify:**
```sh
test ! -f Makefile \
  && just --list \
  && just clean \
  && just build && [ -x ./breezyd ] && [ -x ./breezy ] \
  && just test \
  && just check \
  && grep -n "^[[:space:]]*just" flake.nix
```
Expected: every command exits 0; `just --list` prints all recipes; `flake.nix` grep matches the line you added.

**Steps:**

- [ ] **Step 1: Create the `justfile`**

Create `/home/hugh/twinfresh/justfile` with this exact content:

```just
# default: list recipes
default:
    @just --list

# build both binaries
build:
    go build -o ./breezyd ./cmd/breezyd
    go build -o ./breezy ./cmd/breezy

# fast tests (no race detector)
test:
    go test ./...

# race tests; uses clang because gcc on this host lacks the TSan runtime
test-race:
    CGO_ENABLED=1 CC=clang go test -race ./...

# live integration tests; WRITES to device (each test t.Cleanup-restores)
test-integration ip id password:
    BREEZY_INTEGRATION=1 \
    BREEZY_TEST_DEVICE_IP='{{ip}}' \
    BREEZY_TEST_DEVICE_ID='{{id}}' \
    BREEZY_TEST_DEVICE_PASSWORD='{{password}}' \
    go test -tags integration ./pkg/breezy/...

lint:
    go vet ./...

fmt:
    gofmt -w .

# quick pre-commit gate: vet + fast tests
check: lint test

tidy:
    go mod tidy

clean:
    rm -f ./breezy ./breezyd
    go clean -testcache

# run daemon from source
run-daemon *ARGS:
    go run ./cmd/breezyd {{ARGS}}

# run CLI from source
run-cli *ARGS:
    go run ./cmd/breezy {{ARGS}}

# build release archives locally (no publish)
release-snapshot:
    goreleaser release --snapshot --clean

# print the steps to recompute flake.nix vendorHash after go.sum changes
nix-vendor-hash:
    @echo "1. In flake.nix, set: vendorHash = pkgs.lib.fakeHash;"
    @echo "2. Run: nix build"
    @echo "3. Copy the 'got: sha256-...' value into vendorHash."
```

Note for the implementer: `just` recipe bodies are TAB-indented. If your editor inserted spaces, `just` will fail with a parse error — re-indent to a single tab.

- [ ] **Step 2: Smoke-test the justfile before deleting Makefile**

Run from the repo root:

```sh
just --list
just clean
just build
just test
just check
```

Expected: `just --list` prints every recipe (each with its `#` comment as the description); `just clean` succeeds; `just build` produces `./breezyd` and `./breezy`; `just test` and `just check` exit 0.

If any recipe fails to parse (`just` prints `error: ... near ...`), the most likely cause is space-indented recipe bodies — fix to tabs.

- [ ] **Step 3: Delete the Makefile**

```sh
rm Makefile
```

Confirm:
```sh
test ! -f Makefile && echo "Makefile gone"
```

- [ ] **Step 4: Add `just` to the Nix devShell**

Edit `/home/hugh/twinfresh/flake.nix`. The current devShell block (lines 68–76):

```nix
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            go-tools
            goreleaser
          ];
        };
```

becomes:

```nix
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            gotools
            go-tools
            goreleaser
            just
          ];
        };
```

(Single-line addition. Don't reorder the existing entries.)

- [ ] **Step 5: Re-run the verification commands**

```sh
test ! -f Makefile \
  && just --list \
  && just clean \
  && just build && [ -x ./breezyd ] && [ -x ./breezy ] \
  && just test \
  && just check \
  && grep -n "just" flake.nix
```

Expected: all commands exit 0; the `flake.nix` grep prints the line `            just` inside the devShell packages list.

- [ ] **Step 6: Confirm no stale `make` references slipped in**

```sh
git ls-files | xargs grep -l "make build\|make test\|make lint\|make tidy\|make clean" 2>/dev/null
```

Expected output: `README.md`, `CLAUDE.md`, and `docs/superpowers/plans/2026-05-03-twinfresh-cli.md` (and possibly `docs/superpowers/specs/2026-05-04-justfile-migration-design.md`, which legitimately quotes the old commands). The plan file is the historical artifact we deliberately leave alone. README and CLAUDE.md are Task 2's job. Anything else in the list is a problem — investigate before continuing.

- [ ] **Step 7: Commit**

```sh
git add justfile flake.nix
git rm Makefile
git commit -m "$(cat <<'EOF'
build: replace Makefile with justfile

Adds parity recipes (build/test/lint/tidy/clean) plus race, integration,
run-daemon/run-cli, fmt, check (vet+test), release-snapshot, and a
nix-vendor-hash reminder. Adds `just` to the Nix devShell so
`nix develop` brings it along.

CI is unaffected: .github/workflows/*.yml already invoke `go` and
`goreleaser` directly.
EOF
)"
```

---

### Task 2: Update README and CLAUDE.md to reference just

**Goal:** Replace every `make` reference in the operator-facing docs with the matching `just` invocation, and collapse the old "set CGO_ENABLED=1 CC=clang before make test" workaround paragraph since `just test-race` bakes in the clang env. The historical implementation plan in `docs/superpowers/plans/` is left untouched.

**Files:**
- Modify: `README.md` (three regions: build/test block at lines 75–85, Testing section at lines 305–331)
- Modify: `CLAUDE.md` (Build/test/lint section at lines 5–18; mention `just` in the Nix dev-shell line)

**Acceptance Criteria:**
- [ ] `README.md` "Build from source" block lists `just build`, `just test-race`, `just check` (no `make`).
- [ ] `README.md` Testing section uses `just test-race` instead of `make test`, and shows the integration test as a single `just test-integration <ip> <id> <password>` line.
- [ ] `README.md` no longer contains the standalone "set CGO_ENABLED=1 CC=clang" workaround block at the bottom of the Testing section — the recipe handles it.
- [ ] `CLAUDE.md` Build/test/lint section uses `just` recipes; the clang note is a single sentence pointing at `just test-race`.
- [ ] No `make build`, `make test`, `make lint`, `make tidy`, or `make clean` strings remain in `README.md` or `CLAUDE.md`.
- [ ] Single-test recipe examples in `CLAUDE.md` (the `go test ./pkg/breezy/... -run X` form) are kept as raw `go` per the spec.

**Verify:**
```sh
! grep -nE "make (build|test|lint|tidy|clean)" README.md CLAUDE.md \
  && grep -n "just build\|just test-race\|just check\|just test-integration" README.md \
  && grep -n "just build\|just test-race\|just check" CLAUDE.md
```
Expected: the negated grep finds nothing (exits 0 due to `!`); the two positive greps each match multiple lines.

**Steps:**

- [ ] **Step 1: Update README.md "Build from source" block**

Replace lines 74–85 (from "Requires Go 1.22+" through "before `make test`.") with:

```markdown
Requires Go 1.22+ (developed on 1.26). No other system dependencies for the
binaries themselves; the race-detector recipe (`just test-race`) needs a
working C toolchain.

```sh
just build       # produces ./breezyd and ./breezy
just check       # vet + fast tests (pre-commit gate)
just test-race   # full race-detector run (the CI command)
```

`just test-race` already sets `CGO_ENABLED=1 CC=clang`, so the recipe works
out of the box on dev hosts whose default `gcc` lacks the TSan runtime.
```

(Keep the surrounding Markdown headers and the "Run with Nix" section that follows untouched.)

- [ ] **Step 2: Update README.md Testing section**

Replace lines 303–331 (from `## Testing` through the trailing `CGO_ENABLED=1 CC=clang go test -race ./...` block) with:

```markdown
## Testing

```sh
just test                       # unit tests (uses fakedevice)
just test-race                  # same, with -race (the CI command)
just lint                       # go vet ./...
just check                      # lint + fast tests (pre-commit gate)
```

Run a single package or test with raw `go`:

```sh
go test ./pkg/breezy/...
go test ./cmd/breezyd -run TestPoller_FanSettle
```

Live integration tests against real hardware are gated by both the
`integration` build tag and `BREEZY_INTEGRATION=1`, plus three env vars
identifying the target device. The `just test-integration` recipe wraps
all of that:

```sh
just test-integration 192.168.1.148 BREEZY00000000A0 <your password>
```

These tests write to the device — each one registers a `t.Cleanup` that
restores the prior value, so re-runs leave the unit in its original state.
```

(The next section header `## Security` follows immediately.)

- [ ] **Step 3: Update CLAUDE.md "Build, test, lint" section**

Replace lines 5–18 (from `## Build, test, lint` through the closing fence of the clang code block) with:

```markdown
## Build, test, lint

```sh
just build       # go build -> ./breezyd ./breezy
just test        # go test ./...           (fast, no race)
just test-race   # go test -race ./...     (cgo+clang; the CI command)
just lint        # go vet ./...
just check       # lint + fast tests       (pre-commit gate)
just tidy        # go mod tidy
just clean       # remove binaries + clean test cache
just fmt         # gofmt -w .
```

`just test-race` bakes in `CGO_ENABLED=1 CC=clang` because the default `gcc` on this host lacks the TSan runtime.
```

- [ ] **Step 4: Update CLAUDE.md integration-test invocation**

Find the integration-test code block (currently the multi-line `BREEZY_INTEGRATION=1 \ ...` form, ~lines 29–33). Replace with:

```markdown
```sh
just test-integration <ip> <id> <password>
```
```

Keep the surrounding "double-gated", "WRITES to device", and "never remove or weaken those cleanups" sentences as-is — they're still accurate.

- [ ] **Step 5: Update the Nix line in CLAUDE.md**

Find this line in `CLAUDE.md` (in the "Build, test, lint" or "Architecture" area — search for `nix develop`):

> Nix flake builds work too: `nix build`, `nix develop`, `nix run .#breezy -- ls`. The flake's `vendorHash` in `flake.nix` must be updated whenever `go.sum` changes.

Append one sentence:

> `nix develop` includes `just`, so all recipes are available without a global install.

- [ ] **Step 6: Run verification**

```sh
! grep -nE "make (build|test|lint|tidy|clean)" README.md CLAUDE.md
just check
just --list | head -20
```

Expected: the first command exits 0 (no matches); `just check` passes; the recipe list still looks right.

- [ ] **Step 7: Commit**

```sh
git add README.md CLAUDE.md
git commit -m "$(cat <<'EOF'
docs: update README and CLAUDE.md to reference just

Replaces every `make ...` reference with its `just` equivalent and
collapses the old `CGO_ENABLED=1 CC=clang` workaround paragraph since
`just test-race` bakes that env into the recipe. Integration-test
invocation collapses from a five-line shell example to a single
`just test-integration <ip> <id> <password>` call.

The historical implementation plan under docs/superpowers/plans/ is
left untouched.
EOF
)"
```

---

## Self-review notes

- **Spec coverage:** Task 1 covers spec §"Recipe set" + flake.nix devShell change + Makefile deletion. Task 2 covers spec §"Documentation updates" for README and CLAUDE.md. The spec's "leave the historical plan alone" rule is enforced by Task 1 Step 6's grep allowlist.
- **Placeholder scan:** All recipe content is verbatim from the spec; all README/CLAUDE.md replacement blocks are spelled out; commit messages are full HEREDOCs.
- **Consistency:** Recipe names used across both tasks (`build`, `test`, `test-race`, `check`, `test-integration`, etc.) match the spec exactly. The "fast tests = `just test`" / "race tests = `just test-race`" split is consistent in every doc block.
- **Risk:** The most likely failure mode is space-indented recipe bodies in the `justfile` — Task 1 Step 1 calls this out explicitly.
