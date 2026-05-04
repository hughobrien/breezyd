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

# install Node deps + Playwright's Chromium browser (one-time)
test-ui-install:
	cd tests/ui && pnpm install
	cd tests/ui && pnpm exec playwright install chromium

# end-to-end UI tests via Playwright (requires test-ui-install first)
test-ui:
	cd tests/ui && pnpm exec playwright test

# screenshot dashboard in 3-col + 1-col viewports (needs test-ui-install)
screenshot:
	cd tests/ui && pnpm exec tsx screenshot.ts

# go vet + gofmt-drift check (fails if `just fmt` would rewrite anything)
lint:
	go vet ./...
	@bad=$(gofmt -l .); if [ -n "$bad" ]; then echo "gofmt drift in:" >&2; echo "$bad" >&2; echo "(run \`just fmt\` to fix)" >&2; exit 1; fi

fmt:
	gofmt -w .

# quick pre-commit gate: vet + fast tests
check: lint test

# full pre-push gate: vet + gofmt + tests + race + Playwright (needs test-ui-install)
check-all: lint test test-race test-ui

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
