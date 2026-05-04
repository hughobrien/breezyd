# Justfile migration — design

**Date:** 2026-05-04
**Status:** approved for implementation
**Repo:** `~/twinfresh`

## Summary

Replace the project's three-line `Makefile` with a `justfile` that covers the same parity targets plus a small set of frequently useful recipes (race tests, integration tests against real hardware, run-from-source helpers, formatting, a release-snapshot wrapper, and a `nix-vendor-hash` foot-gun reminder). Update README and CLAUDE.md to point at the new commands. CI is unaffected — the GitHub Actions workflows already invoke `go` directly.

## Motivation

Two things motivate this:

1. The current `Makefile` is just a stash of `go` invocations. `just` is a better fit: it lists recipes by default with their doc-comments, takes positional arguments cleanly, and doesn't carry POSIX-make's tab/quoting baggage.
2. Several useful commands are not in the Makefile because adding them would make the Makefile messier than it's worth — most painfully, the integration-test invocation, which today is a five-line shell incantation that the operator has to retype each time. Recipes with parameters fix that.

## Scope

In scope:
- Add `justfile` at repo root with the recipes listed below.
- Delete `Makefile` (no shim).
- Update `README.md` and `CLAUDE.md` to reference `just` instead of `make`.
- Add `just` to the `flake.nix` devShell so `nix develop` brings it along.

Out of scope:
- CI workflow changes. `.github/workflows/test.yml` and `release.yml` already use raw `go` and `goreleaser`; nothing references `make`.
- Editing `docs/superpowers/plans/2026-05-03-twinfresh-cli.md` or its accompanying `.tasks.json`. Those are dated implementation-plan artifacts; preserving their `make build` references keeps the historical record accurate.
- `goimports` in place of `gofmt`. Extra dep, marginal gain.
- Pulling integration-test parameters from `~/.config/breezy/config.toml`. Implicit device targeting from a config file is exactly the kind of magic that causes accidental writes to the wrong unit; positional args keep the call site self-documenting.

## Recipe set

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

### Design choices

- **Default recipe lists everything.** `just` alone shows all recipes with their `#`-comments. Beats a silent alias.
- **`check` runs fast tests, not race tests.** Race is its own opt-in recipe (`test-race`), since cgo+TSan dominates wall time. CI runs `test-race`.
- **`test-integration` takes positional args, not env-var defaults.** The call site documents which physical device is being written to. The `# WRITES to device` comment fires on `--list` so nobody runs it accidentally. Per-test `t.Cleanup` restores prior values; we rely on that, not on the recipe.
- **`run-daemon` / `run-cli` use `go run`**, not the prebuilt binary. Avoids stale-binary surprises during iteration.
- **`nix-vendor-hash` prints instructions rather than automating** the rewrite. Automating it means parsing nix-build error output and editing `flake.nix` in place — cute, brittle, and rarely run. A four-line reminder is enough.

## Documentation updates

### `README.md`

Three call sites change:

1. **"Build from source"** block (~line 79–82):
   ```
   make build       # produces ./breezyd and ./breezy
   make test        # go test -race ./...
   make lint        # go vet ./...
   ```
   becomes:
   ```
   just build       # produces ./breezyd and ./breezy
   just test-race   # go test -race ./... (the CI command)
   just check       # vet + fast tests (pre-commit gate)
   ```
   Note that the canonical "default" test command becomes the fast `just test`; `just test-race` is the explicit race-detector recipe.

2. **CGO/clang note** (~line 84–85): the sentence "set `CGO_ENABLED=1 CC=clang` before `make test`" no longer applies — `just test-race` already bakes in the clang env. Replace with a one-liner saying the recipe handles this.

3. **"Testing" section** (~line 304–309): replace the `make test` reference with `just test-race`. The integration-test block right below it (currently a five-line shell example) becomes:
   ```sh
   just test-integration 192.168.1.148 BREEZY00000000A0 <your password>
   ```

### `CLAUDE.md`

Replace the "Build, test, lint" opening block. The race-test workaround paragraph collapses into a single sentence pointing at `just test-race`. Single-test recipes (`go test ./pkg/breezy/... -run X`) stay as raw `go` since `just` doesn't add value there. Add a mention of `just` to the dev-shell description.

### `flake.nix`

Add `just` to `devShells.default.packages`. One-line change.

## Migration steps (high level)

1. Write `justfile`.
2. Delete `Makefile`.
3. Update `flake.nix` devShell.
4. Update `README.md`.
5. Update `CLAUDE.md`.
6. Verify: `just build` produces both binaries; `just test` exits 0; `just check` exits 0; `just --list` shows every recipe with its comment.

## Risks / non-issues

- **CI breakage:** none. `.github/workflows/*.yml` use `go` directly.
- **Nix build break:** none. The flake's `buildGoModule` doesn't shell out to make. The `vendorHash` is unrelated to this change.
- **Operator muscle memory:** `make build` → `just build` is the worst of it. Old shell history will fail loudly with "make: command not found" once Make is gone, which is preferable to a silent shim that drifts.
- **Re-running `just test-integration` against a unit you didn't mean to:** the positional-arg design forces the operator to type the IP/ID/password on every invocation. That is the safety property — don't regress it by adding env-var fallbacks.
