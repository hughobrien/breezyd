# default: list recipes
default:
	@just --list

# Run templ codegen (writes *_templ.go next to *.templ sources).
generate:
	templ generate

# Fail if generated templ files differ from sources (drift check).
test-templ-drift:
	templ generate
	git diff --quiet -- 'cmd/breezyd/ui/templates/*_templ.go' || (echo "templ drift: run 'just generate' and commit"; exit 1)

# build both binaries
build: generate
	go build -o ./breezyd ./cmd/breezyd
	go build -o ./breezy ./cmd/breezy

# fast tests (no race detector)
test:
	go test ./...

# race tests; uses clang because gcc on this host lacks the TSan runtime
test-race:
	CGO_ENABLED=1 CC=clang go test -race ./...

# race tests, repeated 5x to flush flakes / heisen-races (clang+TSan)
test-race-flake:
	CGO_ENABLED=1 CC=clang go test -race -count=5 ./...

# memory sanitizer: catches reads of uninitialised memory in cgo (clang only)
test-msan:
	CGO_ENABLED=1 CC=clang go test -msan ./...

# address sanitizer: catches OOB / use-after-free in cgo (clang only)
test-asan:
	CGO_ENABLED=1 CC=clang go test -asan ./...

# golangci-lint full pass; errcheck is the strict bit, hence the broader gate.
# Linter set + timeout live in .golangci.yml.
test-staticcheck:
	golangci-lint run ./...

# Test the breezyd build-tagged /test/devices/... admin surface (memory backend).
test-test-admin:
	go test -tags breezyd_test_admin ./cmd/breezyd/ -run TestAdmin

# Tests with coverage. Per-package lines come from `go test -cover`;
# the `total:` line from `go tool cover -func`. HTML drill-down:
#   go tool cover -html=coverage.out
coverage:
	go test -coverprofile=coverage.out -covermode=atomic ./...
	@echo ""
	@go tool cover -func=coverage.out | tail -1

# match what CI runs on every PR: vet + race + lint + asan + msan + UI.
# Slower than check-all (~3 min sequential locally; CI runs the same set in
# parallel jobs). Use this when you want to reproduce a CI failure locally
# without waiting for the next push.
ci: lint test test-race test-staticcheck test-asan test-msan test-ui test-templ-drift test-test-admin

# heavy gate: ci + race-flake. Slow (~5 min); run before tagging a release
# or after risky concurrency / cgo / unsafe code.
check-deep: ci test-race-flake

# live integration tests; WRITES to device (each test t.Cleanup-restores)
test-integration ip id password:
	BREEZY_INTEGRATION=1 \
	BREEZY_TEST_DEVICE_IP='{{ip}}' \
	BREEZY_TEST_DEVICE_ID='{{id}}' \
	BREEZY_TEST_DEVICE_PASSWORD='{{password}}' \
	go test -tags integration ./pkg/breezy/...

# install Node deps + Playwright's Chromium browser (one-time)
test-ui-install:
	cd tests/ui && pnpm install
	cd tests/ui && pnpm exec playwright install chromium

# end-to-end UI tests via Playwright (requires test-ui-install first)
test-ui:
	cd tests/ui && pnpm exec playwright test

# run a subset of UI tests matching a grep pattern; e.g. just test-ui-grep "open state"
test-ui-grep PATTERN:
	cd tests/ui && pnpm exec playwright test --grep "{{PATTERN}}"

# screenshot dashboard in 3-col + 1-col viewports (needs test-ui-install)
screenshot:
	cd tests/ui && pnpm exec tsx screenshot.ts

# Safety net: kill orphan breezyd / fakedevice procs left over by an
# interrupted `just screenshot` or `just test-ui` run. The TS scripts
# kill cleanly on success, but Ctrl-C mid-run can leave the compiled
# child binaries (spawned by `go run`) bound to loopback ports.
kill-test-daemons:
	@pkill -TERM -f '/breezyd( |$)|/fakedevice( |$)' 2>/dev/null || true
	@echo "killed any orphan breezyd / fakedevice processes"

# go vet + gofmt-drift check (fails if `just fmt` would rewrite anything)
lint:
	go vet ./...
	@bad=$(gofmt -l .); if [ -n "$bad" ]; then echo "gofmt drift in:" >&2; echo "$bad" >&2; echo "(run \`just fmt\` to fix)" >&2; exit 1; fi

fmt:
	gofmt -w .

# quick pre-commit gate: vet + fast tests
check: lint test test-templ-drift

# full pre-push gate: vet + gofmt + tests + race + Playwright (needs test-ui-install)
check-all: lint test test-race test-ui test-templ-drift

# parse-check nix/module.nix (fast; `nix build` is the heavy variant)
nix-check:
	nix-instantiate --parse nix/module.nix > /dev/null && echo "nix/module.nix parses OK"

tidy:
	go mod tidy

clean:
	rm -f ./breezy ./breezyd
	go clean -testcache
	rm -rf tests/ui/test-results tests/ui/playwright-report

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
