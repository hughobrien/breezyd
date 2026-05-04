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

# render the dashboard against mocked /v1/... data and screenshot it
# in 3-col (1400x900) and 1-col (480x900) viewports.
# requires test-ui-install to have run first (tsx + Playwright deps).
screenshot:
	cd tests/ui && pnpm exec tsx screenshot.ts

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
