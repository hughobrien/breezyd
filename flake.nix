{
  description = "breezyd — Go library, daemon, and CLI for Vents Twinfresh Breezy / Elite 160 Pro HRV units";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }: let
    # Inlined replacement for flake-utils.lib.eachDefaultSystem. Builds the
    # per-system attrset once (so pkgs / breezyd-pkg evaluate once per
    # system), then transposes via genAttrs so the flake exports the
    # standard `packages.<system>.*`, `apps.<system>.*`, etc. shape.
    systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
    forEachSystem = f: nixpkgs.lib.genAttrs systems f;

    perSystem = forEachSystem (system: let
      pkgs = import nixpkgs { inherit system; };

      version = "2.0.2";
      commitOrDirty = if self ? rev then self.rev else "dirty";

      breezyd-pkg = pkgs.buildGoModule {
        pname = "breezyd";
        inherit version;
        src = ./.;
        vendorHash = "sha256-GlETv7Dza4XC56Ll/vJmsn4ChZIlVffRSBJl5ebS7fc=";
        subPackages = [ "cmd/breezyd" "cmd/breezy" ];
        # Reproducible: omit `-X main.date=…` so two builds of the same
        # commit produce identical binaries.
        ldflags = [
          "-s" "-w"
          "-X main.version=${version}"
          "-X main.commit=${commitOrDirty}"
        ];
        # This is a network-protocol package — its tests don't need
        # network access, but the integration tests need a real device
        # behind a build tag. Run only the default test set.
        doCheck = true;
        meta = with pkgs.lib; {
          description = "Go library, daemon, and CLI for Vents Twinfresh Breezy ERVs";
          homepage = "https://github.com/hughobrien/breezyd";
          license = licenses.gpl3Plus;
          platforms = platforms.unix;
          mainProgram = "breezyd";
        };
      };
    in {
      packages = {
        default = breezyd-pkg;
        breezyd = breezyd-pkg;
        breezy = breezyd-pkg; # same derivation produces both binaries
      };

      apps = {
        default = {
          type = "app";
          program = "${breezyd-pkg}/bin/breezyd";
        };
        breezyd = {
          type = "app";
          program = "${breezyd-pkg}/bin/breezyd";
        };
        breezy = {
          type = "app";
          program = "${breezyd-pkg}/bin/breezy";
        };
      };

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

      # Pure CI merge-gate, run HERMETICALLY in nix's build sandbox (no host
      # /etc, no network, no live daemons) via `nix build .#checks.<sys>.gate`.
      # This is what the forge test workflow gates merges on. Running the gate
      # as a pure build (not an impure `nix develop` on the host) is what makes
      # it hermetic — e.g. the standalone test can't see the host's
      # /etc/breezy/config.toml here, so it correctly resolves standalone.
      checks.gate = breezyd-pkg.overrideAttrs (old: {
        pname = "breezyd-ci-gate";
        nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ [
          pkgs.clang          # CC=clang for -race
          pkgs.golangci-lint  # test-staticcheck
        ];
        doCheck = true;
        checkPhase = ''
          runHook preCheck
          export HOME="$TMPDIR"
          echo "== go vet =="
          go vet ./...
          echo "== gofmt =="
          # buildGoModule vendors deps into ./vendor in the sandbox; gofmt walks
          # the filesystem (unlike `go ... ./...`), so exclude vendor explicitly.
          drift=$(gofmt -l . | grep -v '^vendor/' || true)
          if [ -n "$drift" ]; then echo "gofmt drift in:"; echo "$drift"; exit 1; fi
          echo "== go test =="
          go test ./...
          echo "== go test -race =="
          CGO_ENABLED=1 CC=clang go test -race ./...
          echo "== golangci-lint =="
          golangci-lint run ./...
          echo "== test-test-admin =="
          go test -tags breezyd_test_admin ./cmd/breezyd/ -run TestAdmin
          # NOTE: templ-drift intentionally omitted — it's a codegen-freshness
          # check sensitive to the templ binary version (committed files are
          # from templ v0.3.1020; nixpkgs here ships v0.3.1001), so it produces
          # spurious diffs. It stays on GitHub where the toolchain matches.
          runHook postCheck
        '';
      });

      formatter = pkgs.nixpkgs-fmt;
    });

    # Wrap the bare module so it defaults services.breezyd.package to the
    # flake's own build for the host's system. Without this wrapper, the
    # module falls back to pkgs.breezyd — which doesn't exist in nixpkgs
    # (this is a third-party flake) — and throws on evaluation.
    defaultModule = { pkgs, lib, ... }: {
      imports = [ ./nix/module.nix ];
      services.breezyd.package = lib.mkDefault
        self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };
  in {
    nixosModules.default = defaultModule;
    nixosModules.breezyd = defaultModule;

    packages   = forEachSystem (system: perSystem.${system}.packages);
    apps       = forEachSystem (system: perSystem.${system}.apps);
    checks     = forEachSystem (system: perSystem.${system}.checks);
    devShells  = forEachSystem (system: perSystem.${system}.devShells);
    formatter  = forEachSystem (system: perSystem.${system}.formatter);
  };
}
